package cli

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/adaptive-scale/katana/internal/coverage"
)

func TestCoverageProfileRecordsHistoryAndReportsRegression(t *testing.T) {
	root := fakeProject(t, 1)
	first := filepath.Join(root, "first.out")
	second := filepath.Join(root, "second.out")
	writeCoverageProfile(t, first, `mode: set
example.com/project/a.go:1.1,2.1 5 1
example.com/project/a.go:3.1,4.1 5 0
`)
	writeCoverageProfile(t, second, `mode: set
example.com/project/a.go:1.1,2.1 4 1
example.com/project/a.go:3.1,4.1 6 0
`)

	captureStdout(t, func() {
		if err := runCoverage([]string{"--dir", root, "--profile", first, "--color", "never"}); err != nil {
			t.Fatalf("first coverage: %v", err)
		}
	})
	out := captureStdout(t, func() {
		if err := runCoverage([]string{"--dir", root, "--profile", second, "--color", "never"}); err != nil {
			t.Fatalf("second coverage: %v", err)
		}
	})

	h, err := coverage.LoadHistory(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(h.Runs) != 2 {
		t.Fatalf("coverage history has %d runs, want 2", len(h.Runs))
	}
	last := h.Runs[1]
	if !last.Imported || last.Profile == "" || last.Format != coverage.FormatGo {
		t.Errorf("imported profile metadata was not recorded: %+v", last)
	}
	if last.Total().Statements != 10 || last.Total().Covered != 4 {
		t.Errorf("last total = %+v, want 4 of 10", last.Total())
	}
	for _, want := range []string{"coverage", "latest 40.0%", "2 run(s)", "-10.0 points", "1 regressed", "worst example.com/project/a.go"} {
		if !strings.Contains(out, want) {
			t.Errorf("coverage output is missing %q:\n%s", want, out)
		}
	}
}

func TestMeasuredCoverageRunRecordsExecutionMetadata(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the stand-in runner is a shell command")
	}
	root := projectWithTestCommand(t, `profile=""
for arg in "$@"; do
  case "$arg" in
    -coverprofile=*) profile="${arg#-coverprofile=}" ;;
  esac
done
printf '%s\n' 'mode: set' 'example.com/project/a.go:1.1,2.1 10 1' > "$profile"
echo PASS
`)

	out := captureStdout(t, func() {
		if err := runCoverage([]string{"--dir", root, "--color", "never"}); err != nil {
			t.Fatalf("coverage: %v", err)
		}
	})
	h, err := coverage.LoadHistory(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(h.Runs) != 1 {
		t.Fatalf("history has %d runs, want 1", len(h.Runs))
	}
	run := h.Runs[0]
	if run.Imported || run.Command == "" || !strings.Contains(run.Command, "-coverprofile=") {
		t.Errorf("suite execution metadata is wrong: %+v", run)
	}
	if run.ExitCode != 0 || run.Total().Percent() != 100 {
		t.Errorf("recorded measured run is wrong: %+v", run)
	}
	if !strings.Contains(out, "latest 100.0%") {
		t.Errorf("coverage trend was not printed:\n%s", out)
	}
}

func TestCoverageBelowMinimumIsRecordedBeforeItFails(t *testing.T) {
	root := fakeProject(t, 1)
	profile := filepath.Join(root, "low.out")
	writeCoverageProfile(t, profile, `mode: set
example.com/project/a.go:1.1,2.1 3 1
example.com/project/a.go:3.1,4.1 7 0
`)
	var runErr error
	captureStdout(t, func() {
		runErr = runCoverage([]string{
			"--dir", root, "--profile", profile, "--min", "80", "--color", "never",
		})
	})
	if runErr == nil || !strings.Contains(runErr.Error(), "below the 80.0%") {
		t.Fatalf("coverage threshold error = %v", runErr)
	}
	h, err := coverage.LoadHistory(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(h.Runs) != 1 || h.Runs[0].Percent() != 30 {
		t.Fatalf("threshold failure was not recorded first: %+v", h.Runs)
	}
}

func TestCoverageRunWithoutAReportIsStillRecorded(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the stand-in runner is a shell command")
	}
	root := projectWithTestCommand(t, "echo no coverage report\n")
	var runErr error
	captureStdout(t, func() {
		runErr = runCoverage([]string{"--dir", root, "--color", "never"})
	})
	if runErr == nil || !strings.Contains(runErr.Error(), "wrote no coverage report") {
		t.Fatalf("coverage error = %v", runErr)
	}
	h, err := coverage.LoadHistory(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(h.Runs) != 1 || h.Runs[0].Command == "" || h.Runs[0].Error == "" {
		t.Fatalf("failed coverage attempt was not recorded: %+v", h.Runs)
	}
}

func writeCoverageProfile(t *testing.T, path, body string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}
