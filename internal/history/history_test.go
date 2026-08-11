package history

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestRecordRoundTrip(t *testing.T) {
	root := t.TempDir()
	at := time.Now().Add(-time.Hour)

	if err := Record(root, Run{
		RanAt:    at,
		Command:  "go test ./... -v",
		ExitCode: 1,
		PerCase:  true,
		Pass:     2,
		Fail:     1,
		Behaviors: []Behavior{
			{Source: "behaviors/a.md", Pass: 2, Fail: 1},
		},
	}); err != nil {
		t.Fatal(err)
	}

	h, err := Load(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(h.Runs) != 1 {
		t.Fatalf("want 1 run, got %d", len(h.Runs))
	}
	got := h.Runs[0]
	if got.OK() {
		t.Error("a run that exited 1 did not pass")
	}
	if got.Total() != 3 || got.Rate() != 2.0/3.0 {
		t.Errorf("run tallies are wrong: %+v", got)
	}
	b, ok := got.Find("behaviors/a.md")
	if !ok {
		t.Fatal("the behavior was not recorded")
	}
	if rate, known := b.Rate(); !known || rate != 2.0/3.0 {
		t.Errorf("behavior rate = %v (known %v), want 2/3", rate, known)
	}
	if !got.RanAt.Equal(at.UTC()) {
		t.Errorf("RanAt = %v, want %v", got.RanAt, at.UTC())
	}
}

// TestHistoryIsBounded keeps the file from growing without limit: it is a
// picture of recent runs, not an archive.
func TestHistoryIsBounded(t *testing.T) {
	h := &History{Version: Version}
	base := time.Now().Add(-Max * time.Hour)
	for i := range Max + 10 {
		h.Add(Run{RanAt: base.Add(time.Duration(i) * time.Hour), Pass: i})
	}
	if len(h.Runs) != Max {
		t.Fatalf("kept %d runs, want %d", len(h.Runs), Max)
	}
	// The oldest go, not the newest.
	if h.Runs[len(h.Runs)-1].Pass != Max+9 {
		t.Errorf("the last run is not the newest: %+v", h.Runs[len(h.Runs)-1])
	}
}

// TestTotalsOutliveTheWindow is the point of keeping them: the run list is
// trimmed, and the count of what a project has run must not be trimmed with it.
func TestTotalsOutliveTheWindow(t *testing.T) {
	h := &History{Version: Version}
	base := time.Now().Add(-(Max + 10) * time.Hour)
	for i := range Max + 10 {
		h.Add(Run{
			RanAt:    base.Add(time.Duration(i) * time.Hour),
			ExitCode: i % 2, // every other run fails
			Millis:   1000,
			Pass:     2,
			Fail:     1,
			Skip:     1,
		})
	}

	got := h.Totals
	if got.Runs != Max+10 {
		t.Errorf("Totals.Runs = %d, want %d — the trimmed runs still happened", got.Runs, Max+10)
	}
	if got.Passed+got.Failed != got.Runs {
		t.Errorf("every run passed or failed: %d + %d != %d", got.Passed, got.Failed, got.Runs)
	}
	if got.Passed != (Max+10)/2 {
		t.Errorf("Totals.Passed = %d, want %d", got.Passed, (Max+10)/2)
	}
	if got.Cases() != 4*(Max+10) {
		t.Errorf("Totals.Cases() = %d, want %d", got.Cases(), 4*(Max+10))
	}
	if got.Duration() != time.Duration(Max+10)*time.Second {
		t.Errorf("Totals.Duration() = %s, want %ds", got.Duration(), Max+10)
	}
	if rate, known := got.Rate(); !known || rate != 0.5 {
		t.Errorf("Rate() = %v (known %v), want 0.5", rate, known)
	}
	// The window's ends are the runs it kept; the totals' ends are all of them.
	if !got.FirstRanAt.Equal(base.UTC()) {
		t.Errorf("FirstRanAt = %v, want the very first run at %v", got.FirstRanAt, base.UTC())
	}
	if want := base.Add(time.Duration(Max+9) * time.Hour).UTC(); !got.LastRanAt.Equal(want) {
		t.Errorf("LastRanAt = %v, want %v", got.LastRanAt, want)
	}
}

// TestTotalsSurviveARoundTrip keeps them on file rather than recomputing them
// from a window that no longer holds the runs they counted.
func TestTotalsSurviveARoundTrip(t *testing.T) {
	root := t.TempDir()
	at := time.Now().Add(-2 * time.Hour)
	for i := range 3 {
		if err := Record(root, Run{
			RanAt:    at.Add(time.Duration(i) * time.Minute),
			ExitCode: i,
			Millis:   500,
			Pass:     2,
		}); err != nil {
			t.Fatal(err)
		}
	}

	h, err := Load(root)
	if err != nil {
		t.Fatal(err)
	}
	if h.Totals.Runs != 3 || h.Totals.Passed != 1 || h.Totals.Failed != 2 {
		t.Errorf("totals did not survive the round trip: %+v", h.Totals)
	}
	if h.Totals.Pass != 6 || h.Totals.Millis != 1500 {
		t.Errorf("case and time totals are wrong: %+v", h.Totals)
	}

	// Loading does not count the runs a second time on top of what is on file.
	h.Add(Run{RanAt: time.Now(), Pass: 2})
	if err := h.Save(root); err != nil {
		t.Fatal(err)
	}
	again, err := Load(root)
	if err != nil {
		t.Fatal(err)
	}
	if again.Totals.Runs != 4 {
		t.Errorf("Totals.Runs = %d after one more run, want 4", again.Totals.Runs)
	}
}

// TestTotalsBackfilledFromAnOlderHistory covers the upgrade: a file written
// before totals were recorded has runs and no totals, and starting the count at
// what it can still see beats starting it at zero.
func TestTotalsBackfilledFromAnOlderHistory(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, ".katana"), 0o755); err != nil {
		t.Fatal(err)
	}
	old := `{"version":1,"runs":[
		{"ran_at":"2026-01-01T00:00:00Z","exit_code":0,"millis":100,"pass":3},
		{"ran_at":"2026-01-02T00:00:00Z","exit_code":1,"millis":100,"pass":2,"fail":1}
	]}`
	if err := os.WriteFile(Path(root), []byte(old), 0o644); err != nil {
		t.Fatal(err)
	}

	h, err := Load(root)
	if err != nil {
		t.Fatal(err)
	}
	if h.Totals.Runs != 2 || h.Totals.Passed != 1 || h.Totals.Failed != 1 {
		t.Errorf("totals were not backfilled from the runs on file: %+v", h.Totals)
	}
	if h.Totals.Pass != 5 || h.Totals.Fail != 1 {
		t.Errorf("case totals were not backfilled: %+v", h.Totals)
	}
	if !h.Totals.FirstRanAt.Equal(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)) {
		t.Errorf("FirstRanAt = %v, want the oldest run on file", h.Totals.FirstRanAt)
	}
}

// TestForSkipsRunsThatDidNotCoverTheBehavior is what keeps a behavior's
// histogram honest: a targeted run of another behavior says nothing about this
// one, and drawing it as an empty bar would read as a run in which it failed.
func TestForSkipsRunsThatDidNotCoverTheBehavior(t *testing.T) {
	h := &History{Version: Version}
	now := time.Now()
	h.Add(Run{RanAt: now.Add(-2 * time.Hour), Behaviors: []Behavior{{Source: "a.md", Pass: 1}}})
	h.Add(Run{RanAt: now.Add(-time.Hour), Scope: "b.md", Behaviors: []Behavior{{Source: "b.md", Pass: 3}}})
	h.Add(Run{RanAt: now, Behaviors: []Behavior{
		{Source: "a.md", Pass: 1},
		{Source: "b.md", Fail: 1},
	}})

	if got := len(h.For("a.md", 0)); got != 2 {
		t.Errorf("a.md was covered by 2 runs, For returned %d", got)
	}
	if got := len(h.For("b.md", 0)); got != 2 {
		t.Errorf("b.md was covered by 2 runs, For returned %d", got)
	}
	if got := h.For("a.md", 1); len(got) != 1 || !got[0].RanAt.Equal(now.UTC()) {
		t.Errorf("For(n) should return the newest runs, got %+v", got)
	}
	// A behavior the run listed but had no outcome for is not a data point.
	h.Add(Run{RanAt: now.Add(time.Hour), Behaviors: []Behavior{{Source: "a.md", Unknown: 2}}})
	if got := len(h.For("a.md", 0)); got != 2 {
		t.Errorf("a run with no outcomes for a.md should not be plotted, got %d runs", got)
	}
}

func TestLoadMissingIsEmpty(t *testing.T) {
	h, err := Load(t.TempDir())
	if err != nil {
		t.Fatalf("a project with no history is not an error: %v", err)
	}
	if len(h.Runs) != 0 || len(h.Recent(5)) != 0 {
		t.Errorf("want an empty history, got %+v", h)
	}
}

func TestLoadCorrupt(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, ".katana"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(Path(root), []byte("{not json"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(root); err == nil {
		t.Error("a corrupt history should be reported")
	}
	// Recording past it starts the history again rather than failing the run.
	if err := Record(root, Run{RanAt: time.Now()}); err != nil {
		t.Fatalf("Record: %v", err)
	}
	h, err := Load(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(h.Runs) != 1 {
		t.Errorf("want the new run recorded, got %d", len(h.Runs))
	}
}
