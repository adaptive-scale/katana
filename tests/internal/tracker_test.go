// This file covers behaviors/internal/tracker.md: the state store behind
// katana's "what changed since last time" decision — loading and saving the
// record for a project, hashing behavior and generated files so they can be
// compared, and naming the reasons a behavior is or is not out of date.
//
// Every assertion goes through the tracker package's exported API: Path, Load,
// Get, Record, Prune, Save, HashBytes, HashFile and the Status reasons. The
// specification also describes what the saved file looks like — which fields
// are left out, how it is indented — so a few tests read the written file back
// as raw JSON, which is the only way that is observable from outside.
//
// Deciding a behavior's status is not done here: this store names the reasons
// and says which of them call for regeneration, while the comparison that
// picks one is made by the caller that holds the freshly hashed files.

package internal

import (
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/adaptive-scale/katana/internal/tracker"
)

// generatedAt is a fixed generation time, so a round trip through the file can
// be compared exactly.
var generatedAt = time.Date(2026, 3, 4, 5, 6, 7, 0, time.UTC)

// checkoutEntry is a fully populated record. Tests that care about one field
// take this and override it, so what a test is about stays visible.
func checkoutEntry() tracker.Entry {
	return tracker.Entry{
		Source:      "behaviors/checkout.md",
		SourceHash:  "behavior-hash",
		Output:      "tests/checkout_test.go",
		OutputHash:  "output-hash",
		Tests:       []string{"TestAppliesDiscount", "TestRejectsExpiredCode"},
		Language:    "go",
		Framework:   "go-test",
		Harness:     "claude",
		GeneratedAt: generatedAt,
	}
}

// newTrackerProject lays out a project whose .katana/tracker.json holds exactly
// contents, and returns the project root.
func newTrackerProject(t *testing.T, contents string) string {
	t.Helper()
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Dir(tracker.Path(root)), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(tracker.Path(root), []byte(contents), 0o644); err != nil {
		t.Fatal(err)
	}
	return root
}

// loadTracker loads a project's record, failing the test if it is rejected.
func loadTracker(t *testing.T, root string) *tracker.Tracker {
	t.Helper()
	tr, err := tracker.Load(root)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	return tr
}

// loadTrackerErr returns the error a project's record is rejected with, failing
// the test if it loads.
func loadTrackerErr(t *testing.T, root string) error {
	t.Helper()
	tr, err := tracker.Load(root)
	if err == nil {
		t.Fatalf("Load succeeded, want an error; tracker = %+v", tr)
	}
	if tr != nil {
		t.Errorf("a rejected tracker should not be handed back for use: %+v", tr)
	}
	return err
}

// saveTracker saves a record, failing the test if the save fails.
func saveTracker(t *testing.T, tr *tracker.Tracker) {
	t.Helper()
	if err := tr.Save(); err != nil {
		t.Fatalf("Save: %v", err)
	}
}

// trackerBytes reads the saved tracker file verbatim.
func trackerBytes(t *testing.T, root string) []byte {
	t.Helper()
	b, err := os.ReadFile(tracker.Path(root))
	if err != nil {
		t.Fatalf("reading the saved tracker: %v", err)
	}
	return b
}

// trackerFileExists reports whether a project has a tracker file at all.
func trackerFileExists(t *testing.T, root string) bool {
	t.Helper()
	_, err := os.Stat(tracker.Path(root))
	if err == nil {
		return true
	}
	if !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("stat %s: %v", tracker.Path(root), err)
	}
	return false
}

// savedEntryFields returns one saved entry as the raw fields it was written
// with, so a test can see which fields were left out of the file entirely.
func savedEntryFields(t *testing.T, root, source string) map[string]json.RawMessage {
	t.Helper()
	var file struct {
		Entries map[string]map[string]json.RawMessage `json:"entries"`
	}
	if err := json.Unmarshal(trackerBytes(t, root), &file); err != nil {
		t.Fatalf("the saved tracker is not valid JSON: %v", err)
	}
	fields, ok := file.Entries[source]
	if !ok {
		t.Fatalf("the saved tracker has no entry for %q", source)
	}
	return fields
}

// katanaDirNames lists what the .katana directory holds after a save.
func katanaDirNames(t *testing.T, root string) []string {
	t.Helper()
	entries, err := os.ReadDir(filepath.Dir(tracker.Path(root)))
	if err != nil {
		t.Fatalf("reading the .katana directory: %v", err)
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		names = append(names, e.Name())
	}
	return names
}

// recorded saves one entry into a fresh project and returns the project root.
func recorded(t *testing.T, e tracker.Entry) string {
	t.Helper()
	root := t.TempDir()
	tr := loadTracker(t, root)
	tr.Record(e)
	saveTracker(t, tr)
	return root
}

// writeFile writes a file for the hashing tests and returns its path.
func writeFile(t *testing.T, name, contents string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(p, []byte(contents), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

// Known SHA-256 vectors, written in the lowercase hexadecimal the
// specification calls for.
const (
	hashOfEmpty = "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855"
	hashOfHello = "2cf24dba5fb0a30e26e83b2ac5b9e29e1b161e5c1fa7425e73043362938b9824"
)

// --- Where the record lives ------------------------------------------------

func TestTheRecordIsTrackerJsonInsideTheKatanaDirectory(t *testing.T) {
	root := t.TempDir()

	if tracker.FileName != "tracker.json" {
		t.Errorf("FileName = %q, want tracker.json", tracker.FileName)
	}
	want := filepath.Join(root, ".katana", "tracker.json")
	if got := tracker.Path(root); got != want {
		t.Errorf("Path(%q) = %q, want %q", root, got, want)
	}

	// And that is where a save actually puts it.
	tr := loadTracker(t, root)
	tr.Record(checkoutEntry())
	saveTracker(t, tr)
	if !trackerFileExists(t, root) {
		t.Errorf("no record was written to %q", want)
	}
}

func TestAProjectWithNoTrackerFileLoadsAsAnEmptyRecord(t *testing.T) {
	root := t.TempDir()

	tr, err := tracker.Load(root)
	if err != nil {
		t.Fatalf("a first run should need no setup: %v", err)
	}
	if len(tr.Entries) != 0 {
		t.Errorf("entries = %+v, want none", tr.Entries)
	}
}

func TestTheSchemaVersionThisKatanaWritesIsOne(t *testing.T) {
	if tracker.Version != 1 {
		t.Errorf("Version = %d, want 1", tracker.Version)
	}

	root := recorded(t, checkoutEntry())
	var file struct {
		Version int `json:"version"`
	}
	if err := json.Unmarshal(trackerBytes(t, root), &file); err != nil {
		t.Fatalf("the saved tracker is not valid JSON: %v", err)
	}
	if file.Version != 1 {
		t.Errorf("saved version = %d, want 1", file.Version)
	}
}

// --- What is recorded per behavior ----------------------------------------

func TestRecordingTwiceForOneSourceReplacesTheEarlierRecord(t *testing.T) {
	tr := loadTracker(t, t.TempDir())

	tr.Record(checkoutEntry())
	second := checkoutEntry()
	second.SourceHash = "regenerated"
	tr.Record(second)

	if len(tr.Entries) != 1 {
		t.Fatalf("entries = %+v, want one record keyed by the source", tr.Entries)
	}
	e, ok := tr.Get("behaviors/checkout.md")
	if !ok {
		t.Fatal("the behavior is no longer recorded")
	}
	if e.SourceHash != "regenerated" {
		t.Errorf("source_hash = %q, want the later record's %q", e.SourceHash, "regenerated")
	}
}

func TestAnEntryHoldsEveryRecordedFactAboutAGeneration(t *testing.T) {
	root := recorded(t, checkoutEntry())

	e, ok := loadTracker(t, root).Get("behaviors/checkout.md")
	if !ok {
		t.Fatal("the entry did not survive the round trip")
	}
	want := checkoutEntry()
	if e.Source != want.Source {
		t.Errorf("Source = %q, want %q", e.Source, want.Source)
	}
	if e.SourceHash != want.SourceHash {
		t.Errorf("SourceHash = %q, want %q", e.SourceHash, want.SourceHash)
	}
	if e.Output != want.Output {
		t.Errorf("Output = %q, want %q", e.Output, want.Output)
	}
	if e.OutputHash != want.OutputHash {
		t.Errorf("OutputHash = %q, want %q", e.OutputHash, want.OutputHash)
	}
	if e.Language != want.Language {
		t.Errorf("Language = %q, want %q", e.Language, want.Language)
	}
	if e.Framework != want.Framework {
		t.Errorf("Framework = %q, want %q", e.Framework, want.Framework)
	}
	if e.Harness != want.Harness {
		t.Errorf("Harness = %q, want %q", e.Harness, want.Harness)
	}
	if !e.GeneratedAt.Equal(want.GeneratedAt) {
		t.Errorf("GeneratedAt = %v, want %v", e.GeneratedAt, want.GeneratedAt)
	}
}

func TestTheKatanaVersionIsRecordedWhenItIsSet(t *testing.T) {
	e := checkoutEntry()
	e.KatanaVersion = "v1.4.0"
	root := recorded(t, e)

	got, ok := loadTracker(t, root).Get(e.Source)
	if !ok {
		t.Fatal("the entry did not survive the round trip")
	}
	if got.KatanaVersion != "v1.4.0" {
		t.Errorf("KatanaVersion = %q, want v1.4.0", got.KatanaVersion)
	}
}

func TestAnEmptyKatanaVersionIsLeftOutOfTheSavedFile(t *testing.T) {
	e := checkoutEntry()
	e.KatanaVersion = ""
	root := recorded(t, e)

	if _, present := savedEntryFields(t, root, e.Source)["katana_version"]; present {
		t.Errorf("katana_version was written; an empty one should be left out entirely\n%s", trackerBytes(t, root))
	}
}

func TestTheTestIndexKeepsTheOrderTheCasesAppearInTheGeneratedFile(t *testing.T) {
	e := checkoutEntry()
	e.Tests = []string{"TestZebra", "TestApple", "TestMango"}
	root := recorded(t, e)

	got, ok := loadTracker(t, root).Get(e.Source)
	if !ok {
		t.Fatal("the entry did not survive the round trip")
	}
	if !reflect.DeepEqual(got.Tests, e.Tests) {
		t.Errorf("Tests = %q, want %q in the order they appear", got.Tests, e.Tests)
	}
}

func TestAnEmptyTestIndexLeavesTheHashesThatDecideStalenessUntouched(t *testing.T) {
	// An empty index means the cases could not be read out of the generated
	// file; it is never a statement about staleness, which is decided by the
	// behavior hash and the output hash alone. What the store owes a caller is
	// therefore both hashes, kept whole whether or not any case was indexed.
	e := checkoutEntry()
	e.Tests = nil
	root := recorded(t, e)

	got, ok := loadTracker(t, root).Get(e.Source)
	if !ok {
		t.Fatal("an entry with no indexed cases was dropped")
	}
	if len(got.Tests) != 0 {
		t.Errorf("Tests = %q, want none", got.Tests)
	}
	if got.SourceHash != e.SourceHash || got.OutputHash != e.OutputHash {
		t.Errorf("hashes = %q / %q, want %q / %q", got.SourceHash, got.OutputHash, e.SourceHash, e.OutputHash)
	}
}

func TestTheStoredCountIsTheLengthOfTheTestIndex(t *testing.T) {
	tr := loadTracker(t, t.TempDir())

	e := checkoutEntry()
	e.Tests = []string{"TestOne", "TestTwo", "TestThree"}
	// A count supplied by the caller is replaced with the true length.
	e.TestCount = 99
	tr.Record(e)

	got, ok := tr.Get(e.Source)
	if !ok {
		t.Fatal("the entry was not recorded")
	}
	if got.TestCount != 3 {
		t.Errorf("TestCount = %d, want 3, the length of the index", got.TestCount)
	}
}

func TestAnEmptyTestIndexLeavesBothTheIndexAndTheCountOutOfTheSavedFile(t *testing.T) {
	e := checkoutEntry()
	e.Tests = nil
	root := recorded(t, e)

	fields := savedEntryFields(t, root, e.Source)
	for _, name := range []string{"tests", "test_count"} {
		if _, present := fields[name]; present {
			t.Errorf("%s was written for an empty index; it should be left out entirely\n%s", name, trackerBytes(t, root))
		}
	}
}

// --- Reading and rejecting an existing record ------------------------------

func TestATrackerThatIsNotValidJsonIsRejected(t *testing.T) {
	root := newTrackerProject(t, "{ this is not json")

	err := loadTrackerErr(t, root)
	if prefix := "parsing " + tracker.Path(root) + ": "; !strings.HasPrefix(err.Error(), prefix) {
		t.Errorf("error = %q, want it to name the file: %q", err.Error(), prefix)
	}
	if !strings.HasSuffix(err.Error(), "(delete it to start over)") {
		t.Errorf("error = %q, want it to end with the advice %q", err.Error(), "(delete it to start over)")
	}
	var syntax *json.SyntaxError
	if !errors.As(err, &syntax) {
		t.Errorf("error = %v, want it to carry the parse failure", err)
	}
}

func TestATrackerWithAnyOtherVersionIsRejected(t *testing.T) {
	for _, found := range []int{0, 2, 99} {
		t.Run("version "+strconv.Itoa(found), func(t *testing.T) {
			root := newTrackerProject(t, `{"version":`+strconv.Itoa(found)+`,"entries":{}}`)

			err := loadTrackerErr(t, root)
			want := "tracker " + tracker.Path(root) + " has version " + strconv.Itoa(found) + ", this katana understands 1"
			if err.Error() != want {
				t.Errorf("error = %q, want %q", err.Error(), want)
			}
		})
	}
}

func TestATrackerWithNoEntriesSectionLoadsAsAnEmptyRecord(t *testing.T) {
	root := newTrackerProject(t, `{"version":1}`)

	tr, err := tracker.Load(root)
	if err != nil {
		t.Fatalf("a tracker with no entries section should load: %v", err)
	}
	if len(tr.Entries) != 0 {
		t.Errorf("entries = %+v, want none", tr.Entries)
	}
	// The record is usable: recording into it must not panic on a nil map.
	tr.Record(checkoutEntry())
	if _, ok := tr.Get("behaviors/checkout.md"); !ok {
		t.Error("a record loaded with no entries section could not be recorded into")
	}
}

func TestLookingUpABehaviorThatWasNeverRecordedReportsItAbsent(t *testing.T) {
	tr := loadTracker(t, t.TempDir())
	// A behavior recorded with nothing but its source is present-but-empty; the
	// one never recorded at all is a different answer.
	tr.Record(tracker.Entry{Source: "behaviors/recorded.md"})

	if _, ok := tr.Get("behaviors/never-generated.md"); ok {
		t.Error("an unrecorded behavior was reported as present")
	}
	if _, ok := tr.Get("behaviors/recorded.md"); !ok {
		t.Error("an empty record should still be reported as present")
	}
}

// --- Dropping records that no longer apply ---------------------------------

func TestPruningRemovesEntriesWhoseSourceIsNoLongerConfigured(t *testing.T) {
	tr := loadTracker(t, t.TempDir())
	tr.Record(tracker.Entry{Source: "behaviors/keep.md"})
	tr.Record(tracker.Entry{Source: "behaviors/gone.md"})

	tr.Prune(map[string]bool{"behaviors/keep.md": true})

	if _, ok := tr.Get("behaviors/gone.md"); ok {
		t.Error("an unconfigured behavior is still recorded")
	}
	if _, ok := tr.Get("behaviors/keep.md"); !ok {
		t.Error("a configured behavior was dropped")
	}
}

func TestPruningReportsTheRemovedSourcesAlphabetically(t *testing.T) {
	tr := loadTracker(t, t.TempDir())
	for _, src := range []string{"behaviors/zebra.md", "behaviors/apple.md", "behaviors/mango.md", "behaviors/keep.md"} {
		tr.Record(tracker.Entry{Source: src})
	}

	removed := tr.Prune(map[string]bool{"behaviors/keep.md": true})

	want := []string{"behaviors/apple.md", "behaviors/mango.md", "behaviors/zebra.md"}
	if !reflect.DeepEqual(removed, want) {
		t.Errorf("Prune removed %q, want %q in alphabetical order", removed, want)
	}
}

func TestPruningAgainstNoConfiguredSourcesRemovesEveryEntry(t *testing.T) {
	// "No configured sources" is read as a set holding nothing; an absent set is
	// the same set, so both forms are checked.
	for name, keep := range map[string]map[string]bool{
		"empty set": {},
		"no set":    nil,
	} {
		t.Run(name, func(t *testing.T) {
			tr := loadTracker(t, t.TempDir())
			tr.Record(tracker.Entry{Source: "behaviors/one.md"})
			tr.Record(tracker.Entry{Source: "behaviors/two.md"})

			removed := tr.Prune(keep)

			if want := []string{"behaviors/one.md", "behaviors/two.md"}; !reflect.DeepEqual(removed, want) {
				t.Errorf("Prune removed %q, want %q", removed, want)
			}
			if len(tr.Entries) != 0 {
				t.Errorf("entries = %+v, want none left", tr.Entries)
			}
		})
	}
}

func TestPruningThatRemovesNothingReportsAnEmptyListAndChangesNothing(t *testing.T) {
	root := newTrackerProject(t, `{"version":1,"entries":{"behaviors/one.md":{"source":"behaviors/one.md","source_hash":"h"}}}`)
	before := trackerBytes(t, root)
	tr := loadTracker(t, root)

	removed := tr.Prune(map[string]bool{"behaviors/one.md": true})

	if len(removed) != 0 {
		t.Errorf("Prune removed %q, want nothing", removed)
	}
	if _, ok := tr.Get("behaviors/one.md"); !ok {
		t.Error("a configured behavior was dropped")
	}
	// Nothing was dropped, so nothing is pending: the file is left alone.
	saveTracker(t, tr)
	if after := trackerBytes(t, root); !reflect.DeepEqual(before, after) {
		t.Errorf("the tracker file was rewritten after a prune that removed nothing:\n%s", after)
	}
}

// --- Saving ----------------------------------------------------------------

func TestSavingARecordWithNothingPendingWritesNoFile(t *testing.T) {
	root := t.TempDir()
	tr := loadTracker(t, root)

	if err := tr.Save(); err != nil {
		t.Fatalf("Save of an unchanged record should succeed: %v", err)
	}
	if trackerFileExists(t, root) {
		t.Error("an unchanged project had a tracker file created for it")
	}
}

func TestSavingAnUnchangedProjectDoesNotRewriteItsTrackerFile(t *testing.T) {
	// Written compactly and without a trailing newline: a rewrite would
	// reformat it, so identical bytes mean the file was genuinely left alone.
	root := newTrackerProject(t, `{"version":1,"entries":{"behaviors/one.md":{"source":"behaviors/one.md"}}}`)
	before := trackerBytes(t, root)

	saveTracker(t, loadTracker(t, root))

	if after := trackerBytes(t, root); !reflect.DeepEqual(before, after) {
		t.Errorf("the tracker file was rewritten:\ngot  %s\nwant %s", after, before)
	}
}

func TestRecordingAnEntryMarksTheRecordAsNeedingASave(t *testing.T) {
	root := t.TempDir()
	tr := loadTracker(t, root)

	tr.Record(checkoutEntry())
	saveTracker(t, tr)

	if !trackerFileExists(t, root) {
		t.Error("a recorded entry was not written out")
	}
}

func TestPruningAnEntryMarksTheRecordAsNeedingASave(t *testing.T) {
	root := newTrackerProject(t, `{"version":1,"entries":{"behaviors/gone.md":{"source":"behaviors/gone.md"}}}`)
	tr := loadTracker(t, root)

	if removed := tr.Prune(map[string]bool{}); len(removed) != 1 {
		t.Fatalf("Prune removed %q, want the one unconfigured behavior", removed)
	}
	saveTracker(t, tr)

	again := loadTracker(t, root)
	if _, ok := again.Get("behaviors/gone.md"); ok {
		t.Error("the pruned entry is still in the saved file")
	}
}

func TestASuccessfulSaveClearsThePendingChange(t *testing.T) {
	root := t.TempDir()
	tr := loadTracker(t, root)
	tr.Record(checkoutEntry())
	saveTracker(t, tr)

	// Removing the file makes a second write visible: if the mark were still
	// set, the repeated save would put the file back.
	if err := os.Remove(tracker.Path(root)); err != nil {
		t.Fatal(err)
	}
	saveTracker(t, tr)

	if trackerFileExists(t, root) {
		t.Error("an immediately repeated save wrote the file again")
	}
}

func TestSavingStampsTheRecordWithTheCurrentTimeInUtc(t *testing.T) {
	root := newTrackerProject(t, `{"version":1,"updated_at":"2000-01-01T00:00:00Z","entries":{}}`)
	tr := loadTracker(t, root)
	tr.Record(checkoutEntry())

	before := time.Now().Add(-time.Second)
	saveTracker(t, tr)
	after := time.Now().Add(time.Second)

	var file struct {
		UpdatedAt time.Time `json:"updated_at"`
	}
	if err := json.Unmarshal(trackerBytes(t, root), &file); err != nil {
		t.Fatalf("the saved tracker is not valid JSON: %v", err)
	}
	if file.UpdatedAt.Before(before) || file.UpdatedAt.After(after) {
		t.Errorf("updated_at = %v, want the current time, overwriting what was loaded", file.UpdatedAt)
	}
	// A UTC timestamp is written with a Z offset rather than a local one.
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(trackerBytes(t, root), &raw); err != nil {
		t.Fatal(err)
	}
	if stamp := string(raw["updated_at"]); !strings.HasSuffix(stamp, `Z"`) {
		t.Errorf("updated_at = %s, want it stamped in UTC", stamp)
	}
}

func TestSavingStampsTheSchemaVersion(t *testing.T) {
	// A loaded file can only ever hold version 1 — anything else is rejected —
	// so the stamp is observed by putting another version on the record in
	// memory and seeing the save overwrite it.
	root := t.TempDir()
	tr := loadTracker(t, root)
	tr.Version = 99
	tr.Record(checkoutEntry())

	saveTracker(t, tr)

	var file struct {
		Version int `json:"version"`
	}
	if err := json.Unmarshal(trackerBytes(t, root), &file); err != nil {
		t.Fatalf("the saved tracker is not valid JSON: %v", err)
	}
	if file.Version != 1 {
		t.Errorf("saved version = %d, want 1", file.Version)
	}
}

func TestSavingCreatesTheKatanaDirectory(t *testing.T) {
	root := t.TempDir()
	tr := loadTracker(t, root)
	tr.Record(checkoutEntry())

	saveTracker(t, tr)

	info, err := os.Stat(filepath.Join(root, ".katana"))
	if err != nil {
		t.Fatalf("the .katana directory was not created: %v", err)
	}
	if !info.IsDir() {
		t.Errorf(".katana is not a directory: %v", info.Mode())
	}
}

func TestTheSavedFileIsJsonIndentedWithTwoSpacesAndEndsWithANewline(t *testing.T) {
	root := recorded(t, checkoutEntry())
	text := string(trackerBytes(t, root))

	if !strings.HasSuffix(text, "\n") {
		t.Errorf("the saved tracker does not end with a newline:\n%s", text)
	}
	if strings.HasSuffix(text, "\n\n") {
		t.Errorf("the saved tracker ends with more than one newline:\n%s", text)
	}
	if strings.Contains(text, "\t") {
		t.Errorf("the saved tracker is indented with tabs:\n%s", text)
	}
	// One level in is two spaces, two levels in is four.
	if !strings.Contains(text, "\n  \"version\": 1,") {
		t.Errorf("top-level fields are not indented with two spaces:\n%s", text)
	}
	if !strings.Contains(text, "\n    \"behaviors/checkout.md\": {") {
		t.Errorf("nested fields are not indented two spaces per level:\n%s", text)
	}
}

func TestASuccessfulSaveLeavesNoTemporaryFileBehind(t *testing.T) {
	root := recorded(t, checkoutEntry())

	if got := katanaDirNames(t, root); !reflect.DeepEqual(got, []string{tracker.FileName}) {
		t.Errorf(".katana holds %q, want only %q", got, tracker.FileName)
	}
}

func TestAFailedSaveLeavesNoTemporaryFileBehind(t *testing.T) {
	root := t.TempDir()
	tr := loadTracker(t, root)
	tr.Record(checkoutEntry())
	// A non-empty directory sitting where the tracker file belongs: the rename
	// that completes a save cannot replace it, so the save fails after the
	// temporary file has already been written.
	blocked := filepath.Join(root, ".katana", tracker.FileName, "occupied")
	if err := os.MkdirAll(blocked, 0o755); err != nil {
		t.Fatal(err)
	}

	if err := tr.Save(); err == nil {
		t.Skip("this platform renamed over a non-empty directory; the save did not fail")
	}

	if got := katanaDirNames(t, root); !reflect.DeepEqual(got, []string{tracker.FileName}) {
		t.Errorf(".katana holds %q after a failed save, want no scratch file left behind", got)
	}
}

// --- Hashing behavior and output files -------------------------------------

func TestContentInMemoryIsHashedAsLowercaseHexSha256(t *testing.T) {
	if got := tracker.HashBytes([]byte("hello")); got != hashOfHello {
		t.Errorf("HashBytes(\"hello\") = %q, want %q", got, hashOfHello)
	}
	if got := tracker.HashBytes(nil); got != hashOfEmpty {
		t.Errorf("HashBytes(nil) = %q, want %q", got, hashOfEmpty)
	}
	if got := tracker.HashBytes([]byte("hello")); got != strings.ToLower(got) {
		t.Errorf("HashBytes = %q, want lowercase hexadecimal", got)
	}
}

func TestHashingAFileByPathGivesTheSameHashAsItsContent(t *testing.T) {
	p := writeFile(t, "checkout.md", "hello")

	got, err := tracker.HashFile(p)
	if err != nil {
		t.Fatalf("HashFile: %v", err)
	}
	if got != hashOfHello {
		t.Errorf("HashFile = %q, want %q, the hash of its content", got, hashOfHello)
	}
	if got != tracker.HashBytes([]byte("hello")) {
		t.Errorf("HashFile = %q, want the same as HashBytes %q", got, tracker.HashBytes([]byte("hello")))
	}
}

func TestAMissingFileHashesToTheEmptyStringWithNoError(t *testing.T) {
	got, err := tracker.HashFile(filepath.Join(t.TempDir(), "never-generated_test.go"))
	if err != nil {
		t.Fatalf("a missing file should not error, so a caller need not check first: %v", err)
	}
	if got != "" {
		t.Errorf("hash = %q, want the empty string", got)
	}
}

func TestAnEmptyFileIsDistinguishableFromAMissingOneByItsHash(t *testing.T) {
	p := writeFile(t, "empty_test.go", "")

	got, err := tracker.HashFile(p)
	if err != nil {
		t.Fatalf("HashFile: %v", err)
	}
	if got != hashOfEmpty {
		t.Errorf("hash = %q, want the SHA-256 of empty content %q", got, hashOfEmpty)
	}
	if got == "" {
		t.Error("an empty file hashed to the empty string, which is what a missing file means")
	}
}

func TestAFileThatCannotBeOpenedIsReportedAsAnError(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root can read a file with no permission bits set")
	}
	p := writeFile(t, "unreadable_test.go", "hello")
	if err := os.Chmod(p, 0o000); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(p, 0o644) })

	got, err := tracker.HashFile(p)
	if err == nil {
		t.Fatalf("HashFile = %q, want an error rather than a hash", got)
	}
	if !errors.Is(err, fs.ErrPermission) {
		t.Errorf("error = %v, want the underlying open failure", err)
	}
	if got != "" {
		t.Errorf("hash = %q, want no hash alongside the error", got)
	}
}

func TestAFileThatCannotBeReadIsReportedAsAnError(t *testing.T) {
	// A directory opens but cannot be read as a file, which is the read failure
	// the specification distinguishes from a missing file.
	got, err := tracker.HashFile(t.TempDir())
	if err == nil {
		t.Fatalf("HashFile = %q, want an error rather than a hash", got)
	}
	if got != "" {
		t.Errorf("hash = %q, want no hash alongside the error", got)
	}
}

// --- Why a behavior is or is not out of date -------------------------------

// reasons pairs each reason a behavior can be in with its fixed wording for
// command-line output.
var reasons = []struct {
	status tracker.Status
	want   string
}{
	{tracker.StatusUpToDate, "up to date"},
	{tracker.StatusNew, "new"},
	{tracker.StatusBehaviorChanged, "behavior changed"},
	{tracker.StatusOutputMissing, "output missing"},
	{tracker.StatusOutputModified, "output edited by hand"},
	{tracker.StatusConfigChanged, "config changed"},
	{tracker.StatusOutputUntracked, "output not tracked"},
}

func TestEachReasonHasItsFixedWordingForCommandLineOutput(t *testing.T) {
	for _, r := range reasons {
		t.Run(r.want, func(t *testing.T) {
			if got := r.status.String(); got != r.want {
				t.Errorf("String() = %q, want %q", got, r.want)
			}
		})
	}
}

func TestTheSevenReasonsAreEachDistinct(t *testing.T) {
	if len(reasons) != 7 {
		t.Fatalf("the specification names seven reasons, the table has %d", len(reasons))
	}
	seenStatus := map[tracker.Status]string{}
	seenWording := map[string]bool{}
	for _, r := range reasons {
		if other, dup := seenStatus[r.status]; dup {
			t.Errorf("%q and %q are the same reason", r.want, other)
		}
		if seenWording[r.want] {
			t.Errorf("%q is used for more than one reason", r.want)
		}
		seenStatus[r.status] = r.want
		seenWording[r.want] = true
	}
}

func TestAnyOtherReasonRendersAsUnknown(t *testing.T) {
	for _, s := range []tracker.Status{tracker.Status(7), tracker.Status(42), tracker.Status(-1)} {
		if got := s.String(); got != "unknown" {
			t.Errorf("Status(%d).String() = %q, want unknown", int(s), got)
		}
	}
}

func TestTheSettingsThatCountAsAConfigChangeAreEachRecorded(t *testing.T) {
	// Settings changing means the language, framework or harness differ from
	// what was recorded. The comparison is made by the caller holding the
	// current settings; what this store owes it is the three, kept apart so a
	// change in any one of them is visible.
	root := recorded(t, checkoutEntry())

	e, ok := loadTracker(t, root).Get("behaviors/checkout.md")
	if !ok {
		t.Fatal("the entry did not survive the round trip")
	}
	for _, c := range []struct{ name, got, want string }{
		{"language", e.Language, "go"},
		{"framework", e.Framework, "go-test"},
		{"harness", e.Harness, "claude"},
	} {
		if c.got != c.want {
			t.Errorf("%s = %q, want %q", c.name, c.got, c.want)
		}
	}
}

func TestRegenerationIsCalledForByDefault(t *testing.T) {
	for _, s := range []tracker.Status{
		tracker.StatusNew,
		tracker.StatusBehaviorChanged,
		tracker.StatusOutputMissing,
		tracker.StatusConfigChanged,
	} {
		t.Run(s.String(), func(t *testing.T) {
			if !s.NeedsGeneration() {
				t.Errorf("%v.NeedsGeneration() = false, want true", s)
			}
		})
	}
}

func TestAHandEditedOutputDoesNotCallForRegenerationByDefault(t *testing.T) {
	// Katana will not silently discard hand-written edits, so this case has to
	// be forced.
	if tracker.StatusOutputModified.NeedsGeneration() {
		t.Error("output edited by hand should not be regenerated without forcing")
	}
}

func TestATestFileKatanaNeverRecordedWritingDoesNotCallForRegenerationByDefault(t *testing.T) {
	if tracker.StatusOutputUntracked.NeedsGeneration() {
		t.Error("an untracked test file should be treated like a hand edit and require forcing")
	}
}

func TestAnUnchangedBehaviorDoesNotCallForRegeneration(t *testing.T) {
	if tracker.StatusUpToDate.NeedsGeneration() {
		t.Error("an up to date behavior should not be regenerated")
	}
}
