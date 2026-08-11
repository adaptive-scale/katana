package tui

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/adaptive-scale/katana/internal/config"
	"github.com/adaptive-scale/katana/internal/history"
	"github.com/adaptive-scale/katana/internal/report"
	"github.com/adaptive-scale/katana/internal/results"
	"github.com/adaptive-scale/katana/internal/suite"
	"github.com/adaptive-scale/katana/internal/tracker"
	"github.com/adaptive-scale/katana/internal/ui"
)

// TestListShowsEveryBehavior is the view the UI opens on: every behavior, its
// state, and how its cases last did, without anything having to be run.
func TestListShowsEveryBehavior(t *testing.T) {
	m := newModel(fakeProject(t), ui.Plain(), 120, 30)

	frame := strings.Join(m.render(), "\n")
	for _, want := range []string{
		"behaviors/checkout.md", "tests/checkout_test.go",
		"behaviors/login.md",
		"up to date",
		// One of checkout's two cases failed in the recorded run.
		"1/2",
		"2 behavior(s)",
	} {
		if !strings.Contains(frame, want) {
			t.Errorf("the list is missing %q:\n%s", want, frame)
		}
	}
}

// TestDetailShowsCasesAndHistory is what opening a behavior is for: the cases it
// owns, how each one last fared, and a row per past run.
func TestDetailShowsCasesAndHistory(t *testing.T) {
	m := newModel(fakeProject(t), ui.Plain(), 120, 40)
	m.view = viewDetail

	frame := strings.Join(m.render(), "\n")
	for _, want := range []string{
		"behaviors/checkout.md → tests/checkout_test.go",
		"✓ TestCheckoutAdds",
		"✗ TestCheckoutTax",
		"history",
		// The bar of the recorded run, and its count.
		"1/2",
	} {
		if !strings.Contains(frame, want) {
			t.Errorf("the detail view is missing %q:\n%s", want, frame)
		}
	}
}

// TestRunUpdatesTheStatus is the promise the UI makes: running a behavior's
// tests from here records the run the way `katana run` does and refreshes what
// is on screen from it, so the status is never older than the last run.
func TestRunUpdatesTheStatus(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the stand-in test runner is a shell script")
	}
	cfg := fakeProject(t)
	m := newModel(cfg, ui.Plain(), 120, 30)

	// Before the run, the recorded results say one of checkout's cases failed.
	if got := ui.Strip(m.p.PassedText(m.tally(m.items[0]))); got != "1/2" {
		t.Fatalf("the fixture should start at 1/2, got %s", got)
	}

	target, ok := suite.TargetFor(m.tracker, m.items[0])
	if !ok {
		t.Fatal("the behavior has a tracker entry, so it has a target")
	}
	m.startRun(target, m.items[0].Source)
	select {
	case o := <-m.done:
		m.finishRun(o)
	case <-time.After(30 * time.Second):
		t.Fatal("the run never finished")
	}

	if m.running {
		t.Error("the run finished, so the model should not still say it is running")
	}
	if !strings.Contains(m.message, "passed") {
		t.Errorf("the run's outcome should be reported, got %q", m.message)
	}
	// The stand-in runner passes both cases, and the list must say so now.
	if got := ui.Strip(m.p.PassedText(m.tally(m.items[0]))); got != "2/2" {
		t.Errorf("the status was not updated from the run: %s", got)
	}

	// And it must be recorded where the rest of katana reads it from.
	res, err := results.Load(cfg.Root)
	if err != nil {
		t.Fatal(err)
	}
	if status, ok := res.Outcome("TestCheckoutTax"); !ok || status != report.StatusPass {
		t.Errorf("results.json was not updated: %v %v", status, ok)
	}
	// The other behavior's outcome is left standing: this run said nothing
	// about it, which is not the same as it having failed.
	if status, ok := res.Outcome("TestLoginRejects"); !ok || status != report.StatusPass {
		t.Errorf("a targeted run must not erase the other behaviors: %v %v", status, ok)
	}
	if res.Scope != "behaviors/checkout.md" {
		t.Errorf("the record should say which behavior ran, got %q", res.Scope)
	}

	h, err := history.Load(cfg.Root)
	if err != nil {
		t.Fatal(err)
	}
	if len(h.Runs) != 2 {
		t.Fatalf("the run should have been appended to the history, got %d run(s)", len(h.Runs))
	}
	last := h.Runs[len(h.Runs)-1]
	if last.Scope != "behaviors/checkout.md" || last.Pass != 2 {
		t.Errorf("the history row is wrong: %+v", last)
	}
	if b, ok := last.Find("behaviors/checkout.md"); !ok || b.Pass != 2 {
		t.Errorf("the behavior's own tally was not recorded: %+v", last.Behaviors)
	}
}

// TestRunNeedsGeneratedTests keeps the UI from running nothing: a behavior with
// no tests yet has nothing to run, and says so rather than running the suite.
func TestRunNeedsGeneratedTests(t *testing.T) {
	cfg := fakeProject(t)
	m := newModel(cfg, ui.Plain(), 120, 30)
	// The second behavior in the fixture has no tracker entry.
	m.sel = 1
	for i, it := range m.items {
		if _, ok := m.tracker.Get(it.Source); !ok {
			m.sel = i
		}
	}
	m.runSelected()
	if m.running {
		t.Error("a behavior that has never been generated has no tests to run")
	}
	if !strings.Contains(m.message, "katana generate") {
		t.Errorf("the message should say what to do about it, got %q", m.message)
	}
}

// TestKeysMoveAndOpen covers the navigation the whole UI rests on.
func TestKeysMoveAndOpen(t *testing.T) {
	m := newModel(fakeProject(t), ui.Plain(), 120, 30)

	m.handle(key{kind: keyDown})
	if m.sel != 1 {
		t.Errorf("down should move to the second behavior, got %d", m.sel)
	}
	m.handle(key{kind: keyDown})
	if m.sel != 1 {
		t.Errorf("the selection should stop at the end rather than wrap, got %d", m.sel)
	}
	m.handle(key{kind: keyRune, r: 'k'})
	if m.sel != 0 {
		t.Errorf("k should move back up, got %d", m.sel)
	}
	m.handle(key{kind: keyEnter})
	if m.view != viewDetail {
		t.Error("enter should open the selected behavior")
	}
	m.handle(key{kind: keyEsc})
	if m.view != viewList {
		t.Error("esc should go back to the list")
	}
	m.handle(key{kind: keyRune, r: '?'})
	if m.view != viewHelp {
		t.Error("? should show the keys")
	}
	m.handle(key{kind: keyRune, r: 'q'})
	if !m.quit {
		t.Error("q should leave")
	}
}

func TestDecodeKeys(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want []key
	}{
		{"letters", "jk", []key{{kind: keyRune, r: 'j'}, {kind: keyRune, r: 'k'}}},
		{"arrow up", "\x1b[A", []key{{kind: keyUp}}},
		{"arrow down", "\x1b[B", []key{{kind: keyDown}}},
		{"page down", "\x1b[6~", []key{{kind: keyPageDown}}},
		{"application arrow", "\x1bOA", []key{{kind: keyUp}}},
		{"bare escape", "\x1b", []key{{kind: keyEsc}}},
		{"enter", "\r", []key{{kind: keyEnter}}},
		{"ctrl-c", "\x03", []key{{kind: keyCtrlC}}},
		// A key katana has no use for is consumed whole rather than acted on as
		// the characters it is made of.
		{"function key", "\x1b[15~", []key{{kind: keyNone}}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := decode([]byte(c.in))
			if len(got) != len(c.want) {
				t.Fatalf("decode(%q) = %+v, want %+v", c.in, got, c.want)
			}
			for i := range got {
				if got[i] != c.want[i] {
					t.Errorf("decode(%q)[%d] = %+v, want %+v", c.in, i, got[i], c.want[i])
				}
			}
		})
	}
}

// TestLiveOutputKeepsTheTail covers what the output view reads while a suite is
// running: whole lines, without the escapes and carriage returns a runner uses
// to rewrite them.
func TestLiveOutputKeepsTheTail(t *testing.T) {
	l := newLiveOutput()
	l.Write([]byte("first\n\x1b[32msecond\x1b[0m\nrewritten\rthird"))

	got := l.snapshot()
	want := []string{"first", "second", "third"}
	if len(got) != len(want) {
		t.Fatalf("snapshot = %q, want %q", got, want)
	}
	for i := range got {
		if got[i] != want[i] {
			t.Errorf("line %d = %q, want %q", i, got[i], want[i])
		}
	}
}

// TestFramesFitTheTerminal keeps the drawing honest: a frame wider or taller
// than the terminal would scroll the screen and tear the view apart.
func TestFramesFitTheTerminal(t *testing.T) {
	for _, size := range [][2]int{{120, 30}, {80, 24}, {60, 12}} {
		m := newModel(fakeProject(t), ui.Plain(), size[0], size[1])
		for _, v := range []view{viewList, viewDetail, viewHelp, viewOutput} {
			m.view = v
			lines := m.render()
			if len(lines) > size[1] {
				t.Errorf("%dx%d view %d drew %d lines", size[0], size[1], v, len(lines))
			}
			for i, l := range lines {
				if ui.Width(l) > size[0] {
					t.Errorf("%dx%d view %d line %d is %d columns:\n%s",
						size[0], size[1], v, i, ui.Width(l), l)
				}
			}
		}
	}
}

// fakeProject writes a project with two behaviors: one generated, run and
// recorded, and one that has never been generated.
func fakeProject(t *testing.T) *config.Config {
	t.Helper()
	root := t.TempDir()

	mkdir(t, filepath.Join(root, "behaviors"))
	mkdir(t, filepath.Join(root, "tests"))

	// A stand-in for the test runner: it prints what `go test -v` prints for two
	// passing cases, so the parsers katana already has can read it.
	runner := filepath.Join(root, "run-tests.sh")
	write(t, runner, `#!/bin/sh
echo "=== RUN   TestCheckoutAdds"
echo "--- PASS: TestCheckoutAdds (0.00s)"
echo "=== RUN   TestCheckoutTax"
echo "--- PASS: TestCheckoutTax (0.00s)"
echo "PASS"
echo "ok  	example/tests	0.01s"
`)
	if err := os.Chmod(runner, 0o755); err != nil {
		t.Fatal(err)
	}

	write(t, filepath.Join(root, "katana.yaml"), `harness:
  name: claude
defaults:
  language: go
  framework: go-test
  output_dir: tests
behaviors:
  - path: behaviors/*.md
test:
  command: sh ./run-tests.sh
`)
	write(t, filepath.Join(root, "behaviors", "checkout.md"), "# checkout\n\nIt adds items.\n")
	write(t, filepath.Join(root, "behaviors", "login.md"), "# login\n\nIt rejects bad passwords.\n")
	write(t, filepath.Join(root, "tests", "checkout_test.go"), "package tests\n")

	cfg, err := config.Load(filepath.Join(root, "katana.yaml"))
	if err != nil {
		t.Fatal(err)
	}

	// checkout is generated and up to date; login has never been generated.
	tr, err := tracker.Load(root)
	if err != nil {
		t.Fatal(err)
	}
	tr.Record(tracker.Entry{
		Source:      "behaviors/checkout.md",
		SourceHash:  hash(t, filepath.Join(root, "behaviors", "checkout.md")),
		Output:      "tests/checkout_test.go",
		OutputHash:  hash(t, filepath.Join(root, "tests", "checkout_test.go")),
		Tests:       []string{"TestCheckoutAdds", "TestCheckoutTax"},
		Language:    "go",
		Framework:   "go-test",
		Harness:     "claude",
		GeneratedAt: time.Now().UTC().Add(-2 * time.Hour),
	})
	if err := tr.Save(); err != nil {
		t.Fatal(err)
	}

	ran := time.Now().Add(-30 * time.Minute)
	res := results.Record("sh ./run-tests.sh", ran, 1, true, []report.Case{
		{Name: "TestCheckoutAdds", Status: report.StatusPass},
		{Name: "TestCheckoutTax", Status: report.StatusFail},
		{Name: "TestLoginRejects", Status: report.StatusPass},
	})
	if err := res.Save(root); err != nil {
		t.Fatal(err)
	}
	if err := history.Record(root, history.Run{
		RanAt: ran, Command: "sh ./run-tests.sh", ExitCode: 1, PerCase: true,
		Pass: 2, Fail: 1,
		Behaviors: []history.Behavior{{Source: "behaviors/checkout.md", Pass: 1, Fail: 1}},
	}); err != nil {
		t.Fatal(err)
	}
	return cfg
}

func mkdir(t *testing.T, dir string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
}

func write(t *testing.T, path, body string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func hash(t *testing.T, path string) string {
	t.Helper()
	h, err := tracker.HashFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return h
}
