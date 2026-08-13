package coverage

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestCoverageHistoryRoundTripAndStatistics(t *testing.T) {
	root := t.TempDir()
	base := time.Date(2026, 8, 10, 12, 0, 0, 0, time.FixedZone("test", 2*60*60))
	h := &History{Version: HistoryVersion}
	h.Add(Run{
		RanAt: base, Imported: true, Profile: "coverage.out", Format: FormatGo,
		Files: []File{{Path: "a.go", Statements: 10, Covered: 5}},
	})
	h.Add(Run{
		RanAt: base.Add(time.Hour), Command: "go test ./...", Millis: 1250,
		Files: []File{{Path: "a.go", Statements: 10, Covered: 9}},
	})
	// An attempted coverage run without a usable report is retained, but is not
	// averaged in as zero coverage.
	h.Add(Run{RanAt: base.Add(2 * time.Hour), Command: "go test ./...", ExitCode: 1, Error: "no report"})
	if err := h.SaveHistory(root); err != nil {
		t.Fatal(err)
	}

	got, err := LoadHistory(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Runs) != 3 {
		t.Fatalf("runs = %d, want every one of 3", len(got.Runs))
	}
	if !got.Runs[0].RanAt.Equal(base.UTC()) || !got.Runs[0].Imported {
		t.Errorf("first run did not round trip in UTC: %+v", got.Runs[0])
	}
	if got.Runs[0].Files[0].Path != "a.go" || got.Runs[0].Files[0].Covered != 5 {
		t.Errorf("file summary did not round trip: %+v", got.Runs[0].Files)
	}

	stats := got.Stats()
	if stats.Runs != 3 || stats.Measured != 2 {
		t.Errorf("stats counts = %+v, want 3 observations and 2 measurements", stats)
	}
	if stats.Average != 70 || stats.Min != 50 || stats.Max != 90 {
		t.Errorf("coverage stats = %+v, want avg 70, min 50, max 90", stats)
	}
	if recent := got.Recent(0); len(recent) != 2 {
		t.Errorf("Recent plots %d observations, want the 2 measurable ones", len(recent))
	}
	before, after, ok := got.LatestTwo()
	if !ok || before.Percent() != 50 || after.Percent() != 90 {
		t.Errorf("LatestTwo = %+v, %+v, %v", before, after, ok)
	}
}

// TestCoverageHistoryKeepsEveryCompactRun pins the difference from test-run
// history: coverage trends retain every summary rather than trimming a window.
func TestCoverageHistoryKeepsEveryCompactRun(t *testing.T) {
	h := &History{Version: HistoryVersion}
	for i := range 125 {
		h.Add(Run{
			RanAt: time.Now().Add(time.Duration(i) * time.Minute),
			Files: []File{{Path: "a.go", Statements: 100, Covered: i % 101}},
		})
	}
	if len(h.Runs) != 125 {
		t.Fatalf("history retained %d runs, want all 125", len(h.Runs))
	}
	if recent := h.Recent(12); len(recent) != 12 || recent[11].Percent() != 23 {
		t.Errorf("recent window is wrong: %+v", recent)
	}
}

func TestCoverageChangesCompareOnlyLikeForLikeFiles(t *testing.T) {
	before := Run{Files: []File{
		{Path: "down.go", Statements: 10, Covered: 8},
		{Path: "up.go", Statements: 10, Covered: 5},
		{Path: "removed.go", Statements: 10, Covered: 10},
	}}
	after := Run{Files: []File{
		{Path: "down.go", Statements: 10, Covered: 5},
		{Path: "up.go", Statements: 10, Covered: 8},
		{Path: "added.go", Statements: 10, Covered: 0},
	}}
	changes := Changes(before, after)
	if len(changes) != 2 {
		t.Fatalf("changes = %+v, want only the two comparable files", changes)
	}
	if changes[0].Path != "down.go" || changes[0].PercentagePoints != -30 {
		t.Errorf("first change = %+v, want down.go at -30 points", changes[0])
	}
	if changes[1].Path != "up.go" || changes[1].PercentagePoints != 30 {
		t.Errorf("second change = %+v, want up.go at +30 points", changes[1])
	}
}

func TestLoadCoverageHistoryMissingAndMalformed(t *testing.T) {
	root := t.TempDir()
	h, err := LoadHistory(root)
	if err != nil || h.Version != HistoryVersion || len(h.Runs) != 0 {
		t.Fatalf("missing history = %+v, %v", h, err)
	}
	if err := os.MkdirAll(filepath.Dir(HistoryPath(root)), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(HistoryPath(root), []byte("not json"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadHistory(root); err == nil || !strings.Contains(err.Error(), "starts a new coverage history") {
		t.Fatalf("malformed history error = %v", err)
	}
}
