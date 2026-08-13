package internal

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/adaptive-scale/katana/internal/config"
	"github.com/adaptive-scale/katana/internal/history"
	"github.com/adaptive-scale/katana/internal/plan"
	"github.com/adaptive-scale/katana/internal/report"
	"github.com/adaptive-scale/katana/internal/results"
	"github.com/adaptive-scale/katana/internal/suite"
	"github.com/adaptive-scale/katana/internal/tracker"
)

func suiteCfg(root, command string) *config.Config {
	return &config.Config{Root: root, Test: config.Test{Command: command}, Defaults: config.Defaults{Framework: "go-test"}}
}

func TestSuiteRejectsAnEmptyTestCommand(t *testing.T) {
	_, err := suite.Run(context.Background(), suiteCfg(t.TempDir(), " \t"), suite.Request{})
	if err == nil || err.Error() != "no test.command set in katana.yaml" {
		t.Fatalf("error = %v", err)
	}
}

func TestSuiteRunsFromConfiguredDirectoryAndCapturesAndStreamsBothOutputs(t *testing.T) {
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, "sub"), 0755); err != nil {
		t.Fatal(err)
	}
	var out, stderr bytes.Buffer
	cfg := suiteCfg(root, "printf 'out'; printf 'err' >&2; test \"$PWD\" = \""+filepath.Join(root, "sub")+"\"")
	cfg.Test.Dir = "sub"
	r, err := suite.Run(context.Background(), cfg, suite.Request{Stdout: &out, Stderr: &stderr})
	if err != nil || r.ExitCode != 0 {
		t.Fatalf("run = %#v, %v", r, err)
	}
	if r.Output != "outerr" || out.String() != "out" || stderr.String() != "err" {
		t.Fatalf("captured=%q stdout=%q stderr=%q", r.Output, out.String(), stderr.String())
	}
}

func TestSuiteUsesStdinAndQuotesExtraArguments(t *testing.T) {
	root := t.TempDir()
	cfg := suiteCfg(root, "read x; test \"$x\" = \"a b'c\"")
	r, err := suite.Run(context.Background(), cfg, suite.Request{Stdin: strings.NewReader("a b'c\n"), Extra: []string{"a b'c"}})
	if err != nil || r.ExitCode != 0 || !strings.Contains(r.Command, "'a b'\\''c'") {
		t.Fatalf("result=%#v err=%v", r, err)
	}
}

func TestSuiteRunCommandReplacesConfiguredCommandAndTrimsIt(t *testing.T) {
	r, err := suite.Run(context.Background(), suiteCfg(t.TempDir(), "exit 9"), suite.Request{Command: "  printf supplied  "})
	if err != nil || r.ExitCode != 0 || r.Command != "printf supplied" || r.Output != "supplied" {
		t.Fatalf("result=%#v err=%v", r, err)
	}
}

func TestSuiteMissingOutputWritersStillCaptureWithoutStreaming(t *testing.T) {
	r, err := suite.Run(context.Background(), suiteCfg(t.TempDir(), "printf out; printf err >&2"), suite.Request{})
	if err != nil || r.Output != "outerr" {
		t.Fatalf("output=%q err=%v", r.Output, err)
	}
}

func TestSuiteSuppressesVerboseGoNoTestsLinesOnlyFromStream(t *testing.T) {
	var out bytes.Buffer
	cmd := "printf '%s\\n' 'testing: warning: no tests to run' '--- PASS: TestMissing (0.00s) [no tests to run]' 'PASS'"
	r, err := suite.Run(context.Background(), suiteCfg(t.TempDir(), cmd), suite.Request{Only: &suite.Target{Source: "b.md", Output: "tests/b_test.go", Tests: []string{"TestMissing"}}, Stdout: &out})
	if err != nil || r.Output == "" || strings.Contains(out.String(), "no tests to run") || strings.TrimSpace(out.String()) != "" {
		t.Fatalf("captured=%q streamed=%q err=%v", r.Output, out.String(), err)
	}
}

func TestSuiteNoCaseCallbackIsNeverInvoked(t *testing.T) {
	called := false
	_, err := suite.Run(context.Background(), suiteCfg(t.TempDir(), "printf '%s\\n' '--- PASS: TestA (0.00s)'"), suite.Request{OnCases: nil})
	if err != nil || called {
		t.Fatalf("err=%v called=%v", err, called)
	}
}

func TestSuiteNonzeroExitIsAResultAndZeroIsOK(t *testing.T) {
	r, err := suite.Run(context.Background(), suiteCfg(t.TempDir(), "exit 7"), suite.Request{})
	if err != nil || r.ExitCode != 7 || r.OK() {
		t.Fatalf("result=%#v err=%v", r, err)
	}
	_, err = suite.Run(context.Background(), suiteCfg(t.TempDir(), "does-not-exist-command"), suite.Request{})
	if err == nil || !strings.HasPrefix(err.Error(), "running test command:") {
		t.Fatalf("execution error = %v", err)
	}
}

func TestSuitePerCaseAddsVerboseOnlyForSupportedFramework(t *testing.T) {
	r, err := suite.Run(context.Background(), suiteCfg(t.TempDir(), "printf 'ok'"), suite.Request{PerCase: true})
	if err != nil || !strings.Contains(r.Command, "-v") || len(r.Notes) != 1 {
		t.Fatalf("go result=%#v err=%v", r, err)
	}
	cfg := suiteCfg(t.TempDir(), "printf ok")
	cfg.Defaults.Framework = "cargo-test"
	r, err = suite.Run(context.Background(), cfg, suite.Request{PerCase: true})
	if err != nil || strings.Contains(r.Command, "-v") || len(r.Notes) != 0 {
		t.Fatalf("cargo result=%#v err=%v", r, err)
	}
}

func TestSuiteTargetNarrowsOrExplainsWhyItCannot(t *testing.T) {
	target := &suite.Target{Source: "behaviors/x.md", Output: "tests/x_test.go", Tests: []string{"TestA", "TestB"}}
	r, err := suite.Run(context.Background(), suiteCfg(t.TempDir(), "printf ok"), suite.Request{Only: target})
	if err != nil || !r.Narrowed || r.Scope != target.Source || !strings.Contains(r.Command, "-run '^(TestA|TestB)$'") {
		t.Fatalf("targeted=%#v err=%v", r, err)
	}
	cfg := suiteCfg(t.TempDir(), "printf ok")
	cfg.Defaults.Framework = "cargo-test"
	r, err = suite.Run(context.Background(), cfg, suite.Request{Only: target})
	if err != nil || r.Narrowed || !strings.Contains(strings.Join(r.Notes, " "), target.Source) {
		t.Fatalf("untargeted=%#v err=%v", r, err)
	}
}

func TestSuiteParsesCasesCountsAndObservesIncrementally(t *testing.T) {
	var seen [][]report.Case
	cfg := suiteCfg(t.TempDir(), "printf '%s\\n' '--- PASS: TestA (0.01s)' '--- SKIP: TestB (0.00s)' '--- FAIL: TestC (0.00s)'")
	r, err := suite.Run(context.Background(), cfg, suite.Request{OnCases: func(c []report.Case) { seen = append(seen, c) }})
	if err != nil || !r.Parsed || len(r.Cases) != 3 || len(seen) == 0 {
		t.Fatalf("result=%#v seen=%d err=%v", r, len(seen), err)
	}
	p, f, s := r.Counts()
	if p != 1 || f != 1 || s != 1 {
		t.Fatalf("counts=%d,%d,%d", p, f, s)
	}
}

func TestSuiteBlockedReturnsDistinctSuitesInFirstSeenOrder(t *testing.T) {
	r := suite.Result{Cases: []report.Case{{Suite: "b", Blocked: true}, {Suite: "a", Blocked: true}, {Suite: "b", Blocked: true}, {Suite: "c"}}}
	if got := r.Blocked(); !reflect.DeepEqual(got, []string{"b", "a"}) {
		t.Fatalf("blocked=%v", got)
	}
}

func TestSuiteTargetForUsesTrackerEntryIncludingEmptyTests(t *testing.T) {
	tr := &tracker.Tracker{Entries: map[string]tracker.Entry{"b.md": {Source: "b.md", Output: "tests/b_test.go", Tests: nil}}}
	target, ok := suite.TargetFor(tr, plan.Item{Resolved: config.Resolved{Source: "b.md"}})
	if !ok || target.Source != "b.md" || target.Output != "tests/b_test.go" || len(target.Tests) != 0 {
		t.Fatalf("target=%#v ok=%v", target, ok)
	}
	if _, ok := suite.TargetFor(tr, plan.Item{Resolved: config.Resolved{Source: "missing.md"}}); ok {
		t.Fatal("untracked behavior got a target")
	}
}

func TestSuiteRecordWritesRunHistoryFromReportedCases(t *testing.T) {
	root := t.TempDir()
	tr := &tracker.Tracker{Entries: map[string]tracker.Entry{"b.md": {Source: "b.md", Tests: []string{"TestA"}}}}
	r := &suite.Result{Command: "go test -v", StartedAt: time.Now().UTC(), Duration: 123 * time.Millisecond, Parsed: true, Cases: []report.Case{{Name: "TestA", Status: report.StatusPass}}, Scope: "b.md"}
	if _, err := r.Record(root, tr); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(root, ".katana", "history.json"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "\"scope\": \"b.md\"") || !strings.Contains(string(data), "\"pass\": 1") {
		t.Fatalf("history=%s", data)
	}
}

func TestSuiteRecordPreservesUnreportedResultsAndHistoryTotalsOnlyReportedCases(t *testing.T) {
	root := t.TempDir()
	tr := &tracker.Tracker{Entries: map[string]tracker.Entry{"b.md": {Source: "b.md", Tests: []string{"TestA", "TestB"}}, "empty.md": {Source: "empty.md"}}}
	old := results.Record("old", time.Now().Add(-time.Hour), 0, true, []report.Case{{Name: "TestB", Status: report.StatusPass}})
	if err := old.Save(root); err != nil {
		t.Fatal(err)
	}
	r := &suite.Result{Command: "new", StartedAt: time.Now().UTC(), Duration: 123 * time.Millisecond, Parsed: true, Cases: []report.Case{{Name: "TestA", Status: report.StatusFail}}, Scope: "b.md"}
	got, err := r.Record(root, tr)
	if err != nil || len(got.Cases) != 2 {
		t.Fatalf("saved=%#v err=%v", got, err)
	}
	h, err := history.Load(root)
	if err != nil || len(h.Runs) != 1 {
		t.Fatalf("history=%#v err=%v", h, err)
	}
	if h.Runs[0].Pass != 0 || h.Runs[0].Fail != 1 || h.Runs[0].Millis != 123 || h.Runs[0].Scope != "b.md" {
		t.Fatalf("run=%+v", h.Runs[0])
	}
	if _, ok := h.Runs[0].Find("empty.md"); ok {
		t.Fatal("empty tracker entry was included")
	}
}

func TestSuiteRecordContinuesWhenPreviousResultsAreUnreadable(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, ".katana"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, ".katana", "results.json"), []byte("not json"), 0644); err != nil {
		t.Fatal(err)
	}
	r := &suite.Result{Command: "ok", StartedAt: time.Now().UTC()}
	if _, err := r.Record(root, &tracker.Tracker{}); err != nil {
		t.Fatal(err)
	}
}

func TestSuiteRecordReturnsSavedResultsWhenHistoryCannotBeRecorded(t *testing.T) {
	root := t.TempDir()
	tr := &tracker.Tracker{Entries: map[string]tracker.Entry{}}
	r := &suite.Result{Command: "ok", StartedAt: time.Now().UTC()}
	if err := os.MkdirAll(filepath.Join(root, ".katana"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, ".katana", "history.json"), []byte(`{"version":99}`), 0644); err != nil {
		t.Fatal(err)
	}
	saved, err := r.Record(root, tr)
	if saved == nil || err == nil {
		t.Fatalf("saved=%#v err=%v", saved, err)
	}
}

func TestSuiteRunRecordsStartDurationCommandAndCapturedOutput(t *testing.T) {
	r, err := suite.Run(context.Background(), suiteCfg(t.TempDir(), "printf x"), suite.Request{})
	if err != nil || r.StartedAt.IsZero() || r.Duration < 0 || r.Command == "" || r.Output != "x" {
		t.Fatalf("result=%#v err=%v", r, err)
	}
}

func TestSuiteTargetForReturnsNoTargetForUnknownSource(t *testing.T) {
	tr := &tracker.Tracker{Entries: map[string]tracker.Entry{"known.md": {Source: "known.md", Output: "tests/k.go", Tests: []string{"TestK"}}}}
	if target, ok := suite.TargetFor(tr, plan.Item{Resolved: config.Resolved{Source: "unknown.md"}}); ok || target != nil {
		t.Fatalf("target=%#v ok=%v", target, ok)
	}
}
