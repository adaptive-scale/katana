package cli

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/adaptive-scale/katana/internal/generator"
)

// TestWatchTestsReportsCasesAsTheyLand is the point of the watch: a case is
// named while the harness is still working, not only in the summary afterwards.
func TestWatchTestsReportsCasesAsTheyLand(t *testing.T) {
	path := filepath.Join(t.TempDir(), "cart_test.go")
	var out bytes.Buffer
	w := &syncWriter{w: &out}

	watch := watchTests(w, path, "go")

	write(t, path, "package tests\n\nfunc TestAddToCart(t *testing.T) {}\n")
	watch.sweep() // stands in for the tick, so the test does not wait on one

	if got := out.String(); got != "  test     TestAddToCart\n" {
		t.Fatalf("first case not reported while the harness was running, got %q", got)
	}

	// The harness keeps going: the case already named is not named again.
	write(t, path, "package tests\n\nfunc TestAddToCart(t *testing.T) {}\n\nfunc TestRemoveFromCart(t *testing.T) {}\n")
	watch.stop()

	want := "  test     TestAddToCart\n  test     TestRemoveFromCart\n"
	if got := out.String(); got != want {
		t.Errorf("watch reported %q, want %q", got, want)
	}
	if !watch.announced("TestRemoveFromCart") {
		t.Error("a case the watch named should not be listed again in the summary")
	}
	if watch.announced("TestNeverWritten") {
		t.Error("the watch claims a case it never saw")
	}
}

// TestWatchTestsIgnoresTestsItDidNotWrite covers regeneration: the tests already
// on disk are not the harness's work, and reporting them the moment the watch
// starts would say the agent had produced them in no time at all.
func TestWatchTestsIgnoresTestsItDidNotWrite(t *testing.T) {
	path := filepath.Join(t.TempDir(), "cart_test.go")
	existing := "package tests\n\nfunc TestAlreadyHere(t *testing.T) {}\n"
	write(t, path, existing)

	var out bytes.Buffer
	watch := watchTests(&syncWriter{w: &out}, path, "go")
	watch.sweep()
	if got := out.String(); got != "" {
		t.Fatalf("watch reported tests that were there before it started: %q", got)
	}

	// Once the harness rewrites the file, everything in it is its own work —
	// including a case it chose to keep.
	write(t, path, existing+"\nfunc TestJustWritten(t *testing.T) {}\n")
	watch.stop()

	want := "  test     TestAlreadyHere\n  test     TestJustWritten\n"
	if got := out.String(); got != want {
		t.Errorf("watch reported %q, want %q", got, want)
	}
}

// TestWatchTestsSurvivesAMissingFile: a harness that prints instead of writing
// leaves nothing to watch until katana recovers the file from stdout, and a
// harness that fails leaves nothing at all.
func TestWatchTestsSurvivesAMissingFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "never_written_test.go")
	var out bytes.Buffer

	watch := watchTests(&syncWriter{w: &out}, path, "go")
	watch.sweep()
	watch.stop()

	if got := out.String(); got != "" {
		t.Errorf("watch reported %q for a file that was never written", got)
	}
}

// TestWatchTestsUnknownLanguage: katana indexes what it has rules for, and says
// nothing about the rest rather than guessing.
func TestWatchTestsUnknownLanguage(t *testing.T) {
	path := filepath.Join(t.TempDir(), "spec.brainfuck")
	var out bytes.Buffer

	watch := watchTests(&syncWriter{w: &out}, path, "brainfuck")
	write(t, path, "++++[>++++<-]\n")
	watch.stop()

	if got := out.String(); got != "" {
		t.Errorf("watch reported %q for a language it cannot index", got)
	}
}

// TestDescribeOutcomeListsWhatTheWatchMissed checks the two halves add up: every
// case is named exactly once, whether the watch caught it or the summary did.
func TestDescribeOutcomeListsWhatTheWatchMissed(t *testing.T) {
	path := filepath.Join(t.TempDir(), "cart_test.go")
	var out bytes.Buffer
	w := &syncWriter{w: &out}

	body := "package tests\n\nfunc TestSeen(t *testing.T) {}\n"
	watch := watchTests(w, path, "go")
	write(t, path, body)
	watch.stop()

	// The final file has a case the watch never got a look at.
	body += "\nfunc TestMissed(t *testing.T) {}\n"
	describeOutcome(w, "tests/cart_test.go", body, "go", &generator.Outcome{
		WroteFile: true,
		Bytes:     len(body),
	}, watch)

	got := out.String()
	if strings.Count(got, "TestSeen") != 1 {
		t.Errorf("TestSeen named %d times, want once:\n%s", strings.Count(got, "TestSeen"), got)
	}
	if !strings.Contains(got, "    • TestMissed\n") {
		t.Errorf("the summary dropped a case the watch never saw:\n%s", got)
	}
	if !strings.Contains(got, "2 test case(s)") {
		t.Errorf("the count should cover the whole index:\n%s", got)
	}
}

func write(t *testing.T, path, body string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}
