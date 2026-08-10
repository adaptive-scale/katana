package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeProject(t *testing.T, files map[string]string) string {
	t.Helper()
	root := t.TempDir()
	for name, body := range files {
		p := filepath.Join(root, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

func TestResolveAppliesDefaultsAndGlobs(t *testing.T) {
	root := writeProject(t, map[string]string{
		"katana.yaml": `
version: 1
harness:
  name: claude
defaults:
  language: go
  output_dir: tests
behaviors:
  - path: behaviors/*.md
`,
		"behaviors/checkout.md":  "# checkout",
		"behaviors/user-auth.md": "# auth",
	})

	cfg, err := Load(filepath.Join(root, "katana.yaml"))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	got, err := cfg.Resolve()
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("got %d behaviors, want 2", len(got))
	}
	// Sorted by source path, so user-auth follows checkout.
	if got[0].Source != "behaviors/checkout.md" || got[0].Output != "tests/checkout_test.go" {
		t.Errorf("first behavior = %+v", got[0])
	}
	if got[1].Output != "tests/user_auth_test.go" {
		t.Errorf("hyphenated name should become snake_case, got %q", got[1].Output)
	}
	if got[0].Framework != "go-test" || got[0].Harness != "claude" {
		t.Errorf("defaults not applied: %+v", got[0])
	}
}

func TestResolvePerBehaviorLanguageGetsMatchingConventions(t *testing.T) {
	// A behavior that overrides only the language must not inherit the global
	// framework and file-name template from a different language.
	root := writeProject(t, map[string]string{
		"katana.yaml": `
version: 1
defaults:
  language: go
behaviors:
  - path: behaviors/billing.md
    language: python
`,
		"behaviors/billing.md": "# billing",
	})

	cfg, err := Load(filepath.Join(root, "katana.yaml"))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	got, err := cfg.Resolve()
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if got[0].Framework != "pytest" {
		t.Errorf("framework = %q, want pytest", got[0].Framework)
	}
	if got[0].Output != "tests/test_billing.py" {
		t.Errorf("output = %q, want tests/test_billing.py", got[0].Output)
	}
}

func TestResolveRejectsCollidingOutputs(t *testing.T) {
	root := writeProject(t, map[string]string{
		"katana.yaml": `
version: 1
behaviors:
  - path: a/checkout.md
  - path: b/checkout.md
`,
		"a/checkout.md": "# a",
		"b/checkout.md": "# b",
	})

	cfg, err := Load(filepath.Join(root, "katana.yaml"))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if _, err := cfg.Resolve(); err == nil {
		t.Fatal("expected an error when two behaviors generate the same file")
	} else if !strings.Contains(err.Error(), "both generate") {
		t.Errorf("unhelpful error: %v", err)
	}
}

func TestResolveDeduplicatesOverlappingGlobs(t *testing.T) {
	root := writeProject(t, map[string]string{
		"katana.yaml": `
version: 1
behaviors:
  - path: behaviors/*.md
  - path: behaviors/checkout.md
`,
		"behaviors/checkout.md": "# checkout",
	})

	cfg, err := Load(filepath.Join(root, "katana.yaml"))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	got, err := cfg.Resolve()
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("a file matched twice should resolve once, got %d", len(got))
	}
}

func TestDirectoryPathExpandsToMarkdown(t *testing.T) {
	root := writeProject(t, map[string]string{
		"katana.yaml": `
version: 1
behaviors:
  - path: behaviors
`,
		"behaviors/one.md":    "# one",
		"behaviors/notes.txt": "ignored",
	})

	cfg, err := Load(filepath.Join(root, "katana.yaml"))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	got, err := cfg.Resolve()
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if len(got) != 1 || got[0].Source != "behaviors/one.md" {
		t.Fatalf("directory should expand to *.md only, got %+v", got)
	}
}

func TestDirectoryPathRecursesIntoSubdirectories(t *testing.T) {
	root := writeProject(t, map[string]string{
		"katana.yaml": `
version: 1
behaviors:
  - path: behaviors
`,
		"behaviors/one.md":                "# one",
		"behaviors/billing/invoices.md":   "# invoices",
		"behaviors/billing/deep/plans.md": "# plans",
		"behaviors/billing/notes.txt":     "ignored",
		"behaviors/.hidden/skipped.md":    "# skipped",
	})

	cfg, err := Load(filepath.Join(root, "katana.yaml"))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	got, err := cfg.Resolve()
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	want := []string{"behaviors/billing/deep/plans.md", "behaviors/billing/invoices.md", "behaviors/one.md"}
	if len(got) != len(want) {
		t.Fatalf("got %d behaviors, want %d: %+v", len(got), len(want), got)
	}
	for i, w := range want {
		if got[i].Source != w {
			t.Errorf("behavior %d = %q, want %q", i, got[i].Source, w)
		}
	}
	// Subfolders are mirrored under output_dir; a top-level behavior is not nested.
	if got[0].Output != "tests/billing/deep/plans_test.go" {
		t.Errorf("nested output = %q, want tests/billing/deep/plans_test.go", got[0].Output)
	}
	if got[1].Output != "tests/billing/invoices_test.go" {
		t.Errorf("nested output = %q, want tests/billing/invoices_test.go", got[1].Output)
	}
	if got[2].Output != "tests/one_test.go" {
		t.Errorf("top-level output = %q, want tests/one_test.go", got[2].Output)
	}
}

func TestSameNameInDifferentSubdirectoriesDoesNotCollide(t *testing.T) {
	root := writeProject(t, map[string]string{
		"katana.yaml": `
version: 1
behaviors:
  - path: behaviors
`,
		"behaviors/billing/limits.md": "# billing limits",
		"behaviors/auth/limits.md":    "# auth limits",
	})

	cfg, err := Load(filepath.Join(root, "katana.yaml"))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	got, err := cfg.Resolve()
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("got %d behaviors, want 2: %+v", len(got), got)
	}
	if got[0].Output != "tests/auth/limits_test.go" || got[1].Output != "tests/billing/limits_test.go" {
		t.Fatalf("outputs should mirror subfolders, got %q and %q", got[0].Output, got[1].Output)
	}
}

func TestDoubleStarGlobSpansDirectories(t *testing.T) {
	root := writeProject(t, map[string]string{
		"katana.yaml": `
version: 1
behaviors:
  - path: behaviors/**/*.md
`,
		"behaviors/one.md":              "# one",
		"behaviors/billing/invoices.md": "# invoices",
		"behaviors/billing/notes.txt":   "ignored",
		"docs/other.md":                 "outside",
	})

	cfg, err := Load(filepath.Join(root, "katana.yaml"))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	got, err := cfg.Resolve()
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	want := []string{"behaviors/billing/invoices.md", "behaviors/one.md"}
	if len(got) != len(want) {
		t.Fatalf("got %d behaviors, want %d: %+v", len(got), len(want), got)
	}
	for i, w := range want {
		if got[i].Source != w {
			t.Errorf("behavior %d = %q, want %q", i, got[i].Source, w)
		}
	}
	// A ** glob roots at its fixed prefix, so nesting is relative to behaviors/.
	if got[0].Output != "tests/billing/invoices_test.go" {
		t.Errorf("nested output = %q, want tests/billing/invoices_test.go", got[0].Output)
	}
}

func TestSingleStarGlobStaysInOneDirectory(t *testing.T) {
	root := writeProject(t, map[string]string{
		"katana.yaml": `
version: 1
behaviors:
  - path: behaviors/*.md
`,
		"behaviors/one.md":              "# one",
		"behaviors/billing/invoices.md": "# invoices",
	})

	cfg, err := Load(filepath.Join(root, "katana.yaml"))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	got, err := cfg.Resolve()
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if len(got) != 1 || got[0].Source != "behaviors/one.md" {
		t.Fatalf("* should not cross directories, got %+v", got)
	}
}

func TestUnknownFieldIsRejected(t *testing.T) {
	root := writeProject(t, map[string]string{
		"katana.yaml": `
version: 1
harnes:
  name: claude
behaviors:
  - path: b.md
`,
		"b.md": "# b",
	})
	if _, err := Load(filepath.Join(root, "katana.yaml")); err == nil {
		t.Fatal("a misspelled key should be an error, not silently ignored")
	}
}

func TestFindWalksUpward(t *testing.T) {
	root := writeProject(t, map[string]string{
		"katana.yaml":   "version: 1\nbehaviors:\n  - path: b.md\n",
		"b.md":          "# b",
		"deep/nested/x": "",
	})
	got, err := Find(filepath.Join(root, "deep", "nested"))
	if err != nil {
		t.Fatalf("Find: %v", err)
	}
	want := filepath.Join(root, "katana.yaml")
	if got != want {
		t.Errorf("Find = %q, want %q", got, want)
	}
}

func TestHarnessTimeout(t *testing.T) {
	c := &Config{}
	d, err := c.HarnessTimeout()
	if err != nil || d.Minutes() != 10 {
		t.Errorf("default timeout = %v, %v; want 10m", d, err)
	}
	c.Harness.Timeout = "90s"
	if d, err = c.HarnessTimeout(); err != nil || d.Seconds() != 90 {
		t.Errorf("parsed timeout = %v, %v; want 90s", d, err)
	}
	c.Harness.Timeout = "nonsense"
	if _, err = c.HarnessTimeout(); err == nil {
		t.Error("expected an error for an unparseable duration")
	}
}

func TestNameTemplates(t *testing.T) {
	cases := []struct{ tmpl, src, want string }{
		{"{snake}_test.go", "behaviors/checkout-flow.md", "checkout_flow_test.go"},
		{"test_{snake}.py", "behaviors/checkout flow.md", "test_checkout_flow.py"},
		{"{Name}Test.java", "behaviors/user-auth.md", "UserAuthTest.java"},
		{"{name}.test.ts", "behaviors/Checkout.md", "Checkout.test.ts"},
		{"{snake}_test.go", "behaviors/checkoutFlow.md", "checkout_flow_test.go"},
	}
	for _, c := range cases {
		if got := renderTemplate(c.tmpl, c.src); got != c.want {
			t.Errorf("renderTemplate(%q, %q) = %q, want %q", c.tmpl, c.src, got, c.want)
		}
	}
}
