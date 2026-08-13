package internal

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/adaptive-scale/katana/internal/history"
)

func historyRun(at time.Time, code, pass, fail, skip int) history.Run {
	return history.Run{RanAt: at, ExitCode: code, Pass: pass, Fail: fail, Skip: skip, Millis: 125}
}

func TestHistoryAddConvertsTimestampAndUpdatesCumulativeTotals(t *testing.T) {
	h := &history.History{}
	at := time.Date(2026, 1, 2, 3, 4, 5, 0, time.FixedZone("west", -2*60*60))
	h.Add(historyRun(at, 0, 2, 1, 3))
	h.Add(historyRun(at.Add(time.Hour), 7, 1, 0, 1))
	if !h.Runs[0].RanAt.Equal(at.UTC()) || h.Totals.Runs != 2 || h.Totals.Passed != 1 || h.Totals.Failed != 1 || h.Totals.Pass != 3 || h.Totals.Fail != 1 || h.Totals.Skip != 4 || h.Totals.Millis != 250 {
		t.Fatalf("unexpected history: %+v", h)
	}
	if !h.Totals.FirstRanAt.Equal(at.UTC()) || !h.Totals.LastRanAt.Equal(at.Add(time.Hour).UTC()) {
		t.Fatalf("bad range: %+v", h.Totals)
	}
}

func TestHistoryRunDerivedQueries(t *testing.T) {
	r := history.Run{ExitCode: 1, Pass: 2, Fail: 1, Skip: 1}
	if r.OK() || r.Total() != 4 || r.Rate() != .5 {
		t.Fatalf("bad run queries: %+v", r)
	}
	if !(&history.Run{}).OK() { // A zero exit code is the success boundary.
		t.Fatal("zero exit code should report success")
	}
	b := history.Behavior{Pass: 2, Fail: 1, Skip: 1, Unknown: 4}
	if b.Known() != 4 || b.Total() != 8 {
		t.Fatalf("bad behavior counts: %+v", b)
	}
	if rate, ok := b.Rate(); !ok || rate != .5 {
		t.Fatalf("bad behavior rate: %v %v", rate, ok)
	}
	if rate, ok := (history.Behavior{}).Rate(); ok || rate != 0 {
		t.Fatalf("empty behavior rate: %v %v", rate, ok)
	}
	if rate, ok := (history.Totals{}).Rate(); ok || rate != 0 {
		t.Fatalf("empty totals rate: %v %v", rate, ok)
	}
}

func TestHistoryRecentReturnsNewestRunsInOldestToNewestOrder(t *testing.T) {
	h := &history.History{}
	for i := 0; i < 3; i++ {
		h.Add(history.Run{RanAt: time.Unix(int64(i), 0)})
	}
	if got := h.Recent(2); len(got) != 2 || got[0].RanAt.Unix() != 1 || got[1].RanAt.Unix() != 2 {
		t.Fatalf("Recent: %+v", got)
	}
	if len(h.Recent(0)) != 3 || len(h.Recent(9)) != 3 {
		t.Fatal("non-positive or large count should return all runs")
	}
}

func TestHistoryForFiltersKnownBehaviorsAndLimitsNewest(t *testing.T) {
	h := &history.History{}
	for i, b := range []history.Behavior{{Source: "a", Pass: 1}, {Source: "b", Pass: 1}, {Source: "a", Fail: 1}, {Source: "a", Unknown: 2}} {
		h.Add(history.Run{RanAt: time.Unix(int64(i), 0), Behaviors: []history.Behavior{b}})
	}
	got := h.For("a", 1)
	if len(got) != 1 || got[0].RanAt.Unix() != 2 {
		t.Fatalf("For: %+v", got)
	}
	if len(h.For("a", 0)) != 2 || len(h.For("missing", 0)) != 0 {
		t.Fatal("For filtering/count handling is wrong")
	}
}

func TestHistoryLoadMissingAndSaveCreatesIndentedNewlineTerminatedFile(t *testing.T) {
	root := t.TempDir()
	h, err := history.Load(root)
	if err != nil || h.Version != 1 || len(h.Runs) != 0 {
		t.Fatalf("Load missing: %+v %v", h, err)
	}
	if err := h.Save(root); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(history.Path(root))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(string(b), "\n") || !strings.Contains(string(b), "\n  \"version\"") || filepath.Base(history.Path(root)) != "history.json" {
		t.Fatalf("bad saved document: %q", b)
	}
	var decoded history.History
	if json.Unmarshal(b, &decoded) != nil || decoded.Version != 1 {
		t.Fatal("saved JSON is invalid")
	}
}

func TestHistoryLoadSortsStableAndRejectsBadVersionOrJSON(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Dir(history.Path(root)), 0755); err != nil {
		t.Fatal(err)
	}
	content := `{"version":1,"runs":[{"ran_at":"2026-01-02T00:00:00Z"},{"ran_at":"2026-01-01T00:00:00Z"},{"ran_at":"2026-01-01T00:00:00Z","command":"second"}]}`
	if err := os.WriteFile(history.Path(root), []byte(content), 0644); err != nil {
		t.Fatal(err)
	}
	h, err := history.Load(root)
	if err != nil || h.Runs[0].Command != "" || h.Runs[1].Command != "second" {
		t.Fatalf("sorting: %+v %v", h, err)
	}
	if err := os.WriteFile(history.Path(root), []byte(`{"version":2}`), 0644); err != nil {
		t.Fatal(err)
	}
	if _, err := history.Load(root); err == nil || !strings.Contains(err.Error(), "version 2") || !strings.Contains(err.Error(), "understands 1") {
		t.Fatal("bad version not explained")
	}
	if err := os.WriteFile(history.Path(root), []byte("nope"), 0644); err != nil {
		t.Fatal(err)
	}
	if _, err := history.Load(root); err == nil || !strings.HasPrefix(err.Error(), "parsing ") || !strings.Contains(err.Error(), "delete it") {
		t.Fatal("bad JSON not explained")
	}
}

func TestHistoryRecordRecoversFromUnreadableHistory(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Dir(history.Path(root)), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(history.Path(root), []byte("bad"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := history.Record(root, history.Run{ExitCode: 0}); err != nil {
		t.Fatal(err)
	}
	h, err := history.Load(root)
	if err != nil || len(h.Runs) != 1 || h.Totals.Runs != 1 {
		t.Fatalf("record recovery: %+v %v", h, err)
	}
}
