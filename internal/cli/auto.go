package cli

import (
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/adaptive-scale/katana/internal/coverage"
	"github.com/adaptive-scale/katana/internal/report"
)

// runAuto deliberately keeps going after a failed stage. A release-readiness
// check is useful precisely when it can tell the user which later evidence is
// still missing instead of stopping at the first red command.
func runAuto(args []string) error {
	fs := flag.NewFlagSet("katana auto", flag.ContinueOnError)
	dir := fs.String("dir", "", "project directory (defaults to the current directory)")
	force := fs.Bool("force", false, "refresh existing behavior files and regenerate every behavior")
	out := fs.String("out", "out", "directory for the test and readiness reports")
	min := fs.Float64("min", 0, "require at least this total coverage percentage")
	fs.Usage = func() {
		fmt.Fprint(os.Stderr, `Usage: katana auto [flags]

Runs the complete release check: discover, generate, run, coverage, then writes
a readiness report. Every stage runs even when an earlier stage fails.

Flags:
`)
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *min < 0 || *min > 100 {
		return fmt.Errorf("--min must be between 0 and 100, got %s", strconv.FormatFloat(*min, 'g', -1, 64))
	}
	cfg, err := loadProject(*dir)
	if err != nil {
		return err
	}
	root := cfg.Root
	dirArgs := []string{"--dir", root}
	stages := make([]report.ReadinessStage, 0, 5)
	runStage := func(name string, fn func() error) {
		started := time.Now()
		err := fn()
		stage := report.ReadinessStage{Name: name, Duration: time.Since(started)}
		if err != nil {
			stage.Error = err.Error()
			var ee exitError
			if errors.As(err, &ee) {
				stage.Error = fmt.Sprintf("exit %d", ee.code)
				fmt.Printf("auto: %s failed (exit %d); continuing\n", name, ee.code)
			} else {
				fmt.Fprintf(os.Stderr, "katana auto: %s failed: %v\n", name, err)
			}
		} else {
			stage.OK = true
			fmt.Printf("auto: %s passed\n", name)
		}
		stages = append(stages, stage)
	}

	discoverArgs := append([]string(nil), dirArgs...)
	if *force {
		discoverArgs = append(discoverArgs, "--force")
	}
	runStage("discover", func() error { return runDiscover(discoverArgs) })
	generateArgs := append([]string(nil), dirArgs...)
	if *force {
		generateArgs = append(generateArgs, "--force")
	}
	runStage("generate", func() error { return runGenerate(generateArgs) })
	runStage("run", func() error {
		return runTest(append(append([]string(nil), dirArgs...), "--save", "--out", *out))
	})
	coverageArgs := append([]string(nil), dirArgs...)
	if *min > 0 {
		coverageArgs = append(coverageArgs, "--min", strconv.FormatFloat(*min, 'f', -1, 64))
	}
	runStage("coverage", func() error { return runCoverage(coverageArgs) })

	// Coverage is a distinct readiness signal: a successful runner with no
	// measured statements is not evidence that the product was exercised.
	history, historyErr := coverage.LoadHistory(root)
	measured := false
	percent := 0.0
	if historyErr == nil && len(history.Runs) > 0 {
		latest := history.Runs[len(history.Runs)-1]
		measured = latest.Measured()
		percent = latest.Percent()
	}
	if len(stages) == 4 && historyErr == nil && !measured {
		stages[3].OK = false
		stages[3].Error = "coverage measured no statements"
	}
	ready := historyErr == nil && measured
	for _, s := range stages {
		ready = ready && s.OK
	}
	if historyErr != nil {
		stages[3].OK = false
		stages[3].Error = "reading coverage history: " + historyErr.Error()
		ready = false
	}

	var reasons, improvements []string
	for _, stage := range stages {
		if stage.OK {
			improvements = append(improvements, stage.Name+" completed successfully")
		} else {
			reasons = append(reasons, stage.Name+": "+stage.Error)
		}
	}
	if len(improvements) == 0 {
		improvements = []string{"no readiness stage completed successfully"}
	}
	if historyErr != nil {
		reasons = append(reasons, "coverage: "+historyErr.Error())
	}
	reason := strings.Join(reasons, "; ")
	if ready {
		reason = "all release checks passed"
	}
	path, writeErr := (report.Readiness{
		Project: filepath.Base(root), Root: root, Version: Version,
		StartedAt: time.Now(), Ready: ready, Coverage: percent,
		Stages: stages, Reason: reason, Improvements: improvements,
	}).WriteHTML(reportDir(cfg, *out))
	if writeErr != nil {
		return fmt.Errorf("writing readiness report: %w", writeErr)
	}
	verdict := "not release ready"
	if ready {
		verdict = "release ready"
	}
	fmt.Printf("\nrelease readiness: %s\nreason: %s\nimprovements: %s\nreport: %s\n", verdict, reason, strings.Join(improvements, "; "), displayPath(path))
	if !ready {
		return fmt.Errorf("platform is not release ready: %s", reason)
	}
	return nil
}
