package cli

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"sort"

	"github.com/adaptive-scale/katana/internal/config"
	"github.com/adaptive-scale/katana/internal/history"
	"github.com/adaptive-scale/katana/internal/plan"
	"github.com/adaptive-scale/katana/internal/results"
	"github.com/adaptive-scale/katana/internal/tracker"
	"github.com/adaptive-scale/katana/internal/ui"
)

func runStatus(args []string) error {
	fs := flag.NewFlagSet("katana status", flag.ContinueOnError)
	var only stringList
	var (
		dir    = fs.String("dir", "", "project directory (defaults to the current directory)")
		strict = fs.Bool("strict", false, "exit non-zero when any behavior is out of date")
		tests  = fs.Bool("tests", false, "list the test cases each behavior is mapped to")
		color  = fs.String("color", "auto", "colour the output: auto, always or never")
	)
	fs.Var(&only, "file", "limit to this behavior file (repeatable)")
	fs.Usage = func() {
		fmt.Fprint(os.Stderr, `Usage: katana status [flags]

Shows what the tracker holds, as a table: which behavior maps to which test file,
how many test cases came out of it, how many of them passed in the last
`+"`katana run`"+`, when it was last generated, how it has fared over the last few
runs, and which behaviors are now out of date with respect to their generated
tests.

Pass counts come from `+"`.katana/results.json`"+`, written by the last `+"`katana run`"+`;
status never runs the suite itself, so a case the last run did not cover counts
as neither passed nor failed. The RECENT column is one column per run from
`+"`.katana/history.json`"+`, oldest on the left, tall for a run that passed
entirely and red for one that did not.

Tracker entries whose behavior is no longer configured are listed separately;
`+"`katana generate`"+` prunes them.

--tests names every test case the tracker has recorded for each behavior, with
how it fared in the last run.

Colour is used when the output is a terminal. It is left off when it is not, and
NO_COLOR, CLICOLOR_FORCE and --color decide it outright.

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
	p := ui.For(os.Stdout)

	cfg, err := loadProject(*dir)
	if err != nil {
		return err
	}
	t, err := tracker.Load(cfg.Root)
	if err != nil {
		return err
	}
	items, err := plan.Build(cfg, t, only)
	if err != nil {
		return err
	}
	// A corrupt or unreadable results file costs the pass counts, not the
	// tracker report the command is really for, so it is reported and stepped
	// over rather than returned. The same goes for the history behind the chart.
	res, err := results.Load(cfg.Root)
	if err != nil {
		fmt.Fprintf(os.Stderr, "katana: %v\n", err)
		res = &results.Results{}
	}
	hist, err := history.Load(cfg.Root)
	if err != nil {
		fmt.Fprintf(os.Stderr, "katana: %v\n", err)
		hist = &history.History{}
	}

	printTrackerSummary(p, cfg, t)
	printRunSummary(p, res)
	printHistorySummary(p, hist)

	if len(items) == 0 {
		fmt.Println("\nno behaviors matched")
		return nil
	}

	fmt.Println()
	table := ui.NewTable("STATUS", "BEHAVIOR", "TESTS", "CASES", "PASSED", "RECENT", "GENERATED", "STACK").
		RightAlign(3, 4)
	stale, cases := 0, 0
	var tally results.Tally
	for _, it := range items {
		if it.Stale() {
			stale++
		}
		entry, mapped := t.Get(it.Source)
		count, passed, when, recent := p.Dim("-"), p.Dim("-"), p.Dim("-"), ""
		if mapped {
			n := caseCount(entry)
			cases += n
			count = fmt.Sprint(n)
			when = ui.Age(entry.GeneratedAt)
			behavior := res.Tally(entry.Tests)
			tally.Add(behavior)
			passed = p.PassedText(behavior)
			recent = p.BehaviorSpark(it.Source, hist.For(it.Source, sparkRuns))
		}
		table.Row(p.StatusText(it.Status), it.Source, it.Output, count, passed, recent, when, p.Dim(it.Stack()))
	}
	if err := table.MaxWidth(ui.TerminalWidth(os.Stdout)).Render(os.Stdout, p); err != nil {
		return err
	}

	if *tests {
		printTestIndex(p, t, items, res)
	}

	fmt.Printf("\n%d behavior(s), %s, %d test case(s) mapped%s\n",
		len(items), staleText(p, stale), cases, mappedOutcome(p, res, tally))

	// An orphan is a behavior the tracker still remembers but the config no
	// longer names — a deleted or renamed spec. It is reported, never counted as
	// out of date: there is nothing left to regenerate, only a record to drop.
	// A --file filter narrows which behaviors are shown, not which are
	// configured, so orphans are still whatever the whole config leaves over.
	orphans, err := orphaned(cfg, t)
	if err != nil {
		return err
	}
	if len(orphans) > 0 {
		fmt.Printf("\n%d tracker entry(ies) no longer in %s:\n", len(orphans), config.FileName)
		for _, e := range orphans {
			fmt.Printf("  %s → %s (%d case(s), generated %s)\n",
				p.Dim(e.Source), p.Dim(e.Output), caseCount(e), ui.Age(e.GeneratedAt))
		}
		fmt.Println("run `katana generate` to prune them")
	}

	if stale > 0 {
		fmt.Println("\nrun `katana generate` to bring them up to date")
		if *strict {
			return fmt.Errorf("%d behavior(s) out of date", stale)
		}
	}
	return nil
}

// sparkRuns is how many past runs the RECENT column plots. It is as many as fit
// in a column narrow enough to sit beside the rest of the table.
const sparkRuns = 12

// printTrackerSummary reports the state of the tracker file itself, so a run
// that finds nothing recorded says why — an absent tracker and an empty one read
// the same in the table below.
func printTrackerSummary(p ui.Printer, cfg *config.Config, t *tracker.Tracker) {
	path := tracker.Path(cfg.Root)
	if rel, err := filepath.Rel(cfg.Root, path); err == nil {
		path = filepath.ToSlash(rel)
	}
	if _, err := os.Stat(tracker.Path(cfg.Root)); err != nil {
		fmt.Printf("tracker  %s (%s)\n", p.Cyan(path), p.Yellow("not created yet; nothing has been generated"))
		return
	}
	fmt.Printf("tracker  %s (v%d, %d entry(ies), updated %s)\n",
		p.Cyan(path), t.Version, len(t.Entries), ui.Age(t.UpdatedAt))
}

// printRunSummary reports what the last `katana run` found, which is where every
// pass count below comes from. Saying when it ran is the point: a pass count is
// only as current as the run that produced it.
func printRunSummary(p ui.Printer, res *results.Results) {
	if !res.Recorded() {
		fmt.Printf("last run  %s\n", p.Dim("none recorded (run `katana run`)"))
		return
	}
	verdict := p.Green("passed")
	if !res.OK() {
		verdict = p.Red(fmt.Sprintf("failed (exit %d)", res.ExitCode))
	}
	scope := ""
	if res.Scope != "" {
		// A targeted run is not a verdict on the suite, and the counts that
		// follow are only the cases it covered.
		scope = fmt.Sprintf(" of %s", p.Cyan(res.Scope))
	}
	if !res.PerCase {
		// The output named no cases — a runner katana cannot read, or a run
		// that matched none — so the exit code is the whole of the outcome.
		fmt.Printf("last run  %s%s, %s (no per-case results in its output)\n", ui.Age(res.RanAt), scope, verdict)
		return
	}
	c := res.Counts()
	fmt.Printf("last run  %s%s, %s — %s of %d case(s) passed, %s, %d skipped\n",
		ui.Age(res.RanAt), scope, verdict,
		p.Green(fmt.Sprint(c.Pass)), c.Total(), failedText(p, c.Fail), c.Skip)
	if n := res.Inherited(); n > 0 {
		fmt.Printf("          %s\n",
			p.Dim(fmt.Sprintf("%d case(s) below are from earlier runs, which this one did not cover", n)))
	}
	// A suite that did not build has no per-case outcomes to show in the table
	// below, and the behaviors mapped into it read as "up to date" because they
	// are: their tests are current, and none of them ran.
	if blocked := res.BlockedSuites(); len(blocked) > 0 {
		fmt.Printf("          %s\n",
			p.Red(fmt.Sprintf("%d suite(s) failed to build, so none of their cases ran:", len(blocked))))
		for _, s := range blocked {
			fmt.Printf("          %s %s\n", p.Dim("·"), s)
		}
	}
}

// printHistorySummary is the chart of the runs before the last one: a column per
// run, so a suite that has been failing for a week does not read the same as one
// that broke this morning.
func printHistorySummary(p ui.Printer, h *history.History) {
	runs := h.Recent(sparkRuns * 2)
	if len(runs) == 0 {
		return
	}
	first, last := runs[0], runs[len(runs)-1]
	fmt.Printf("history   %s  %d run(s), %s to %s\n",
		p.RunSpark(runs), len(h.Runs), ui.Age(first.RanAt), ui.Age(last.RanAt))
}

// staleText colours the count of out-of-date behaviors in the summary line, so
// the one number a reader is looking for stands out of the sentence.
func staleText(p ui.Printer, stale int) string {
	s := fmt.Sprintf("%d out of date", stale)
	if stale == 0 {
		return p.Green(s)
	}
	return p.Yellow(s)
}

// mappedOutcome is the pass count for the summary line, empty when the last run
// covered none of the mapped cases — or when there was no last run.
//
// Where some outcomes were carried over from earlier runs — a targeted run
// updates one behavior and leaves the rest of the record standing — the line
// says so rather than crediting them all to the last one.
func mappedOutcome(p ui.Printer, res *results.Results, t results.Tally) string {
	if t.Known() == 0 {
		return ""
	}
	when := "in the last run"
	if res.Inherited() > 0 {
		when = "as of the run that last covered each"
	}
	s := fmt.Sprintf(", %s of %d passed %s", p.Green(fmt.Sprint(t.Pass)), t.Total(), when)
	if t.Unknown > 0 {
		s += p.Dim(fmt.Sprintf(" (%d case(s) it did not cover)", t.Unknown))
	}
	return s
}

// passedSuffix is the short form of the same count, for a heading the marks
// below already break down case by case.
func passedSuffix(t results.Tally) string {
	if t.Known() == 0 {
		return ""
	}
	return fmt.Sprintf(", %d of %d passed", t.Pass, t.Total())
}

// printTestIndex lists the test cases recorded for each behavior, marked with
// how each one fared in the last run. The index is what the last generation
// produced, not what the suite would run now, so a behavior generated by an
// older katana — or in a language testindex has no rules for — can be mapped to
// a file without naming any cases.
func printTestIndex(p ui.Printer, t *tracker.Tracker, items []plan.Item, res *results.Results) {
	fmt.Println()
	for _, it := range items {
		entry, ok := t.Get(it.Source)
		switch {
		case !ok:
			fmt.Printf("%s → %s (%s)\n", it.Source, it.Output, p.Dim("not generated yet"))
		case len(entry.Tests) == 0:
			fmt.Printf("%s → %s (%s)\n", it.Source, entry.Output, p.Dim("no test cases recorded"))
		default:
			fmt.Printf("%s → %s (%d case(s)%s)\n", it.Source, entry.Output, len(entry.Tests),
				passedSuffix(res.Tally(entry.Tests)))
			for _, name := range entry.Tests {
				status, known := res.Outcome(name)
				// An outcome older than the last run is dated, so a tick from a
				// run three days ago is not read as one from this morning.
				age := ""
				if ranAt, ok := res.CaseRanAt(name); ok && !ranAt.Equal(res.RanAt) {
					age = p.Dim(" (" + ui.Age(ranAt) + ")")
				}
				fmt.Printf("  %s %s%s\n", p.CaseMark(status, known), name, age)
			}
		}
	}
}

// orphaned returns the tracker entries whose behavior the config no longer
// resolves to, sorted by source.
func orphaned(cfg *config.Config, t *tracker.Tracker) ([]tracker.Entry, error) {
	resolved, err := cfg.Resolve()
	if err != nil {
		return nil, err
	}
	configured := make(map[string]bool, len(resolved))
	for _, r := range resolved {
		configured[r.Source] = true
	}

	var out []tracker.Entry
	for src, e := range t.Entries {
		if !configured[src] {
			out = append(out, e)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Source < out[j].Source })
	return out, nil
}

// caseCount is how many test cases an entry maps to. The index and its count are
// written together, but a tracker written by an older katana can carry the names
// alone, so the names win where there are any.
func caseCount(e tracker.Entry) int {
	if n := len(e.Tests); n > 0 {
		return n
	}
	return e.TestCount
}
