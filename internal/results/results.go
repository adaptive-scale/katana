// Package results records the outcome of the most recent `katana run`, so
// `katana status` can say what passed without running the suite again.
//
// The record is local by nature: it describes one machine's last run, not what
// the project generated. That is why it lives beside the tracker rather than in
// it — the tracker is shared state that belongs in version control, and a run's
// results do not.
package results

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/adaptive-scale/katana/internal/report"
)

// FileName is the results file inside the .katana directory.
const FileName = "results.json"

// Version is the results schema version.
const Version = 1

// Status is the outcome of one test case. It is report's own status type, so
// what is written here cannot drift from what the parsers produce.
type Status = report.Status

// Case is one test case as the last run left it.
type Case struct {
	// Suite is the package, file or class the case belongs to.
	Suite string `json:"suite,omitempty"`
	// Name is the case name as the runner printed it.
	Name string `json:"name"`
	// Status is the recorded outcome.
	Status Status `json:"status"`
	// RanAt is the run this outcome came from. A run of one behavior leaves the
	// other behaviors' outcomes standing rather than erasing them, so the cases
	// in one file are not all the same age.
	RanAt time.Time `json:"ran_at,omitempty"`
	// Blocked marks a case standing for a whole suite that never ran its tests.
	Blocked bool `json:"blocked,omitempty"`
}

// Results is the outcome of the most recent run of the suite.
type Results struct {
	Version  int       `json:"version"`
	RanAt    time.Time `json:"ran_at"`
	Command  string    `json:"command"`
	ExitCode int       `json:"exit_code"`
	// PerCase reports whether the runner's output could be read case by case.
	// When it is false, Cases holds a single entry standing for the whole
	// suite: katana does not own the test runner, and not every output can be
	// attributed to individual cases.
	PerCase bool `json:"per_case"`
	// Scope names the behavior a targeted run covered, and is empty for a run
	// of the whole suite. Running one behavior's tests updates that behavior's
	// outcomes and leaves the rest of the record standing, so a reader has to be
	// told that the last run was not the whole story.
	Scope string `json:"scope,omitempty"`
	Cases []Case `json:"cases,omitempty"`

	// index maps a test case name to every case recorded under it, including
	// the subtests below it. Built on first lookup.
	index map[string][]Case
}

// Record builds the results of one run from the cases a report recovered.
func Record(command string, ranAt time.Time, exitCode int, perCase bool, cases []report.Case) *Results {
	r := &Results{
		Version:  Version,
		RanAt:    ranAt.UTC(),
		Command:  command,
		ExitCode: exitCode,
		PerCase:  perCase,
		Cases:    make([]Case, 0, len(cases)),
	}
	for _, c := range cases {
		r.Cases = append(r.Cases, Case{
			Suite:   c.Suite,
			Name:    c.Name,
			Status:  c.Status,
			RanAt:   r.RanAt,
			Blocked: c.Blocked,
		})
	}
	return r
}

// BlockedSuites names the suites this run could not run at all — one that failed
// to compile or to set up — in the order they were recorded.
func (r *Results) BlockedSuites() []string {
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

// Inherit carries forward the outcomes of test cases this run did not report on,
// which is what makes a run of one behavior an update to the record rather than
// a replacement of it. Each case keeps the timestamp of the run that produced
// it, so nothing inherited is passed off as having just run.
//
// Only per-case records take part: a run whose output katana could not read case
// by case holds a single entry standing for the whole suite, and mixing that
// with real case outcomes would attribute the suite's result to a test name.
//
// A suite this run could not build is deliberately not inherited into. The run
// did reach that suite; it is the tests that never ran. Carrying the last green
// outcome forward would report a package that no longer compiles as passing,
// which is the one answer a reader must not be given.
func (r *Results) Inherit(prev *Results) {
	if prev == nil || !prev.Recorded() || !prev.PerCase || !r.PerCase {
		return
	}
	blocked := make(map[string]bool)
	for _, c := range r.Cases {
		if c.Blocked {
			blocked[c.Suite] = true
		}
	}
	have := make(map[string]bool, len(r.Cases))
	for _, c := range r.Cases {
		have[key(c.Name)] = true
	}
	for _, c := range prev.Cases {
		// A blocked marker is a fact about one run, not an outcome for a test
		// that can still be true later. Inheriting it would leave a package that
		// has since been fixed reporting a build failure for good.
		if have[key(c.Name)] || blocked[c.Suite] || c.Blocked {
			continue
		}
		if c.RanAt.IsZero() {
			c.RanAt = prev.RanAt
		}
		r.Cases = append(r.Cases, c)
		have[key(c.Name)] = true
	}
	r.index = nil
}

// Path returns the results file path for a project root.
func Path(root string) string {
	return filepath.Join(root, ".katana", FileName)
}

// Load reads the recorded results, returning an empty record if no run has been
// saved yet. An empty record answers every query with "nothing recorded", so
// callers do not have to test for it before asking.
func Load(root string) (*Results, error) {
	p := Path(root)
	data, err := os.ReadFile(p)
	if errors.Is(err, fs.ErrNotExist) {
		return &Results{Version: Version}, nil
	}
	if err != nil {
		return nil, err
	}

	r := &Results{}
	if err := json.Unmarshal(data, r); err != nil {
		return nil, fmt.Errorf("parsing %s: %w (delete it; the next `katana run` writes it again)", p, err)
	}
	if r.Version != Version {
		return nil, fmt.Errorf("results %s have version %d, this katana understands %d", p, r.Version, Version)
	}
	// A record written before results carried per-case timestamps has one age:
	// the run's own. Filling it in here means the rest of the package never has
	// to ask which shape of file an outcome came from.
	for i := range r.Cases {
		if r.Cases[i].RanAt.IsZero() {
			r.Cases[i].RanAt = r.RanAt
		}
	}
	return r, nil
}

// Save writes the results for a project root.
func (r *Results) Save(root string) error {
	p := Path(root)
	r.Version = Version

	data, err := json.MarshalIndent(r, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')

	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return err
	}
	// Write to a temp file and rename so an interrupted run cannot leave a
	// half-written record behind.
	tmp, err := os.CreateTemp(filepath.Dir(p), ".results-*.json")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)

	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmpName, p)
}

// Recorded reports whether a run has been recorded at all.
func (r *Results) Recorded() bool { return r != nil && !r.RanAt.IsZero() }

// OK reports whether the recorded run passed. The runner's exit code decides,
// not the cases: a suite can exit non-zero for reasons no case reports.
func (r *Results) OK() bool { return r.ExitCode == 0 }

// Tally counts test cases by outcome.
type Tally struct {
	Pass int
	Fail int
	Skip int
	// Unknown counts cases the last run has no outcome for: cases it did not
	// run, or cases generated since it ran.
	Unknown int
}

// Total is how many cases the tally covers.
func (t Tally) Total() int { return t.Pass + t.Fail + t.Skip + t.Unknown }

// Known is how many of them the last run reported an outcome for.
func (t Tally) Known() int { return t.Pass + t.Fail + t.Skip }

// Add folds another tally into this one.
func (t *Tally) Add(o Tally) {
	t.Pass += o.Pass
	t.Fail += o.Fail
	t.Skip += o.Skip
	t.Unknown += o.Unknown
}

func (t *Tally) count(s Status, known bool) {
	switch {
	case !known:
		t.Unknown++
	case s == report.StatusFail:
		t.Fail++
	case s == report.StatusSkip:
		t.Skip++
	default:
		t.Pass++
	}
}

// Counts tallies the cases the last run itself reported, which is the answer to
// "how many passed out of all the cases that ran". Outcomes inherited from an
// earlier run are left out: they are still true, but they are not this run's
// result.
func (r *Results) Counts() Tally {
	var t Tally
	if !r.Recorded() {
		return t
	}
	for _, c := range r.Cases {
		if !r.ranHere(c) {
			continue
		}
		t.count(c.Status, true)
	}
	return t
}

// Inherited is how many recorded outcomes came from a run before the last one.
func (r *Results) Inherited() int {
	n := 0
	for _, c := range r.Cases {
		if !r.ranHere(c) {
			n++
		}
	}
	return n
}

// ranHere reports whether a case's outcome came from the run this record is of.
func (r *Results) ranHere(c Case) bool {
	return c.RanAt.IsZero() || c.RanAt.Equal(r.RanAt)
}

// Outcome returns the status recorded for one test case name.
//
// Subtests count towards their parent: `go test` reports `TestX/sub` as a case
// of its own, and a parent whose subtest failed did not pass.
func (r *Results) Outcome(name string) (Status, bool) {
	got, ok := r.recorded(name)
	if !ok {
		return "", false
	}
	return worst(got), true
}

// CaseRanAt reports when the outcome recorded for a case was produced, which is
// not always the last run: a targeted run leaves the rest of the record
// standing.
func (r *Results) CaseRanAt(name string) (time.Time, bool) {
	got, ok := r.recorded(name)
	if !ok {
		return time.Time{}, false
	}
	newest := time.Time{}
	for _, c := range got {
		if c.RanAt.After(newest) {
			newest = c.RanAt
		}
	}
	if newest.IsZero() {
		return r.RanAt, true
	}
	return newest, true
}

// LastRun reports when any of the named cases last produced an outcome. A
// targeted run can leave cases belonging to other behaviors untouched, so this
// is deliberately per case rather than the timestamp of the most recent
// suite invocation.
func (r *Results) LastRun(names []string) (time.Time, bool) {
	newest := time.Time{}
	for _, name := range names {
		when, ok := r.CaseRanAt(name)
		if ok && when.After(newest) {
			newest = when
		}
	}
	if newest.IsZero() {
		return time.Time{}, false
	}
	return newest, true
}

func (r *Results) recorded(name string) ([]Case, bool) {
	if !r.Recorded() || !r.PerCase {
		return nil, false
	}
	r.buildIndex()
	got, ok := r.index[key(name)]
	return got, ok
}

// Tally reports the outcomes recorded for a set of test case names — the tests
// of one behavior, as the tracker indexed them.
func (r *Results) Tally(names []string) Tally {
	var t Tally
	for _, n := range names {
		s, ok := r.Outcome(n)
		t.count(s, ok)
	}
	return t
}

// buildIndex indexes each recorded case under its own name and under every
// ancestor it is a subtest of.
func (r *Results) buildIndex() {
	if r.index != nil {
		return
	}
	r.index = make(map[string][]Case, len(r.Cases))
	for _, c := range r.Cases {
		for _, k := range ancestors(key(c.Name)) {
			r.index[k] = append(r.index[k], c)
		}
	}
}

// ancestors returns a case name and every name it is a subtest of:
// "TestX/sub/deep" also counts as a result for "TestX/sub" and "TestX".
func ancestors(name string) []string {
	out := []string{name}
	for i := len(name) - 1; i > 0; i-- {
		if name[i] == '/' {
			out = append(out, name[:i])
		}
	}
	return out
}

// worst reduces the outcomes recorded under one name to a single status: a
// failure anywhere outranks a pass, and a name that only skipped is a skip.
func worst(cs []Case) Status {
	out := report.StatusSkip
	for _, c := range cs {
		switch c.Status {
		case report.StatusFail:
			return report.StatusFail
		case report.StatusPass:
			out = report.StatusPass
		}
	}
	return out
}

// key normalizes a case name for lookup. Runners print the name the source
// declares, but a Go subtest name has its spaces replaced with underscores, so
// the two spellings have to meet somewhere.
func key(name string) string {
	return strings.ReplaceAll(strings.TrimSpace(name), " ", "_")
}
