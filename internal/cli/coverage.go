package cli

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/adaptive-scale/katana/internal/config"
	"github.com/adaptive-scale/katana/internal/coverage"
	"github.com/adaptive-scale/katana/internal/suite"
	"github.com/adaptive-scale/katana/internal/ui"
)

func runCoverage(args []string) error {
	fs := flag.NewFlagSet("katana coverage", flag.ContinueOnError)
	var (
		dir      = fs.String("dir", "", "project directory (defaults to the current directory)")
		profile  = fs.String("profile", "", "read this coverage report instead of running the suite")
		by       = fs.String("by", "package", "group the table by: package or file")
		order    = fs.String("sort", "path", "order the rows by: path or coverage (least covered first)")
		min      = fs.Float64("min", 0, "fail when total coverage is below this percentage")
		save     = fs.String("save", "", "keep the coverage report at this path instead of discarding it")
		coverPkg = fs.String("coverpkg", "./...", "go only: which packages to measure; empty measures only the packages the tests are in")
		limit    = fs.Int("limit", 0, "show at most this many rows (0 shows all)")
		color    = fs.String("color", "auto", "colour the output: auto, always or never")
	)
	fs.Usage = func() {
		fmt.Fprint(os.Stderr, `Usage: katana coverage [flags] [-- extra args]

Runs the test command from katana.yaml with coverage turned on and reports how
much of the project's code the suite executed. Arguments after -- are appended
to the command, as `+"`katana run`"+` appends them.

katana measures nothing itself: it asks the runner the project already uses, and
reads what that runner writes. It knows how for

  go test         -coverprofile, and -coverpkg so that tests generated into a
                  directory of their own still measure the code they exercise
  pytest          --cov, via the pytest-cov plugin
  jest, vitest    --coverage, writing lcov
  mocha           run under nyc
  node --test     --experimental-test-coverage, writing lcov
  cargo test      run under cargo-llvm-cov

Any other runner can still be reported on: run its own coverage tool and point
katana at the result with --profile. Go cover profiles, LCOV and Cobertura XML
are all understood, whichever tool wrote them.

The raw report is discarded once it has been read. Compact project and per-file
totals are appended to `+"`.katana/coverage-history.json`"+` for trends in coverage,
status and the TUI. --save keeps the raw report too, which is what
`+"`go tool cover -html=coverage.out`"+` and the coverage viewers in CI want.

--min is the form for CI: it fails the command when total coverage is below the
percentage given, leaving the exit code to say so.

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
	grouped, err := parseGrouping(*by)
	if err != nil {
		return err
	}
	if *order != "path" && *order != "coverage" {
		return fmt.Errorf("invalid --sort %q (want path or coverage)", *order)
	}

	cfg, err := loadProject(*dir)
	if err != nil {
		return err
	}
	out := ui.For(os.Stdout)

	var (
		prof     *coverage.Profile
		exitCode int
		record   coverage.Run
	)
	if *profile != "" {
		prof, err = coverage.Load(*profile)
		if err != nil {
			return err
		}
		prof.Files = coverage.Localize(cfg.Root, prof.Files)
		fmt.Printf("report: %s\n", out.Cyan(displayPath(*profile)))
		record = coverage.Run{
			RanAt: time.Now(), Imported: true, Profile: displayPath(*profile),
			Format: prof.Format, Mode: prof.Mode, Files: prof.Files,
		}
	} else {
		var res *suite.Result
		prof, res, err = measure(cfg, out, fs.Args(), *coverPkg, *save)
		if err != nil {
			// A command which actually ran is part of the coverage history even
			// when its coverage tool failed to leave a readable report.
			if res != nil {
				recordCoverage(cfg.Root, out, coverage.Run{
					RanAt: res.StartedAt, Command: res.Command, ExitCode: res.ExitCode,
					Millis: res.Duration.Milliseconds(), Error: err.Error(),
				})
			}
			return err
		}
		exitCode = res.ExitCode
		record = coverage.Run{
			RanAt: res.StartedAt, Command: res.Command, ExitCode: res.ExitCode,
			Millis: res.Duration.Milliseconds(), Format: prof.Format, Mode: prof.Mode,
			Files: prof.Files,
		}
	}

	printCoverage(out, prof, grouped, *order, *limit)
	recordCoverage(cfg.Root, out, record)

	if exitCode != 0 {
		fmt.Fprintf(os.Stderr, "\n%s the suite failed (exit %d); the coverage above is of that run\n",
			ui.For(os.Stderr).Yellow("warning:"), exitCode)
	}
	if *min > 0 {
		// A hair of tolerance, so a suite at exactly the threshold is not failed
		// by the last bit of a float.
		if pct := prof.Total().Percent(); pct+1e-9 < *min {
			return fmt.Errorf("coverage is %.1f%%, below the %.1f%% required by --min", pct, *min)
		}
	}
	if exitCode != 0 {
		// A failed suite can still produce a useful coverage report. The warning
		// above preserves that information without making coverage itself fail;
		// --min remains the explicit gate for coverage quality.
	}
	return nil
}

// measure runs the project's suite with coverage turned on and reads what the
// runner wrote. The suite's exit code is returned rather than acted on: a
// failing suite still covered whatever it covered, and the report is worth
// printing before the command exits on it.
func measure(cfg *config.Config, out ui.Printer, extra []string, coverPkg, save string) (*coverage.Profile, *suite.Result, error) {
	if strings.TrimSpace(cfg.Test.Command) == "" {
		return nil, nil, errors.New("no test.command set in katana.yaml")
	}

	// The raw report is written somewhere temporary and copied out only if asked
	// for. The compact local history is deliberate state; a runner-specific
	// artifact at the project root would only leave the next git status to explain.
	dest, err := os.MkdirTemp("", "katana-coverage-")
	if err != nil {
		return nil, nil, err
	}
	defer os.RemoveAll(dest)

	in := coverage.For(coverage.Options{
		Framework: cfg.Defaults.Framework,
		Command:   cfg.Test.Command,
		Dest:      dest,
		Packages:  coverPkg,
	})
	if !in.Known {
		return nil, nil, unknownRunner(cfg)
	}
	if in.Note != "" {
		fmt.Fprintf(os.Stderr, "%s %s\n", out.Dim("note:"), in.Note)
	}
	if in.Tool != "" {
		fmt.Fprintf(os.Stderr, "%s coverage here needs %s\n", out.Dim("note:"), in.Tool)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	req := suite.Request{
		Command: in.Command,
		Extra:   append(append([]string(nil), in.Args...), extra...),
		Stdout:  os.Stdout,
		Stderr:  os.Stderr,
		Stdin:   os.Stdin,
	}
	res, err := suite.Run(ctx, cfg, req)
	if err != nil {
		return nil, nil, err
	}
	fmt.Printf("\nran: %s\n", out.Cyan(res.Command))
	fmt.Printf("in:  %s\n", res.Duration.Round(time.Millisecond))

	prof, err := coverage.Load(in.Profile)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, res, missingReport(in, res.ExitCode)
		}
		return nil, res, err
	}
	prof.Files = coverage.Localize(cfg.Root, prof.Files)

	if save != "" {
		path := saveProfile(cfg, in.Profile, save)
		if path != "" {
			fmt.Printf("report: %s\n", out.Cyan(displayPath(path)))
			if prof.Format == coverage.FormatGo {
				fmt.Printf("        %s\n", out.Dim("go tool cover -html="+displayPath(path)))
			}
		}
	}
	return prof, res, nil
}

// recordCoverage appends the compact observation and prints the trend it now
// belongs to. A local-state write failure never outranks a usable report or the
// suite's own result.
func recordCoverage(root string, p ui.Printer, run coverage.Run) {
	h, err := coverage.LoadHistory(root)
	if err == nil {
		h.Add(run)
		err = h.SaveHistory(root)
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "katana: recording coverage history: %v\n", err)
		return
	}
	printCoverageHistory(p, h)
}

// printCoverageHistory is shared by coverage and status: a compact chart and
// lifetime statistics, followed by a like-for-like comparison of the newest
// two measurable reports.
func printCoverageHistory(p ui.Printer, h *coverage.History) {
	stats := h.Stats()
	if stats.Runs == 0 {
		return
	}
	if stats.Measured == 0 {
		fmt.Printf("coverage  %d run(s) recorded; none measured any statements\n", stats.Runs)
		return
	}
	runs := h.Recent(sparkRuns * 2)
	latest := runs[len(runs)-1]
	extra := ""
	if stats.Measured != stats.Runs {
		extra = fmt.Sprintf(", %d empty", stats.Runs-stats.Measured)
	}
	fmt.Printf("coverage  %s  latest %s, %d run(s)%s — avg %.1f%%, min %.1f%%, max %.1f%%\n",
		p.CoverageSpark(runs), coveragePercentText(p, latest.Percent()), stats.Runs, extra,
		stats.Average, stats.Min, stats.Max)

	previous, current, ok := h.LatestTwo()
	if !ok {
		return
	}
	delta := current.Percent() - previous.Percent()
	changes := coverage.Changes(previous, current)
	regressed, improved := 0, 0
	var worst *coverage.Change
	for i := range changes {
		switch {
		case changes[i].PercentagePoints < 0:
			regressed++
			if worst == nil {
				worst = &changes[i]
			}
		case changes[i].PercentagePoints > 0:
			improved++
		}
	}
	fmt.Printf("          %s since %s; %d file(s) improved, %d regressed",
		coverageDeltaText(p, delta), ui.Age(previous.RanAt), improved, regressed)
	if worst != nil {
		fmt.Printf(" — worst %s (%s)", worst.Path, coverageDeltaText(p, worst.PercentagePoints))
	}
	fmt.Println()
}

func coverageDeltaText(p ui.Printer, points float64) string {
	s := fmt.Sprintf("%+.1f points", points)
	switch {
	case points > 0:
		return p.Green(s)
	case points < 0:
		return p.Red(s)
	default:
		return p.Dim(s)
	}
}

// unknownRunner explains the one thing katana cannot do here, and the way round
// it. A project katana has no coverage flags for is not a project without
// coverage: its own tool writes a report katana can still read.
func unknownRunner(cfg *config.Config) error {
	var b strings.Builder
	fmt.Fprintf(&b, "katana does not know how to turn coverage on for %s",
		describeRunner(cfg))
	b.WriteString("\n  run your project's own coverage tool, then point katana at what it wrote:")
	b.WriteString("\n    katana coverage --profile <report>")
	b.WriteString("\n  Go cover profiles, LCOV and Cobertura XML are all understood")
	if found, ok := coverage.Find(cfg.Root); ok {
		fmt.Fprintf(&b, "\n  this project already has one at %s", displayPath(found))
	}
	return errors.New(b.String())
}

func describeRunner(cfg *config.Config) string {
	if f := strings.TrimSpace(cfg.Defaults.Framework); f != "" {
		return fmt.Sprintf("%q", f)
	}
	return fmt.Sprintf("the test command %q", strings.TrimSpace(cfg.Test.Command))
}

// missingReport is the failure worth explaining twice: the suite ran, and the
// coverage file it was told to write is not there. Nearly always the plugin the
// runner needs is not installed, and the runner said so a screen ago.
func missingReport(in coverage.Instrumentation, exitCode int) error {
	var b strings.Builder
	b.WriteString("the test run wrote no coverage report")
	if exitCode != 0 {
		fmt.Fprintf(&b, " and exited %d", exitCode)
	}
	if in.Tool != "" {
		fmt.Fprintf(&b, "\n  coverage here needs %s; the runner's output above says whether it was found", in.Tool)
	}
	b.WriteString("\n  a report written by another tool can be read with `katana coverage --profile <report>`")
	return errors.New(b.String())
}

// saveProfile copies the report out of the temporary directory it was written
// to. A copy that fails costs the kept file, not the coverage report the
// command is really for, so it is reported and stepped over.
func saveProfile(cfg *config.Config, from, to string) string {
	path := to
	if !filepath.IsAbs(path) {
		path = filepath.Join(cfg.Root, filepath.FromSlash(to))
	}
	data, err := os.ReadFile(from)
	if err == nil {
		err = os.MkdirAll(filepath.Dir(path), 0o755)
	}
	if err == nil {
		err = os.WriteFile(path, data, 0o644)
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "katana: keeping the coverage report: %v\n", err)
		return ""
	}
	return path
}

func parseGrouping(by string) (bool, error) {
	switch strings.ToLower(strings.TrimSpace(by)) {
	case "", "package", "packages", "dir", "directory":
		return true, nil
	case "file", "files":
		return false, nil
	}
	return false, fmt.Errorf("invalid --by %q (want package or file)", by)
}

// printCoverage draws the table and the total under it.
func printCoverage(p ui.Printer, prof *coverage.Profile, byPackage bool, order string, limit int) {
	total := prof.Total()
	if prof.Empty() {
		fmt.Printf("\n%s\n", p.Yellow("the report measured no statements at all"))
		fmt.Println(p.Dim("  the runner recorded coverage for nothing; check that the tests ran, and\n" +
			"  for go, that --coverpkg names the packages the tests exercise"))
		return
	}

	rows := prof.Files
	heading := "FILE"
	if byPackage {
		rows, heading = prof.ByDir(), "PACKAGE"
	}
	if order == "coverage" {
		rows = coverage.SortByCoverage(rows)
	}
	shown := rows
	if limit > 0 && len(shown) > limit {
		shown = shown[:limit]
	}

	fmt.Println()
	table := ui.NewTable(heading, "STMTS", "COVERED", "MISSED", "COVERAGE").RightAlign(1, 2, 3)
	for _, f := range shown {
		table.Row(f.Path, fmt.Sprint(f.Statements), fmt.Sprint(f.Covered), missedText(p, f.Missed()),
			coverageCell(p, f))
	}
	if err := table.MaxWidth(ui.TerminalWidth(os.Stdout)).Render(os.Stdout, p); err != nil {
		fmt.Fprintf(os.Stderr, "katana: %v\n", err)
	}
	if len(shown) < len(rows) {
		fmt.Printf("%s\n", p.Dim(fmt.Sprintf("  %d of %d rows shown (--limit)", len(shown), len(rows))))
	}

	fmt.Printf("\ntotal %s of %d statement(s) — %d covered, %s\n",
		coverageText(p, total.Percent()), total.Statements, total.Covered,
		missedText(p, total.Missed())+" missed")
	source := prof.Format + " report"
	if prof.Mode != "" {
		source += ", mode " + prof.Mode
	}
	fmt.Printf("%s\n", p.Dim(fmt.Sprintf("%d file(s) measured, from a %s", len(prof.Files), source)))
}

// coverageCell is a bar and its percentage, which read faster together than
// either does alone: the bar is scanned down the column, the number is read.
func coverageCell(p ui.Printer, f coverage.File) string {
	if f.Statements == 0 {
		return p.Dim("no statements")
	}
	pct := f.Percent()
	return p.Paint(ui.Bar(pct/100, 12), coverageStyle(pct)) + " " + coverageText(p, pct)
}

func coverageText(p ui.Printer, pct float64) string {
	return p.Paint(fmt.Sprintf("%5.1f%%", pct), coverageStyle(pct))
}

func coveragePercentText(p ui.Printer, pct float64) string {
	return p.Paint(fmt.Sprintf("%.1f%%", pct), coverageStyle(pct))
}

// coverageStyle colours a percentage the way a reader already reads one: green
// is fine, yellow is thin, red is a file the suite barely touches. The
// thresholds are conventional rather than measured, and are only ever a colour:
// katana fails a run on --min, never on a hue.
func coverageStyle(pct float64) ui.Style {
	switch {
	case pct >= 80:
		return ui.Green
	case pct >= 50:
		return ui.Yellow
	default:
		return ui.Red
	}
}

// missedText leaves a fully covered row uncoloured: red on a zero reads as a
// problem where there is none.
func missedText(p ui.Printer, n int) string {
	s := fmt.Sprint(n)
	if n == 0 {
		return p.Dim(s)
	}
	return s
}
