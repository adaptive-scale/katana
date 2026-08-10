package cli

import (
	"path/filepath"

	"github.com/adaptive-scale/katana/internal/config"
	"github.com/adaptive-scale/katana/internal/tracker"
)

// item pairs a resolved behavior with why it does or does not need work.
type item struct {
	config.Resolved
	Status     tracker.Status
	SourceHash string
	OutputHash string
}

// plan compares the configured behaviors against the tracker and reports the
// state of each one.
func plan(cfg *config.Config, t *tracker.Tracker, only []string) ([]item, error) {
	resolved, err := cfg.Resolve()
	if err != nil {
		return nil, err
	}
	filter := filterSet(cfg, only)

	items := make([]item, 0, len(resolved))
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

		it := item{Resolved: r, SourceHash: srcHash, OutputHash: outHash}
		it.Status = classify(t, r, srcHash, outHash)
		items = append(items, it)
	}
	return items, nil
}

// classify determines why a behavior is or is not current.
//
// Order matters. A changed behavior outranks a hand-edited output: when the
// spec moved, the spec wins and regeneration is expected. Only when the spec is
// unchanged does a hand-edited output become the reason to hold back.
func classify(t *tracker.Tracker, r config.Resolved, srcHash, outHash string) tracker.Status {
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

// filterSet normalizes --file values to project-relative slash paths. It returns
// nil when no filter was given, meaning "every behavior".
func filterSet(cfg *config.Config, only []string) map[string]bool {
	if len(only) == 0 {
		return nil
	}
	set := make(map[string]bool, len(only))
	for _, o := range only {
		set[normalizePath(cfg.Root, o)] = true
	}
	return set
}

func normalizePath(root, p string) string {
	if filepath.IsAbs(p) {
		if r, err := filepath.Rel(root, p); err == nil {
			return filepath.ToSlash(r)
		}
	}
	return filepath.ToSlash(filepath.Clean(p))
}

// stringList collects a repeatable string flag.
type stringList []string

func (s *stringList) String() string { return "" }

func (s *stringList) Set(v string) error {
	*s = append(*s, v)
	return nil
}
