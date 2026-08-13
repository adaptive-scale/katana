package internal

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/adaptive-scale/katana/internal/config"
	"github.com/adaptive-scale/katana/internal/tui"
)

// tuiProject creates the smallest real project that can be loaded by the TUI.
func tuiProject(t *testing.T, yaml, behavior string) *config.Config {
	t.Helper()
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "behaviors"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "katana.yaml"), []byte(yaml), 0o644); err != nil {
		t.Fatal(err)
	}
	if behavior != "" {
		if err := os.WriteFile(filepath.Join(root, "behaviors", "one.md"), []byte(behavior), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	cfg, err := config.Load(filepath.Join(root, "katana.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	return cfg
}

func TestTUISnapshotRendersConfiguredBehaviorsAndTerminatesLines(t *testing.T) {
	cfg := tuiProject(t, "version: 1\ndefaults:\n  language: go\n  output_dir: tests\nbehaviors:\n  - path: behaviors/*.md\n", "# one\n\n- does one thing\n- does another\n")
	var b strings.Builder
	if err := tui.Snapshot(cfg, &b, 120, 30); err != nil {
		t.Fatal(err)
	}
	got := b.String()
	if !strings.Contains(got, "behaviors/one.md") || !strings.Contains(got, "no test cases") && !strings.Contains(got, "never") {
		t.Fatalf("snapshot does not show the configured behavior:\n%s", got)
	}
	for _, line := range strings.Split(strings.TrimSuffix(got, "\n"), "\n") {
		if strings.HasSuffix(line, " ") || strings.HasSuffix(line, "\t") {
			t.Fatalf("snapshot line has trailing whitespace: %q", line)
		}
	}
}

func TestTUISnapshotEmptyProjectExplainsHowToConfigureBehaviors(t *testing.T) {
	cfg := tuiProject(t, "version: 1\ndefaults:\n  language: go\nbehaviors: []\n", "")
	var b strings.Builder
	if err := tui.Snapshot(cfg, &b, 100, 24); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(b.String(), "no behaviors configured") || !strings.Contains(b.String(), "katana generate") {
		t.Fatalf("empty snapshot lacks guidance:\n%s", b.String())
	}
}

func TestTUISnapshotReturnsInitialTrackerLoadErrorWithoutWritingAFrame(t *testing.T) {
	cfg := tuiProject(t, "version: 1\ndefaults:\n  language: go\nbehaviors:\n  - path: behaviors/*.md\n", "# one\n\n- a\n- b\n")
	if err := os.Mkdir(filepath.Join(cfg.Root, ".katana"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(cfg.Root, ".katana", "tracker.json"), []byte("{"), 0o644); err != nil {
		t.Fatal(err)
	}
	var b strings.Builder
	err := tui.Snapshot(cfg, &b, 80, 24)
	if err == nil || b.Len() != 0 || !strings.Contains(err.Error(), "tracker") {
		t.Fatalf("Snapshot error=%v output=%q", err, b.String())
	}
}

func TestTUISnapshotReturnsInitialPlanErrorWithoutWritingAFrame(t *testing.T) {
	cfg := tuiProject(t, "version: 1\ndefaults:\n  language: go\nbehaviors:\n  - path: behaviors/missing.md\n", "")
	var b strings.Builder
	err := tui.Snapshot(cfg, &b, 80, 24)
	if err == nil || b.Len() != 0 {
		t.Fatalf("Snapshot error=%v output=%q", err, b.String())
	}
}

type tuiFailWriter struct{}

func (tuiFailWriter) Write([]byte) (int, error) { return 0, errors.New("sink closed") }

func TestTUISnapshotReturnsWriterErrorWhileWritingTheFrame(t *testing.T) {
	cfg := tuiProject(t, "version: 1\ndefaults:\n  language: go\nbehaviors: []\n", "")
	err := tui.Snapshot(cfg, tuiFailWriter{}, 80, 24)
	if err == nil || err.Error() != "sink closed" {
		t.Fatalf("Snapshot error=%v, want writer error", err)
	}
}

func TestTUISnapshotFitsTheSuppliedWidthAndHeight(t *testing.T) {
	cfg := tuiProject(t, "version: 1\ndefaults:\n  language: go\nbehaviors: []\n", "")
	var b strings.Builder
	if err := tui.Snapshot(cfg, &b, 20, 4); err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSuffix(b.String(), "\n"), "\n")
	if len(lines) > 4 {
		t.Fatalf("snapshot has %d lines for a four-row terminal", len(lines))
	}
}

func TestTUIInteractiveModeRejectsNonTerminalOutput(t *testing.T) {
	cfg := tuiProject(t, "version: 1\ndefaults:\n  language: go\nbehaviors: []\n", "")
	err := tui.Run(cfg)
	want := "katana tui needs a terminal (stdout is not one); try `katana status`, or `katana tui --snapshot`"
	if err == nil || err.Error() != want {
		t.Fatalf("Run error=%v, want %q", err, want)
	}
}

func TestTUISnapshotKeepsTheBehaviorListWhenResultsCannotBeRead(t *testing.T) {
	cfg := tuiProject(t, "version: 1\ndefaults:\n  language: go\nbehaviors:\n  - path: behaviors/*.md\n", "# one\n\n- a\n- b\n")
	if err := os.Mkdir(filepath.Join(cfg.Root, "results.json"), 0o755); err != nil {
		t.Fatal(err)
	}
	var b strings.Builder
	if err := tui.Snapshot(cfg, &b, 100, 24); err != nil {
		t.Fatal(err)
	}
	got := b.String()
	if !strings.Contains(got, "behaviors/one.md") || !strings.Contains(got, "katana:") {
		t.Fatalf("snapshot lost the behavior list or read error:\n%s", got)
	}
}

func TestTUISnapshotKeepsTheBehaviorListWhenHistoryCannotBeRead(t *testing.T) {
	cfg := tuiProject(t, "version: 1\ndefaults:\n  language: go\nbehaviors:\n  - path: behaviors/*.md\n", "# one\n\n- a\n- b\n")
	if err := os.Mkdir(filepath.Join(cfg.Root, "history.json"), 0o755); err != nil {
		t.Fatal(err)
	}
	var b strings.Builder
	if err := tui.Snapshot(cfg, &b, 100, 24); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(b.String(), "behaviors/one.md") || !strings.Contains(b.String(), "katana:") {
		t.Fatalf("snapshot did not report the history read error:\n%s", b.String())
	}
}

func TestTUISnapshotKeepsTheBehaviorListWhenCoverageHistoryCannotBeRead(t *testing.T) {
	cfg := tuiProject(t, "version: 1\ndefaults:\n  language: go\nbehaviors:\n  - path: behaviors/*.md\n", "# one\n\n- a\n- b\n")
	if err := os.MkdirAll(filepath.Join(cfg.Root, ".katana"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(cfg.Root, ".katana", "coverage-history.json"), 0o755); err != nil {
		t.Fatal(err)
	}
	var b strings.Builder
	if err := tui.Snapshot(cfg, &b, 100, 24); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(b.String(), "behaviors/one.md") || !strings.Contains(b.String(), "katana:") {
		t.Fatalf("snapshot did not report the coverage-history read error:\n%s", b.String())
	}
}
