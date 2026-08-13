package internal

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/adaptive-scale/katana/internal/config"
	"github.com/adaptive-scale/katana/internal/plan"
	"github.com/adaptive-scale/katana/internal/tracker"
)

func planResolved() config.Resolved {
	return config.Resolved{Source: "behaviors/cart.md", Output: "tests/cart_test.go", Language: "go", Framework: "go-test", Harness: "claude"}
}

func planEntry(r config.Resolved, sourceHash, outputHash string) tracker.Entry {
	return tracker.Entry{Source: r.Source, Output: r.Output, SourceHash: sourceHash, OutputHash: outputHash, Language: r.Language, Framework: r.Framework, Harness: r.Harness}
}

func TestPlanBuildIncludesEveryResolvedBehaviorWithoutSelection(t *testing.T) {
	root := t.TempDir()
	for _, name := range []string{"a.md", "b.md"} {
		p := filepath.Join(root, "behaviors", name)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(name), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	cfg := &config.Config{Root: root, Defaults: config.Defaults{Language: "go", Framework: "go-test", OutputDir: "tests", OutputTemplate: "{name}_test.go"}, Behaviors: []config.Behavior{{Path: "behaviors"}}}
	tr, err := tracker.Load(root)
	if err != nil {
		t.Fatal(err)
	}
	items, err := plan.Build(cfg, tr, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 2 || items[0].Source != "behaviors/a.md" || items[1].Source != "behaviors/b.md" {
		t.Fatalf("items = %+v", items)
	}
}

func TestPlanBuildSelectedPathsAreNormalizedAndDeduplicated(t *testing.T) {
	root := t.TempDir()
	source := filepath.Join(root, "behaviors", "cart.md")
	if err := os.MkdirAll(filepath.Dir(source), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(source, []byte("cart"), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg := &config.Config{Root: root, Defaults: config.Defaults{Language: "go", Framework: "go-test", OutputDir: "tests", OutputTemplate: "{name}_test.go"}, Behaviors: []config.Behavior{{Path: "behaviors/cart.md"}}}
	tr, _ := tracker.Load(root)
	items, err := plan.Build(cfg, tr, []string{"./behaviors/cart.md", source, "behaviors/cart.md"})
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 || items[0].Source != "behaviors/cart.md" {
		t.Fatalf("items = %+v", items)
	}
}

func TestPlanBuildReturnsResolutionErrorWithoutItems(t *testing.T) {
	cfg := &config.Config{Root: t.TempDir(), Behaviors: []config.Behavior{{Path: "missing.md"}}}
	tr, _ := tracker.Load(cfg.Root)
	items, err := plan.Build(cfg, tr, nil)
	if err == nil || items != nil {
		t.Fatalf("Build = items %v, err %v", items, err)
	}
}

func TestPlanBuildReturnsSourceHashErrorWithoutCompletedResults(t *testing.T) {
	cfg := &config.Config{Root: t.TempDir(), Behaviors: []config.Behavior{{Path: "behaviors/missing.md"}}}
	tr, _ := tracker.Load(cfg.Root)
	items, err := plan.Build(cfg, tr, nil)
	if err == nil || items != nil {
		t.Fatalf("Build = items %v, err %v", items, err)
	}
}

func TestPlanClassificationCoversTrackerStates(t *testing.T) {
	r := planResolved()
	src, out := "src", "out"
	cases := map[string]struct {
		entry  *tracker.Entry
		sh, oh string
		want   tracker.Status
	}{
		"missing tracker and output": {sh: src, want: tracker.StatusNew},
		"untracked output":           {sh: src, oh: out, want: tracker.StatusOutputUntracked},
		"source changed":             {entry: func() *tracker.Entry { e := planEntry(r, "old", out); return &e }(), sh: src, oh: out, want: tracker.StatusBehaviorChanged},
		"configuration changed":      {entry: func() *tracker.Entry { e := planEntry(r, src, out); e.Harness = "codex"; return &e }(), sh: src, oh: out, want: tracker.StatusConfigChanged},
		"output missing":             {entry: func() *tracker.Entry { e := planEntry(r, src, out); return &e }(), sh: src, want: tracker.StatusOutputMissing},
		"output modified":            {entry: func() *tracker.Entry { e := planEntry(r, src, out); return &e }(), sh: src, oh: "new", want: tracker.StatusOutputModified},
		"up to date":                 {entry: func() *tracker.Entry { e := planEntry(r, src, out); return &e }(), sh: src, oh: out, want: tracker.StatusUpToDate},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			x, _ := tracker.Load(t.TempDir())
			if tc.entry != nil {
				x.Record(*tc.entry)
			}
			if got := plan.Classify(x, r, tc.sh, tc.oh); got != tc.want {
				t.Fatalf("Classify = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestPlanItemStaleAndStackDescribeTheResolvedBehavior(t *testing.T) {
	i := plan.Item{Resolved: planResolved(), Status: tracker.StatusOutputMissing}
	if !i.Stale() || (plan.Item{Status: tracker.StatusUpToDate}).Stale() {
		t.Fatal("stale status classification incorrect")
	}
	if got := i.Stack(); got != "go/go-test via claude" {
		t.Fatalf("Stack = %q", got)
	}
}

func TestPlanFilterSetMapsEmptySelectionToNoFilterAndNormalizesPaths(t *testing.T) {
	cfg := &config.Config{Root: "/project"}
	if plan.FilterSet(cfg, nil) != nil {
		t.Fatal("nil selection should select everything")
	}
	got := plan.FilterSet(cfg, []string{"./behaviors/a.md", "behaviors/../behaviors/a.md", ""})
	want := map[string]bool{"behaviors/a.md": true, ".": true}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("FilterSet = %#v, want %#v", got, want)
	}
}

func TestPlanClassificationIgnoresOutputDifferencesWithoutStoredOutputHash(t *testing.T) {
	r := planResolved()
	tr, _ := tracker.Load(t.TempDir())
	tr.Record(planEntry(r, "src", ""))
	if got := plan.Classify(tr, r, "src", "current"); got != tracker.StatusUpToDate {
		t.Fatalf("Classify = %v, want up-to-date", got)
	}
}
