package suite

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/adaptive-scale/katana/internal/config"
	"github.com/adaptive-scale/katana/internal/history"
	"github.com/adaptive-scale/katana/internal/plan"
	"github.com/adaptive-scale/katana/internal/results"
	"github.com/adaptive-scale/katana/internal/tracker"
)

// TestRunRecordsWhatHappened is the contract every caller depends on: the suite
// runs, its output is read case by case, and the record left behind is what
// `katana status` and the charts are read from.
func TestRunRecordsWhatHappened(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the stand-in runner is a shell script")
	}
	cfg, tr := project(t)

	res, err := Run(context.Background(), cfg, Request{PerCase: true})
	if err != nil {
		t.Fatal(err)
	}
	if !res.OK() || !res.Parsed {
		t.Fatalf("the stand-in suite passes and names its cases: %+v", res)
	}
	if pass, fail, _ := res.Counts(); pass != 2 || fail != 0 {
		t.Errorf("counts = %d passed, %d failed; want 2 and 0", pass, fail)
	}

	if _, err := res.Record(cfg.Root, tr); err != nil {
		t.Fatal(err)
	}
	saved, err := results.Load(cfg.Root)
	if err != nil {
		t.Fatal(err)
	}
	if status, ok := saved.Outcome("TestOne"); !ok || status != "pass" {
		t.Errorf("results.json does not hold the run: %v %v", status, ok)
	}
	h, err := history.Load(cfg.Root)
	if err != nil {
		t.Fatal(err)
	}
	if len(h.Runs) != 1 {
		t.Fatalf("the run should be in the history, got %d", len(h.Runs))
	}
	if b, ok := h.Runs[0].Find("behaviors/a.md"); !ok || b.Pass != 2 {
		t.Errorf("the behavior's own tally was not recorded: %+v", h.Runs[0].Behaviors)
	}
}

// TestRunNarrowsToABehavior covers running one behavior on demand: the command
// katana runs selects that behavior's cases, and the record says so.
func TestRunNarrowsToABehavior(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the stand-in runner is a shell script")
	}
	cfg, tr := project(t)

	target, ok := TargetFor(tr, item(t, cfg, tr))
	if !ok {
		t.Fatal("the behavior is tracked, so it has a target")
	}
	res, err := Run(context.Background(), cfg, Request{Only: target, PerCase: true})
	if err != nil {
		t.Fatal(err)
	}
	if !res.Narrowed {
		t.Errorf("a go suite can be narrowed by test name, got %q", res.Command)
	}
	if !strings.Contains(res.Command, "-run '^(TestOne|TestTwo)$'") {
		t.Errorf("the command was not narrowed to the behavior's cases: %q", res.Command)
	}
	if res.Scope != "behaviors/a.md" {
		t.Errorf("scope = %q, want the behavior", res.Scope)
	}
}

// TestRunSaysWhenItCannotNarrow is the honest half of running one behavior: a
// runner katana cannot narrow runs whole, and the caller is told rather than
// left believing only one behavior ran.
func TestRunSaysWhenItCannotNarrow(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the stand-in runner is a shell script")
	}
	cfg, tr := project(t)
	cfg.Defaults.Framework = "cargo-test"

	target, _ := TargetFor(tr, item(t, cfg, tr))
	res, err := Run(context.Background(), cfg, Request{Only: target, PerCase: true})
	if err != nil {
		t.Fatal(err)
	}
	if res.Narrowed {
		t.Error("katana does not know how to narrow cargo, so it must not claim to have")
	}
	if len(res.Notes) == 0 || !strings.Contains(strings.Join(res.Notes, " "), "whole suite") {
		t.Errorf("the caller should be told the whole suite ran, got %q", res.Notes)
	}
}

func TestRunNeedsACommand(t *testing.T) {
	cfg, _ := project(t)
	cfg.Test.Command = "  "
	if _, err := Run(context.Background(), cfg, Request{}); err == nil {
		t.Error("a project with no test command cannot run one")
	}
}

// project writes a project whose test command is a stand-in for a Go suite, and
// a tracker that maps its one behavior to two cases.
func project(t *testing.T) (*config.Config, *tracker.Tracker) {
	t.Helper()
	root := t.TempDir()
	if real, err := filepath.EvalSymlinks(root); err == nil {
		root = real
	}
	mkdir(t, filepath.Join(root, "behaviors"))
	mkdir(t, filepath.Join(root, "tests"))

	runner := filepath.Join(root, "run-tests.sh")
	write(t, runner, `#!/bin/sh
echo "=== RUN   TestOne"
echo "--- PASS: TestOne (0.00s)"
echo "=== RUN   TestTwo"
echo "--- PASS: TestTwo (0.00s)"
echo "PASS"
echo "ok  	example/tests	0.01s"
`)
	if err := os.Chmod(runner, 0o755); err != nil {
		t.Fatal(err)
	}

	write(t, filepath.Join(root, "katana.yaml"), `harness:
  name: claude
defaults:
  language: go
  framework: go-test
  output_dir: tests
behaviors:
  - path: behaviors/*.md
test:
  command: sh ./run-tests.sh
`)
	write(t, filepath.Join(root, "behaviors", "a.md"), "# a\n\nIt works.\n")
	write(t, filepath.Join(root, "tests", "a_test.go"), "package tests\n")

	cfg, err := config.Load(filepath.Join(root, "katana.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	tr, err := tracker.Load(root)
	if err != nil {
		t.Fatal(err)
	}
	tr.Record(tracker.Entry{
		Source:      "behaviors/a.md",
		Output:      "tests/a_test.go",
		Tests:       []string{"TestOne", "TestTwo"},
		Language:    "go",
		Framework:   "go-test",
		Harness:     "claude",
		GeneratedAt: time.Now().UTC(),
	})
	if err := tr.Save(); err != nil {
		t.Fatal(err)
	}
	return cfg, tr
}

// item is the project's one behavior, as the planner sees it.
func item(t *testing.T, cfg *config.Config, tr *tracker.Tracker) plan.Item {
	t.Helper()
	items, err := plan.Build(cfg, tr, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) == 0 {
		t.Fatal("the fixture has a behavior, so the plan has an item")
	}
	return items[0]
}

func mkdir(t *testing.T, dir string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
}

func write(t *testing.T, path, body string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}
