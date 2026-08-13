// Package internal holds katana's generated behavior tests for its internal
// packages.
//
// This file covers behaviors/internal/config.md: locating katana.yaml, loading
// it, filling in what the project left unsaid, rejecting what katana cannot
// honour, and expanding behavior sources into concrete generation work.
//
// Every assertion goes through the config package's exported API. The
// specification also describes path expansion, template rendering and the
// snake_case/PascalCase conversions, which are not exported on their own — they
// are exercised through Resolve, which is the only way a project reaches them.
package internal

import (
	"errors"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/adaptive-scale/katana/internal/config"
)

// minimalConfig is a config that loads: version 1 and one behavior. Load never
// touches the behavior file, so tests about loading and validation do not have
// to create it.
const minimalConfig = "version: 1\nbehaviors:\n  - path: behaviors/one.md\n"

// newProject lays out a katana project in a temp directory, mirroring the
// helper the config package's own tests use.
func newProject(t *testing.T, files map[string]string) string {
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

// loadConfig writes a project and loads its katana.yaml, failing the test if
// the load is rejected.
func loadConfig(t *testing.T, files map[string]string) *config.Config {
	t.Helper()
	root := newProject(t, files)
	cfg, err := config.Load(filepath.Join(root, config.FileName))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	return cfg
}

// loadConfigErr writes a project and returns the error its katana.yaml is
// rejected with, failing the test if it loads.
func loadConfigErr(t *testing.T, files map[string]string) error {
	t.Helper()
	root := newProject(t, files)
	cfg, err := config.Load(filepath.Join(root, config.FileName))
	if err == nil {
		t.Fatalf("Load succeeded, want an error; config = %+v", cfg)
	}
	return err
}

// resolved writes a project, loads it and resolves its behaviors.
func resolved(t *testing.T, files map[string]string) []config.Resolved {
	t.Helper()
	rs, err := loadConfig(t, files).Resolve()
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	return rs
}

// resolveErr returns the error a project's behaviors fail to resolve with.
func resolveErr(t *testing.T, files map[string]string) error {
	t.Helper()
	rs, err := loadConfig(t, files).Resolve()
	if err == nil {
		t.Fatalf("Resolve succeeded, want an error; resolved = %+v", rs)
	}
	return err
}

// only resolves a project that is expected to yield exactly one behavior.
func only(t *testing.T, files map[string]string) config.Resolved {
	t.Helper()
	rs := resolved(t, files)
	if len(rs) != 1 {
		t.Fatalf("resolved %d behaviors, want 1: %+v", len(rs), rs)
	}
	return rs[0]
}

func sourcePaths(rs []config.Resolved) []string {
	out := make([]string, 0, len(rs))
	for _, r := range rs {
		out = append(out, r.Source)
	}
	return out
}

func outputPaths(rs []config.Resolved) []string {
	out := make([]string, 0, len(rs))
	for _, r := range rs {
		out = append(out, r.Output)
	}
	return out
}

// equalPaths compares two paths through the symlinks a temp directory may sit
// behind (on macOS /var is a link to /private/var).
func equalPaths(a, b string) bool {
	if a == b {
		return true
	}
	ra, err := filepath.EvalSymlinks(a)
	if err != nil {
		ra = a
	}
	rb, err := filepath.EvalSymlinks(b)
	if err != nil {
		rb = b
	}
	return ra == rb
}

// renderedName resolves a project holding a single behavior file and returns
// the base name of the file the template produced. It is how the specification's
// template and case-conversion rules are observed from outside the package.
func renderedName(t *testing.T, template, behaviorFile string) string {
	t.Helper()
	r := only(t, map[string]string{
		"katana.yaml": "version: 1\ndefaults:\n  output_dir: tests\n  output_template: \"" + template + "\"\n" +
			"behaviors:\n  - path: behaviors\n",
		"behaviors/" + behaviorFile: "# behavior",
	})
	return path.Base(r.Output)
}

func assertTestPath(t *testing.T, language, p string, want bool) {
	t.Helper()
	if got := config.IsTestPath(language, p); got != want {
		t.Errorf("IsTestPath(%q, %q) = %v, want %v", language, p, got, want)
	}
}

// --- Locating the configuration file ---------------------------------------

func TestFindReturnsTheConfigInTheStartingDirectory(t *testing.T) {
	root := newProject(t, map[string]string{"katana.yaml": minimalConfig})

	got, err := config.Find(root)
	if err != nil {
		t.Fatalf("Find: %v", err)
	}
	if want := filepath.Join(root, "katana.yaml"); !equalPaths(got, want) {
		t.Errorf("Find(%q) = %q, want %q", root, got, want)
	}
}

func TestFindWalksUpwardsToAnAncestorDirectory(t *testing.T) {
	root := newProject(t, map[string]string{
		"katana.yaml":    minimalConfig,
		"a/b/c/keep.txt": "",
	})
	start := filepath.Join(root, "a", "b", "c")

	got, err := config.Find(start)
	if err != nil {
		t.Fatalf("Find: %v", err)
	}
	if want := filepath.Join(root, "katana.yaml"); !equalPaths(got, want) {
		t.Errorf("Find(%q) = %q, want %q", start, got, want)
	}
}

func TestFindReturnsTheFirstConfigFoundWalkingUpwards(t *testing.T) {
	root := newProject(t, map[string]string{
		"katana.yaml":        minimalConfig,
		"nested/katana.yaml": minimalConfig,
	})
	start := filepath.Join(root, "nested")

	got, err := config.Find(start)
	if err != nil {
		t.Fatalf("Find: %v", err)
	}
	if want := filepath.Join(start, "katana.yaml"); !equalPaths(got, want) {
		t.Errorf("Find(%q) = %q, want the nearest config %q", start, got, want)
	}
}

func TestFindResolvesARelativeStartingDirectory(t *testing.T) {
	root := newProject(t, map[string]string{
		"katana.yaml":          minimalConfig,
		"deep/nested/keep.txt": "",
	})
	t.Chdir(filepath.Join(root, "deep", "nested"))

	got, err := config.Find(".")
	if err != nil {
		t.Fatalf("Find: %v", err)
	}
	if want := filepath.Join(root, "katana.yaml"); !equalPaths(got, want) {
		t.Errorf(`Find(".") = %q, want %q`, got, want)
	}
}

func TestFindReportsTheStartingDirectoryWhenNoConfigExists(t *testing.T) {
	start := t.TempDir()

	got, err := config.Find(start)
	if err == nil {
		t.Fatalf("Find(%q) = %q, want an error", start, got)
	}
	want := "no katana.yaml found in " + start + " or any parent directory (run `katana init` first)"
	if err.Error() != want {
		t.Errorf("error = %q, want %q", err.Error(), want)
	}
}

// --- Loading and parsing ---------------------------------------------------

func TestLoadSurfacesAMissingFileUnchanged(t *testing.T) {
	p := filepath.Join(t.TempDir(), "katana.yaml")

	_, err := config.Load(p)
	if err == nil {
		t.Fatal("Load of a missing file succeeded, want an error")
	}
	if !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("error = %v, want the underlying read failure", err)
	}
	if strings.Contains(err.Error(), "parsing") {
		t.Errorf("a read failure should not be reported as a parse failure: %v", err)
	}
}

func TestLoadSurfacesAnUnreadableFileUnchanged(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root can read a file with no permission bits set")
	}
	root := newProject(t, map[string]string{"katana.yaml": minimalConfig})
	p := filepath.Join(root, "katana.yaml")
	if err := os.Chmod(p, 0o000); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(p, 0o644) })

	_, err := config.Load(p)
	if err == nil {
		t.Fatal("Load of an unreadable file succeeded, want an error")
	}
	if !errors.Is(err, fs.ErrPermission) {
		t.Errorf("error = %v, want the underlying read failure", err)
	}
}

func TestLoadRejectsAnUnknownField(t *testing.T) {
	root := newProject(t, map[string]string{
		"katana.yaml": "version: 1\nharnes:\n  name: claude\nbehaviors:\n  - path: behaviors/one.md\n",
	})
	p := filepath.Join(root, "katana.yaml")

	_, err := config.Load(p)
	if err == nil {
		t.Fatal("a misspelled key should be an error, not silently ignored")
	}
	if prefix := "parsing " + p + ": "; !strings.HasPrefix(err.Error(), prefix) {
		t.Errorf("error = %q, want it to start with %q", err.Error(), prefix)
	}
}

func TestLoadAcceptsEveryRecognisedTopLevelSection(t *testing.T) {
	cfg := loadConfig(t, map[string]string{
		"katana.yaml": `
version: 1
harness:
  name: codex
defaults:
  language: python
test:
  command: pytest -q
behaviors:
  - path: behaviors/one.md
`,
	})

	if cfg.Version != 1 {
		t.Errorf("version = %d, want 1", cfg.Version)
	}
	if cfg.Harness.Name != "codex" {
		t.Errorf("harness.name = %q, want codex", cfg.Harness.Name)
	}
	if cfg.Defaults.Language != "python" {
		t.Errorf("defaults.language = %q, want python", cfg.Defaults.Language)
	}
	if cfg.Test.Command != "pytest -q" {
		t.Errorf("test.command = %q, want %q", cfg.Test.Command, "pytest -q")
	}
	if len(cfg.Behaviors) != 1 || cfg.Behaviors[0].Path != "behaviors/one.md" {
		t.Errorf("behaviors = %+v", cfg.Behaviors)
	}
}

func TestTheConfigDirectoryBecomesTheProjectRoot(t *testing.T) {
	root := newProject(t, map[string]string{"katana.yaml": minimalConfig})

	cfg, err := config.Load(filepath.Join(root, "katana.yaml"))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if !equalPaths(cfg.Root, root) {
		t.Errorf("Root = %q, want %q", cfg.Root, root)
	}
}

func TestRelativePathsResolveAgainstTheProjectRootNotTheWorkingDirectory(t *testing.T) {
	root := newProject(t, map[string]string{
		"katana.yaml":      "version: 1\nbehaviors:\n  - path: behaviors\n",
		"behaviors/one.md": "# one",
	})
	// A working directory with no behaviors of its own: resolving against it
	// would fail rather than find behaviors/one.md.
	t.Chdir(t.TempDir())

	cfg, err := config.Load(filepath.Join(root, "katana.yaml"))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	rs, err := cfg.Resolve()
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if got := sourcePaths(rs); !reflect.DeepEqual(got, []string{"behaviors/one.md"}) {
		t.Errorf("sources = %v, want [behaviors/one.md]", got)
	}
}

func TestDefaultsAreAppliedBeforeValidation(t *testing.T) {
	// An absent version must be validated as the 1 it defaults to, not as the
	// zero value, which validation would reject as unsupported.
	cfg := loadConfig(t, map[string]string{
		"katana.yaml": "behaviors:\n  - path: behaviors/one.md\n",
	})
	if cfg.Version != 1 {
		t.Errorf("version = %d, want 1", cfg.Version)
	}
}

// --- Defaults applied to an incomplete configuration -----------------------

func TestZeroVersionDefaultsToOne(t *testing.T) {
	cfg := loadConfig(t, map[string]string{
		"katana.yaml": "version: 0\nbehaviors:\n  - path: behaviors/one.md\n",
	})
	if cfg.Version != 1 {
		t.Errorf("version = %d, want 1", cfg.Version)
	}
}

func TestAbsentHarnessNameDefaultsToClaude(t *testing.T) {
	cfg := loadConfig(t, map[string]string{"katana.yaml": minimalConfig})
	if cfg.Harness.Name != "claude" {
		t.Errorf("harness.name = %q, want claude", cfg.Harness.Name)
	}
}

func TestAbsentDefaultLanguageDefaultsToGo(t *testing.T) {
	cfg := loadConfig(t, map[string]string{"katana.yaml": minimalConfig})
	if cfg.Defaults.Language != "go" {
		t.Errorf("defaults.language = %q, want go", cfg.Defaults.Language)
	}
}

func TestAbsentOutputDirDefaultsToTests(t *testing.T) {
	cfg := loadConfig(t, map[string]string{"katana.yaml": minimalConfig})
	if cfg.Defaults.OutputDir != "tests" {
		t.Errorf("defaults.output_dir = %q, want tests", cfg.Defaults.OutputDir)
	}
}

func TestAbsentFrameworkFollowsTheDefaultLanguage(t *testing.T) {
	cfg := loadConfig(t, map[string]string{
		"katana.yaml": "version: 1\ndefaults:\n  language: python\nbehaviors:\n  - path: behaviors/one.md\n",
	})
	if cfg.Defaults.Framework != "pytest" {
		t.Errorf("defaults.framework = %q, want pytest", cfg.Defaults.Framework)
	}
}

func TestAbsentOutputTemplateFollowsTheDefaultLanguage(t *testing.T) {
	cfg := loadConfig(t, map[string]string{
		"katana.yaml": "version: 1\ndefaults:\n  language: python\nbehaviors:\n  - path: behaviors/one.md\n",
	})
	if cfg.Defaults.OutputTemplate != "test_{snake}.py" {
		t.Errorf("defaults.output_template = %q, want test_{snake}.py", cfg.Defaults.OutputTemplate)
	}
}

func TestAbsentTestCommandFollowsTheDefaultLanguage(t *testing.T) {
	cfg := loadConfig(t, map[string]string{
		"katana.yaml": "version: 1\ndefaults:\n  language: python\nbehaviors:\n  - path: behaviors/one.md\n",
	})
	if cfg.Test.Command != "pytest" {
		t.Errorf("test.command = %q, want pytest", cfg.Test.Command)
	}
}

func TestAConfigurationThatSetsNothingGetsTheGoConventions(t *testing.T) {
	cfg := loadConfig(t, map[string]string{
		"katana.yaml": "behaviors:\n  - path: behaviors/one.md\n",
	})
	if cfg.Defaults.Language != "go" {
		t.Errorf("defaults.language = %q, want go", cfg.Defaults.Language)
	}
	if cfg.Defaults.Framework != "go-test" {
		t.Errorf("defaults.framework = %q, want go-test", cfg.Defaults.Framework)
	}
	if cfg.Defaults.OutputTemplate != "{snake}_test.go" {
		t.Errorf("defaults.output_template = %q, want {snake}_test.go", cfg.Defaults.OutputTemplate)
	}
	if cfg.Test.Command != "go test ./..." {
		t.Errorf("test.command = %q, want %q", cfg.Test.Command, "go test ./...")
	}
}

// --- Configuration that is rejected ---------------------------------------

func TestUnsupportedVersionIsRejected(t *testing.T) {
	err := loadConfigErr(t, map[string]string{
		"katana.yaml": "version: 2\nbehaviors:\n  - path: behaviors/one.md\n",
	})
	want := "unsupported config version 2 (this katana understands version 1)"
	if err.Error() != want {
		t.Errorf("error = %q, want %q", err.Error(), want)
	}
}

func TestAbsentBehaviorsListIsRejected(t *testing.T) {
	err := loadConfigErr(t, map[string]string{"katana.yaml": "version: 1\n"})
	want := "no behaviors configured in katana.yaml"
	if err.Error() != want {
		t.Errorf("error = %q, want %q", err.Error(), want)
	}
}

func TestEmptyBehaviorsListIsRejected(t *testing.T) {
	err := loadConfigErr(t, map[string]string{"katana.yaml": "version: 1\nbehaviors: []\n"})
	want := "no behaviors configured in katana.yaml"
	if err.Error() != want {
		t.Errorf("error = %q, want %q", err.Error(), want)
	}
}

func TestBehaviorWithAnEmptyPathIsRejected(t *testing.T) {
	err := loadConfigErr(t, map[string]string{
		"katana.yaml": "version: 1\nbehaviors:\n  - path: \"\"\n",
	})
	want := "behaviors[0]: path is required"
	if err.Error() != want {
		t.Errorf("error = %q, want %q", err.Error(), want)
	}
}

func TestBehaviorWithAWhitespacePathIsRejected(t *testing.T) {
	err := loadConfigErr(t, map[string]string{
		"katana.yaml": "version: 1\nbehaviors:\n  - path: \"   \"\n",
	})
	want := "behaviors[0]: path is required"
	if err.Error() != want {
		t.Errorf("error = %q, want %q", err.Error(), want)
	}
}

func TestMissingPathIsReportedWithItsPositionInTheList(t *testing.T) {
	err := loadConfigErr(t, map[string]string{
		"katana.yaml": "version: 1\nbehaviors:\n  - path: behaviors/one.md\n  - path: \"\"\n",
	})
	want := "behaviors[1]: path is required"
	if err.Error() != want {
		t.Errorf("error = %q, want %q", err.Error(), want)
	}
}

func TestExplicitOutputForAGlobPathIsRejected(t *testing.T) {
	for _, pattern := range []string{"behaviors/*.md", "behaviors/one?.md", "behaviors/[ab].md"} {
		t.Run(pattern, func(t *testing.T) {
			err := loadConfigErr(t, map[string]string{
				"katana.yaml": "version: 1\nbehaviors:\n  - path: \"" + pattern + "\"\n    output: tests/one_test.go\n",
			})
			want := "behaviors[0]: output cannot be set for a glob path \"" + pattern +
				"\"; use output_template or list the files individually"
			if err.Error() != want {
				t.Errorf("error = %q, want %q", err.Error(), want)
			}
		})
	}
}

func TestUnknownHarnessPromptIsRejected(t *testing.T) {
	err := loadConfigErr(t, map[string]string{
		"katana.yaml": "version: 1\nharness:\n  prompt: pipe\nbehaviors:\n  - path: behaviors/one.md\n",
	})
	want := `harness.prompt must be "stdin" or "arg", got "pipe"`
	if err.Error() != want {
		t.Errorf("error = %q, want %q", err.Error(), want)
	}
}

func TestAcceptedHarnessPrompts(t *testing.T) {
	for name, harnessSection := range map[string]string{
		"stdin":  "harness:\n  prompt: stdin\n",
		"arg":    "harness:\n  prompt: arg\n",
		"empty":  "harness:\n  prompt: \"\"\n",
		"absent": "",
	} {
		t.Run(name, func(t *testing.T) {
			loadConfig(t, map[string]string{
				"katana.yaml": "version: 1\n" + harnessSection + "behaviors:\n  - path: behaviors/one.md\n",
			})
		})
	}
}

func TestNegativeHarnessJobsIsRejected(t *testing.T) {
	err := loadConfigErr(t, map[string]string{
		"katana.yaml": "version: 1\nharness:\n  jobs: -1\nbehaviors:\n  - path: behaviors/one.md\n",
	})
	want := "harness.jobs must be zero or positive, got -1"
	if err.Error() != want {
		t.Errorf("error = %q, want %q", err.Error(), want)
	}
}

func TestZeroHarnessJobsIsAccepted(t *testing.T) {
	cfg := loadConfig(t, map[string]string{
		"katana.yaml": "version: 1\nharness:\n  jobs: 0\nbehaviors:\n  - path: behaviors/one.md\n",
	})
	if cfg.Harness.Jobs != 0 {
		t.Errorf("harness.jobs = %d, want 0", cfg.Harness.Jobs)
	}
}

func TestAnInvalidTimeoutIsRejectedAtLoadTime(t *testing.T) {
	err := loadConfigErr(t, map[string]string{
		"katana.yaml": "version: 1\nharness:\n  timeout: nonsense\nbehaviors:\n  - path: behaviors/one.md\n",
	})
	if prefix := `harness.timeout "nonsense": `; !strings.HasPrefix(err.Error(), prefix) {
		t.Errorf("error = %q, want it to start with %q", err.Error(), prefix)
	}
}

// --- Generation timeout ---------------------------------------------------

func TestAbsentTimeoutIsTenMinutes(t *testing.T) {
	got, err := (&config.Config{}).HarnessTimeout()
	if err != nil {
		t.Fatalf("HarnessTimeout: %v", err)
	}
	if got != 10*time.Minute {
		t.Errorf("timeout = %v, want 10m", got)
	}
}

func TestWhitespaceTimeoutIsTenMinutes(t *testing.T) {
	cfg := &config.Config{}
	cfg.Harness.Timeout = "   "

	got, err := cfg.HarnessTimeout()
	if err != nil {
		t.Fatalf("HarnessTimeout: %v", err)
	}
	if got != 10*time.Minute {
		t.Errorf("timeout = %v, want 10m", got)
	}
}

func TestTimeoutIsWrittenInGoDurationSyntax(t *testing.T) {
	for written, want := range map[string]time.Duration{
		"10m": 10 * time.Minute,
		"90s": 90 * time.Second,
	} {
		t.Run(written, func(t *testing.T) {
			cfg := loadConfig(t, map[string]string{
				"katana.yaml": "version: 1\nharness:\n  timeout: " + written + "\nbehaviors:\n  - path: behaviors/one.md\n",
			})
			got, err := cfg.HarnessTimeout()
			if err != nil {
				t.Fatalf("HarnessTimeout: %v", err)
			}
			if got != want {
				t.Errorf("timeout = %v, want %v", got, want)
			}
		})
	}
}

func TestUnparseableTimeoutIsRejected(t *testing.T) {
	cfg := &config.Config{}
	cfg.Harness.Timeout = "nonsense"

	_, err := cfg.HarnessTimeout()
	if err == nil {
		t.Fatal("expected an error for an unparseable duration")
	}
	if prefix := `harness.timeout "nonsense": `; !strings.HasPrefix(err.Error(), prefix) {
		t.Errorf("error = %q, want it to start with %q", err.Error(), prefix)
	}
}

func TestZeroTimeoutIsRejected(t *testing.T) {
	cfg := &config.Config{}
	cfg.Harness.Timeout = "0s"

	_, err := cfg.HarnessTimeout()
	if err == nil {
		t.Fatal("expected an error for a zero duration")
	}
	want := `harness.timeout must be positive, got "0s"`
	if err.Error() != want {
		t.Errorf("error = %q, want %q", err.Error(), want)
	}
}

func TestNegativeTimeoutIsRejected(t *testing.T) {
	cfg := &config.Config{}
	cfg.Harness.Timeout = "-5m"

	_, err := cfg.HarnessTimeout()
	if err == nil {
		t.Fatal("expected an error for a negative duration")
	}
	want := `harness.timeout must be positive, got "-5m"`
	if err.Error() != want {
		t.Errorf("error = %q, want %q", err.Error(), want)
	}
}

// --- Generation concurrency ----------------------------------------------

func TestConfiguredJobsIsTheNumberGeneratedAtOnce(t *testing.T) {
	cfg := loadConfig(t, map[string]string{
		"katana.yaml": "version: 1\nharness:\n  jobs: 3\nbehaviors:\n  - path: behaviors/one.md\n",
	})
	if got := cfg.HarnessJobs(); got != 3 {
		t.Errorf("HarnessJobs() = %d, want 3", got)
	}
}

func TestAbsentJobsIsFourConcurrentGenerations(t *testing.T) {
	cfg := loadConfig(t, map[string]string{"katana.yaml": minimalConfig})
	if got := cfg.HarnessJobs(); got != 4 {
		t.Errorf("HarnessJobs() = %d, want 4", got)
	}
	if config.DefaultJobs != 4 {
		t.Errorf("DefaultJobs = %d, want 4", config.DefaultJobs)
	}
}

func TestZeroJobsIsFourConcurrentGenerations(t *testing.T) {
	cfg := loadConfig(t, map[string]string{
		"katana.yaml": "version: 1\nharness:\n  jobs: 0\nbehaviors:\n  - path: behaviors/one.md\n",
	})
	if got := cfg.HarnessJobs(); got != 4 {
		t.Errorf("HarnessJobs() = %d, want 4", got)
	}
}

func TestOneJobMeansSequentialGeneration(t *testing.T) {
	cfg := loadConfig(t, map[string]string{
		"katana.yaml": "version: 1\nharness:\n  jobs: 1\nbehaviors:\n  - path: behaviors/one.md\n",
	})
	if got := cfg.HarnessJobs(); got != 1 {
		t.Errorf("HarnessJobs() = %d, want 1", got)
	}
}

// --- Where discovered behaviours are written ------------------------------

func TestBehaviorDirIsTheFixedPartBeforeAWildcard(t *testing.T) {
	cfg := loadConfig(t, map[string]string{
		"katana.yaml": "version: 1\nbehaviors:\n  - path: specs/**/*.md\n",
	})
	if got := cfg.BehaviorDir(); got != "specs" {
		t.Errorf("BehaviorDir() = %q, want specs", got)
	}
}

func TestBehaviorDirComesFromTheFirstUsableConfiguredPath(t *testing.T) {
	cfg := loadConfig(t, map[string]string{
		"katana.yaml": "version: 1\nbehaviors:\n  - path: specs\n  - path: docs\n",
	})
	if got := cfg.BehaviorDir(); got != "specs" {
		t.Errorf("BehaviorDir() = %q, want specs from the first behavior", got)
	}
}

func TestBehaviorDirIsADirectoryPathItself(t *testing.T) {
	cfg := loadConfig(t, map[string]string{
		"katana.yaml":       "version: 1\nbehaviors:\n  - path: docs/specs\n",
		"docs/specs/one.md": "# one",
	})
	if got := cfg.BehaviorDir(); got != "docs/specs" {
		t.Errorf("BehaviorDir() = %q, want docs/specs", got)
	}
}

func TestBehaviorDirIsTheContainingDirectoryOfASingleFile(t *testing.T) {
	cfg := loadConfig(t, map[string]string{
		"katana.yaml":      "version: 1\nbehaviors:\n  - path: docs/checkout.md\n",
		"docs/checkout.md": "# checkout",
	})
	if got := cfg.BehaviorDir(); got != "docs" {
		t.Errorf("BehaviorDir() = %q, want docs", got)
	}
}

func TestBehaviorDirIsTheParentOfABehaviorFileThatDoesNotExistYet(t *testing.T) {
	cfg := loadConfig(t, map[string]string{
		"katana.yaml": "version: 1\nbehaviors:\n  - path: docs/specs/checkout.md\n",
	})
	if got := cfg.BehaviorDir(); got != "docs/specs" {
		t.Errorf("BehaviorDir() = %q, want docs/specs", got)
	}
}

func TestBehaviorDirSkipsAPathWithNoFixedDirectory(t *testing.T) {
	cfg := loadConfig(t, map[string]string{
		"katana.yaml": "version: 1\nbehaviors:\n  - path: \"*.md\"\n  - path: specs\n",
	})
	if got := cfg.BehaviorDir(); got != "specs" {
		t.Errorf("BehaviorDir() = %q, want specs from the second behavior", got)
	}
}

func TestBehaviorDirSkipsTheProjectRoot(t *testing.T) {
	cfg := loadConfig(t, map[string]string{
		"katana.yaml": "version: 1\nbehaviors:\n  - path: \".\"\n  - path: specs\n",
	})
	if got := cfg.BehaviorDir(); got != "specs" {
		t.Errorf("BehaviorDir() = %q, want specs from the second behavior", got)
	}
}

func TestBehaviorDirSkipsTheFilesystemRoot(t *testing.T) {
	cfg := loadConfig(t, map[string]string{
		"katana.yaml": "version: 1\nbehaviors:\n  - path: \"/\"\n  - path: specs\n",
	})
	if got := cfg.BehaviorDir(); got != "specs" {
		t.Errorf("BehaviorDir() = %q, want specs from the second behavior", got)
	}
}

func TestBehaviorDirFallsBackToBehaviors(t *testing.T) {
	cfg := loadConfig(t, map[string]string{
		"katana.yaml": "version: 1\nbehaviors:\n  - path: \".\"\n",
	})
	if got := cfg.BehaviorDir(); got != "behaviors" {
		t.Errorf("BehaviorDir() = %q, want behaviors", got)
	}
}

// --- Expanding behaviour paths -------------------------------------------

func TestNonWildcardFilePathResolvesToThatOneFile(t *testing.T) {
	rs := resolved(t, map[string]string{
		"katana.yaml":      "version: 1\nbehaviors:\n  - path: behaviors/one.md\n",
		"behaviors/one.md": "# one",
		"behaviors/two.md": "# two",
	})
	if got := sourcePaths(rs); !reflect.DeepEqual(got, []string{"behaviors/one.md"}) {
		t.Errorf("sources = %v, want [behaviors/one.md]", got)
	}
}

func TestNonWildcardDirectoryResolvesToEveryMarkdownFileRecursively(t *testing.T) {
	rs := resolved(t, map[string]string{
		"katana.yaml":                     "version: 1\nbehaviors:\n  - path: behaviors\n",
		"behaviors/one.md":                "# one",
		"behaviors/billing/invoices.md":   "# invoices",
		"behaviors/billing/deep/plans.md": "# plans",
		"behaviors/billing/notes.txt":     "not a behavior",
	})
	want := []string{"behaviors/billing/deep/plans.md", "behaviors/billing/invoices.md", "behaviors/one.md"}
	if got := sourcePaths(rs); !reflect.DeepEqual(got, want) {
		t.Errorf("sources = %v, want %v", got, want)
	}
}

func TestMarkdownExtensionIsMatchedCaseInsensitively(t *testing.T) {
	rs := resolved(t, map[string]string{
		"katana.yaml":       "version: 1\nbehaviors:\n  - path: behaviors\n",
		"behaviors/loud.MD": "# loud",
	})
	if got := sourcePaths(rs); !reflect.DeepEqual(got, []string{"behaviors/loud.MD"}) {
		t.Errorf("sources = %v, want [behaviors/loud.MD]", got)
	}
}

func TestMissingNonWildcardPathReportsTheStatError(t *testing.T) {
	err := resolveErr(t, map[string]string{
		"katana.yaml": "version: 1\nbehaviors:\n  - path: behaviors/missing.md\n",
	})
	if prefix := `behaviors[0]: "behaviors/missing.md": `; !strings.HasPrefix(err.Error(), prefix) {
		t.Errorf("error = %q, want it to start with %q", err.Error(), prefix)
	}
	if !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("error = %v, want the underlying stat error", err)
	}
}

func TestDoubleStarMatchesAnyNumberOfSegments(t *testing.T) {
	rs := resolved(t, map[string]string{
		"katana.yaml":            "version: 1\nbehaviors:\n  - path: behaviors/**/*.md\n",
		"behaviors/one.md":       "# zero segments",
		"behaviors/a/two.md":     "# one segment",
		"behaviors/a/b/three.md": "# two segments",
		"docs/outside.md":        "# outside the pattern",
	})
	want := []string{"behaviors/a/b/three.md", "behaviors/a/two.md", "behaviors/one.md"}
	if got := sourcePaths(rs); !reflect.DeepEqual(got, want) {
		t.Errorf("sources = %v, want %v", got, want)
	}
}

func TestSingleStarStaysWithinOnePathSegment(t *testing.T) {
	rs := resolved(t, map[string]string{
		"katana.yaml":          "version: 1\nbehaviors:\n  - path: behaviors/*.md\n",
		"behaviors/one.md":     "# one",
		"behaviors/sub/two.md": "# two",
	})
	if got := sourcePaths(rs); !reflect.DeepEqual(got, []string{"behaviors/one.md"}) {
		t.Errorf("sources = %v, want [behaviors/one.md]: * must not cross a /", got)
	}
}

func TestDoubleStarWithAMissingBaseDirectoryYieldsNoMatches(t *testing.T) {
	// Expansion is only reachable through Resolve, which turns an empty match
	// set into "matched no files" — the point is that it is not a stat error.
	err := resolveErr(t, map[string]string{
		"katana.yaml": "version: 1\nbehaviors:\n  - path: specs/**/*.md\n",
	})
	want := `behaviors[0]: "specs/**/*.md" matched no files`
	if err.Error() != want {
		t.Errorf("error = %q, want %q", err.Error(), want)
	}
	if errors.Is(err, fs.ErrNotExist) {
		t.Errorf("an absent base directory should be no matches, not a filesystem error: %v", err)
	}
}

func TestQuestionMarkGlobIsExpanded(t *testing.T) {
	rs := resolved(t, map[string]string{
		"katana.yaml":        "version: 1\nbehaviors:\n  - path: behaviors/api?.md\n",
		"behaviors/api1.md":  "# api1",
		"behaviors/api2.md":  "# api2",
		"behaviors/api10.md": "# api10",
	})
	want := []string{"behaviors/api1.md", "behaviors/api2.md"}
	if got := sourcePaths(rs); !reflect.DeepEqual(got, want) {
		t.Errorf("sources = %v, want %v", got, want)
	}
}

func TestCharacterClassGlobIsExpanded(t *testing.T) {
	rs := resolved(t, map[string]string{
		"katana.yaml":        "version: 1\nbehaviors:\n  - path: behaviors/[ab]*.md\n",
		"behaviors/alpha.md": "# alpha",
		"behaviors/beta.md":  "# beta",
		"behaviors/gamma.md": "# gamma",
	})
	want := []string{"behaviors/alpha.md", "behaviors/beta.md"}
	if got := sourcePaths(rs); !reflect.DeepEqual(got, want) {
		t.Errorf("sources = %v, want %v", got, want)
	}
}

func TestDirectoriesAreNeverMatched(t *testing.T) {
	// behaviors/* names the sub directory as well as the file; only the file is
	// a behavior.
	rs := resolved(t, map[string]string{
		"katana.yaml":          "version: 1\nbehaviors:\n  - path: behaviors/*\n",
		"behaviors/one.md":     "# one",
		"behaviors/sub/two.md": "# two",
	})
	if got := sourcePaths(rs); !reflect.DeepEqual(got, []string{"behaviors/one.md"}) {
		t.Errorf("sources = %v, want [behaviors/one.md]", got)
	}
}

func TestHiddenDirectoriesAreSkipped(t *testing.T) {
	rs := resolved(t, map[string]string{
		"katana.yaml":               "version: 1\nbehaviors:\n  - path: behaviors\n",
		"behaviors/one.md":          "# one",
		"behaviors/.git/hooks.md":   "# tool state",
		"behaviors/.katana/last.md": "# tool state",
	})
	if got := sourcePaths(rs); !reflect.DeepEqual(got, []string{"behaviors/one.md"}) {
		t.Errorf("sources = %v, want [behaviors/one.md]", got)
	}
}

func TestMatchesAreProjectRelativeForwardSlashPathsInAlphabeticalOrder(t *testing.T) {
	root := newProject(t, map[string]string{
		"katana.yaml":      "version: 1\nbehaviors:\n  - path: behaviors\n",
		"behaviors/c.md":   "# c",
		"behaviors/a.md":   "# a",
		"behaviors/b/d.md": "# d",
	})
	cfg, err := config.Load(filepath.Join(root, "katana.yaml"))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	rs, err := cfg.Resolve()
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}

	want := []string{"behaviors/a.md", "behaviors/b/d.md", "behaviors/c.md"}
	got := sourcePaths(rs)
	if !reflect.DeepEqual(got, want) {
		t.Errorf("sources = %v, want %v", got, want)
	}
	for _, s := range got {
		if strings.Contains(s, "\\") {
			t.Errorf("source %q should use forward slashes", s)
		}
		if filepath.IsAbs(s) || strings.HasPrefix(s, root) {
			t.Errorf("source %q should be relative to the project root", s)
		}
	}
}

// --- Resolving behaviours into generation work ---------------------------

func TestResolveCarriesEveryPerBehaviorSetting(t *testing.T) {
	r := only(t, map[string]string{
		"katana.yaml": `
version: 1
harness:
  name: claude
behaviors:
  - path: behaviors/one.md
    output: tests/custom_test.py
    language: python
    framework: pytest
    harness: codex
    instructions: Stub the payment gateway.
`,
		"behaviors/one.md": "# one",
	})

	if r.Source != "behaviors/one.md" {
		t.Errorf("Source = %q, want behaviors/one.md", r.Source)
	}
	if r.Output != "tests/custom_test.py" {
		t.Errorf("Output = %q, want tests/custom_test.py", r.Output)
	}
	if r.Language != "python" {
		t.Errorf("Language = %q, want python", r.Language)
	}
	if r.Framework != "pytest" {
		t.Errorf("Framework = %q, want pytest", r.Framework)
	}
	if r.Harness != "codex" {
		t.Errorf("Harness = %q, want codex", r.Harness)
	}
	if strings.TrimSpace(r.Instructions) != "Stub the payment gateway." {
		t.Errorf("Instructions = %q, want the per-behavior instructions", r.Instructions)
	}
}

func TestResolveIsSortedBySourcePath(t *testing.T) {
	rs := resolved(t, map[string]string{
		"katana.yaml": "version: 1\nbehaviors:\n  - path: b/zebra.md\n  - path: a/apple.md\n",
		"b/zebra.md":  "# zebra",
		"a/apple.md":  "# apple",
	})
	want := []string{"a/apple.md", "b/zebra.md"}
	if got := sourcePaths(rs); !reflect.DeepEqual(got, want) {
		t.Errorf("sources = %v, want %v regardless of configuration order", got, want)
	}
}

func TestBehaviorThatMatchesNoFilesIsAnError(t *testing.T) {
	err := resolveErr(t, map[string]string{
		"katana.yaml":         "version: 1\nbehaviors:\n  - path: behaviors/*.md\n",
		"behaviors/notes.txt": "not a behavior",
	})
	want := `behaviors[0]: "behaviors/*.md" matched no files`
	if err.Error() != want {
		t.Errorf("error = %q, want %q", err.Error(), want)
	}
}

func TestExpansionFailureIsReportedWithTheBehaviorIndex(t *testing.T) {
	err := resolveErr(t, map[string]string{
		"katana.yaml":      "version: 1\nbehaviors:\n  - path: behaviors/one.md\n  - path: nope/missing.md\n",
		"behaviors/one.md": "# one",
	})
	if prefix := "behaviors[1]: "; !strings.HasPrefix(err.Error(), prefix) {
		t.Errorf("error = %q, want it to start with %q", err.Error(), prefix)
	}
}

func TestFileMatchedTwiceIsGeneratedOnceAndTheFirstEntryWins(t *testing.T) {
	rs := resolved(t, map[string]string{
		"katana.yaml": `
version: 1
behaviors:
  - path: behaviors/*.md
    language: python
  - path: behaviors/one.md
    language: ruby
`,
		"behaviors/one.md": "# one",
	})
	if len(rs) != 1 {
		t.Fatalf("resolved %d behaviors, want 1: %+v", len(rs), rs)
	}
	if rs[0].Language != "python" {
		t.Errorf("Language = %q, want python from the first matching entry", rs[0].Language)
	}
}

func TestBehaviorLanguageOverridesTheDefaultLanguage(t *testing.T) {
	r := only(t, map[string]string{
		"katana.yaml": "version: 1\ndefaults:\n  language: go\nbehaviors:\n" +
			"  - path: behaviors/one.md\n    language: ruby\n",
		"behaviors/one.md": "# one",
	})
	if r.Language != "ruby" {
		t.Errorf("Language = %q, want ruby", r.Language)
	}
}

func TestWhitespaceBehaviorLanguageFallsBackToTheDefault(t *testing.T) {
	r := only(t, map[string]string{
		"katana.yaml": "version: 1\ndefaults:\n  language: python\nbehaviors:\n" +
			"  - path: behaviors/one.md\n    language: \"   \"\n",
		"behaviors/one.md": "# one",
	})
	if r.Language != "python" {
		t.Errorf("Language = %q, want the default python", r.Language)
	}
}

func TestBehaviorHarnessOverridesTheHarnessName(t *testing.T) {
	r := only(t, map[string]string{
		"katana.yaml": "version: 1\nharness:\n  name: claude\nbehaviors:\n" +
			"  - path: behaviors/one.md\n    harness: codex\n",
		"behaviors/one.md": "# one",
	})
	if r.Harness != "codex" {
		t.Errorf("Harness = %q, want codex", r.Harness)
	}
}

func TestWhitespaceBehaviorHarnessFallsBackToTheHarnessName(t *testing.T) {
	r := only(t, map[string]string{
		"katana.yaml": "version: 1\nharness:\n  name: codex\nbehaviors:\n" +
			"  - path: behaviors/one.md\n    harness: \"  \"\n",
		"behaviors/one.md": "# one",
	})
	if r.Harness != "codex" {
		t.Errorf("Harness = %q, want the configured codex", r.Harness)
	}
}

func TestBehaviorFrameworkIsUsedAsGiven(t *testing.T) {
	r := only(t, map[string]string{
		"katana.yaml": "version: 1\ndefaults:\n  framework: go-test\nbehaviors:\n" +
			"  - path: behaviors/one.md\n    framework: ginkgo\n",
		"behaviors/one.md": "# one",
	})
	if r.Framework != "ginkgo" {
		t.Errorf("Framework = %q, want ginkgo", r.Framework)
	}
}

func TestBehaviorInTheDefaultLanguageInheritsTheDefaultFramework(t *testing.T) {
	r := only(t, map[string]string{
		"katana.yaml": "version: 1\ndefaults:\n  language: go\n  framework: custom-runner\nbehaviors:\n" +
			"  - path: behaviors/one.md\n    language: go\n",
		"behaviors/one.md": "# one",
	})
	if r.Framework != "custom-runner" {
		t.Errorf("Framework = %q, want the configured custom-runner", r.Framework)
	}
}

func TestBehaviorInAnotherLanguageGetsItsOwnConventionalFramework(t *testing.T) {
	r := only(t, map[string]string{
		"katana.yaml": "version: 1\ndefaults:\n  language: go\n  framework: custom-runner\nbehaviors:\n" +
			"  - path: behaviors/one.md\n    language: python\n",
		"behaviors/one.md": "# one",
	})
	if r.Framework != "pytest" {
		t.Errorf("Framework = %q, want pytest rather than the mismatched default", r.Framework)
	}
}

// --- Output paths --------------------------------------------------------

func TestExplicitOutputIsNormalised(t *testing.T) {
	r := only(t, map[string]string{
		"katana.yaml": "version: 1\nbehaviors:\n  - path: behaviors/one.md\n" +
			"    output: \"./tests/gen/../unit/one_test.go\"\n",
		"behaviors/one.md": "# one",
	})
	if r.Output != "tests/unit/one_test.go" {
		t.Errorf("Output = %q, want tests/unit/one_test.go", r.Output)
	}
}

func TestOutputWithoutAnExplicitPathGoesUnderTheOutputDirectory(t *testing.T) {
	r := only(t, map[string]string{
		"katana.yaml": "version: 1\ndefaults:\n  output_dir: generated\n  output_template: \"{snake}_test.go\"\n" +
			"behaviors:\n  - path: behaviors/checkout-flow.md\n",
		"behaviors/checkout-flow.md": "# checkout",
	})
	if r.Output != "generated/checkout_flow_test.go" {
		t.Errorf("Output = %q, want generated/checkout_flow_test.go", r.Output)
	}
}

func TestOutputTemplateFollowsTheBehaviorLanguageWhenItDiffers(t *testing.T) {
	r := only(t, map[string]string{
		"katana.yaml": "version: 1\ndefaults:\n  language: go\n  output_template: \"{name}-generated.go\"\n" +
			"behaviors:\n  - path: behaviors/one.md\n    language: python\n",
		"behaviors/one.md": "# one",
	})
	if r.Output != "tests/test_one.py" {
		t.Errorf("Output = %q, want tests/test_one.py from python's own template", r.Output)
	}
}

func TestSubfoldersOfThePatternBaseAreKeptUnderTheOutputDirectory(t *testing.T) {
	r := only(t, map[string]string{
		"katana.yaml":                   "version: 1\nbehaviors:\n  - path: behaviors\n",
		"behaviors/billing/invoices.md": "# invoices",
	})
	if r.Output != "tests/billing/invoices_test.go" {
		t.Errorf("Output = %q, want tests/billing/invoices_test.go", r.Output)
	}
}

func TestSameFileNameInDifferentSubfoldersStaysDistinct(t *testing.T) {
	rs := resolved(t, map[string]string{
		"katana.yaml":                 "version: 1\nbehaviors:\n  - path: behaviors\n",
		"behaviors/auth/limits.md":    "# auth limits",
		"behaviors/billing/limits.md": "# billing limits",
	})
	want := []string{"tests/auth/limits_test.go", "tests/billing/limits_test.go"}
	if got := outputPaths(rs); !reflect.DeepEqual(got, want) {
		t.Errorf("outputs = %v, want %v", got, want)
	}
}

func TestBehaviorDirectlyInThePatternBaseGetsNoExtraSubfolder(t *testing.T) {
	r := only(t, map[string]string{
		"katana.yaml":      "version: 1\nbehaviors:\n  - path: behaviors\n",
		"behaviors/one.md": "# one",
	})
	if r.Output != "tests/one_test.go" {
		t.Errorf("Output = %q, want tests/one_test.go", r.Output)
	}
}

func TestTwoBehaviorsGeneratingTheSameOutputAreRejected(t *testing.T) {
	err := resolveErr(t, map[string]string{
		"katana.yaml":   "version: 1\nbehaviors:\n  - path: a/checkout.md\n  - path: b/checkout.md\n",
		"a/checkout.md": "# a",
		"b/checkout.md": "# b",
	})
	want := `behaviors "a/checkout.md" and "b/checkout.md" both generate "tests/checkout_test.go"; give one an explicit output`
	if err.Error() != want {
		t.Errorf("error = %q, want %q", err.Error(), want)
	}
}

// --- Languages katana knows ---------------------------------------------

func TestBuiltInLanguagesAreListedAlphabetically(t *testing.T) {
	want := []string{
		"csharp", "go", "java", "javascript", "kotlin", "php",
		"python", "ruby", "rust", "swift", "typescript",
	}
	if got := config.Languages(); !reflect.DeepEqual(got, want) {
		t.Errorf("Languages() = %v, want %v", got, want)
	}
}

func TestLanguageNamesAreTrimmedAndLowercased(t *testing.T) {
	for _, written := range []string{"Go", " GO ", "go"} {
		if got := config.NormalizeLanguage(written); got != "go" {
			t.Errorf("NormalizeLanguage(%q) = %q, want go", written, got)
		}
		if got := config.DefaultFramework(written); got != "go-test" {
			t.Errorf("DefaultFramework(%q) = %q, want go-test", written, got)
		}
	}
}

func TestLanguageAliases(t *testing.T) {
	aliases := map[string]string{
		"js": "javascript", "node": "javascript", "nodejs": "javascript",
		"ts": "typescript",
		"py": "python",
		"rb": "ruby",
		"cs": "csharp", "c#": "csharp", ".net": "csharp", "dotnet": "csharp",
		"golang": "go",
	}
	for alias, want := range aliases {
		t.Run(alias, func(t *testing.T) {
			if got := config.NormalizeLanguage(alias); got != want {
				t.Errorf("NormalizeLanguage(%q) = %q, want %q", alias, got, want)
			}
			if got, wantFw := config.DefaultFramework(alias), config.DefaultFramework(want); got != wantFw {
				t.Errorf("DefaultFramework(%q) = %q, want %q", alias, got, wantFw)
			}
		})
	}
}

func TestUnknownLanguageIsLeftAsWrittenAndMatchesNothing(t *testing.T) {
	if got := config.NormalizeLanguage("cobol"); got != "cobol" {
		t.Errorf("NormalizeLanguage(\"cobol\") = %q, want cobol", got)
	}
	for _, known := range config.Languages() {
		if known == "cobol" {
			t.Fatal("cobol should not be a known language")
		}
	}
	if got := config.DefaultFramework("cobol"); got != "" {
		t.Errorf("DefaultFramework(\"cobol\") = %q, want no match", got)
	}
}

// --- Per-language conventions ------------------------------------------

func TestLanguageConventions(t *testing.T) {
	cases := []struct {
		language   string
		framework  string
		template   string
		command    string
		extensions []string
	}{
		{"go", "go-test", "{snake}_test.go", "go test ./...", []string{".go"}},
		{"python", "pytest", "test_{snake}.py", "pytest", []string{".py"}},
		{"javascript", "jest", "{snake}.test.js", "npm test", []string{".js", ".jsx", ".mjs", ".cjs"}},
		{"typescript", "vitest", "{snake}.test.ts", "npm test", []string{".ts", ".tsx", ".mts", ".cts"}},
		{"java", "junit5", "{Name}Test.java", "mvn test", []string{".java"}},
		{"kotlin", "junit5", "{Name}Test.kt", "gradle test", []string{".kt"}},
		{"ruby", "rspec", "{snake}_spec.rb", "bundle exec rspec", []string{".rb"}},
		{"rust", "cargo-test", "{snake}_test.rs", "cargo test", []string{".rs"}},
		{"csharp", "xunit", "{Name}Tests.cs", "dotnet test", []string{".cs"}},
		{"php", "phpunit", "{Name}Test.php", "vendor/bin/phpunit", []string{".php"}},
		{"swift", "xctest", "{Name}Tests.swift", "swift test", []string{".swift"}},
	}

	for _, c := range cases {
		t.Run(c.language, func(t *testing.T) {
			if got := config.DefaultFramework(c.language); got != c.framework {
				t.Errorf("DefaultFramework = %q, want %q", got, c.framework)
			}
			if got := config.DefaultOutputTemplate(c.language); got != c.template {
				t.Errorf("DefaultOutputTemplate = %q, want %q", got, c.template)
			}
			if got := config.DefaultTestCommand(c.language); got != c.command {
				t.Errorf("DefaultTestCommand = %q, want %q", got, c.command)
			}
			if got := config.Extensions(c.language); !reflect.DeepEqual(got, c.extensions) {
				t.Errorf("Extensions = %v, want %v", got, c.extensions)
			}
		})
	}
}

// --- Conventions for an unknown language -------------------------------

func TestUnknownLanguageHasNoFramework(t *testing.T) {
	if got := config.DefaultFramework("cobol"); got != "" {
		t.Errorf("DefaultFramework(\"cobol\") = %q, want \"\"", got)
	}
}

func TestUnknownLanguageHasNoTestCommand(t *testing.T) {
	if got := config.DefaultTestCommand("cobol"); got != "" {
		t.Errorf("DefaultTestCommand(\"cobol\") = %q, want \"\"", got)
	}
}

func TestUnknownLanguageFallsBackToATextOutputTemplate(t *testing.T) {
	if got := config.DefaultOutputTemplate("cobol"); got != "{snake}_test.txt" {
		t.Errorf("DefaultOutputTemplate(\"cobol\") = %q, want {snake}_test.txt", got)
	}
}

func TestUnknownLanguageReportsNoSourceExtensions(t *testing.T) {
	if got := config.Extensions("cobol"); len(got) != 0 {
		t.Errorf("Extensions(\"cobol\") = %v, want none", got)
	}
}

// --- Recognising source files -----------------------------------------

func TestSourceExtensionsAreMatchedCaseInsensitively(t *testing.T) {
	if !config.IsSourcePath("go", "cmd/main.GO") {
		t.Error("IsSourcePath(\"go\", \"cmd/main.GO\") = false, want true")
	}
	if config.IsSourcePath("go", "README.md") {
		t.Error("IsSourcePath(\"go\", \"README.md\") = true, want false")
	}
}

func TestAPathInAnUnknownLanguageIsNeverSource(t *testing.T) {
	if config.IsSourcePath("cobol", "payroll.cbl") {
		t.Error("IsSourcePath(\"cobol\", \"payroll.cbl\") = true, want false")
	}
}

// --- Recognising test files -------------------------------------------

func TestTestDirectorySegmentsMarkTestCode(t *testing.T) {
	for _, dir := range []string{"test", "tests", "spec", "specs", "__tests__", "testdata", "fixtures", "e2e"} {
		t.Run(dir, func(t *testing.T) {
			assertTestPath(t, "go", "internal/"+dir+"/helper.go", true)
		})
	}
}

func TestTestDirectorySegmentsAreMatchedCaseInsensitively(t *testing.T) {
	assertTestPath(t, "go", "internal/TESTS/helper.go", true)
	assertTestPath(t, "go", "internal/TestData/sample.go", true)
}

func TestTestDirectoryRuleIgnoresTheFileName(t *testing.T) {
	assertTestPath(t, "go", "e2e/checkout.go", true)
}

func TestTestDirectoryRuleOnlyLooksAtDirectorySegments(t *testing.T) {
	// "spec" as the file name is not the directory convention.
	assertTestPath(t, "ruby", "lib/spec.rb", false)
}

func TestTestDirectoryRuleAppliesToAnUnknownLanguage(t *testing.T) {
	assertTestPath(t, "cobol", "tests/payroll.cbl", true)
}

func TestFileNameRuleDoesNotApplyToAnUnknownLanguage(t *testing.T) {
	assertTestPath(t, "cobol", "src/payroll_test.cbl", false)
}

func TestTestNameIsMatchedWithTheExtensionStripped(t *testing.T) {
	assertTestPath(t, "go", "internal/user_test.go", true)
	assertTestPath(t, "java", "src/UserTest.java", true)
}

func TestTestNameMatchingIsCaseSensitive(t *testing.T) {
	assertTestPath(t, "java", "src/Latest.java", false)
}

func TestGoTestNamesEndInUnderscoreTest(t *testing.T) {
	assertTestPath(t, "go", "internal/app/service_test.go", true)
	assertTestPath(t, "go", "internal/app/service.go", false)
}

func TestPythonTestNamesUseATestPrefixOrSuffix(t *testing.T) {
	assertTestPath(t, "python", "app/test_service.py", true)
	assertTestPath(t, "python", "app/service_test.py", true)
	assertTestPath(t, "python", "app/service.py", false)
}

func TestJavaScriptTestNamesEndInTestOrSpec(t *testing.T) {
	assertTestPath(t, "javascript", "web/cart.test.js", true)
	assertTestPath(t, "javascript", "web/cart.spec.js", true)
	assertTestPath(t, "javascript", "web/cart.js", false)
}

func TestTypeScriptTestNamesEndInTestOrSpec(t *testing.T) {
	assertTestPath(t, "typescript", "web/cart.test.ts", true)
	assertTestPath(t, "typescript", "web/cart.spec.ts", true)
	assertTestPath(t, "typescript", "web/cart.ts", false)
}

func TestJavaTestNamesEndInTestTestsOrIT(t *testing.T) {
	assertTestPath(t, "java", "src/CartTest.java", true)
	assertTestPath(t, "java", "src/CartTests.java", true)
	assertTestPath(t, "java", "src/CartIT.java", true)
	assertTestPath(t, "java", "src/Cart.java", false)
}

func TestKotlinTestNamesEndInTestTestsOrSpec(t *testing.T) {
	assertTestPath(t, "kotlin", "src/CartTest.kt", true)
	assertTestPath(t, "kotlin", "src/CartTests.kt", true)
	assertTestPath(t, "kotlin", "src/CartSpec.kt", true)
	assertTestPath(t, "kotlin", "src/Cart.kt", false)
}

func TestRubyTestNamesEndInSpecOrTest(t *testing.T) {
	assertTestPath(t, "ruby", "lib/cart_spec.rb", true)
	assertTestPath(t, "ruby", "lib/cart_test.rb", true)
	assertTestPath(t, "ruby", "lib/cart.rb", false)
}

func TestRustTestNamesEndInTestOrTests(t *testing.T) {
	assertTestPath(t, "rust", "src/cart_test.rs", true)
	assertTestPath(t, "rust", "src/cart_tests.rs", true)
	assertTestPath(t, "rust", "src/cart.rs", false)
}

func TestCSharpTestNamesEndInTestOrTests(t *testing.T) {
	assertTestPath(t, "csharp", "src/CartTest.cs", true)
	assertTestPath(t, "csharp", "src/CartTests.cs", true)
	assertTestPath(t, "csharp", "src/Cart.cs", false)
}

func TestPHPTestNamesEndInTestOrTests(t *testing.T) {
	assertTestPath(t, "php", "src/CartTest.php", true)
	assertTestPath(t, "php", "src/CartTests.php", true)
	assertTestPath(t, "php", "src/Cart.php", false)
}

func TestSwiftTestNamesEndInTestOrTests(t *testing.T) {
	assertTestPath(t, "swift", "Sources/CartTest.swift", true)
	assertTestPath(t, "swift", "Sources/CartTests.swift", true)
	assertTestPath(t, "swift", "Sources/Cart.swift", false)
}

// --- Rendering output file names ---------------------------------------

func TestNamePlaceholderIsTheBaseNameWithoutItsExtension(t *testing.T) {
	if got := renderedName(t, "{name}", "Checkout Flow.md"); got != "Checkout Flow" {
		t.Errorf("{name} = %q, want %q", got, "Checkout Flow")
	}
}

func TestSnakePlaceholderIsTheNameInSnakeCase(t *testing.T) {
	if got := renderedName(t, "{snake}", "CheckoutFlow.md"); got != "checkout_flow" {
		t.Errorf("{snake} = %q, want checkout_flow", got)
	}
}

func TestPascalPlaceholderIsTheNameInPascalCase(t *testing.T) {
	if got := renderedName(t, "{Name}", "checkout-flow.md"); got != "CheckoutFlow" {
		t.Errorf("{Name} = %q, want CheckoutFlow", got)
	}
}

func TestTextOutsideThePlaceholdersIsKeptAsWritten(t *testing.T) {
	if got := renderedName(t, "{snake}_test.go", "checkout-flow.md"); got != "checkout_flow_test.go" {
		t.Errorf("rendered = %q, want checkout_flow_test.go", got)
	}
}

// --- Snake_case conversion --------------------------------------------

func TestSnakeCaseTurnsSeparatorsIntoUnderscores(t *testing.T) {
	for _, name := range []string{"checkout-flow.md", "checkout flow.md", "checkout.flow.md", "checkout_flow.md"} {
		t.Run(name, func(t *testing.T) {
			if got := renderedName(t, "{snake}", name); got != "checkout_flow" {
				t.Errorf("{snake} of %q = %q, want checkout_flow", name, got)
			}
		})
	}
}

func TestSnakeCaseCollapsesRunsOfSeparators(t *testing.T) {
	if got := renderedName(t, "{snake}", "checkout--_ flow.md"); got != "checkout_flow" {
		t.Errorf("{snake} = %q, want checkout_flow with no doubled underscore", got)
	}
}

func TestSnakeCaseSplitsBeforeAnUppercaseLetterFollowingALowercaseOne(t *testing.T) {
	if got := renderedName(t, "{snake}", "checkoutFlow.md"); got != "checkout_flow" {
		t.Errorf("{snake} = %q, want checkout_flow", got)
	}
}

func TestSnakeCaseSplitsBeforeAnUppercaseLetterFollowingADigit(t *testing.T) {
	if got := renderedName(t, "{snake}", "api2Key.md"); got != "api2_key" {
		t.Errorf("{snake} = %q, want api2_key", got)
	}
}

func TestSnakeCaseKeepsARunOfUppercaseLettersTogether(t *testing.T) {
	if got := renderedName(t, "{snake}", "HTTPServer.md"); got != "httpserver" {
		t.Errorf("{snake} = %q, want httpserver", got)
	}
}

func TestSnakeCaseTrimsLeadingAndTrailingUnderscores(t *testing.T) {
	if got := renderedName(t, "{snake}", "-checkout-flow-.md"); got != "checkout_flow" {
		t.Errorf("{snake} = %q, want checkout_flow", got)
	}
}

func TestEquivalentNamingStylesShareOneSnakeCaseForm(t *testing.T) {
	for _, name := range []string{"Checkout Flow.md", "checkout-flow.md", "checkoutFlow.md"} {
		t.Run(name, func(t *testing.T) {
			if got := renderedName(t, "{snake}", name); got != "checkout_flow" {
				t.Errorf("{snake} of %q = %q, want checkout_flow", name, got)
			}
		})
	}
}

// --- PascalCase conversion --------------------------------------------

func TestPascalCaseUppercasesEachPartAndJoinsThemWithoutASeparator(t *testing.T) {
	if got := renderedName(t, "{Name}", "checkout_flow.md"); got != "CheckoutFlow" {
		t.Errorf("{Name} = %q, want CheckoutFlow", got)
	}
}

func TestPascalCaseLeavesTheRestOfEachPartAsItStands(t *testing.T) {
	// Only the first character of each snake_case part is touched.
	if got := renderedName(t, "{Name}", "api_v2.md"); got != "ApiV2" {
		t.Errorf("{Name} = %q, want ApiV2", got)
	}
}
