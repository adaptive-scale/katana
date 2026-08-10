package cli

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"text/tabwriter"
	"time"

	"github.com/adaptive-scale/katana/internal/config"
	"github.com/adaptive-scale/katana/internal/report"
	"github.com/adaptive-scale/katana/internal/results"
	"github.com/adaptive-scale/katana/internal/tracker"
)

func runStatus(args []string) error {
	fs := flag.NewFlagSet("katana status", flag.ContinueOnError)
	var only stringList
	var (
		dir    = fs.String("dir", "", "project directory (defaults to the current directory)")
		strict = fs.Bool("strict", false, "exit non-zero when any behavior is out of date")
		tests  = fs.Bool("tests", false, "list the test cases each behavior is mapped to")
	)
	fs.Var(&only, "file", "limit to this behavior file (repeatable)")
	fs.Usage = func() {
		fmt.Fprint(os.Stderr, `Usage: katana status [flags]

Shows what the tracker holds: which behavior maps to which test file, how many
test cases came out of it, how many of them passed in the last `+"`katana run`"+`,
when it was last generated, and which behaviors are now out of date with respect
to their generated tests.

Pass counts come from `+"`.katana/results.json`"+`, written by the last `+"`katana run`"+`;
status never runs the suite itself, so a case the last run did not cover counts
as neither passed nor failed.

Tracker entries whose behavior is no longer configured are listed separately;
`+"`katana generate`"+` prunes them.

--tests names every test case the tracker has recorded for each behavior, with
how it fared in the last run.

Flags:
`)
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		return err
	}

	cfg, err := loadProject(*dir)
	if err != nil {
		return err
	}
	t, err := tracker.Load(cfg.Root)
	if err != nil {
		return err
	}
	items, err := plan(cfg, t, only)
	if err != nil {
		return err
	}
	// A corrupt or unreadable results file costs the pass counts, not the
	// tracker report the command is really for, so it is reported and stepped
	// over rather than returned.
	res, err := results.Load(cfg.Root)
	if err != nil {
		fmt.Fprintf(os.Stderr, "katana: %v\n", err)
		res = &results.Results{}
	}

	printTrackerSummary(cfg, t)
	printRunSummary(res)

	if len(items) == 0 {
		fmt.Println("\nno behaviors matched")
		return nil
	}

	fmt.Println()
	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "STATUS\tBEHAVIOR\tTESTS\tCASES\tPASSED\tGENERATED\tSTACK")
	stale, cases := 0, 0
	var tally results.Tally
	for _, it := range items {
		if it.Status != tracker.StatusUpToDate {
			stale++
		}
		entry, mapped := t.Get(it.Source)
		count, passed, when := "-", "-", "-"
		if mapped {
			n := caseCount(entry)
			cases += n
			count = fmt.Sprint(n)
			when = age(entry.GeneratedAt)
			behavior := res.Tally(entry.Tests)
			tally.Add(behavior)
			passed = passedCell(behavior)
		}
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%s\t%s/%s via %s\n",
			it.Status, it.Source, it.Output, count, passed, when, it.Language, it.Framework, it.Harness)
	}
	if err := w.Flush(); err != nil {
		return err
	}

	if *tests {
		printTestIndex(t, items, res)
	}

	fmt.Printf("\n%d behavior(s), %d out of date, %d test case(s) mapped%s\n",
		len(items), stale, cases, mappedOutcome(tally))

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
				e.Source, e.Output, caseCount(e), age(e.GeneratedAt))
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

// printTrackerSummary reports the state of the tracker file itself, so a run
// that finds nothing recorded says why — an absent tracker and an empty one read
// the same in the table below.
func printTrackerSummary(cfg *config.Config, t *tracker.Tracker) {
	path := tracker.Path(cfg.Root)
	if rel, err := filepath.Rel(cfg.Root, path); err == nil {
		path = filepath.ToSlash(rel)
	}
	if _, err := os.Stat(tracker.Path(cfg.Root)); err != nil {
		fmt.Printf("tracker  %s (not created yet; nothing has been generated)\n", path)
		return
	}
	fmt.Printf("tracker  %s (v%d, %d entry(ies), updated %s)\n",
		path, t.Version, len(t.Entries), age(t.UpdatedAt))
}

// printRunSummary reports what the last `katana run` found, which is where every
// pass count below comes from. Saying when it ran is the point: a pass count is
// only as current as the run that produced it.
func printRunSummary(res *results.Results) {
	if !res.Recorded() {
		fmt.Println("last run  none recorded (run `katana run`)")
		return
	}
	verdict := "passed"
	if !res.OK() {
		verdict = fmt.Sprintf("failed (exit %d)", res.ExitCode)
	}
	if !res.PerCase {
		// The output named no cases — a runner katana cannot read, or a run
		// that matched none — so the exit code is the whole of the outcome.
		fmt.Printf("last run  %s, %s (no per-case results in its output)\n", age(res.RanAt), verdict)
		return
	}
	c := res.Counts()
	fmt.Printf("last run  %s, %s — %d of %d case(s) passed, %d failed, %d skipped\n",
		age(res.RanAt), verdict, c.Pass, c.Total(), c.Fail, c.Skip)
}

// passedCell renders one behavior's outcome as passed-out-of-mapped. Cases the
// last run has no outcome for stay in the denominator: they are cases the
// behavior owns, and calling them anything else would overstate the result.
func passedCell(t results.Tally) string {
	if t.Known() == 0 {
		return "-"
	}
	return fmt.Sprintf("%d/%d", t.Pass, t.Total())
}

// mappedOutcome is the pass count for the summary line, empty when the last run
// covered none of the mapped cases — or when there was no last run.
func mappedOutcome(t results.Tally) string {
	if t.Known() == 0 {
		return ""
	}
	s := fmt.Sprintf(", %d of %d passed in the last run", t.Pass, t.Total())
	if t.Unknown > 0 {
		s += fmt.Sprintf(" (%d case(s) it did not cover)", t.Unknown)
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
func printTestIndex(t *tracker.Tracker, items []item, res *results.Results) {
	fmt.Println()
	for _, it := range items {
		entry, ok := t.Get(it.Source)
		switch {
		case !ok:
			fmt.Printf("%s → %s (not generated yet)\n", it.Source, it.Output)
		case len(entry.Tests) == 0:
			fmt.Printf("%s → %s (no test cases recorded)\n", it.Source, entry.Output)
		default:
			fmt.Printf("%s → %s (%d case(s)%s)\n", it.Source, entry.Output, len(entry.Tests),
				passedSuffix(res.Tally(entry.Tests)))
			for _, name := range entry.Tests {
				fmt.Printf("  %s %s\n", caseMark(res, name), name)
			}
		}
	}
}

// caseMark is how one test case fared in the last run: a bullet when that run
// has nothing to say about it, so an unrun case never reads as a failed one.
func caseMark(res *results.Results, name string) string {
	status, ok := res.Outcome(name)
	if !ok {
		return "•"
	}
	switch status {
	case report.StatusFail:
		return "✗"
	case report.StatusSkip:
		return "○"
	}
	return "✓"
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

// age renders a timestamp as how long ago it was, which is what a reader of the
// tracker actually wants to know. Anything older than a week is a date instead:
// "63d ago" takes longer to place than the day it happened.
func age(ts time.Time) string {
	if ts.IsZero() {
		return "-"
	}
	d := time.Since(ts)
	switch {
	case d < 0:
		// A clock that moved backwards, or a tracker written on another machine.
		return ts.Local().Format("2006-01-02")
	case d < time.Minute:
		return "just now"
	case d < time.Hour:
		return fmt.Sprintf("%dm ago", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh ago", int(d.Hours()))
	case d < 7*24*time.Hour:
		return fmt.Sprintf("%dd ago", int(d.Hours()/24))
	default:
		return ts.Local().Format("2006-01-02")
	}
}
