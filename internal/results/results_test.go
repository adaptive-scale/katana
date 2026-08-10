package results

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/adaptive-scale/katana/internal/report"
)

func TestSaveAndReload(t *testing.T) {
	root := t.TempDir()
	ran := time.Now().UTC().Add(-time.Hour)
	rec := Record("go test ./... -v", ran, 1, true, []report.Case{
		{Suite: "pkg", Name: "TestOne", Status: report.StatusPass},
		{Suite: "pkg", Name: "TestTwo", Status: report.StatusFail},
	})
	if err := rec.Save(root); err != nil {
		t.Fatalf("Save: %v", err)
	}

	again, err := Load(root)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if !again.Recorded() || again.OK() {
		t.Errorf("a failed run did not survive the round trip: %+v", again)
	}
	if again.Command != "go test ./... -v" || len(again.Cases) != 2 {
		t.Errorf("results corrupted: %+v", again)
	}
	if c := again.Counts(); c.Pass != 1 || c.Fail != 1 || c.Total() != 2 {
		t.Errorf("counts = %+v, want one pass and one failure", c)
	}
}

// TestLoadWithoutARun is the state `katana status` sees before anything has been
// run: an empty record that answers every query rather than an error.
func TestLoadWithoutARun(t *testing.T) {
	r, err := Load(t.TempDir())
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if r.Recorded() {
		t.Error("nothing has been run, so nothing should be recorded")
	}
	tally := r.Tally([]string{"TestOne", "TestTwo"})
	if tally.Known() != 0 || tally.Unknown != 2 || tally.Total() != 2 {
		t.Errorf("tally = %+v, want both cases unknown", tally)
	}
	if _, ok := r.Outcome("TestOne"); ok {
		t.Error("an unrun case has no outcome")
	}
}

func TestSaveIsAtomic(t *testing.T) {
	root := t.TempDir()
	if err := Record("go test", time.Now(), 0, true, nil).Save(root); err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(filepath.Join(root, ".katana"))
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if e.Name() != FileName {
			t.Errorf("unexpected leftover file %q", e.Name())
		}
	}
}

func TestCorruptResultsAreReported(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, ".katana"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(Path(root), []byte("{not json"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(root); err == nil {
		t.Fatal("a corrupt results file should be reported, not read as a passing run")
	}
}

func TestVersionMismatchIsRejected(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, ".katana"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(Path(root), []byte(`{"version":99}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(root); err == nil {
		t.Fatal("results from a future katana should not be read as this schema")
	}
}

// TestTallyAttributesTheCasesOfABehavior is what the status table is built from:
// the tracker's list of test names for one behavior, scored against the run.
func TestTallyAttributesTheCasesOfABehavior(t *testing.T) {
	r := Record("go test -v", time.Now(), 1, true, []report.Case{
		{Name: "TestAppliesDiscount", Status: report.StatusPass},
		{Name: "TestRejectsExpiredCode", Status: report.StatusFail},
		{Name: "TestSkipped", Status: report.StatusSkip},
		{Name: "TestSomeoneElse", Status: report.StatusPass},
	})

	tally := r.Tally([]string{"TestAppliesDiscount", "TestRejectsExpiredCode", "TestSkipped", "TestNeverRan"})
	want := Tally{Pass: 1, Fail: 1, Skip: 1, Unknown: 1}
	if tally != want {
		t.Errorf("tally = %+v, want %+v", tally, want)
	}
	// A case belonging to another behavior is not counted here, but it is still
	// part of the run's own totals.
	if c := r.Counts(); c.Pass != 2 || c.Total() != 4 {
		t.Errorf("run counts = %+v, want 2 of 4 passed", c)
	}
}

// TestSubtestsCountTowardsTheirParent covers what `go test -v` actually prints:
// the tracker indexes TestX, the runner reports TestX and TestX/case.
func TestSubtestsCountTowardsTheirParent(t *testing.T) {
	r := Record("go test -v", time.Now(), 1, true, []report.Case{
		{Name: "TestParent", Status: report.StatusFail},
		{Name: "TestParent/happy_path", Status: report.StatusPass},
		{Name: "TestParent/sad_path", Status: report.StatusFail},
		{Name: "TestOnlySubtests/one", Status: report.StatusPass},
		{Name: "TestSkippedThroughout/one", Status: report.StatusSkip},
	})

	cases := map[string]report.Status{
		"TestParent":            report.StatusFail,
		"TestParent/happy_path": report.StatusPass,
		// A parent reported only through its subtests still has an outcome.
		"TestOnlySubtests":      report.StatusPass,
		"TestSkippedThroughout": report.StatusSkip,
	}
	for name, want := range cases {
		got, ok := r.Outcome(name)
		if !ok {
			t.Errorf("%s has no recorded outcome", name)
			continue
		}
		if got != want {
			t.Errorf("Outcome(%q) = %q, want %q", name, got, want)
		}
	}
}

// TestSubtestNamesMatchTheirSource keeps the lookup honest about how go renames
// subtests: the source says "handles empty carts", the runner prints
// "TestX/handles_empty_carts".
func TestSubtestNamesMatchTheirSource(t *testing.T) {
	r := Record("go test -v", time.Now(), 0, true, []report.Case{
		{Name: "TestCart/handles_empty_carts", Status: report.StatusPass},
	})
	if _, ok := r.Outcome("TestCart/handles empty carts"); !ok {
		t.Error("a subtest name with spaces should match the runner's underscored form")
	}
}

// TestUnparsedRunHasNoPerCaseOutcomes covers a runner katana cannot read: the
// suite result is recorded, but no case may be reported as passing on the
// strength of it.
func TestUnparsedRunHasNoPerCaseOutcomes(t *testing.T) {
	r := Record("make test", time.Now(), 0, false, []report.Case{
		{Suite: "test suite", Name: "make test", Status: report.StatusPass},
	})
	if !r.Recorded() || !r.OK() {
		t.Error("the run itself is still recorded")
	}
	if _, ok := r.Outcome("TestOne"); ok {
		t.Error("a suite-wide pass says nothing about an individual case")
	}
	if tally := r.Tally([]string{"TestOne"}); tally.Unknown != 1 {
		t.Errorf("tally = %+v, want the case unknown", tally)
	}
}
