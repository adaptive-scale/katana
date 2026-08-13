package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/adaptive-scale/katana/internal/coverage"
)

func TestInitIgnoresCoverageHistory(t *testing.T) {
	root := filepath.Join(t.TempDir(), "project")
	captureStdout(t, func() {
		if err := runInit([]string{"--dir", root, "--no-sample", "--harness", "claude"}); err != nil {
			t.Fatalf("init: %v", err)
		}
	})
	data, err := os.ReadFile(filepath.Join(root, ".katana", ".gitignore"))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{coverage.HistoryFileName, ".coverage-history-*.json"} {
		if !strings.Contains(string(data), want) {
			t.Errorf(".katana/.gitignore is missing %q:\n%s", want, data)
		}
	}
}
