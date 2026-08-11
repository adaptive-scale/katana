package cli

import (
	"fmt"
	"io"
	"os"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/adaptive-scale/katana/internal/history"
	"github.com/adaptive-scale/katana/internal/report"
	"github.com/adaptive-scale/katana/internal/results"
	"github.com/adaptive-scale/katana/internal/tracker"
)

// TestStatusReportsTrackerMapping drives the real command after a real
// generation: status has to say what the tracker holds — the file it was read
// from, which behavior maps to which test file, and the cases that came out of
// it — without running the suite.
func TestStatusReportsTrackerMapping(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the stand-in harness is a shell script")
	}
	root := fakeProject(t, 2)
	if err := runGenerate([]string{"--dir", root}); err != nil {
		t.Fatalf("generate: %v", err)
	}

	out := captureStdout(t, func() {
		if err := runStatus([]string{"--dir", root, "--tests"}); err != nil {
			t.Fatalf("status: %v", err)
		}
	})

	for _, want := range []string{
		".katana/tracker.json",
		"2 entry(ies)",
		"behaviors/b0.md",
		"tests/b0_test.go",
		"• TestGenerated",
		"2 behavior(s), 0 out of date, 2 test case(s) mapped",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("status output is missing %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "run `katana generate` to bring them up to date") {
		t.Errorf("nothing is stale, so nothing should be prompted for:\n%s", out)
	}
}

// TestStatusListsOrphanedEntries covers the half of the tracker the table cannot
// show: a behavior that was deleted still has an entry, and status is where that
// becomes visible rather than at the next generate.
func TestStatusListsOrphanedEntries(t *testing.T) {
	root := fakeProject(t, 1)

	tr, err := tracker.Load(root)
	if err != nil {
		t.Fatal(err)
	}
	tr.Record(tracker.Entry{
		Source:      "behaviors/deleted.md",
		Output:      "tests/deleted_test.go",
		Tests:       []string{"TestOld", "TestOlder"},
		Language:    "go",
		Framework:   "go-test",
		Harness:     "fake",
		GeneratedAt: time.Now().UTC().Add(-2 * time.Hour),
	})
	if err := tr.Save(); err != nil {
		t.Fatal(err)
	}

	out := captureStdout(t, func() {
		if err := runStatus([]string{"--dir", root}); err != nil {
			t.Fatalf("status: %v", err)
		}
	})

	if !strings.Contains(out, "behaviors/deleted.md → tests/deleted_test.go (2 case(s), generated 2h ago)") {
		t.Errorf("the orphaned entry was not reported:\n%s", out)
	}
	// The orphan has nothing left to regenerate, so it is not one of the
	// behaviors the run counts, and --strict must not fail on it.
	if !strings.Contains(out, "1 behavior(s), 1 out of date") {
		t.Errorf("the orphan should not be counted as a behavior:\n%s", out)
	}
}

// TestStatusStrictFailsOnStaleBehaviors is the CI contract: the command reports
// as usual and then exits non-zero.
func TestStatusStrictFailsOnStaleBehaviors(t *testing.T) {
	root := fakeProject(t, 1)

	var err error
	out := captureStdout(t, func() {
		err = runStatus([]string{"--dir", root, "--strict"})
	})
	if err == nil {
		t.Fatal("--strict should fail while a behavior is ungenerated")
	}
	if !strings.Contains(out, "not created yet") {
		t.Errorf("an absent tracker should say so:\n%s", out)
	}

	if err := runStatus([]string{"--dir", root}); err != nil {
		t.Errorf("without --strict the same state is not an error: %v", err)
	}
}

// TestStatusReportsWhatPassed is the other half of the tracker report: how the
// mapped test cases fared the last time the suite ran, counted per behavior and
// for the project, without running anything.
func TestStatusReportsWhatPassed(t *testing.T) {
	root := fakeProject(t, 1)

	tr, err := tracker.Load(root)
	if err != nil {
		t.Fatal(err)
	}
	tr.Record(tracker.Entry{
		Source:      "behaviors/b0.md",
		Output:      "tests/b0_test.go",
		Tests:       []string{"TestPasses", "TestFails", "TestNeverRan"},
		Language:    "go",
		Framework:   "go-test",
		Harness:     "fake",
		GeneratedAt: time.Now().UTC().Add(-time.Hour),
	})
	if err := tr.Save(); err != nil {
		t.Fatal(err)
	}

	run := results.Record("go test ./... -v", time.Now().Add(-5*time.Minute), 1, true, []report.Case{
		{Name: "TestPasses", Status: report.StatusPass},
		{Name: "TestFails", Status: report.StatusFail},
	})
	if err := run.Save(root); err != nil {
		t.Fatal(err)
	}

	out := captureStdout(t, func() {
		if err := runStatus([]string{"--dir", root, "--tests"}); err != nil {
			t.Fatalf("status: %v", err)
		}
	})

	for _, want := range []string{
		"last run  5m ago, failed (exit 1) — 1 of 2 case(s) passed, 1 failed, 0 skipped",
		// One of the behavior's three cases passed; the third was not in the run.
		"1/3",
		"1 of 3 passed in the last run (1 case(s) it did not cover)",
		"✓ TestPasses",
		"✗ TestFails",
		"• TestNeverRan",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("status output is missing %q:\n%s", want, out)
		}
	}
}

// TestStatusWithoutARun keeps an unrun project honest: no case may be reported
// as passing or failing when nothing has run.
func TestStatusWithoutARun(t *testing.T) {
	root := fakeProject(t, 1)

	out := captureStdout(t, func() {
		_ = runStatus([]string{"--dir", root})
	})

	if !strings.Contains(out, "last run  none recorded (run `katana run`)") {
		t.Errorf("status should say no run has been recorded:\n%s", out)
	}
	if strings.Contains(out, "passed in the last run") {
		t.Errorf("nothing ran, so nothing passed:\n%s", out)
	}
}

func TestCaseCount(t *testing.T) {
	cases := map[string]struct {
		entry tracker.Entry
		want  int
	}{
		"names and count agree": {tracker.Entry{Tests: []string{"A", "B"}, TestCount: 2}, 2},
		"names win":             {tracker.Entry{Tests: []string{"A", "B"}, TestCount: 0}, 2},
		"count alone":           {tracker.Entry{TestCount: 3}, 3},
		"nothing recorded":      {tracker.Entry{}, 0},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			if got := caseCount(c.entry); got != c.want {
				t.Errorf("caseCount = %d, want %d", got, c.want)
			}
		})
	}
}

// captureStdout runs fn with os.Stdout redirected and returns what it printed.
func captureStdout(t *testing.T, fn func()) string {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	saved := os.Stdout
	os.Stdout = w

	done := make(chan string, 1)
	go func() {
		b, _ := io.ReadAll(r)
		done <- string(b)
	}()

	func() {
		defer func() {
			os.Stdout = saved
			w.Close()
		}()
		fn()
	}()
	return <-done
}

// TestStatusChartsRecentRuns covers the two things the table gained: a column
// per recent run for each behavior, and a line saying when a run only covered
// one of them — a targeted run is not a verdict on the whole suite.
func TestStatusChartsRecentRuns(t *testing.T) {
	root := fakeProject(t, 1)

	tr, err := tracker.Load(root)
	if err != nil {
		t.Fatal(err)
	}
	tr.Record(tracker.Entry{
		Source:      "behaviors/b0.md",
		Output:      "tests/b0_test.go",
		Tests:       []string{"TestOne", "TestTwo"},
		Language:    "go",
		Framework:   "go-test",
		Harness:     "fake",
		GeneratedAt: time.Now().UTC().Add(-time.Hour),
	})
	if err := tr.Save(); err != nil {
		t.Fatal(err)
	}

	run := results.Record("go test ./... -run '^(TestOne|TestTwo)$' -v", time.Now().Add(-time.Minute), 0, true,
		[]report.Case{
			{Name: "TestOne", Status: report.StatusPass},
			{Name: "TestTwo", Status: report.StatusPass},
		})
	run.Scope = "behaviors/b0.md"
	if err := run.Save(root); err != nil {
		t.Fatal(err)
	}
	for i := range 3 {
		if err := history.Record(root, history.Run{
			RanAt:   time.Now().Add(-time.Duration(3-i) * time.Hour),
			Command: "go test ./... -v",
			PerCase: true,
			Pass:    2,
			Behaviors: []history.Behavior{
				{Source: "behaviors/b0.md", Pass: 2},
			},
		}); err != nil {
			t.Fatal(err)
		}
	}

	out := captureStdout(t, func() {
		if err := runStatus([]string{"--dir", root}); err != nil {
			t.Fatalf("status: %v", err)
		}
	})

	for _, want := range []string{
		"RECENT",
		// The run only covered this behavior, and status has to say so.
		"of behaviors/b0.md",
		"history",
		// Three runs in which everything passed, drawn as three full columns.
		"███",
		// And how much has been run here in total, which the chart stops
		// counting once the history is full.
		"totals",
		"3 run(s) since",
		"3 passed",
		"6 case outcome(s) recorded",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("status output is missing %q:\n%s", want, out)
		}
	}
}

// TestStatusTotalsCountRunsTheHistoryHasDropped is what the totals line is for:
// once a project has run more than the history keeps, the chart's count stops
// climbing and the totals line is the only place the real number is left.
func TestStatusTotalsCountRunsTheHistoryHasDropped(t *testing.T) {
	root := fakeProject(t, 1)

	h, err := history.Load(root)
	if err != nil {
		t.Fatal(err)
	}
	base := time.Now().Add(-(history.Max + 5) * time.Hour)
	for i := range history.Max + 5 {
		h.Add(history.Run{
			RanAt:   base.Add(time.Duration(i) * time.Hour),
			Command: "go test ./... -v",
			Millis:  1500,
			PerCase: true,
			Pass:    2,
		})
	}
	if err := h.Save(root); err != nil {
		t.Fatal(err)
	}

	out := captureStdout(t, func() {
		if err := runStatus([]string{"--dir", root}); err != nil {
			t.Fatalf("status: %v", err)
		}
	})

	if want := fmt.Sprintf("%d run(s) since", history.Max+5); !strings.Contains(out, want) {
		t.Errorf("status should report every run recorded (%q):\n%s", want, out)
	}
	if want := fmt.Sprintf("%d run(s), ", history.Max); !strings.Contains(out, want) {
		t.Errorf("the history line should still report the window of %d runs:\n%s", history.Max, out)
	}
}
