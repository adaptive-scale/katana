// Package suite runs a project's test command and records what came of it.
//
// It is where `katana run` and the terminal UI meet: both run the same command
// the same way, and both leave the same record behind, so what the UI shows
// after running one behavior is what the next `katana status` will say.
package suite

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/adaptive-scale/katana/internal/config"
	"github.com/adaptive-scale/katana/internal/history"
	"github.com/adaptive-scale/katana/internal/plan"
	"github.com/adaptive-scale/katana/internal/report"
	"github.com/adaptive-scale/katana/internal/results"
	"github.com/adaptive-scale/katana/internal/tracker"
)

// Target is one behavior a run is narrowed to.
type Target struct {
	// Source is the behavior file, which is what the record is scoped by.
	Source string
	// Output is its generated test file, project-relative.
	Output string
	// Tests are the case names the tracker recorded for it.
	Tests []string
}

// Request is one execution of the test command.
type Request struct {
	// Only narrows the run to a single behavior. Nil runs the whole suite.
	Only *Target
	// Extra arguments are appended to the configured command, as `katana run --`
	// passes them.
	Extra []string
	// PerCase asks the runner to name every case where katana knows the flag.
	PerCase bool
	// Stdout and Stderr receive the runner's output as it streams. Either may be
	// nil, and the output is captured for parsing regardless.
	Stdout, Stderr io.Writer
	// Stdin is handed to the runner, for a suite that expects a terminal. The
	// UI leaves it nil: its own terminal is in raw mode and is not the test
	// runner's to read.
	Stdin io.Reader
}

// Result is what a run produced.
type Result struct {
	Command   string
	StartedAt time.Time
	Duration  time.Duration
	ExitCode  int
	// Parsed reports whether per-case results were recovered from the output.
	Parsed bool
	Cases  []report.Case
	Output string
	// Scope is the behavior the run was narrowed to, empty for a whole suite.
	Scope string
	// Narrowed reports whether the command really was narrowed to the target.
	// A runner katana cannot narrow runs whole, which is worth saying out loud.
	Narrowed bool
	// Notes are what katana changed or could not do, in the order it happened.
	Notes []string
}

// OK reports whether the run passed.
func (r *Result) OK() bool { return r.ExitCode == 0 }

// Blocked names the suites that never ran their tests because they failed to
// compile or to set up, in the order the runner reported them.
//
// This is the difference between a suite that is failing and a suite that is
// absent. A package that does not build contributes one failing case in place
// of every test in it, so counting cases alone reports hundreds of tests that
// did not run as a single failure.
func (r *Result) Blocked() []string {
	var out []string
	seen := map[string]bool{}
	for _, c := range r.Cases {
		if !c.Blocked || seen[c.Suite] {
			continue
		}
		seen[c.Suite] = true
		out = append(out, c.Suite)
	}
	return out
}

// Counts tallies the recovered cases by outcome.
func (r *Result) Counts() (pass, fail, skip int) {
	for _, c := range r.Cases {
		switch c.Status {
		case report.StatusFail:
			fail++
		case report.StatusSkip:
			skip++
		default:
			pass++
		}
	}
	return pass, fail, skip
}

// Run executes the project's test command and returns what it produced. An
// error is returned only when the command could not be run at all: a suite that
// fails is a result, not an error.
func Run(ctx context.Context, cfg *config.Config, req Request) (*Result, error) {
	command := strings.TrimSpace(cfg.Test.Command)
	if command == "" {
		return nil, errors.New("no test.command set in katana.yaml")
	}

	res := &Result{}
	if req.PerCase {
		// "The suite passed" is worth little on its own, so ask the runner for
		// per-case output when katana knows how.
		if verbose, added := report.Verbose(cfg.Defaults.Framework, command); added {
			command = verbose
			res.Notes = append(res.Notes, "added -v to the test command so each case is recorded")
		}
	}
	if t := req.Only; t != nil {
		res.Scope = t.Source
		narrowed, ok := report.Filter(cfg.Defaults.Framework, command, t.Output, t.Tests)
		command, res.Narrowed = narrowed, ok
		if !ok {
			res.Notes = append(res.Notes,
				fmt.Sprintf("katana cannot narrow this runner to one behavior; running the whole suite and reporting %s", t.Source))
		}
	}
	if len(req.Extra) > 0 {
		command += " " + strings.Join(quoteAll(req.Extra), " ")
	}
	res.Command = command

	workDir := cfg.Root
	if cfg.Test.Dir != "" {
		workDir = filepath.Join(cfg.Root, filepath.FromSlash(cfg.Test.Dir))
	}

	cmd := exec.CommandContext(ctx, shell(), shellFlag(), command)
	cmd.Dir = workDir
	cmd.Stdin = req.Stdin
	// The suite output is copied as it streams, so the terminal is unchanged and
	// katana still has the runner's own words to recover per-case results from.
	rec := &report.Recorder{}
	cmd.Stdout = rec.Tee(orDiscard(req.Stdout))
	cmd.Stderr = rec.Tee(orDiscard(req.Stderr))

	res.StartedAt = time.Now()
	runErr := cmd.Run()
	res.Duration = time.Since(res.StartedAt)

	if runErr != nil {
		var exitErr *exec.ExitError
		if !errors.As(runErr, &exitErr) {
			return nil, fmt.Errorf("running test command: %w", runErr)
		}
		res.ExitCode = exitErr.ExitCode()
	}

	res.Output = rec.String()
	res.Cases = report.Parse(cfg.Defaults.Framework, res.Output)
	res.Parsed = len(res.Cases) > 0
	return res, nil
}

// Record writes the run to the results file `katana status` reads and appends it
// to the history the charts are drawn from. The tracker is what attributes cases
// to behaviors, so the same record answers "did the suite pass" and "how is this
// behavior doing".
//
// The results returned are the merged record: a run of one behavior updates that
// behavior's outcomes and leaves the rest of the record standing, so status
// after a targeted run still knows what the other behaviors did.
func (r *Result) Record(root string, t *tracker.Tracker) (*results.Results, error) {
	res := results.Record(r.Command, r.StartedAt, r.ExitCode, r.Parsed, r.Cases)
	res.Scope = r.Scope

	// A previous record that cannot be read is not worth failing the run over —
	// the outcome that just happened is the one worth keeping.
	if prev, err := results.Load(root); err == nil {
		res.Inherit(prev)
	}
	if err := res.Save(root); err != nil {
		return nil, err
	}

	run := history.Run{
		RanAt:    r.StartedAt,
		Command:  r.Command,
		ExitCode: r.ExitCode,
		Millis:   r.Duration.Milliseconds(),
		PerCase:  r.Parsed,
		Scope:    r.Scope,
	}
	// The run's own totals come from the cases it reported, not from the merged
	// record: the history is a row per run, and inherited outcomes belong to the
	// runs that produced them.
	just := results.Record(r.Command, r.StartedAt, r.ExitCode, r.Parsed, r.Cases)
	c := just.Counts()
	run.Pass, run.Fail, run.Skip = c.Pass, c.Fail, c.Skip
	for source, entry := range t.Entries {
		if len(entry.Tests) == 0 {
			continue
		}
		tally := just.Tally(entry.Tests)
		run.Behaviors = append(run.Behaviors, history.Behavior{
			Source:  source,
			Pass:    tally.Pass,
			Fail:    tally.Fail,
			Skip:    tally.Skip,
			Unknown: tally.Unknown,
		})
	}
	if err := history.Record(root, run); err != nil {
		return res, err
	}
	return res, nil
}

// TargetFor builds the run target for a behavior from what the tracker knows
// about it. A behavior that has never been generated has no tests to run.
func TargetFor(t *tracker.Tracker, it plan.Item) (*Target, bool) {
	entry, ok := t.Get(it.Source)
	if !ok {
		return nil, false
	}
	return &Target{Source: it.Source, Output: entry.Output, Tests: entry.Tests}, true
}

// orDiscard lets a caller that has nowhere to stream the output leave it nil.
func orDiscard(w io.Writer) io.Writer {
	if w == nil {
		return io.Discard
	}
	return w
}

func shell() string {
	if runtime.GOOS == "windows" {
		return "cmd"
	}
	if sh := os.Getenv("SHELL"); sh != "" {
		return sh
	}
	return "/bin/sh"
}

func shellFlag() string {
	if runtime.GOOS == "windows" {
		return "/C"
	}
	return "-c"
}

// quoteAll shell-quotes pass-through arguments so paths with spaces survive.
func quoteAll(args []string) []string {
	out := make([]string, len(args))
	for i, a := range args {
		out[i] = "'" + strings.ReplaceAll(a, "'", `'\''`) + "'"
	}
	return out
}
