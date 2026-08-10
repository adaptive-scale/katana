// Package harness runs the coding agent that turns a behavior spec into tests.
//
// katana does not talk to a model API directly. It shells out to whichever
// agent CLI the project already uses, so generation inherits that agent's
// authentication, model choice and tool permissions.
package harness

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"sort"
	"strings"
	"time"
)

// PromptMode is how the prompt is handed to the harness process.
type PromptMode string

const (
	// PromptStdin pipes the prompt to the process's standard input.
	PromptStdin PromptMode = "stdin"
	// PromptArg appends the prompt as a final positional argument.
	PromptArg PromptMode = "arg"
)

// Spec describes how to invoke one harness.
type Spec struct {
	// Name is the harness identifier used in katana.yaml.
	Name string
	// Command is the executable to run.
	Command string
	// Args precede the prompt.
	Args []string
	// Prompt selects how the prompt is delivered.
	Prompt PromptMode
	// ModelFlag passes a model override when the config sets one.
	ModelFlag string
	// Docs is a short note shown in `katana harnesses`.
	Docs string
}

// builtins are katana's defaults for the supported agent CLIs.
//
// These are starting points, not a contract with the upstream tools. Agent CLIs
// change their flags; every field here is overridable per project under the
// `harness:` key in katana.yaml, so a changed flag is a config edit rather than
// a katana release.
//
// Where a harness has a file-write permission mode, the default args select the
// narrowest one that lets generation succeed. Generation is exactly "write one
// test file", and a non-interactive agent has nobody to answer a permission
// prompt, so without this the write is denied and katana has nothing to save.
var builtins = map[string]Spec{
	"claude": {
		Name:    "claude",
		Command: "claude",
		// auto lets the agent approve its own file writes, and the reads and
		// lookups it needs to write tests against the real API.
		Args:      []string{"-p", "--permission-mode", "auto"},
		Prompt:    PromptStdin,
		ModelFlag: "--model",
		Docs:      "Claude Code CLI, non-interactive print mode, auto permissions",
	},
	"codex": {
		Name:      "codex",
		Command:   "codex",
		Args:      []string{"exec"},
		Prompt:    PromptArg,
		ModelFlag: "--model",
		Docs:      "Codex CLI, non-interactive exec mode",
	},
	"opencode": {
		Name:      "opencode",
		Command:   "opencode",
		Args:      []string{"run"},
		Prompt:    PromptArg,
		ModelFlag: "--model",
		Docs:      "opencode CLI, single-shot run mode",
	},
	"pi": {
		Name:      "pi",
		Command:   "pi",
		Args:      []string{"-p"},
		Prompt:    PromptStdin,
		ModelFlag: "--model",
		Docs:      "pi CLI, non-interactive prompt mode",
	},
	"hermes": {
		Name:      "hermes",
		Command:   "hermes",
		Args:      []string{"-p"},
		Prompt:    PromptStdin,
		ModelFlag: "--model",
		Docs:      "hermes CLI, non-interactive prompt mode",
	},
}

// Names lists the built-in harnesses.
func Names() []string {
	out := make([]string, 0, len(builtins))
	for k := range builtins {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// Builtin returns the default spec for a harness name.
func Builtin(name string) (Spec, bool) {
	s, ok := builtins[strings.ToLower(strings.TrimSpace(name))]
	return s, ok
}

// Describe returns the built-in specs, sorted by name, for `katana harnesses`.
func Describe() []Spec {
	out := make([]Spec, 0, len(builtins))
	for _, n := range Names() {
		out = append(out, builtins[n])
	}
	return out
}

// Options configure a Runner beyond the harness spec itself.
type Options struct {
	// Dir is the working directory for the harness process, normally the
	// project root so the agent's own file tools resolve relative paths.
	Dir string
	// Timeout bounds a single invocation.
	Timeout time.Duration
	// Env is added to the harness environment.
	Env map[string]string
	// Model is passed via the spec's ModelFlag when non-empty.
	Model string
	// Verbose streams harness output to Stderr as it runs.
	Verbose bool
	// Stderr receives harness diagnostics. Defaults to os.Stderr.
	Stderr io.Writer
}

// Runner invokes a configured harness.
type Runner struct {
	spec Spec
	opts Options
}

// New builds a Runner for name, applying any overrides. An unknown name is only
// an error when no explicit command is supplied, so a project can point katana
// at an agent CLI katana has never heard of.
func New(name string, override Spec, opts Options) (*Runner, error) {
	spec, ok := Builtin(name)
	if !ok {
		if strings.TrimSpace(override.Command) == "" {
			return nil, fmt.Errorf("unknown harness %q; built-in harnesses are %s (or set harness.command to use another agent CLI)",
				name, strings.Join(Names(), ", "))
		}
		spec = Spec{Name: name, Prompt: PromptStdin, ModelFlag: "--model"}
	}
	if override.Command != "" {
		spec.Command = override.Command
	}
	if override.Args != nil {
		spec.Args = override.Args
	}
	if override.Prompt != "" {
		spec.Prompt = override.Prompt
	}
	if override.ModelFlag != "" {
		spec.ModelFlag = override.ModelFlag
	}
	if opts.Stderr == nil {
		opts.Stderr = os.Stderr
	}
	if opts.Timeout <= 0 {
		opts.Timeout = 10 * time.Minute
	}
	return &Runner{spec: spec, opts: opts}, nil
}

// Spec returns the resolved spec, for diagnostics.
func (r *Runner) Spec() Spec { return r.spec }

// WithStderr returns a copy of the runner that sends verbose harness output to
// w instead of the configured writer.
//
// Parallel generation uses it to give each behavior its own buffer, so several
// agents running at once do not interleave line by line on one terminal. A
// Runner holds no mutable state, so the copy is safe to use concurrently with
// the original.
func (r *Runner) WithStderr(w io.Writer) *Runner {
	c := *r
	c.opts.Stderr = w
	return &c
}

// Available reports whether the harness executable is on PATH.
func (r *Runner) Available() error {
	if _, err := exec.LookPath(r.spec.Command); err != nil {
		return fmt.Errorf("harness %q needs %q on your PATH but it was not found: %w",
			r.spec.Name, r.spec.Command, err)
	}
	return nil
}

// Result is the outcome of one harness invocation.
type Result struct {
	Stdout   string
	Stderr   string
	ExitCode int
	Duration time.Duration
}

// Run invokes the harness with prompt and returns its output.
func (r *Runner) Run(ctx context.Context, prompt string) (*Result, error) {
	if err := r.Available(); err != nil {
		return nil, err
	}

	ctx, cancel := context.WithTimeout(ctx, r.opts.Timeout)
	defer cancel()

	args := append([]string{}, r.spec.Args...)
	if r.opts.Model != "" && r.spec.ModelFlag != "" {
		args = append(args, r.spec.ModelFlag, r.opts.Model)
	}
	if r.spec.Prompt == PromptArg {
		args = append(args, prompt)
	}

	cmd := exec.CommandContext(ctx, r.spec.Command, args...)
	cmd.Dir = r.opts.Dir
	cmd.Env = r.environ()
	if r.spec.Prompt == PromptStdin {
		cmd.Stdin = strings.NewReader(prompt)
	}

	var stdout, stderr bytes.Buffer
	if r.opts.Verbose {
		cmd.Stdout = io.MultiWriter(&stdout, r.opts.Stderr)
		cmd.Stderr = io.MultiWriter(&stderr, r.opts.Stderr)
	} else {
		cmd.Stdout = &stdout
		cmd.Stderr = &stderr
	}

	start := time.Now()
	err := cmd.Run()
	res := &Result{
		Stdout:   stdout.String(),
		Stderr:   stderr.String(),
		Duration: time.Since(start),
	}

	if err != nil {
		if ctx.Err() == context.DeadlineExceeded {
			return res, fmt.Errorf("harness %q timed out after %s (raise harness.timeout in katana.yaml)",
				r.spec.Name, r.opts.Timeout)
		}
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			res.ExitCode = exitErr.ExitCode()
			return res, fmt.Errorf("harness %q exited with status %d: %s",
				r.spec.Name, res.ExitCode, firstLines(res.Stderr, 10))
		}
		return res, fmt.Errorf("running harness %q: %w", r.spec.Name, err)
	}
	return res, nil
}

func (r *Runner) environ() []string {
	if len(r.opts.Env) == 0 {
		return nil // inherit the parent environment unchanged
	}
	env := os.Environ()
	keys := make([]string, 0, len(r.opts.Env))
	for k := range r.opts.Env {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		env = append(env, k+"="+r.opts.Env[k])
	}
	return env
}

func firstLines(s string, n int) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return "(no stderr output)"
	}
	lines := strings.Split(s, "\n")
	if len(lines) <= n {
		return s
	}
	return strings.Join(lines[:n], "\n") + "\n  ..."
}
