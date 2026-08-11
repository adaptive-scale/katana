// Package history keeps a short record of the runs that came before the last
// one, so katana can show how a behavior has been doing rather than only how it
// did the last time the suite ran.
//
// It is local state, like the results it is built from: a record of what this
// machine ran, kept beside the tracker rather than in it.
package history

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"time"
)

// FileName is the history file inside the .katana directory.
const FileName = "history.json"

// Version is the history schema version.
const Version = 1

// Max is how many runs are kept. The history is a picture of recent behaviour,
// not an archive: enough runs to see a test that has started failing, few enough
// that the file stays small and cheap to read on every status.
const Max = 50

// Behavior is how one behavior's test cases fared in one run.
type Behavior struct {
	Source  string `json:"source"`
	Pass    int    `json:"pass,omitempty"`
	Fail    int    `json:"fail,omitempty"`
	Skip    int    `json:"skip,omitempty"`
	Unknown int    `json:"unknown,omitempty"`
}

// Known is how many of the behavior's cases the run reported an outcome for.
func (b Behavior) Known() int { return b.Pass + b.Fail + b.Skip }

// Total is how many cases the behavior had at the time of the run.
func (b Behavior) Total() int { return b.Known() + b.Unknown }

// Rate is the share of the run's known cases that passed, and false when it
// reported on none of them — which is not the same as a rate of zero.
func (b Behavior) Rate() (float64, bool) {
	if b.Known() == 0 {
		return 0, false
	}
	return float64(b.Pass) / float64(b.Known()), true
}

// Run is one execution of the test command.
type Run struct {
	RanAt    time.Time `json:"ran_at"`
	Command  string    `json:"command"`
	ExitCode int       `json:"exit_code"`
	// Millis is how long the command took, which is worth having for a run that
	// suddenly takes ten times as long as the ones before it.
	Millis int64 `json:"millis,omitempty"`
	// PerCase reports whether the runner's output could be read case by case.
	PerCase bool `json:"per_case,omitempty"`
	// Scope names the behavior a targeted run covered, empty for the whole
	// suite. A run of one behavior says nothing about the others, and the
	// histogram has to know that to avoid drawing them as having no cases.
	Scope     string     `json:"scope,omitempty"`
	Pass      int        `json:"pass,omitempty"`
	Fail      int        `json:"fail,omitempty"`
	Skip      int        `json:"skip,omitempty"`
	Behaviors []Behavior `json:"behaviors,omitempty"`
}

// OK reports whether the run passed, by its own exit code.
func (r Run) OK() bool { return r.ExitCode == 0 }

// Total is how many cases the run reported.
func (r Run) Total() int { return r.Pass + r.Fail + r.Skip }

// Rate is the share of reported cases that passed.
func (r Run) Rate() float64 {
	if r.Total() == 0 {
		return 0
	}
	return float64(r.Pass) / float64(r.Total())
}

// Find returns what the run recorded for one behavior.
func (r Run) Find(source string) (Behavior, bool) {
	for _, b := range r.Behaviors {
		if b.Source == source {
			return b, true
		}
	}
	return Behavior{}, false
}

// History is the recent runs, oldest first.
type History struct {
	Version int   `json:"version"`
	Runs    []Run `json:"runs,omitempty"`
}

// Path returns the history file path for a project root.
func Path(root string) string {
	return filepath.Join(root, ".katana", FileName)
}

// Load reads the history, returning an empty one when nothing has been recorded
// yet.
func Load(root string) (*History, error) {
	data, err := os.ReadFile(Path(root))
	if errors.Is(err, fs.ErrNotExist) {
		return &History{Version: Version}, nil
	}
	if err != nil {
		return nil, err
	}

	h := &History{}
	if err := json.Unmarshal(data, h); err != nil {
		return nil, fmt.Errorf("parsing %s: %w (delete it; katana starts a new history)", Path(root), err)
	}
	if h.Version != Version {
		return nil, fmt.Errorf("history %s has version %d, this katana understands %d", Path(root), h.Version, Version)
	}
	sort.SliceStable(h.Runs, func(i, j int) bool { return h.Runs[i].RanAt.Before(h.Runs[j].RanAt) })
	return h, nil
}

// Add appends a run, dropping the oldest once there are more than Max.
func (h *History) Add(r Run) {
	r.RanAt = r.RanAt.UTC()
	h.Runs = append(h.Runs, r)
	if n := len(h.Runs) - Max; n > 0 {
		h.Runs = append([]Run(nil), h.Runs[n:]...)
	}
}

// Save writes the history for a project root.
func (h *History) Save(root string) error {
	h.Version = Version
	p := Path(root)

	data, err := json.MarshalIndent(h, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')

	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return err
	}
	// Written through a temporary file, as the tracker and results are: an
	// interrupted run must not leave a half-written history behind.
	tmp, err := os.CreateTemp(filepath.Dir(p), ".history-*.json")
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

// Record appends one run to a project's history in one step.
func Record(root string, r Run) error {
	h, err := Load(root)
	if err != nil {
		// A history that cannot be read is started again rather than lost for
		// good: it is a convenience, and refusing to record any more runs
		// because of one bad file would be the worse failure.
		h = &History{Version: Version}
	}
	h.Add(r)
	return h.Save(root)
}

// Recent returns the last n runs, oldest first.
func (h *History) Recent(n int) []Run {
	if n <= 0 || len(h.Runs) <= n {
		return h.Runs
	}
	return h.Runs[len(h.Runs)-n:]
}

// For returns the last n runs that reported on one behavior, oldest first. A run
// that did not cover the behavior — a targeted run of another one, or a run from
// before the behavior existed — is left out rather than drawn as a run in which
// it had nothing pass.
func (h *History) For(source string, n int) []Run {
	var out []Run
	for _, r := range h.Runs {
		if b, ok := r.Find(source); ok && b.Known() > 0 {
			out = append(out, r)
		}
	}
	if n > 0 && len(out) > n {
		out = out[len(out)-n:]
	}
	return out
}
