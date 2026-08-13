package cli

import (
	"bytes"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/adaptive-scale/katana/internal/config"
	"github.com/adaptive-scale/katana/internal/plan"
	"github.com/adaptive-scale/katana/internal/report"
	"github.com/adaptive-scale/katana/internal/suite"
	"github.com/adaptive-scale/katana/internal/tracker"
	"github.com/adaptive-scale/katana/internal/ui"
)

func runTest(args []string) error {
	fs := flag.NewFlagSet("katana run", flag.ContinueOnError)
	var (
		dir     = fs.String("dir", "", "project directory (defaults to the current directory)")
		check   = fs.Bool("check", false, "fail before running if any behavior is out of date")
		cases   = fs.Bool("cases", true, "ask the runner to report every test case, so `katana status` can show what passed")
		save    = fs.Bool("save", false, "write an HTML report of the test results to the output directory")
		saveDir = fs.String("out", "out", "directory --save writes its HTML report to")
		only    = fs.String("behavior", "", "run only the tests generated for this behavior file")
		color   = fs.String("color", "auto", "colour the output: auto, always or never")
	)
	fs.Usage = func() {
		fmt.Fprint(os.Stderr, `Usage: katana run [flags] [-- extra args]

Runs the test command from katana.yaml. Arguments after -- are appended to it,
so `+"`katana run -- -run TestCheckout`"+` narrows a Go test run.

katana warns when a behavior has changed since its tests were generated; pass
--check to make that a hard failure instead, which is the useful form in CI.

--behavior runs only the tests generated for one behavior, where katana knows
how to narrow the runner: by test name for `+"`go test`"+`, by file for pytest, jest,
vitest and mocha. The outcomes recorded for the other behaviors are left
standing, so `+"`katana status`"+` still knows how they last did.

Every run is recorded to `+"`.katana/results.json`"+`, so `+"`katana status`"+` can say how
many test cases passed without running the suite again, and appended to
`+"`.katana/history.json`"+`, which is what the charts in `+"`katana tui`"+` are drawn from.
Recording per case needs the runner to name each case, which some only do in
verbose mode, so katana adds that flag where it knows it; --cases=false leaves
the command exactly as configured and records the suite-wide result alone.

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
	mode, err := ui.ParseMode(*color)
	if err != nil {
		return err
	}
	ui.SetMode(mode)
	extra := fs.Args()

	cfg, err := loadProject(*dir)
	if err != nil {
		return err
	}
	if strings.TrimSpace(cfg.Test.Command) == "" {
		return errors.New("no test.command set in katana.yaml")
	}

	out := ui.For(os.Stdout)
	errp := ui.For(os.Stderr)

	// Report staleness before running, so a green suite is never mistaken for
	// a suite that covers the current specification.
	t, err := tracker.Load(cfg.Root)
	if err != nil {
		return err
	}
	items, err := plan.Build(cfg, t, nil)
	if err != nil {
		return err
	}
	var stale []plan.Item
	for _, it := range items {
		if it.Stale() {
			stale = append(stale, it)
		}
	}
	if len(stale) > 0 {
		fmt.Fprintf(os.Stderr, "%s %d behavior(s) out of date with their tests:\n",
			errp.Yellow("warning:"), len(stale))
		for _, it := range stale {
			fmt.Fprintf(os.Stderr, "  %s %s\n", errp.StatusCell(it.Status, 22), it.Source)
		}
		fmt.Fprintln(os.Stderr, "  run `katana generate` first")
		if *check {
			return fmt.Errorf("%d behavior(s) out of date (--check)", len(stale))
		}
		fmt.Fprintln(os.Stderr)
	}

	req := suite.Request{
		PerCase: *cases || *save,
		Extra:   extra,
		Stdout:  os.Stdout,
		Stderr:  os.Stderr,
		Stdin:   os.Stdin,
	}
	if *only != "" {
		it, ok := findBehavior(items, plan.NormalizePath(cfg.Root, *only))
		if !ok {
			return fmt.Errorf("no behavior %q in %s", *only, config.FileName)
		}
		target, ok := suite.TargetFor(t, it)
		if !ok {
			return fmt.Errorf("%s has not been generated yet; run `katana generate` first", it.Source)
		}
		req.Only = target
	}
	expected := mappedTests(t)
	if req.Only != nil {
		expected = len(req.Only.Tests)
	}
	bar := newOperationBar(os.Stdout, "run", expected)
	req.Stdout = bar.writer(os.Stdout)
	req.Stderr = bar.writer(os.Stderr)
	req.OnCases = func(cases []report.Case) { bar.setCases(len(cases)) }

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	// Run each tracked test separately. Apart from making the output easier to
	// attribute, recording after every invocation means an interrupted run still
	// leaves the cases that already completed in results.json.
	var res *suite.Result
	individual := req.Only == nil && *cases && mappedTests(t) > 0
	if individual {
		res, err = runCasesIndividually(ctx, cfg, t, req, bar)
	} else {
		res, err = suite.Run(ctx, cfg, req)
	}
	if err != nil {
		bar.stop()
		return err
	}
	if res.Parsed {
		bar.setCases(len(res.Cases))
	}
	bar.stop()
	for _, note := range res.Notes {
		fmt.Fprintf(os.Stderr, "%s %s\n", errp.Dim("note:"), note)
	}
	fmt.Printf("running: %s\n", out.Cyan(res.Command))

	r := &report.Report{
		Project:   filepath.Base(cfg.Root),
		Root:      cfg.Root,
		Command:   res.Command,
		Framework: cfg.Defaults.Framework,
		Version:   Version,
		StartedAt: res.StartedAt,
		Duration:  res.Duration,
		ExitCode:  res.ExitCode,
		Behaviors: reportBehaviors(items),
		Output:    res.Output,
	}
	r.Collect()

	// Record the outcome for `katana status`. A run that could not be recorded
	// is still a run that happened, so this never outranks the suite's result.
	if !individual {
		if _, err := res.Record(cfg.Root, t); err != nil {
			fmt.Fprintf(os.Stderr, "katana: recording test results: %v\n", err)
		}
	}

	printRunOutcome(out, res)

	if *save {
		path, err := r.WriteHTML(reportDir(cfg, *saveDir))
		if err != nil {
			err = fmt.Errorf("writing test report: %w", err)
			if res.ExitCode == 0 {
				return err
			}
			// The suite's own result outranks a failure to write the report.
			fmt.Fprintf(os.Stderr, "katana: %v\n", err)
		} else {
			fmt.Printf("report: %s (%d passed, %d failed, %d skipped)\n",
				displayPath(path), r.Passed(), r.Failed(), r.Skipped())
		}
	}

	if res.ExitCode != 0 {
		// Propagate the suite's own exit code so CI sees the real result.
		return exitError{code: res.ExitCode}
	}
	return nil
}

func runCasesIndividually(ctx context.Context, cfg *config.Config, tr *tracker.Tracker, req suite.Request, bar *operationBar) (*suite.Result, error) {
	var targets []*suite.Target
	seen := map[string]bool{}
	for source, entry := range tr.Entries {
		for _, name := range entry.Tests {
			if seen[name] {
				continue
			}
			seen[name] = true
			targets = append(targets, &suite.Target{Source: source, Output: entry.Output, Tests: []string{name}})
		}
	}
	if len(targets) == 0 {
		return suite.Run(ctx, cfg, req)
	}

	var all *suite.Result
	for _, target := range targets {
		bar.beginBatch(target.Tests[0])
		oneReq := req
		oneReq.Only = target
		// Each invocation gets a fresh capture stream. This prevents output from
		// the previous test batch being reused as the active batch's stdout.
		var batchOut, batchErr bytes.Buffer
		oneReq.Stdout = io.MultiWriter(&batchOut, bar.writer(os.Stdout))
		oneReq.Stderr = io.MultiWriter(&batchErr, bar.writer(os.Stderr))
		one, err := suite.Run(ctx, cfg, oneReq)
		if err != nil {
			return nil, err
		}
		if all == nil {
			all = one
			all.Cases = nil
			all.Output = ""
			all.ExitCode = 0
			all.Duration = 0
			all.Scope = ""
			all.Narrowed = false
		}
		all.Cases = append(all.Cases, one.Cases...)
		all.Output += one.Output
		all.Duration += one.Duration
		if one.ExitCode != 0 {
			all.ExitCode = one.ExitCode
		}
		all.Parsed = all.Parsed || one.Parsed
		bar.setCases(len(all.Cases))
		if _, err := one.Record(cfg.Root, tr); err != nil {
			fmt.Fprintf(os.Stderr, "katana: recording test results: %v\n", err)
		}
	}
	return all, nil
}

func mappedTests(t *tracker.Tracker) int {
	seen := map[string]bool{}
	for _, entry := range t.Entries {
		for _, name := range entry.Tests {
			seen[name] = true
		}
	}
	return len(seen)
}

// printRunOutcome states the result in one line, so a suite whose own output has
// scrolled past still ends with a verdict katana can colour.
func printRunOutcome(p ui.Printer, res *suite.Result) {
	verdict := p.Green("passed")
	if !res.OK() {
		verdict = p.Red(fmt.Sprintf("failed (exit %d)", res.ExitCode))
	}
	scope := ""
	if res.Scope != "" {
		scope = " — " + p.Cyan(res.Scope)
	}
	if !res.Parsed {
		fmt.Printf("\n%s in %s%s\n", verdict, res.Duration.Round(time.Millisecond), scope)
		printBlocked(p, res)
		return
	}
	pass, fail, skip := res.Counts()
	fmt.Printf("\n%s in %s%s — %s, %s, %s\n", verdict, res.Duration.Round(time.Millisecond), scope,
		p.Green(fmt.Sprintf("%d passed", pass)), failedText(p, fail),
		p.Dim(fmt.Sprintf("%d skipped", skip)))
	printBlocked(p, res)
}

// printBlocked calls out the suites that never ran, because the counts above
// cannot. A package that fails to compile reports one failing case in place of
// every test in it, so the difference between "one test broke" and "four hundred
// tests did not run" is invisible in a tally and has to be said outright.
func printBlocked(p ui.Printer, res *suite.Result) {
	blocked := res.Blocked()
	if len(blocked) == 0 {
		return
	}
	fmt.Fprintf(os.Stderr, "\n%s %d suite(s) did not run: they failed to build or set up, so none of their\n",
		p.Red("blocked:"), len(blocked))
	fmt.Fprintln(os.Stderr, "         test cases were run or recorded.")
	for _, s := range blocked {
		fmt.Fprintf(os.Stderr, "  %s\n", s)
	}
	fmt.Fprintln(os.Stderr, "  the runner's output above says why")
}

// failedText keeps a count of zero failures quiet: red is for a real one.
func failedText(p ui.Printer, n int) string {
	s := fmt.Sprintf("%d failed", n)
	if n == 0 {
		return p.Dim(s)
	}
	return p.Red(s)
}

// findBehavior locates a planned behavior by its source path.
func findBehavior(items []plan.Item, source string) (plan.Item, bool) {
	for _, it := range items {
		if it.Source == source {
			return it, true
		}
	}
	return plan.Item{}, false
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
func reportBehaviors(items []plan.Item) []report.Behavior {
	out := make([]report.Behavior, 0, len(items))
	for _, it := range items {
		out = append(out, report.Behavior{
			Source: it.Source,
			Output: it.Output,
			Status: it.Status.String(),
			Stack:  it.Stack(),
			Stale:  it.Stale(),
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
