package internal

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/adaptive-scale/katana/internal/report"
	"github.com/adaptive-scale/katana/internal/results"
)

func resultCase(name string, status report.Status, at time.Time) results.Case {
	return results.Case{Name: name, Status: status, RanAt: at}
}

func TestARecordedRunPreservesItsCommandExitTimeCaseResultsAndSchema(t *testing.T) {
	at := time.Date(2026, 8, 13, 10, 11, 12, 0, time.FixedZone("local", 3600))
	r := results.Record("go test ./...", at, 7, true, []report.Case{{Suite: "pkg", Name: "TestX", Status: report.StatusFail, Blocked: true}})
	if r.Version != 1 || !r.Recorded() || r.RanAt.Location() != time.UTC || r.Command != "go test ./..." || r.ExitCode != 7 || !r.PerCase {
		t.Fatalf("record = %+v", r)
	}
	if len(r.Cases) != 1 || r.Cases[0].Suite != "pkg" || r.Cases[0].Name != "TestX" || r.Cases[0].Status != report.StatusFail || !r.Cases[0].Blocked || !r.Cases[0].RanAt.Equal(at.UTC()) {
		t.Fatalf("cases = %+v", r.Cases)
	}
}

func TestASaveWritesIndentedVersionedResultsWithTrailingNewline(t *testing.T) {
	root := t.TempDir()
	if err := (&results.Results{RanAt: time.Now().UTC(), Command: "go test", PerCase: true}).Save(root); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(filepath.Join(root, ".katana", "results.json"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(string(b), "\n") || !strings.Contains(string(b), "\n  \"version\"") {
		t.Fatalf("saved JSON is not indented/terminated: %q", b)
	}
	loaded, err := results.Load(root)
	if err != nil || loaded.Version != 1 {
		t.Fatalf("loaded = %+v, err=%v", loaded, err)
	}
}

func TestAMissingResultsAreEmptyAndUnrecorded(t *testing.T) {
	r, err := results.Load(t.TempDir())
	if err != nil || r.Version != 1 || r.Recorded() {
		t.Fatalf("record=%+v err=%v", r, err)
	}
}

func TestAMalformedAndWrongVersionResultsExplainTheirPathAndRemedy(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Dir(results.Path(root)), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(results.Path(root), []byte("{"), 0644); err != nil {
		t.Fatal(err)
	}
	_, err := results.Load(root)
	if err == nil || !strings.HasPrefix(err.Error(), "parsing "+results.Path(root)+":") || !strings.HasSuffix(err.Error(), " (delete it; the next `katana run` writes it again)") {
		t.Fatalf("malformed error = %v", err)
	}
	if err := os.WriteFile(results.Path(root), []byte(`{"version":2}`), 0644); err != nil {
		t.Fatal(err)
	}
	_, err = results.Load(root)
	if err == nil || err.Error() != "results "+results.Path(root)+" have version 2, this katana understands 1" {
		t.Fatalf("version error = %v", err)
	}
}

func TestALoadedCasesWithoutTimestampsInheritTheOverallRunTime(t *testing.T) {
	root := t.TempDir()
	at := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	if err := os.MkdirAll(filepath.Dir(results.Path(root)), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(results.Path(root), []byte(`{"version":1,"ran_at":"2026-01-02T03:04:05Z","per_case":true,"cases":[{"name":"TestX","status":"pass"}]}`), 0644); err != nil {
		t.Fatal(err)
	}
	r, err := results.Load(root)
	if err != nil || !r.Cases[0].RanAt.Equal(at) {
		t.Fatalf("loaded=%+v err=%v", r, err)
	}
}

func TestARecordIsRecordedOnlyWithANonzeroOverallTimeAndOKUsesExitCode(t *testing.T) {
	if (&results.Results{}).Recorded() || !(&results.Results{ExitCode: 0}).OK() || (&results.Results{ExitCode: 1}).OK() {
		t.Fatal("recorded/OK rules violated")
	}
}

func TestATargetedInheritanceKeepsUnreportedCasesAndTheirTimesWithoutBlockedFacts(t *testing.T) {
	old := time.Date(2026, 8, 12, 0, 0, 0, 0, time.UTC)
	now := old.Add(time.Hour)
	prev := &results.Results{RanAt: old, PerCase: true, Cases: []results.Case{resultCase("old", report.StatusPass, old), resultCase("same", report.StatusFail, old), {Suite: "blocked", Name: "suite", Status: report.StatusPass, RanAt: old, Blocked: true}}}
	r := &results.Results{RanAt: now, PerCase: true, Cases: []results.Case{{Name: "same", Status: report.StatusPass, RanAt: now}, {Suite: "blocked", Name: "new", Status: report.StatusFail, RanAt: now, Blocked: true}}}
	r.Inherit(prev)
	if len(r.Cases) != 3 || r.Cases[2].Name != "old" || !r.Cases[2].RanAt.Equal(old) {
		t.Fatalf("inherited cases=%+v", r.Cases)
	}
	for _, c := range r.Cases {
		if c.Name == "suite" {
			t.Fatal("blocked marker inherited")
		}
	}
	// A missing/unrecorded/non-per-case predecessor or current run contributes nothing.
	for _, p := range []*results.Results{nil, &results.Results{PerCase: true}, &results.Results{RanAt: old, PerCase: false}} {
		x := &results.Results{RanAt: now, PerCase: true}
		x.Inherit(p)
		if len(x.Cases) != 0 {
			t.Fatal("invalid predecessor was inherited")
		}
	}
}

func TestABlockedSuitesAreUniqueAndOrderedByFirstBlockedCase(t *testing.T) {
	r := &results.Results{Cases: []results.Case{{Suite: "b", Blocked: true}, {Suite: "a", Blocked: true}, {Suite: "b", Blocked: true}}}
	got := r.BlockedSuites()
	if len(got) != 2 || got[0] != "b" || got[1] != "a" {
		t.Fatalf("blocked suites=%v", got)
	}
}

func TestAOutcomeNormalizesNamesIncludesSubtestAncestorsAndRanksFailThenPassThenSkip(t *testing.T) {
	r := &results.Results{RanAt: time.Now(), PerCase: true, Cases: []results.Case{{Name: "TestX/sub/deep", Status: report.StatusPass}, {Name: "TestX/sub", Status: report.StatusSkip}, {Name: "TestX", Status: report.StatusFail}, {Name: "Only skip", Status: report.StatusSkip}}}
	for _, name := range []string{" TestX ", "TestX/sub", "TestX/sub/deep"} {
		if s, ok := r.Outcome(name); !ok || s != report.StatusFail {
			t.Errorf("Outcome(%q)=%v,%v", name, s, ok)
		}
	}
	if s, ok := r.Outcome("Only_skip"); !ok || s != report.StatusSkip {
		t.Errorf("skip outcome=%v,%v", s, ok)
	}
	if _, ok := r.Outcome("missing"); ok {
		t.Error("missing outcome exists")
	}
}

func TestACaseTimesUseNewestSubtestAndFallbackToRunTimeAndLastRunFindsNewest(t *testing.T) {
	at := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	newer := at.Add(time.Hour)
	r := &results.Results{RanAt: at, PerCase: true, Cases: []results.Case{{Name: "TestX/sub", Status: report.StatusPass, RanAt: newer}, {Name: "Zero", Status: report.StatusPass}}}
	if got, _ := r.CaseRanAt("TestX"); !got.Equal(newer) {
		t.Errorf("parent time=%v", got)
	}
	if got, _ := r.CaseRanAt("Zero"); !got.Equal(at) {
		t.Errorf("fallback time=%v", got)
	}
	if got, ok := r.LastRun([]string{"missing", "TestX"}); !ok || !got.Equal(newer) {
		t.Errorf("last run=%v,%v", got, ok)
	}
	if got, ok := r.LastRun(nil); ok || !got.IsZero() {
		t.Errorf("empty last run=%v,%v", got, ok)
	}
}

func TestATalliesCountRunCasesRequestedUnknownsAndAddFields(t *testing.T) {
	at := time.Now().UTC()
	old := at.Add(-time.Hour)
	r := &results.Results{RanAt: at, PerCase: true, Cases: []results.Case{{Name: "p", Status: report.StatusPass}, {Name: "f", Status: report.StatusFail}, {Name: "s", Status: report.StatusSkip}, {Name: "old", Status: report.StatusFail, RanAt: old}}}
	if got, want := r.Counts(), (results.Tally{Pass: 1, Fail: 1, Skip: 1}); got != want {
		t.Errorf("counts=%+v want=%+v", got, want)
	}
	if got, want := r.Tally([]string{"p", "f", "s", "none"}), (results.Tally{Pass: 1, Fail: 1, Skip: 1, Unknown: 1}); got != want || got.Total() != 4 || got.Known() != 3 {
		t.Errorf("tally=%+v", got)
	}
	var sum results.Tally
	sum.Add(results.Tally{Pass: 1, Unknown: 2})
	sum.Add(results.Tally{Fail: 3, Skip: 4})
	if sum != (results.Tally{Pass: 1, Fail: 3, Skip: 4, Unknown: 2}) {
		t.Errorf("sum=%+v", sum)
	}
	if r.Inherited() != 1 {
		t.Errorf("inherited=%d", r.Inherited())
	}
}
