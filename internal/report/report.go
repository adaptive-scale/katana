// Package report records the outcome of a `katana run` as a self-contained HTML
// page, so a suite's results outlive the terminal scrollback.
//
// katana does not own the test runner — `test.command` in katana.yaml can be
// anything — so per-case results are recovered by parsing the runner's own
// output. That is best-effort by nature: when no parser recognises the output
// the report still records the command, the exit code and the full log.
package report

import (
	"bytes"
	"io"
	"strings"
	"sync"
	"time"
)

// Status is the outcome of a single test case.
type Status string

const (
	// StatusPass is a test case that succeeded.
	StatusPass Status = "pass"
	// StatusFail is a test case that failed or errored.
	StatusFail Status = "fail"
	// StatusSkip is a test case the runner did not execute.
	StatusSkip Status = "skip"
)

// defaultSuite labels cases whose runner did not attribute them to a package,
// file or class.
const defaultSuite = "test suite"

// Case is one test case recovered from the suite output.
type Case struct {
	// Suite is the package, file or class the case belongs to.
	Suite string
	// Name is the case name as the runner printed it.
	Name string
	// Status is the recovered outcome.
	Status Status
	// Duration is the runner's own timing, when it reported one.
	Duration time.Duration
	// Output is the failure detail the runner printed for this case.
	Output string
}

// Behavior pairs a behavior spec with the tests generated from it, so a report
// says whether the suite covers the current specification and not just whether
// it was green.
type Behavior struct {
	Source string
	Output string
	Status string
	Stack  string
	Stale  bool
}

// Report is one recorded run of the suite.
type Report struct {
	// Project is the project directory name, used as the report title.
	Project string
	// Root is the absolute project root.
	Root string
	// Command is the test command that produced this report.
	Command string
	// Framework is the configured default framework, used to pick a parser.
	Framework string
	// Version is the katana version that wrote the report.
	Version string
	// StartedAt is when the suite started.
	StartedAt time.Time
	// Duration is how long the suite took.
	Duration time.Duration
	// ExitCode is the test command's exit status: the authoritative verdict.
	ExitCode int
	// Parsed reports whether per-case results were recovered from the output.
	Parsed bool
	// Cases are the recovered test cases.
	Cases []Case
	// Behaviors are the configured behaviors and their staleness at run time.
	Behaviors []Behavior
	// Output is the full captured suite output.
	Output string
}

// Collect fills Cases by parsing the captured output. When no parser recognises
// the output it records a single case standing for the whole suite, so the
// report always states a result even for a runner katana cannot read.
func (r *Report) Collect() {
	r.Cases = Parse(r.Framework, r.Output)
	r.Parsed = len(r.Cases) > 0
	if r.Parsed {
		return
	}
	status := StatusPass
	if r.ExitCode != 0 {
		status = StatusFail
	}
	r.Cases = []Case{{
		Suite:    defaultSuite,
		Name:     r.Command,
		Status:   status,
		Duration: r.Duration,
	}}
}

// OK reports whether the run passed. The runner's exit code decides, not the
// parsed cases: a suite can exit non-zero for reasons no case reports.
func (r *Report) OK() bool { return r.ExitCode == 0 }

// Result is the one-word verdict shown at the top of the report.
func (r *Report) Result() string {
	if r.OK() {
		return "passed"
	}
	return "failed"
}

// Total is the number of recovered cases.
func (r *Report) Total() int { return len(r.Cases) }

// Passed, Failed and Skipped count cases by status.
func (r *Report) Passed() int  { return r.count(StatusPass) }
func (r *Report) Failed() int  { return r.count(StatusFail) }
func (r *Report) Skipped() int { return r.count(StatusSkip) }

func (r *Report) count(s Status) int {
	n := 0
	for _, c := range r.Cases {
		if c.Status == s {
			n++
		}
	}
	return n
}

// PassRate is the percentage of executed (non-skipped) cases that passed.
func (r *Report) PassRate() float64 {
	executed := r.Total() - r.Skipped()
	if executed <= 0 {
		return 0
	}
	return float64(r.Passed()) / float64(executed) * 100
}

// StaleBehaviors counts behaviors whose tests were not up to date when the
// suite ran.
func (r *Report) StaleBehaviors() int {
	n := 0
	for _, b := range r.Behaviors {
		if b.Stale {
			n++
		}
	}
	return n
}

// Suite is a group of cases sharing a package, file or class.
type Suite struct {
	Name     string
	Cases    []Case
	Passed   int
	Failed   int
	Skipped  int
	Duration time.Duration
}

// Suites groups the cases for display, keeping the order the runner reported
// them in so the report reads like the terminal output did.
func (r *Report) Suites() []Suite {
	var out []Suite
	index := map[string]int{}
	for _, c := range r.Cases {
		name := c.Suite
		if strings.TrimSpace(name) == "" {
			name = defaultSuite
		}
		i, ok := index[name]
		if !ok {
			out = append(out, Suite{Name: name})
			i = len(out) - 1
			index[name] = i
		}
		s := &out[i]
		s.Cases = append(s.Cases, c)
		s.Duration += c.Duration
		switch c.Status {
		case StatusPass:
			s.Passed++
		case StatusFail:
			s.Failed++
		case StatusSkip:
			s.Skipped++
		}
	}
	return out
}

// maxRecorded bounds how much suite output a Recorder keeps. A runaway suite
// should not be able to turn a report into a gigabyte of HTML.
const maxRecorded = 8 << 20

// truncationNotice marks output that hit maxRecorded.
const truncationNotice = "\n… output truncated by katana at 8 MB …\n"

// Recorder keeps a copy of everything written through it while passing the
// bytes on, so the terminal still streams the suite output live.
type Recorder struct {
	mu        sync.Mutex
	buf       bytes.Buffer
	truncated bool
}

// Tee returns a writer that forwards to w and records what it sees. The
// returned writers are safe for the separate goroutines exec.Cmd uses to copy
// a process's stdout and stderr.
func (r *Recorder) Tee(w io.Writer) io.Writer { return &tee{rec: r, w: w} }

// String returns the recorded output.
func (r *Recorder) String() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	s := r.buf.String()
	if r.truncated {
		s += truncationNotice
	}
	return s
}

type tee struct {
	rec *Recorder
	w   io.Writer
}

func (t *tee) Write(p []byte) (int, error) {
	t.rec.mu.Lock()
	if room := maxRecorded - t.rec.buf.Len(); room > 0 {
		if len(p) <= room {
			t.rec.buf.Write(p)
		} else {
			t.rec.buf.Write(p[:room])
			t.rec.truncated = true
		}
	} else {
		t.rec.truncated = true
	}
	t.rec.mu.Unlock()
	return t.w.Write(p)
}
