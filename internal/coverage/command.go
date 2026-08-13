package coverage

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// Instrumentation is how one runner is asked to record coverage: what to run,
// what to append to it, and where the report will land.
type Instrumentation struct {
	// Command replaces the project's test command. It is empty for the runners
	// that record coverage themselves — most of them — and set for the ones
	// that need another program wrapped around them, as Rust and mocha do.
	Command string
	// Args are appended to whichever command is run.
	Args []string
	// Profile is the file the runner writes, as an absolute path.
	Profile string
	// Format is what that file will hold.
	Format string
	// Known reports whether katana knows how to ask this runner for coverage.
	// A caller that gets false has to be pointed at a report instead.
	Known bool
	// Tool names the extra program the run depends on, empty when the runner
	// needs nothing installed beyond itself. Coverage is the one part of a test
	// run that routinely needs a plugin, and "command not found" three hundred
	// lines up the scrollback is not an explanation.
	Tool string
	// Note is what katana decided for itself, worth repeating to the user.
	Note string
}

// Options describe the run to instrument.
type Options struct {
	// Framework is the project's configured framework, as katana.yaml spells it.
	Framework string
	// Command is the configured test command. It is what the runner is
	// recognised from when no framework is set, and what a wrapper wraps.
	Command string
	// Dest is a directory katana may have the runner write the report into.
	Dest string
	// Packages is the Go `-coverpkg` pattern. Empty leaves the flag off, which
	// measures only the packages that hold the tests.
	Packages string
}

var (
	goTestCmd    = regexp.MustCompile(`(^|\s)go\s+test(\s|$)`)
	pytestCmd    = regexp.MustCompile(`(^|\s)(pytest|py\.test)(\s|$)`)
	jestCmd      = regexp.MustCompile(`(^|\s)(jest|react-scripts)(\s|$)`)
	vitestCmd    = regexp.MustCompile(`(^|\s)vitest(\s|$)`)
	mochaCmd     = regexp.MustCompile(`(^|\s)mocha(\s|$)`)
	nodeTestCmd  = regexp.MustCompile(`(^|\s)node\s+(.*\s)?--test(\s|=|$)`)
	cargoTestCmd = regexp.MustCompile(`(^|\s)cargo\s+test(\s|$)`)
	cargoCovCmd  = regexp.MustCompile(`(^|\s)cargo\s+llvm-cov(\s|$)`)
	tarpaulinCmd = regexp.MustCompile(`(^|\s)cargo\s+tarpaulin(\s|$)`)
	// npmRun matches the package-manager wrappers a JavaScript project's test
	// command usually is. npm needs `--` before arguments meant for the runner
	// underneath; yarn and pnpm pass them through as they are.
	npmRun = regexp.MustCompile(`(^|\s)npm\s+(run\s+\S+|test|t)(\s|$)`)
	// coverFlag matches a command that already asks for a Go profile, so katana
	// can say that it overrode one rather than quietly winning the argument.
	coverFlag = regexp.MustCompile(`(^|\s)-coverprofile(\s|=)`)
)

// For returns how to ask a runner for coverage. The arguments are appended to
// the command exactly as `katana run -- ...` appends its own, so a command
// katana cannot safely extend is one it reports rather than mangles.
func For(o Options) Instrumentation {
	dest := o.Dest
	lcov := filepath.Join(dest, "lcov.info")
	switch runner(o.Framework, o.Command) {
	case "go":
		profile := filepath.Join(dest, "coverage.out")
		args := []string{"-coverprofile=" + profile}
		if o.Packages != "" {
			// Without -coverpkg, `go test` measures only the package a test
			// lives in. katana's generated tests usually live in a tests
			// directory of their own, so that default would report a project
			// with a full suite as covering none of the code it exercises.
			args = append(args, "-coverpkg="+o.Packages)
		}
		in := Instrumentation{Args: args, Profile: profile, Format: FormatGo, Known: true}
		if coverFlag.MatchString(o.Command) {
			in.Note = "the test command already writes a coverage profile; katana's own -coverprofile takes precedence"
		}
		return in

	case "pytest":
		profile := filepath.Join(dest, "coverage.xml")
		return Instrumentation{
			Args:    []string{"--cov", "--cov-report=xml:" + profile},
			Profile: profile,
			Format:  FormatCobertura,
			Known:   true,
			Tool:    "pytest-cov (pip install pytest-cov)",
		}

	case "jest":
		return Instrumentation{
			Args: withSeparator(o.Command,
				"--coverage", "--coverageReporters=lcovonly", "--coverageDirectory="+dest),
			Profile: lcov,
			Format:  FormatLCOV,
			Known:   true,
		}

	case "vitest":
		return Instrumentation{
			Args: withSeparator(o.Command, "--coverage.enabled=true",
				"--coverage.reporter=lcovonly", "--coverage.reportsDirectory="+dest),
			Profile: lcov,
			Format:  FormatLCOV,
			Known:   true,
			Tool:    "a vitest coverage provider (npm i -D @vitest/coverage-v8)",
		}

	case "mocha":
		// mocha measures nothing itself; nyc runs it under instrumentation.
		return Instrumentation{
			Command: "npx nyc --reporter=lcovonly --report-dir=" + shellQuote(dest) + " " + strings.TrimSpace(o.Command),
			Profile: lcov,
			Format:  FormatLCOV,
			Known:   true,
			Tool:    "nyc (npm i -D nyc)",
		}

	case "node":
		// Node's own runner replaces its reporter when told to use one, so the
		// spec reporter is asked for again alongside lcov: coverage should not
		// cost the per-case output katana reads results from.
		return Instrumentation{
			Args: withSeparator(o.Command, "--experimental-test-coverage",
				"--test-reporter=spec", "--test-reporter-destination=stdout",
				"--test-reporter=lcov", "--test-reporter-destination="+lcov),
			Profile: lcov,
			Format:  FormatLCOV,
			Known:   true,
		}

	case "cargo":
		// cargo test has no coverage of its own. cargo-llvm-cov runs the same
		// suite under the instrumentation the Rust toolchain already ships, so
		// the configured command keeps its own flags and only its verb changes.
		command := cargoTestCmd.ReplaceAllString(strings.TrimSpace(o.Command), "${1}cargo llvm-cov ")
		return Instrumentation{
			Command: strings.TrimSpace(command),
			Args:    []string{"--lcov", "--output-path", lcov},
			Profile: lcov,
			Format:  FormatLCOV,
			Known:   true,
			Tool:    "cargo-llvm-cov (cargo install cargo-llvm-cov)",
		}

	case "cargo-llvm-cov":
		return Instrumentation{
			Args:    []string{"--lcov", "--output-path", lcov},
			Profile: lcov,
			Format:  FormatLCOV,
			Known:   true,
			Tool:    "cargo-llvm-cov (cargo install cargo-llvm-cov)",
		}

	case "tarpaulin":
		profile := filepath.Join(dest, "cobertura.xml")
		return Instrumentation{
			Args:    []string{"--out", "xml", "--output-dir", dest},
			Profile: profile,
			Format:  FormatCobertura,
			Known:   true,
			Tool:    "cargo-tarpaulin (cargo install cargo-tarpaulin)",
		}
	}
	return Instrumentation{}
}

// runner names the test runner from what katana.yaml says, falling back to what
// the command line looks like. The framework wins where it is set: a project
// that says vitest means vitest, whatever wrapper script its command runs.
func runner(framework, command string) string {
	switch strings.ToLower(strings.TrimSpace(framework)) {
	case "go", "golang", "go-test", "gotest", "gotestsum":
		return "go"
	case "pytest", "py.test", "python", "py":
		return "pytest"
	case "jest", "react-scripts":
		return "jest"
	case "vitest":
		return "vitest"
	case "mocha":
		return "mocha"
	case "node", "node-test", "node:test":
		return "node"
	case "cargo", "cargo-test", "rust":
		// A Rust project whose command already names a coverage tool keeps it.
		if cargoCovCmd.MatchString(command) {
			return "cargo-llvm-cov"
		}
		if tarpaulinCmd.MatchString(command) {
			return "tarpaulin"
		}
		return "cargo"
	}
	switch {
	case goTestCmd.MatchString(command):
		return "go"
	case pytestCmd.MatchString(command):
		return "pytest"
	case vitestCmd.MatchString(command):
		return "vitest"
	case jestCmd.MatchString(command):
		return "jest"
	case mochaCmd.MatchString(command):
		return "mocha"
	case nodeTestCmd.MatchString(command):
		return "node"
	case cargoCovCmd.MatchString(command):
		return "cargo-llvm-cov"
	case tarpaulinCmd.MatchString(command):
		return "tarpaulin"
	case cargoTestCmd.MatchString(command):
		return "cargo"
	}
	return ""
}

// withSeparator prefixes `--` to arguments meant for the runner underneath an
// npm script, where anything before it is npm's own. A command that is not an
// npm script takes the arguments directly.
func withSeparator(command string, args ...string) []string {
	if npmRun.MatchString(command) {
		return append([]string{"--"}, args...)
	}
	return args
}

// shellQuote wraps a path katana assembled into a command line, since the
// command is handed to a shell and a directory may contain spaces.
func shellQuote(s string) string { return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'" }

// reportNames are the places a runner katana cannot instrument conventionally
// leaves its report, most specific first.
var reportNames = []string{
	"coverage.out",
	"coverage/lcov.info",
	"lcov.info",
	"coverage/coverage.xml",
	"coverage.xml",
	"coverage/cobertura-coverage.xml",
	"cobertura.xml",
	"target/llvm-cov/lcov.info",
}

// Find looks for a coverage report a previous run left in the project. It is
// the answer for a runner katana has no flags for: whatever wrote the report,
// katana can still read it.
func Find(root string) (string, bool) {
	for _, name := range reportNames {
		path := filepath.Join(root, filepath.FromSlash(name))
		if info, err := os.Stat(path); err == nil && !info.IsDir() {
			return path, true
		}
	}
	return "", false
}
