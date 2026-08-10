package cli

import (
	"testing"

	"github.com/adaptive-scale/katana/internal/config"
	"github.com/adaptive-scale/katana/internal/tracker"
)

func TestClassify(t *testing.T) {
	r := config.Resolved{
		Source:    "behaviors/checkout.md",
		Output:    "tests/checkout_test.go",
		Language:  "go",
		Framework: "go-test",
		Harness:   "claude",
	}
	recorded := tracker.Entry{
		Source:     r.Source,
		SourceHash: "src1",
		Output:     r.Output,
		OutputHash: "out1",
		Language:   r.Language,
		Framework:  r.Framework,
		Harness:    r.Harness,
	}

	cases := []struct {
		name     string
		entry    *tracker.Entry
		srcHash  string
		outHash  string
		resolved config.Resolved
		want     tracker.Status
	}{
		{"never generated", nil, "src1", "", r, tracker.StatusNew},
		// Tests katana has no record of writing are not overwritten by a run it
		// was never asked to force.
		{"untracked tests already there", nil, "src1", "out1", r, tracker.StatusOutputUntracked},
		{"nothing changed", &recorded, "src1", "out1", r, tracker.StatusUpToDate},
		{"behavior edited", &recorded, "src2", "out1", r, tracker.StatusBehaviorChanged},
		{"tests deleted", &recorded, "src1", "", r, tracker.StatusOutputMissing},
		{"tests hand-edited", &recorded, "src1", "out2", r, tracker.StatusOutputModified},
		{"harness switched", &recorded, "src1", "out1", withHarness(r, "codex"), tracker.StatusConfigChanged},
		{"language switched", &recorded, "src1", "out1", withLanguage(r, "python"), tracker.StatusConfigChanged},
		// When both the spec and the generated file moved, the spec is the
		// source of truth and regeneration should not be blocked as a hand edit.
		{"behavior edited and tests hand-edited", &recorded, "src2", "out2", r, tracker.StatusBehaviorChanged},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			tr, err := tracker.Load(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			if c.entry != nil {
				tr.Record(*c.entry)
			}
			if got := classify(tr, c.resolved, c.srcHash, c.outHash); got != c.want {
				t.Errorf("classify = %v, want %v", got, c.want)
			}
		})
	}
}

func withHarness(r config.Resolved, h string) config.Resolved  { r.Harness = h; return r }
func withLanguage(r config.Resolved, l string) config.Resolved { r.Language = l; return r }

func TestNormalizePath(t *testing.T) {
	root := "/project"
	cases := map[string]string{
		"behaviors/a.md":              "behaviors/a.md",
		"./behaviors/a.md":            "behaviors/a.md",
		"/project/behaviors/a.md":     "behaviors/a.md",
		"behaviors/../behaviors/a.md": "behaviors/a.md",
	}
	for in, want := range cases {
		if got := normalizePath(root, in); got != want {
			t.Errorf("normalizePath(%q) = %q, want %q", in, got, want)
		}
	}
}
