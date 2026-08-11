// Package cli implements katana's command line.
package cli

import (
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/adaptive-scale/katana/internal/config"
	"github.com/adaptive-scale/katana/internal/harness"
	"github.com/adaptive-scale/katana/internal/update"
)

// Version is the katana version, overridable at build time with
// -ldflags "-X github.com/adaptive-scale/katana/internal/cli.Version=v1.2.3".
var Version = "dev"

// Run dispatches a katana invocation. args excludes the program name.
func Run(args []string) error {
	if len(args) == 0 {
		// A bare `katana` in a terminal opens the project; everywhere else it
		// prints the usage it always has. The release check is skipped either
		// way: the UI owns the screen, and usage output stays quiet.
		return runDefault()
	}

	// The release check runs alongside the command and reports once it is
	// done, so it never delays the work the user asked for. `update` does its
	// own, better-informed check, and help output stays quiet.
	switch args[0] {
	case "update", "upgrade", "self-update", "help", "--help", "-h":
	default:
		defer update.Start(Version).Notice(os.Stderr)
	}

	switch args[0] {
	case "init":
		return runInit(args[1:])
	case "discover":
		return runDiscover(args[1:])
	case "generate", "gen":
		return runGenerate(args[1:])
	case "run", "test":
		return runTest(args[1:])
	case "status":
		return runStatus(args[1:])
	case "tui", "ui":
		return runTUI(args[1:])
	case "harnesses":
		return runHarnesses(args[1:])
	case "update", "upgrade", "self-update":
		return runUpdate(args[1:])
	case "version", "--version", "-v":
		fmt.Println("katana " + Version)
		return nil
	case "help", "--help", "-h":
		usage(os.Stdout)
		return nil
	default:
		usage(os.Stderr)
		return fmt.Errorf("unknown command %q", args[0])
	}
}

func usage(w io.Writer) {
	fmt.Fprintf(w, `katana generates test code from written product behavior.

Usage:
  katana <command> [flags]

Commands:
  init        Create %s and the %s tracker directory
  discover    Write behavior files for the code this project already has
  generate    Generate tests for behaviors that changed since the last run
  run         Run the generated test suite
  status      Show what the tracker holds and which behaviors are out of date
  tui         Open the full-screen view: behaviors, results and runs on demand
  harnesses   List the supported coding-agent harnesses
  update      Install the newest katana release over this binary
  version     Print the katana version

Run "katana <command> --help" for the flags of a command.

Typical use:
  katana init --language go --harness claude
  $EDITOR behaviors/checkout.md
  katana generate
  katana run

Running katana with no command opens the full-screen view of the project, where
behaviors can be read and their tests run one at a time.

On a codebase that has no behaviors written down yet:
  katana init --language go --harness claude
  katana discover --dry-run
  katana discover

Harnesses: %s
`, config.FileName, config.Dir, strings.Join(harness.Names(), ", "))
}

// stringList collects a repeatable string flag.
type stringList []string

func (s *stringList) String() string { return "" }

func (s *stringList) Set(v string) error {
	*s = append(*s, v)
	return nil
}

// loadProject finds katana.yaml from dir and loads it.
func loadProject(dir string) (*config.Config, error) {
	if dir == "" {
		var err error
		dir, err = os.Getwd()
		if err != nil {
			return nil, err
		}
	}
	path, err := config.Find(dir)
	if err != nil {
		return nil, err
	}
	return config.Load(path)
}

// newRunner builds a harness runner for a named harness, applying the project's
// harness overrides. Overrides apply to any harness the project selects: a
// per-behavior harness inherits the project's timeout, env and model settings.
func newRunner(cfg *config.Config, name string, verbose bool) (*harness.Runner, error) {
	timeout, err := cfg.HarnessTimeout()
	if err != nil {
		return nil, err
	}
	override := harness.Spec{
		Command:   cfg.Harness.Command,
		Args:      cfg.Harness.Args,
		Prompt:    harness.PromptMode(cfg.Harness.Prompt),
		ModelFlag: cfg.Harness.ModelFlag,
	}
	// Command and args are specific to one CLI's flag surface, so they only
	// apply to the harness they were written for.
	if name != cfg.Harness.Name {
		override.Command = ""
		override.Args = nil
	}
	return harness.New(name, override, harness.Options{
		Dir:     cfg.Root,
		Timeout: timeout,
		Env:     cfg.Harness.Env,
		Model:   cfg.Harness.Model,
		Verbose: verbose,
		Stderr:  os.Stderr,
	})
}
