// Package plan compares the behaviors a project configures against what the
// tracker recorded, and reports the state of each one.
//
// It is separate from the command line because more than the command line asks
// the question: `katana status`, `katana generate` and the terminal UI all need
// the same answer, and it must be the same answer in each of them.
package plan

import (
	"path/filepath"

	"github.com/adaptive-scale/katana/internal/config"
	"github.com/adaptive-scale/katana/internal/tracker"
)

// Item pairs a resolved behavior with why it does or does not need work.
type Item struct {
	config.Resolved
	Status     tracker.Status
	SourceHash string
	OutputHash string
}

// Stale reports whether the behavior is out of date with its generated tests.
func (i Item) Stale() bool { return i.Status != tracker.StatusUpToDate }

// Stack is the one-line description of how the behavior's tests are produced.
func (i Item) Stack() string { return i.Language + "/" + i.Framework + " via " + i.Harness }

// Build compares the configured behaviors against the tracker and reports the
// state of each one. only limits the result to the named behavior files; an
// empty list means every behavior.
func Build(cfg *config.Config, t *tracker.Tracker, only []string) ([]Item, error) {
	resolved, err := cfg.Resolve()
	if err != nil {
		return nil, err
	}
	filter := FilterSet(cfg, only)

	items := make([]Item, 0, len(resolved))
	for _, r := range resolved {
		if filter != nil && !filter[r.Source] {
			continue
		}
		srcHash, err := tracker.HashFile(r.AbsSource(cfg.Root))
		if err != nil {
			return nil, err
		}
		outHash, err := tracker.HashFile(r.AbsOutput(cfg.Root))
		if err != nil {
			return nil, err
		}

		it := Item{Resolved: r, SourceHash: srcHash, OutputHash: outHash}
		it.Status = Classify(t, r, srcHash, outHash)
		items = append(items, it)
	}
	return items, nil
}

// Classify determines why a behavior is or is not current.
//
// Order matters. A changed behavior outranks a hand-edited output: when the
// spec moved, the spec wins and regeneration is expected. Only when the spec is
// unchanged does a hand-edited output become the reason to hold back.
func Classify(t *tracker.Tracker, r config.Resolved, srcHash, outHash string) tracker.Status {
	entry, ok := t.Get(r.Source)
	if !ok {
		// Tests are already there for a behavior katana has no record of: an
		// older katana, a teammate's run whose tracker was not committed, or a
		// file written by hand. Generating would overwrite work katana never
		// made, and it cannot claim the spec changed since something it never
		// recorded, so the file is left alone until --force asks for it.
		if outHash != "" {
			return tracker.StatusOutputUntracked
		}
		return tracker.StatusNew
	}
	if entry.SourceHash != srcHash {
		return tracker.StatusBehaviorChanged
	}
	if entry.Output != r.Output || entry.Language != r.Language ||
		entry.Framework != r.Framework || entry.Harness != r.Harness {
		return tracker.StatusConfigChanged
	}
	if outHash == "" {
		return tracker.StatusOutputMissing
	}
	if entry.OutputHash != "" && entry.OutputHash != outHash {
		return tracker.StatusOutputModified
	}
	return tracker.StatusUpToDate
}

// FilterSet normalizes --file values to project-relative slash paths. It returns
// nil when no filter was given, meaning "every behavior".
func FilterSet(cfg *config.Config, only []string) map[string]bool {
	if len(only) == 0 {
		return nil
	}
	set := make(map[string]bool, len(only))
	for _, o := range only {
		set[NormalizePath(cfg.Root, o)] = true
	}
	return set
}

// NormalizePath renders a path the way the tracker and the config name one:
// relative to the project root, with forward slashes.
func NormalizePath(root, p string) string {
	if filepath.IsAbs(p) {
		if r, err := filepath.Rel(root, p); err == nil {
			return filepath.ToSlash(r)
		}
	}
	return filepath.ToSlash(filepath.Clean(p))
}
