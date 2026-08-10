// Package tests holds katana's generated behavior tests.
//
// This file covers behaviors/example.md: katana must support the Claude, Codex,
// Opencode and Hermes coding harnesses. The spec explicitly says not to install
// them, so nothing here shells out or looks the executables up on PATH — the
// assertions are about katana knowing each harness's configuration.
package tests

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/adaptive-scale/katana/internal/config"
	"github.com/adaptive-scale/katana/internal/harness"
)

// required are the harnesses the specification names. The spec lists them
// capitalized and lists what katana must support, not an exhaustive set, so no
// test here asserts that other harnesses are absent.
var required = []string{"claude", "codex", "opencode", "hermes"}

// assertSupported checks that katana carries a usable built-in configuration for
// name. It asserts the shape of the configuration — an executable, a prompt
// delivery mode, a model flag, a description — rather than specific flags, which
// the harness package documents as defaults that upstream CLIs may change.
func assertSupported(t *testing.T, name string) {
	t.Helper()

	spec, ok := harness.Builtin(name)
	if !ok {
		t.Fatalf("harness %q is not supported; supported harnesses are %s", name, strings.Join(harness.Names(), ", "))
	}
	if spec.Name != name {
		t.Errorf("spec.Name = %q, want %q", spec.Name, name)
	}
	if strings.TrimSpace(spec.Command) == "" {
		t.Errorf("harness %q has no command to invoke", name)
	}
	if spec.Prompt != harness.PromptStdin && spec.Prompt != harness.PromptArg {
		t.Errorf("harness %q delivers its prompt via %q, want %q or %q",
			name, spec.Prompt, harness.PromptStdin, harness.PromptArg)
	}
	if strings.TrimSpace(spec.ModelFlag) == "" {
		t.Errorf("harness %q cannot be given a model override", name)
	}
	if strings.TrimSpace(spec.Docs) == "" {
		t.Errorf("harness %q has no description for `katana harnesses`", name)
	}
}

func TestClaudeHarnessIsSupported(t *testing.T) {
	assertSupported(t, "claude")
}

func TestCodexHarnessIsSupported(t *testing.T) {
	assertSupported(t, "codex")
}

func TestOpencodeHarnessIsSupported(t *testing.T) {
	assertSupported(t, "opencode")
}

func TestHermesHarnessIsSupported(t *testing.T) {
	assertSupported(t, "hermes")
}

// The four harnesses must be selectable, so they have to appear in the list
// katana offers (`katana init --harness`, and the error for an unknown name).
func TestSupportedHarnessesAreOfferedForSelection(t *testing.T) {
	names := harness.Names()
	have := map[string]bool{}
	for _, n := range names {
		have[n] = true
	}
	for _, want := range required {
		if !have[want] {
			t.Errorf("harness %q missing from the selectable list %s", want, strings.Join(names, ", "))
		}
	}
}

// `katana harnesses` describes each supported harness; every one the spec names
// must be described there too.
func TestSupportedHarnessesAreDescribed(t *testing.T) {
	described := map[string]harness.Spec{}
	for _, s := range harness.Describe() {
		described[s.Name] = s
	}
	for _, want := range required {
		s, ok := described[want]
		if !ok {
			t.Errorf("harness %q is not described by `katana harnesses`", want)
			continue
		}
		if strings.TrimSpace(s.Command) == "" || strings.TrimSpace(s.Docs) == "" {
			t.Errorf("harness %q is described without a command or docs: %+v", want, s)
		}
	}
}

// The spec writes the names capitalized ("Claude", "Codex", ...), so a
// configuration written that way must select the same harness.
func TestSupportedHarnessesAreMatchedRegardlessOfCase(t *testing.T) {
	for _, name := range required {
		for _, written := range []string{strings.ToUpper(name[:1]) + name[1:], strings.ToUpper(name), "  " + name + "  "} {
			spec, ok := harness.Builtin(written)
			if !ok {
				t.Errorf("harness %q written as %q was not recognised", name, written)
				continue
			}
			if spec.Name != name {
				t.Errorf("harness %q written as %q resolved to %q", name, written, spec.Name)
			}
		}
	}
}

// Support means the harness is configurable from katana.yaml: the config must
// load and resolve with each name, without the CLI being present.
func TestKatanaConfigAcceptsEachSupportedHarness(t *testing.T) {
	for _, name := range required {
		root := writeProject(t, map[string]string{
			"katana.yaml":          "version: 1\nharness:\n  name: " + name + "\nbehaviors:\n  - path: behaviors/example.md\n",
			"behaviors/example.md": "# example",
		})

		cfg, err := config.Load(filepath.Join(root, "katana.yaml"))
		if err != nil {
			t.Errorf("harness %q: Load: %v", name, err)
			continue
		}
		if cfg.Harness.Name != name {
			t.Errorf("harness %q: config harness name = %q", name, cfg.Harness.Name)
		}
		got, err := cfg.Resolve()
		if err != nil {
			t.Errorf("harness %q: Resolve: %v", name, err)
			continue
		}
		if len(got) != 1 || got[0].Harness != name {
			t.Errorf("harness %q: resolved behaviors = %+v", name, got)
		}
	}
}

// A single behavior can pick a different harness from the project default, so
// every supported harness must work as a per-behavior override too.
func TestPerBehaviorHarnessOverrideAcceptsEachSupportedHarness(t *testing.T) {
	for _, name := range required {
		root := writeProject(t, map[string]string{
			// The project default is deliberately a different harness.
			"katana.yaml":          "version: 1\nharness:\n  name: claude\nbehaviors:\n  - path: behaviors/example.md\n    harness: " + name + "\n",
			"behaviors/example.md": "# example",
		})

		cfg, err := config.Load(filepath.Join(root, "katana.yaml"))
		if err != nil {
			t.Errorf("harness %q: Load: %v", name, err)
			continue
		}
		got, err := cfg.Resolve()
		if err != nil {
			t.Errorf("harness %q: Resolve: %v", name, err)
			continue
		}
		if len(got) != 1 || got[0].Harness != name {
			t.Errorf("harness %q: per-behavior override not applied: %+v", name, got)
		}
	}
}

// Configuring a harness must not require it to be installed — the spec calls
// this out explicitly. PATH is emptied so a machine that happens to have one of
// these CLIs installed still exercises the uninstalled case.
func TestSupportedHarnessesConfigureWithoutBeingInstalled(t *testing.T) {
	t.Setenv("PATH", "")

	for _, name := range required {
		runner, err := harness.New(name, harness.Spec{}, harness.Options{})
		if err != nil {
			t.Errorf("harness %q: New: %v", name, err)
			continue
		}
		spec := runner.Spec()
		if spec.Name != name || strings.TrimSpace(spec.Command) == "" {
			t.Errorf("harness %q: resolved spec = %+v", name, spec)
		}
	}
}

// Every field of a supported harness is overridable per project, so a changed
// upstream flag is a config edit rather than a katana release.
func TestSupportedHarnessConfigurationIsOverridable(t *testing.T) {
	override := harness.Spec{
		Command:   "my-agent",
		Args:      []string{"exec", "--full-auto"},
		Prompt:    harness.PromptArg,
		ModelFlag: "-m",
	}
	for _, name := range required {
		runner, err := harness.New(name, override, harness.Options{})
		if err != nil {
			t.Errorf("harness %q: New: %v", name, err)
			continue
		}
		got := runner.Spec()
		if got.Command != override.Command || got.Prompt != override.Prompt || got.ModelFlag != override.ModelFlag {
			t.Errorf("harness %q: overrides not applied: %+v", name, got)
		}
		if strings.Join(got.Args, " ") != strings.Join(override.Args, " ") {
			t.Errorf("harness %q: args = %v, want %v", name, got.Args, override.Args)
		}
		if got.Name != name {
			t.Errorf("harness %q: name changed to %q", name, got.Name)
		}
	}
}

// The spec is silent on unsupported names; the reasonable reading of "supports
// these harnesses" is that anything else is rejected rather than silently
// accepted, and that the message points at the harnesses that are supported.
func TestUnsupportedHarnessIsRejected(t *testing.T) {
	_, err := harness.New("not-a-harness", harness.Spec{}, harness.Options{})
	if err == nil {
		t.Fatal("expected an error for an unsupported harness name")
	}
	for _, name := range required {
		if !strings.Contains(err.Error(), name) {
			t.Errorf("error does not mention supported harness %q: %v", name, err)
		}
	}
}

// writeProject lays out a katana project in a temp directory, mirroring the
// helper used by the config package's own tests.
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
