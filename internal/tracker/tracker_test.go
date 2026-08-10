package tracker

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestSaveAndReload(t *testing.T) {
	root := t.TempDir()
	tr, err := Load(root)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(tr.Entries) != 0 {
		t.Fatal("a fresh tracker should be empty")
	}

	tr.Record(Entry{
		Source:      "behaviors/checkout.md",
		SourceHash:  "abc",
		Output:      "tests/checkout_test.go",
		OutputHash:  "def",
		Tests:       []string{"TestAppliesDiscount", "TestRejectsExpiredCode"},
		Language:    "go",
		Framework:   "go-test",
		Harness:     "claude",
		GeneratedAt: time.Now().UTC(),
	})
	if err := tr.Save(); err != nil {
		t.Fatalf("Save: %v", err)
	}

	again, err := Load(root)
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	e, ok := again.Get("behaviors/checkout.md")
	if !ok {
		t.Fatal("entry did not survive the round trip")
	}
	if e.SourceHash != "abc" || e.Harness != "claude" {
		t.Errorf("entry corrupted: %+v", e)
	}
	if len(e.Tests) != 2 || e.Tests[0] != "TestAppliesDiscount" {
		t.Errorf("test index did not survive the round trip: %q", e.Tests)
	}
}

// TestRecordCountsTheIndex keeps the count honest: it is derived on the way in,
// so no caller can leave it disagreeing with the list beside it.
func TestRecordCountsTheIndex(t *testing.T) {
	tr, _ := Load(t.TempDir())

	tr.Record(Entry{Source: "a.md", Tests: []string{"TestOne", "TestTwo"}, TestCount: 99})
	if e, _ := tr.Get("a.md"); e.TestCount != 2 {
		t.Errorf("TestCount = %d, want 2", e.TestCount)
	}

	// Regenerating into fewer tests must shrink the index, not merge with it.
	tr.Record(Entry{Source: "a.md", Tests: []string{"TestOne"}})
	e, _ := tr.Get("a.md")
	if len(e.Tests) != 1 || e.TestCount != 1 {
		t.Errorf("entry = %q / %d, want one test", e.Tests, e.TestCount)
	}
}

// TestOlderTrackerLoads covers the upgrade path: a tracker written before the
// index existed must still load, with an empty index rather than an error.
func TestOlderTrackerLoads(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, ".katana")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	old := `{"version":1,"entries":{"a.md":{"source":"a.md","source_hash":"abc","output":"tests/a_test.go"}}}`
	if err := os.WriteFile(filepath.Join(dir, FileName), []byte(old), 0o644); err != nil {
		t.Fatal(err)
	}

	tr, err := Load(root)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	e, ok := tr.Get("a.md")
	if !ok {
		t.Fatal("entry was dropped")
	}
	if e.SourceHash != "abc" || len(e.Tests) != 0 {
		t.Errorf("entry = %+v, want the old fields and an empty index", e)
	}
}

func TestSaveIsAtomic(t *testing.T) {
	// A save must leave no scratch files behind, so an interrupted run cannot
	// be mistaken for a corrupt tracker.
	root := t.TempDir()
	tr, _ := Load(root)
	tr.Record(Entry{Source: "a.md", SourceHash: "1"})
	if err := tr.Save(); err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(filepath.Join(root, ".katana"))
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if e.Name() != FileName {
			t.Errorf("unexpected leftover file %q", e.Name())
		}
	}
}

func TestPruneDropsRemovedBehaviors(t *testing.T) {
	root := t.TempDir()
	tr, _ := Load(root)
	tr.Record(Entry{Source: "keep.md"})
	tr.Record(Entry{Source: "gone.md"})

	removed := tr.Prune(map[string]bool{"keep.md": true})
	if len(removed) != 1 || removed[0] != "gone.md" {
		t.Fatalf("Prune removed %v, want [gone.md]", removed)
	}
	if _, ok := tr.Get("keep.md"); !ok {
		t.Error("Prune dropped a configured behavior")
	}
}

func TestHashFileMissingIsEmptyNotError(t *testing.T) {
	h, err := HashFile(filepath.Join(t.TempDir(), "nope.go"))
	if err != nil {
		t.Fatalf("a missing file should not error: %v", err)
	}
	if h != "" {
		t.Errorf("hash = %q, want empty", h)
	}
}

func TestHashFileDetectsChange(t *testing.T) {
	p := filepath.Join(t.TempDir(), "f.md")
	if err := os.WriteFile(p, []byte("one"), 0o644); err != nil {
		t.Fatal(err)
	}
	first, _ := HashFile(p)
	if err := os.WriteFile(p, []byte("two"), 0o644); err != nil {
		t.Fatal(err)
	}
	second, _ := HashFile(p)
	if first == second || first == "" {
		t.Errorf("hashes should differ: %q vs %q", first, second)
	}
}

func TestVersionMismatchIsRejected(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, ".katana")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, FileName), []byte(`{"version":99,"entries":{}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(root); err == nil {
		t.Fatal("a future tracker version should be rejected, not silently reused")
	}
}

func TestNeedsGeneration(t *testing.T) {
	cases := map[Status]bool{
		StatusUpToDate:        false,
		StatusNew:             true,
		StatusBehaviorChanged: true,
		StatusOutputMissing:   true,
		StatusConfigChanged:   true,
		// Hand edits are preserved unless the caller forces regeneration.
		StatusOutputModified: false,
	}
	for s, want := range cases {
		if got := s.NeedsGeneration(); got != want {
			t.Errorf("%v.NeedsGeneration() = %v, want %v", s, got, want)
		}
	}
}
