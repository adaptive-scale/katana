package cli

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"time"

	"github.com/adaptive-scale/katana/internal/config"
	"github.com/adaptive-scale/katana/internal/report"
	"github.com/adaptive-scale/katana/internal/tracker"
)

func runTest(args []string) error {
	fs := flag.NewFlagSet("katana run", flag.ContinueOnError)
	var (
		dir     = fs.String("dir", "", "project directory (defaults to the current directory)")
		check   = fs.Bool("check", false, "fail before running if any behavior is out of date")
		save    = fs.Bool("save", false, "write an HTML report of the test results to the output directory")
		saveDir = fs.String("out", "out", "directory --save writes its HTML report to")
	)
	fs.Usage = func() {
		fmt.Fprint(os.Stderr, `Usage: katana run [flags] [-- extra args]

Runs the test command from katana.yaml. Arguments after -- are appended to it,
so `+"`katana run -- -run TestCheckout`"+` narrows a Go test run.

katana warns when a behavior has changed since its tests were generated; pass
--check to make that a hard failure instead, which is the useful form in CI.

--save writes each run's results to out/report-<timestamp>.html: a self-contained
page listing every test case with its outcome, the failure output, and which
behaviors were out of date at the time. Per-case results are recovered from the
runner's own output for go-test, pytest, jest, vitest, mocha, cargo-test, xunit
and xctest; any other runner still gets a report of the command, exit code and
full output.

Flags:
`)
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		return err
	}
	extra := fs.Args()

	cfg, err := loadProject(*dir)
	if err != nil {
		return err
	}
	if strings.TrimSpace(cfg.Test.Command) == "" {
		return errors.New("no test.command set in katana.yaml")
	}

	// Report staleness before running, so a green suite is never mistaken for
	// a suite that covers the current specification.
	t, err := tracker.Load(cfg.Root)
	if err != nil {
		return err
	}
	items, err := plan(cfg, t, nil)
	if err != nil {
		return err
	}
	var stale []item
	for _, it := range items {
		if it.Status != tracker.StatusUpToDate {
			stale = append(stale, it)
		}
	}
	if len(stale) > 0 {
		fmt.Fprintf(os.Stderr, "warning: %d behavior(s) out of date with their tests:\n", len(stale))
		for _, it := range stale {
			fmt.Fprintf(os.Stderr, "  %-22s %s\n", it.Status, it.Source)
		}
		fmt.Fprintln(os.Stderr, "  run `katana generate` first")
		if *check {
			return fmt.Errorf("%d behavior(s) out of date (--check)", len(stale))
		}
		fmt.Fprintln(os.Stderr)
	}

	command := cfg.Test.Command
	if *save {
		// A report of "the suite passed" is worth little, so ask the runner for
		// per-case output when katana knows how.
		if verbose, added := report.Verbose(cfg.Defaults.Framework, command); added {
			fmt.Fprintf(os.Stderr, "note: --save added -v to the test command so each case is recorded\n")
			command = verbose
		}
	}
	if len(extra) > 0 {
		command += " " + strings.Join(quoteAll(extra), " ")
	}

	workDir := cfg.Root
	if cfg.Test.Dir != "" {
		workDir = filepath.Join(cfg.Root, filepath.FromSlash(cfg.Test.Dir))
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	fmt.Printf("running: %s\n", command)
	cmd := exec.CommandContext(ctx, shell(), shellFlag(), command)
	cmd.Dir = workDir
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr

	// With --save the suite output is copied as it streams, so the terminal is
	// unchanged and the report has the runner's own words to parse.
	var rec *report.Recorder
	if *save {
		rec = &report.Recorder{}
		cmd.Stdout = rec.Tee(os.Stdout)
		cmd.Stderr = rec.Tee(os.Stderr)
	}

	startedAt := time.Now()
	runErr := cmd.Run()
	elapsed := time.Since(startedAt)

	exitCode := 0
	if runErr != nil {
		var exitErr *exec.ExitError
		if !errors.As(runErr, &exitErr) {
			return fmt.Errorf("running test command: %w", runErr)
		}
		exitCode = exitErr.ExitCode()
	}

	if *save {
		r := &report.Report{
			Project:   filepath.Base(cfg.Root),
			Root:      cfg.Root,
			Command:   command,
			Framework: cfg.Defaults.Framework,
			Version:   Version,
			StartedAt: startedAt,
			Duration:  elapsed,
			ExitCode:  exitCode,
			Behaviors: reportBehaviors(items),
			Output:    rec.String(),
		}
		r.Collect()

		path, err := r.WriteHTML(reportDir(cfg, *saveDir))
		if err != nil {
			err = fmt.Errorf("writing test report: %w", err)
			if exitCode == 0 {
				return err
			}
			// The suite's own result outranks a failure to write the report.
			fmt.Fprintf(os.Stderr, "katana: %v\n", err)
		} else {
			fmt.Printf("report: %s (%d passed, %d failed, %d skipped)\n",
				displayPath(path), r.Passed(), r.Failed(), r.Skipped())
		}
	}

	if exitCode != 0 {
		// Propagate the suite's own exit code so CI sees the real result.
		os.Exit(exitCode)
	}
	return nil
}

// reportDir resolves the --out directory against the project root, so a report
// lands in the project rather than wherever katana happened to be invoked from.
func reportDir(cfg *config.Config, out string) string {
	if filepath.IsAbs(out) {
		return out
	}
	return filepath.Join(cfg.Root, filepath.FromSlash(out))
}

// reportBehaviors records how current each behavior's tests were when the suite
// ran, which is the context that turns a green suite into a meaningful one.
func reportBehaviors(items []item) []report.Behavior {
	out := make([]report.Behavior, 0, len(items))
	for _, it := range items {
		out = append(out, report.Behavior{
			Source: it.Source,
			Output: it.Output,
			Status: it.Status.String(),
			Stack:  fmt.Sprintf("%s/%s via %s", it.Language, it.Framework, it.Harness),
			Stale:  it.Status != tracker.StatusUpToDate,
		})
	}
	return out
}

// displayPath shortens a path to a relative one when that is shorter to read.
func displayPath(p string) string {
	wd, err := os.Getwd()
	if err != nil {
		return p
	}
	rel, err := filepath.Rel(wd, p)
	if err != nil || strings.HasPrefix(rel, "..") {
		return p
	}
	return rel
}

func shell() string {
	if runtime.GOOS == "windows" {
		return "cmd"
	}
	if sh := os.Getenv("SHELL"); sh != "" {
		return sh
	}
	return "/bin/sh"
}

func shellFlag() string {
	if runtime.GOOS == "windows" {
		return "/C"
	}
	return "-c"
}

// quoteAll shell-quotes pass-through arguments so paths with spaces survive.
func quoteAll(args []string) []string {
	out := make([]string, len(args))
	for i, a := range args {
		out[i] = "'" + strings.ReplaceAll(a, "'", `'\''`) + "'"
	}
	return out
}
