package cli

import (
	"errors"
	"flag"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/adaptive-scale/katana/internal/config"
	"github.com/adaptive-scale/katana/internal/harness"
	"github.com/adaptive-scale/katana/internal/tracker"
)

func runInit(args []string) error {
	fs := flag.NewFlagSet("katana init", flag.ContinueOnError)
	var (
		dir          = fs.String("dir", ".", "project directory to initialize")
		language     = fs.String("language", "go", "language for generated tests ("+strings.Join(config.Languages(), ", ")+")")
		harnessName  = fs.String("harness", "claude", "coding agent harness ("+strings.Join(harness.Names(), ", ")+")")
		behaviorsDir = fs.String("behaviors", "behaviors", "directory holding behavior markdown files")
		outputDir    = fs.String("output", "tests", "directory for generated tests")
		force        = fs.Bool("force", false, "overwrite an existing "+config.FileName)
		noSample     = fs.Bool("no-sample", false, "skip writing the sample behavior file")
	)
	fs.Usage = func() {
		fmt.Fprintf(os.Stderr, "Usage: katana init [flags]\n\nSets up %s and the %s tracker directory.\n\nFlags:\n", config.FileName, config.Dir)
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		return err
	}

	root, err := filepath.Abs(*dir)
	if err != nil {
		return err
	}
	if _, err := harnessOrError(*harnessName); err != nil {
		return err
	}

	cfgPath := filepath.Join(root, config.FileName)
	if _, err := os.Stat(cfgPath); err == nil && !*force {
		return fmt.Errorf("%s already exists (pass --force to overwrite)", cfgPath)
	}

	if err := os.MkdirAll(root, 0o755); err != nil {
		return err
	}

	// katana.yaml
	cfgBody := scaffoldConfig(*harnessName, *language, *behaviorsDir, *outputDir)
	if err := os.WriteFile(cfgPath, []byte(cfgBody), 0o644); err != nil {
		return err
	}
	fmt.Printf("created %s\n", rel(root, cfgPath))

	// .katana/ tracker directory
	katanaDir := filepath.Join(root, config.Dir)
	if err := os.MkdirAll(katanaDir, 0o755); err != nil {
		return err
	}
	created, err := ensureTracker(root)
	if err != nil {
		return err
	}
	if created {
		fmt.Printf("created %s\n", rel(root, tracker.Path(root)))
	} else {
		fmt.Printf("kept existing %s\n", rel(root, tracker.Path(root)))
	}

	// The tracker is shared state that belongs in version control — it is what
	// lets a teammate's checkout know which tests are already current. Only the
	// scratch files are ignored.
	gitignore := filepath.Join(katanaDir, ".gitignore")
	if err := writeIfAbsent(gitignore, ".tracker-*.json\n"); err != nil {
		return err
	}

	// behaviors/ with a sample so `katana generate` works immediately
	behDir := filepath.Join(root, *behaviorsDir)
	if err := os.MkdirAll(behDir, 0o755); err != nil {
		return err
	}
	if !*noSample {
		sample := filepath.Join(behDir, "example.md")
		if err := writeIfAbsent(sample, sampleBehavior); err != nil {
			return err
		}
		if _, err := os.Stat(sample); err == nil {
			fmt.Printf("created %s\n", rel(root, sample))
		}
	}

	fmt.Printf("\nkatana is set up with the %s harness generating %s tests.\n", *harnessName, *language)
	fmt.Printf("Next: describe a behavior in %s/, then run `katana generate`.\n", *behaviorsDir)
	return nil
}

func harnessOrError(name string) (harness.Spec, error) {
	spec, ok := harness.Builtin(name)
	if !ok {
		return spec, fmt.Errorf("unknown harness %q; choose one of %s, or set harness.command in %s to use another agent CLI",
			name, strings.Join(harness.Names(), ", "), config.FileName)
	}
	return spec, nil
}

// ensureTracker creates an empty tracker file if there is not one already.
func ensureTracker(root string) (bool, error) {
	p := tracker.Path(root)
	if _, err := os.Stat(p); err == nil {
		return false, nil
	}
	t, err := tracker.Load(root)
	if err != nil {
		return false, err
	}
	// Record nothing, but force a write so the file exists from the start.
	t.Record(tracker.Entry{})
	delete(t.Entries, "")
	if err := t.Save(); err != nil {
		return false, err
	}
	return true, nil
}

func writeIfAbsent(path, content string) error {
	if _, err := os.Stat(path); err == nil {
		return nil
	} else if !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	return os.WriteFile(path, []byte(content), 0o644)
}

func rel(root, path string) string {
	r, err := filepath.Rel(root, path)
	if err != nil {
		return path
	}
	return filepath.ToSlash(r)
}

func scaffoldConfig(harnessName, language, behaviorsDir, outputDir string) string {
	return fmt.Sprintf(`# katana — generate test code from written product behavior.
#
# Behavior files are the source of truth. When one changes, `+"`katana generate`"+`
# regenerates the tests derived from it and nothing else.

version: 1

# The coding agent katana shells out to for generation. katana does not call a
# model API itself: it drives the agent CLI you already have configured, so
# authentication, model choice and tool permissions come from that agent.
#
# Built-in harnesses: %s
harness:
  name: %s

  # Every field below is optional. Defaults come from katana's built-in spec for
  # the harness above. Override them when your agent CLI's flags differ — that
  # way a changed upstream flag is a config edit, not a katana upgrade.
  #
  # command: %s           # executable to run
  # args: ["-p", "--permission-mode", "auto"]  # placed before the prompt
  # prompt: stdin               # how the prompt is delivered: stdin | arg
  # model: ""                   # passed through with model_flag when set
  # model_flag: --model
  # timeout: 10m                # bound on a single generation
  # env:                        # added to the harness environment
  #   KATANA: "1"

# Applied to every behavior that does not override them.
defaults:
  language: %s
  framework: %s
  output_dir: %s
  # Names generated files. {name} is the behavior file's base name;
  # {snake} and {Name} are its snake_case and PascalCase forms.
  output_template: "%s"

# How `+"`katana run`"+` executes the suite.
test:
  command: %s
  # dir: .                      # defaults to the project root

# Behavior sources. A path may be a single file, a directory (searched
# recursively for .md files), or a glob — where ** spans any number of
# directories.
behaviors:
  - path: %s

  # Per-behavior overrides, for when one spec targets a different stack:
  #
  # - path: %s/billing.md
  #   output: %s/billing_contract_test.py
  #   language: python
  #   framework: pytest
  #   harness: codex            # use a different agent for this one
  #   instructions: |
  #     Stub the payment gateway; never call it for real.
`,
		strings.Join(harness.Names(), ", "),
		harnessName,
		harnessName,
		language,
		config.DefaultFramework(language),
		outputDir,
		config.DefaultOutputTemplate(language),
		config.DefaultTestCommand(language),
		behaviorsDir,
		behaviorsDir,
		outputDir,
	)
}

const sampleBehavior = `# Shopping cart checkout

Describe product behavior here in plain language. katana turns each behavior
below into a test case, so state what is observable, not how it is implemented.

## Applying a discount code

- A valid, unexpired discount code reduces the order total by its percentage.
- The reduction applies to the item subtotal, before shipping and tax.
- An order total never goes below zero, however large the discount.

## Rejecting an invalid code

- An unknown code leaves the total unchanged and reports "code not recognised".
- An expired code leaves the total unchanged and reports "code has expired".
- Only one discount code applies to an order; applying a second replaces the
  first rather than stacking.

## Empty cart

- Checking out an empty cart is rejected and creates no order.
`
