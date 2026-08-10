package cli

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/adaptive-scale/katana/internal/report"
	"github.com/adaptive-scale/katana/internal/results"
)

// TestRunRecordsCaseResults is the contract `katana status` reads: every run
// leaves behind what each test case did, recovered from the runner's own output.
func TestRunRecordsCaseResults(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the stand-in runner is a shell command")
	}
	root := projectWithTestCommand(t,
		"printf '%s\\n' '--- PASS: TestOne (0.01s)' '--- SKIP: TestTwo (0.00s)' 'PASS'\n")

	captureStdout(t, func() {
		if err := runTest([]string{"--dir", root}); err != nil {
			t.Fatalf("run: %v", err)
		}
	})

	res, err := results.Load(root)
	if err != nil {
		t.Fatalf("loading recorded results: %v", err)
	}
	if !res.Recorded() || !res.PerCase || !res.OK() {
		t.Fatalf("the run was not recorded case by case: %+v", res)
	}
	if c := res.Counts(); c.Pass != 1 || c.Skip != 1 || c.Total() != 2 {
		t.Errorf("counts = %+v, want one pass and one skip", c)
	}
	if s, ok := res.Outcome("TestTwo"); !ok || s != report.StatusSkip {
		t.Errorf("Outcome(TestTwo) = %q/%v, want a skip", s, ok)
	}
}

// TestRunWithoutPerCaseOutputRecordsTheSuite covers a runner katana cannot read:
// the run is still recorded, but as one suite-wide result, so status can say the
// suite passed without claiming anything about individual cases.
func TestRunWithoutPerCaseOutputRecordsTheSuite(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the stand-in runner is a shell command")
	}
	root := projectWithTestCommand(t, "echo everything is fine\n")

	captureStdout(t, func() {
		if err := runTest([]string{"--dir", root}); err != nil {
			t.Fatalf("run: %v", err)
		}
	})

	res, err := results.Load(root)
	if err != nil {
		t.Fatalf("loading recorded results: %v", err)
	}
	if !res.Recorded() || !res.OK() {
		t.Fatalf("the run itself should still be recorded: %+v", res)
	}
	if res.PerCase {
		t.Error("output naming no cases must not be recorded as per-case results")
	}
	if _, ok := res.Outcome("TestOne"); ok {
		t.Error("a suite-wide pass says nothing about an individual case")
	}
}

// projectWithTestCommand writes a project whose suite is a stand-in script
// printing the given output, so a run can be driven without a real test runner.
func projectWithTestCommand(t *testing.T, output string) string {
	t.Helper()
	root := fakeProject(t, 1)

	runner := filepath.Join(root, "fake-runner.sh")
	if err := os.WriteFile(runner, []byte("#!/bin/sh\n"+output), 0o755); err != nil {
		t.Fatal(err)
	}

	path := filepath.Join(root, "katana.yaml")
	cfg, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	cfg = append(cfg, []byte(fmt.Sprintf("test:\n  command: %s\n", runner))...)
	if err := os.WriteFile(path, cfg, 0o644); err != nil {
		t.Fatal(err)
	}
	return root
}
