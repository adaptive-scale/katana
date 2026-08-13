// Package tests holds katana's generated behavior tests.
//
// This file covers behaviors/example.md: katana must support the Claude, Codex,
// Opencode and Hermes coding harnesses. The spec explicitly says not to install
// them, so these tests inspect katana's built-in configurations without looking
// for or invoking the corresponding executables.
package tests

import (
	"strings"
	"testing"

	"github.com/adaptive-scale/katana/internal/harness"
)

// The specification uses product names, while katana.yaml uses the lowercase
// identifiers documented by the harness configuration API.
func assertHarnessConfigurationSupported(t *testing.T, name string) {
	t.Helper()

	if _, ok := harness.Builtin(name); !ok {
		t.Fatalf("harness %q has no built-in configuration; available configurations are %s",
			name, strings.Join(harness.Names(), ", "))
	}
}

func TestClaudeHarnessConfigurationIsSupported(t *testing.T) {
	assertHarnessConfigurationSupported(t, "claude")
}

func TestCodexHarnessConfigurationIsSupported(t *testing.T) {
	assertHarnessConfigurationSupported(t, "codex")
}

func TestOpencodeHarnessConfigurationIsSupported(t *testing.T) {
	assertHarnessConfigurationSupported(t, "opencode")
}

func TestHermesHarnessConfigurationIsSupported(t *testing.T) {
	assertHarnessConfigurationSupported(t, "hermes")
}
