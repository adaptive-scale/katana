package coverage

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

// HistoryFileName is the local coverage history inside the .katana directory.
// It stores compact file totals rather than the raw profiles runners write.
const HistoryFileName = "coverage-history.json"

// HistoryVersion is the coverage-history schema this katana understands.
const HistoryVersion = 1

// Run is one coverage observation. Imported is true when --profile supplied
// the observation; otherwise Command, ExitCode and Millis describe the suite
// execution which produced it.
type Run struct {
	RanAt    time.Time `json:"ran_at"`
	Command  string    `json:"command,omitempty"`
	ExitCode int       `json:"exit_code,omitempty"`
	Millis   int64     `json:"millis,omitempty"`
	Imported bool      `json:"imported,omitempty"`
	Profile  string    `json:"profile,omitempty"`
	Error    string    `json:"error,omitempty"`
	Format   string    `json:"format,omitempty"`
	Mode     string    `json:"mode,omitempty"`
	Files    []File    `json:"files,omitempty"`
}

// Total is the project total recorded by this observation.
func (r Run) Total() File {
	p := Profile{Files: r.Files}
	return p.Total()
}

// Percent is the total statement coverage percentage for this observation.
func (r Run) Percent() float64 { return r.Total().Percent() }

// Measured reports whether the observation contained any measured statements.
func (r Run) Measured() bool { return r.Total().Statements > 0 }

// History holds every compact coverage observation for a project, oldest first.
// Raw reports remain opt-in through --save and are never copied into this file.
type History struct {
	Version int   `json:"version"`
	Runs    []Run `json:"runs,omitempty"`
}

// Stats summarizes the measurable observations in a history. Runs includes
// empty observations as well, while Measured is the number used for percentages.
type Stats struct {
	Runs, Measured int
	Average        float64
	Min, Max       float64
	FirstRanAt     time.Time
	LastRanAt      time.Time
}

// Stats derives lifetime coverage statistics without losing the distinction
// between a valid empty report and one which measured code.
func (h *History) Stats() Stats {
	var s Stats
	s.Runs = len(h.Runs)
	for _, r := range h.Runs {
		if !r.Measured() {
			continue
		}
		pct := r.Percent()
		if s.Measured == 0 || pct < s.Min {
			s.Min = pct
		}
		if s.Measured == 0 || pct > s.Max {
			s.Max = pct
		}
		s.Average += pct
		s.Measured++
		if s.FirstRanAt.IsZero() || r.RanAt.Before(s.FirstRanAt) {
			s.FirstRanAt = r.RanAt
		}
		if r.RanAt.After(s.LastRanAt) {
			s.LastRanAt = r.RanAt
		}
	}
	if s.Measured > 0 {
		s.Average /= float64(s.Measured)
	}
	return s
}

// Recent returns the newest measurable observations, still oldest first. Empty
// reports stay on file but are omitted from charts because zero is not a rate.
func (h *History) Recent(n int) []Run {
	measured := make([]Run, 0, len(h.Runs))
	for _, r := range h.Runs {
		if r.Measured() {
			measured = append(measured, r)
		}
	}
	if n <= 0 || n >= len(measured) {
		return measured
	}
	return measured[len(measured)-n:]
}

// LatestTwo returns the two newest measurable observations. The bool is false
// until a comparison is possible.
func (h *History) LatestTwo() (previous, current Run, ok bool) {
	runs := h.Recent(2)
	if len(runs) < 2 {
		return Run{}, Run{}, false
	}
	return runs[0], runs[1], true
}

// Change is one file's percentage movement between comparable observations.
type Change struct {
	Path             string
	Before, After    File
	PercentagePoints float64
}

// Changes compares files present with statements in both observations. A file
// added to or removed from the report changes the project total, but is not
// called a file regression because there is no like-for-like percentage.
func Changes(before, after Run) []Change {
	old := make(map[string]File, len(before.Files))
	for _, f := range before.Files {
		old[f.Path] = f
	}
	changes := make([]Change, 0, len(after.Files))
	for _, now := range after.Files {
		was, ok := old[now.Path]
		if !ok || was.Statements <= 0 || now.Statements <= 0 {
			continue
		}
		delta := now.Percent() - was.Percent()
		if delta == 0 {
			continue
		}
		changes = append(changes, Change{
			Path: now.Path, Before: was, After: now, PercentagePoints: delta,
		})
	}
	sort.SliceStable(changes, func(i, j int) bool {
		if changes[i].PercentagePoints != changes[j].PercentagePoints {
			return changes[i].PercentagePoints < changes[j].PercentagePoints
		}
		return changes[i].Path < changes[j].Path
	})
	return changes
}

// HistoryPath returns the coverage-history path for a project root.
func HistoryPath(root string) string {
	return filepath.Join(root, ".katana", HistoryFileName)
}

// LoadHistory reads a project's coverage history. A project with none yet gets
// a new empty history.
func LoadHistory(root string) (*History, error) {
	data, err := os.ReadFile(HistoryPath(root))
	if errors.Is(err, fs.ErrNotExist) {
		return &History{Version: HistoryVersion}, nil
	}
	if err != nil {
		return nil, err
	}
	h := &History{}
	if err := json.Unmarshal(data, h); err != nil {
		return nil, fmt.Errorf("parsing %s: %w (delete it; katana starts a new coverage history)", HistoryPath(root), err)
	}
	if h.Version != HistoryVersion {
		return nil, fmt.Errorf("coverage history %s has version %d, this katana understands %d",
			HistoryPath(root), h.Version, HistoryVersion)
	}
	sort.SliceStable(h.Runs, func(i, j int) bool { return h.Runs[i].RanAt.Before(h.Runs[j].RanAt) })
	return h, nil
}

// Add appends an observation without trimming old ones. The rows are compact
// totals and are the data needed for long-term trends and file regressions.
func (h *History) Add(r Run) {
	r.RanAt = r.RanAt.UTC()
	r.Files = Merge(r.Files)
	h.Runs = append(h.Runs, r)
}

// SaveHistory atomically writes a coverage history beneath the project root.
func (h *History) SaveHistory(root string) error {
	h.Version = HistoryVersion
	p := HistoryPath(root)
	data, err := json.MarshalIndent(h, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(p), ".coverage-history-*.json")
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
