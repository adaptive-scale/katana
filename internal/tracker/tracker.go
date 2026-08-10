// Package tracker records what katana generated from which behavior file, so a
// later run can tell what changed and regenerate only that.
package tracker

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"time"
)

// FileName is the tracker file inside the .katana directory.
const FileName = "tracker.json"

// Version is the tracker schema version.
const Version = 1

// Entry is the record of one generated test file.
type Entry struct {
	Source     string `json:"source"`
	SourceHash string `json:"source_hash"`
	Output     string `json:"output"`
	OutputHash string `json:"output_hash"`
	// Tests index the cases the generated file declares, in the order they
	// appear. It answers "which tests came out of this behavior" without
	// running the suite, and is written from what the last generation produced.
	// An empty index means katana could not read cases out of the file, never
	// that the behavior is out of date: staleness is decided by the hashes
	// above and nothing else.
	Tests []string `json:"tests,omitempty"`
	// TestCount is len(Tests), kept in the file so the tracker can be read at a
	// glance. Record is what keeps the two in step.
	TestCount     int       `json:"test_count,omitempty"`
	Language      string    `json:"language"`
	Framework     string    `json:"framework"`
	Harness       string    `json:"harness"`
	GeneratedAt   time.Time `json:"generated_at"`
	KatanaVersion string    `json:"katana_version,omitempty"`
}

// Tracker is the on-disk state, keyed by behavior source path.
type Tracker struct {
	Version   int              `json:"version"`
	UpdatedAt time.Time        `json:"updated_at"`
	Entries   map[string]Entry `json:"entries"`

	path  string
	dirty bool
}

// Status describes why a behavior does or does not need regeneration.
type Status int

const (
	// StatusUpToDate means the behavior and its generated output are unchanged.
	StatusUpToDate Status = iota
	// StatusNew means katana has never generated this behavior.
	StatusNew
	// StatusBehaviorChanged means the behavior markdown changed since generation.
	StatusBehaviorChanged
	// StatusOutputMissing means the generated file is gone.
	StatusOutputMissing
	// StatusOutputModified means the generated file was edited by hand.
	StatusOutputModified
	// StatusConfigChanged means the language, framework or harness changed.
	StatusConfigChanged
	// StatusOutputUntracked means tests are already there for a behavior katana
	// has no record of generating.
	StatusOutputUntracked
)

// String renders the status for CLI output.
func (s Status) String() string {
	switch s {
	case StatusUpToDate:
		return "up to date"
	case StatusNew:
		return "new"
	case StatusBehaviorChanged:
		return "behavior changed"
	case StatusOutputMissing:
		return "output missing"
	case StatusOutputModified:
		return "output edited by hand"
	case StatusConfigChanged:
		return "config changed"
	case StatusOutputUntracked:
		return "output not tracked"
	}
	return "unknown"
}

// NeedsGeneration reports whether the status calls for regeneration by default.
// StatusOutputModified and StatusOutputUntracked deliberately do not: katana
// will not silently discard a test file it did not write, so those cases
// require --force.
func (s Status) NeedsGeneration() bool {
	switch s {
	case StatusNew, StatusBehaviorChanged, StatusOutputMissing, StatusConfigChanged:
		return true
	}
	return false
}

// Path returns the tracker file path for a project root.
func Path(root string) string {
	return filepath.Join(root, ".katana", FileName)
}

// Load reads the tracker, returning an empty one if it does not exist yet.
func Load(root string) (*Tracker, error) {
	p := Path(root)
	t := &Tracker{Version: Version, Entries: map[string]Entry{}, path: p}

	data, err := os.ReadFile(p)
	if errors.Is(err, fs.ErrNotExist) {
		return t, nil
	}
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(data, t); err != nil {
		return nil, fmt.Errorf("parsing %s: %w (delete it to start over)", p, err)
	}
	if t.Entries == nil {
		t.Entries = map[string]Entry{}
	}
	if t.Version != Version {
		return nil, fmt.Errorf("tracker %s has version %d, this katana understands %d", p, t.Version, Version)
	}
	t.path = p
	return t, nil
}

// Get returns the recorded entry for a behavior source.
func (t *Tracker) Get(source string) (Entry, bool) {
	e, ok := t.Entries[source]
	return e, ok
}

// Record stores an entry and marks the tracker for saving. The test count is
// derived here rather than by the caller, so the index and its count cannot
// disagree in the file.
func (t *Tracker) Record(e Entry) {
	e.TestCount = len(e.Tests)
	t.Entries[e.Source] = e
	t.dirty = true
}

// Prune drops entries whose behavior file is no longer configured, returning
// the sources it removed.
func (t *Tracker) Prune(keep map[string]bool) []string {
	var removed []string
	for src := range t.Entries {
		if !keep[src] {
			removed = append(removed, src)
			delete(t.Entries, src)
			t.dirty = true
		}
	}
	sort.Strings(removed)
	return removed
}

// Save writes the tracker if anything changed.
func (t *Tracker) Save() error {
	if !t.dirty {
		return nil
	}
	t.UpdatedAt = time.Now().UTC()
	t.Version = Version

	data, err := json.MarshalIndent(t, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')

	if err := os.MkdirAll(filepath.Dir(t.path), 0o755); err != nil {
		return err
	}
	// Write to a temp file and rename so an interrupted run cannot leave a
	// half-written tracker behind.
	tmp, err := os.CreateTemp(filepath.Dir(t.path), ".tracker-*.json")
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
	if err := os.Rename(tmpName, t.path); err != nil {
		return err
	}
	t.dirty = false
	return nil
}

// HashBytes returns the SHA-256 of content already in memory, so a caller that
// has just read a file does not have to read it a second time to hash it.
func HashBytes(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// HashFile returns the SHA-256 of a file's contents. A missing file hashes to
// the empty string rather than erroring, so callers can compare directly.
func HashFile(path string) (string, error) {
	f, err := os.Open(path)
	if errors.Is(err, fs.ErrNotExist) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	defer f.Close()

	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}
