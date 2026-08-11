package cli

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/adaptive-scale/katana/internal/config"
	"github.com/adaptive-scale/katana/internal/plan"
)

// goItem is a behavior whose tests land at output, in Go.
func goItem(source, output string) plan.Item {
	return plan.Item{Resolved: config.Resolved{Source: source, Output: output, Language: "go"}}
}

// writeTests puts a Go test file declaring names at a project-relative path.
func writeTests(t *testing.T, root, rel string, names ...string) {
	t.Helper()
	abs := filepath.Join(root, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
		t.Fatal(err)
	}
	body := "package internal\n\nimport \"testing\"\n"
	for _, n := range names {
		body += "\nfunc " + n + "(t *testing.T) {}\n"
	}
	if err := os.WriteFile(abs, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestScanDeclaredReadsTheFilesOnDiskNotTheTracker(t *testing.T) {
	root := t.TempDir()
	writeTests(t, root, "tests/internal/a_test.go", "TestOne", "TestTwo")

	// The second behavior has never been generated: nothing is there to read,
	// and that must not stop the first from being read.
	got := scanDeclared(root, []plan.Item{
		goItem("behaviors/internal/a.md", "tests/internal/a_test.go"),
		goItem("behaviors/internal/b.md", "tests/internal/b_test.go"),
	})

	want := map[string][]string{"tests/internal/a_test.go": {"TestOne", "TestTwo"}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("scanDeclared = %v, want %v", got, want)
	}
}

func TestReservedNamesAreTheNeighboursInTheSameDirectory(t *testing.T) {
	declared := map[string][]string{
		"tests/internal/a_test.go": {"TestTwo", "TestOne"},
		"tests/internal/b_test.go": {"TestThree"},
		"tests/katana_test.go":     {"TestElsewhere"},
	}

	got := reservedFor(declared, "tests/internal/b_test.go")

	// Sorted, so the same neighbours never produce two different prompts.
	want := []string{"TestOne", "TestTwo"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("reservedFor = %v, want %v", got, want)
	}
}

func TestABehaviorDoesNotReserveNamesAgainstItself(t *testing.T) {
	declared := map[string][]string{"tests/internal/a_test.go": {"TestOne"}}

	if got := reservedFor(declared, "tests/internal/a_test.go"); len(got) != 0 {
		t.Errorf("reservedFor = %v, want nothing: a file's own names are not taken from it", got)
	}
}

func TestDuplicateTestsReportsEveryFileDeclaringTheName(t *testing.T) {
	declared := map[string][]string{
		"tests/internal/a_test.go": {"TestShared", "TestOnlyInA"},
		"tests/internal/b_test.go": {"TestShared"},
		"tests/internal/c_test.go": {"TestShared"},
	}

	got := duplicateTests(declared)

	want := []clash{{
		Name:  "TestShared",
		Files: []string{"tests/internal/a_test.go", "tests/internal/b_test.go", "tests/internal/c_test.go"},
	}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("duplicateTests = %v, want %v", got, want)
	}
}

func TestTheSameNameInAnotherDirectoryIsNotADuplicate(t *testing.T) {
	declared := map[string][]string{
		"tests/internal/a_test.go": {"TestShared"},
		"tests/katana_test.go":     {"TestShared"},
	}

	if got := duplicateTests(declared); len(got) != 0 {
		t.Errorf("duplicateTests = %v, want nothing: separate directories are separate namespaces", got)
	}
}
