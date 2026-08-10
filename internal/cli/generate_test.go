package cli

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"

	"github.com/adaptive-scale/katana/internal/config"
	"github.com/adaptive-scale/katana/internal/tracker"
)

func TestResolveJobs(t *testing.T) {
	cases := []struct {
		name           string
		want           int
		items          int
		verboseDefault bool
		workers        int
		note           bool
	}{
		{name: "default over many behaviors", want: 4, items: 10, workers: 4},
		{name: "never more workers than work", want: 4, items: 2, workers: 2},
		{name: "explicit one is sequential", want: 1, items: 10, workers: 1},
		{name: "zero cannot stall the run", want: 0, items: 10, workers: 1},
		{name: "verbose narrates one at a time", want: 4, items: 10, verboseDefault: true, workers: 1, note: true},
		// Nothing was taken away, so there is nothing to explain.
		{name: "verbose with one behavior", want: 4, items: 1, verboseDefault: true, workers: 1},
		{name: "explicit jobs beat the verbose default", want: 3, items: 10, workers: 3},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			workers, note := resolveJobs(c.want, c.items, c.verboseDefault)
			if workers != c.workers {
				t.Errorf("resolveJobs(%d, %d, %v) = %d workers, want %d",
					c.want, c.items, c.verboseDefault, workers, c.workers)
			}
			if (note != "") != c.note {
				t.Errorf("note = %q, want note: %v", note, c.note)
			}
		})
	}
}

// TestProgressKeepsBlocksWhole is the point of the buffered mode: two agents
// finishing at once must not interleave their lines.
func TestProgressKeepsBlocksWhole(t *testing.T) {
	var out bytes.Buffer
	const behaviors = 8
	prog := newProgress(&out, &out, behaviors, false)

	var wg sync.WaitGroup
	for i := 0; i < behaviors; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			it := item{Resolved: config.Resolved{
				Source: fmt.Sprintf("behaviors/b%d.md", i),
				Output: fmt.Sprintf("tests/b%d_test.go", i),
			}}
			lg := prog.begin(it.task())
			for line := 0; line < 5; line++ {
				fmt.Fprintf(lg.out, "  b%d line %d\n", i, line)
			}
			prog.finish(it.task(), lg)
		}(i)
	}
	wg.Wait()

	// Every behavior's five lines must appear consecutively, and each block
	// header must carry the next completion number.
	counter := 0
	var block string
	for _, line := range strings.Split(strings.TrimRight(out.String(), "\n"), "\n") {
		switch {
		case strings.HasPrefix(line, "  start "):
			continue
		case strings.HasPrefix(line, "["):
			counter++
			if !strings.HasPrefix(line, fmt.Sprintf("[%d/%d] ", counter, behaviors)) {
				t.Fatalf("block %d has header %q", counter, line)
			}
			block = strings.Fields(strings.TrimPrefix(line, fmt.Sprintf("[%d/%d] ", counter, behaviors)))[0]
			block = strings.TrimSuffix(strings.TrimPrefix(block, "behaviors/"), ".md")
		default:
			if want := "  " + block + " line "; !strings.HasPrefix(line, want) {
				t.Fatalf("line %q does not belong to the block for %q", line, block)
			}
		}
	}
	if counter != behaviors {
		t.Errorf("printed %d blocks, want %d", counter, behaviors)
	}
}

// TestProgressLiveStreams checks that a one-worker run still narrates as it
// goes: the header first, then the output itself, unbuffered.
func TestProgressLiveStreams(t *testing.T) {
	var out, errOut bytes.Buffer
	prog := newProgress(&out, &errOut, 1, true)

	it := item{
		Resolved: config.Resolved{Source: "behaviors/cart.md", Output: "tests/cart_test.go"},
		Status:   tracker.StatusNew,
	}
	lg := prog.begin(it.task())
	if lg.buffered() {
		t.Fatal("a single worker should not buffer its output")
	}
	if got := out.String(); got != "[1/1] behaviors/cart.md → tests/cart_test.go (new)\n" {
		t.Fatalf("header not printed before the work started, got %q", got)
	}

	fmt.Fprint(lg.out, "  running…\n")
	fmt.Fprint(lg.errOut, "  failed: nope\n")
	prog.finish(it.task(), lg)

	if got := out.String(); !strings.HasSuffix(got, "  running…\n") {
		t.Errorf("stdout = %q", got)
	}
	// Failures stay on stderr when there is nothing to interleave with.
	if got := errOut.String(); got != "  failed: nope\n" {
		t.Errorf("stderr = %q", got)
	}
}

// fakeHarness writes a stand-in agent CLI: it logs when it starts and stops,
// takes long enough to overlap with its siblings, and prints a fenced test file
// that katana recovers from stdout.
func fakeHarness(t *testing.T, dir string) string {
	t.Helper()
	path := filepath.Join(dir, "fake-harness.sh")
	script := "#!/bin/sh\n" +
		"cat > /dev/null\n" + // drain the prompt on stdin
		"echo start >> \"$PWD/harness.log\"\n" +
		"sleep 0.2\n" +
		"printf '%s\\n' '```go' 'package tests' '' 'func TestGenerated(t *testing.T) {}' '```'\n" +
		"echo end >> \"$PWD/harness.log\"\n"
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return path
}

func fakeProject(t *testing.T, behaviors int) string {
	t.Helper()
	root := t.TempDir()
	// Resolve symlinks so the harness's own $PWD matches the paths katana logs
	// against (/var vs /private/var on macOS).
	if real, err := filepath.EvalSymlinks(root); err == nil {
		root = real
	}
	if err := os.MkdirAll(filepath.Join(root, "behaviors"), 0o755); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < behaviors; i++ {
		name := filepath.Join(root, "behaviors", fmt.Sprintf("b%d.md", i))
		if err := os.WriteFile(name, []byte(fmt.Sprintf("# behavior %d\n\n- it works\n", i)), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	cfg := fmt.Sprintf(`version: 1
harness:
  name: fake
  command: %s
behaviors:
  - path: behaviors
`, fakeHarness(t, root))
	if err := os.WriteFile(filepath.Join(root, "katana.yaml"), []byte(cfg), 0o644); err != nil {
		t.Fatal(err)
	}
	return root
}

// TestGenerateRunsBehaviorsInParallel drives the real command against a stand-in
// harness: every behavior is generated and tracked, and more than one harness is
// in flight at a time.
func TestGenerateRunsBehaviorsInParallel(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the stand-in harness is a shell script")
	}
	const behaviors = 6
	root := fakeProject(t, behaviors)

	if err := runGenerate([]string{"--dir", root, "--jobs", "3"}); err != nil {
		t.Fatalf("generate: %v", err)
	}

	for i := 0; i < behaviors; i++ {
		out := filepath.Join(root, "tests", fmt.Sprintf("b%d_test.go", i))
		body, err := os.ReadFile(out)
		if err != nil {
			t.Fatalf("behavior %d produced no tests: %v", i, err)
		}
		if !strings.Contains(string(body), "func TestGenerated") {
			t.Errorf("%s = %q", out, body)
		}
	}

	tr, err := tracker.Load(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(tr.Entries) != behaviors {
		t.Errorf("tracker recorded %d entries, want %d", len(tr.Entries), behaviors)
	}

	// Two starts in a row mean a second harness began before the first was done.
	log, err := os.ReadFile(filepath.Join(root, "harness.log"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(log), "start\nstart\n") {
		t.Errorf("harnesses never overlapped, log was:\n%s", log)
	}

	// A second run has nothing left to do, whatever the worker count.
	if err := runGenerate([]string{"--dir", root, "--jobs", "3"}); err != nil {
		t.Fatalf("second generate: %v", err)
	}
	log2, err := os.ReadFile(filepath.Join(root, "harness.log"))
	if err != nil {
		t.Fatal(err)
	}
	if len(log2) != len(log) {
		t.Errorf("the second run invoked the harness again")
	}
}

// TestGenerateRecordsTestIndex checks the other half of what a successful
// generation writes down: not just that the behavior produced a file, but which
// test cases came out of it.
func TestGenerateRecordsTestIndex(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the stand-in harness is a shell script")
	}
	root := fakeProject(t, 1)

	if err := runGenerate([]string{"--dir", root}); err != nil {
		t.Fatalf("generate: %v", err)
	}

	tr, err := tracker.Load(root)
	if err != nil {
		t.Fatal(err)
	}
	e, ok := tr.Get("behaviors/b0.md")
	if !ok {
		t.Fatal("the behavior was not tracked")
	}
	if len(e.Tests) != 1 || e.Tests[0] != "TestGenerated" {
		t.Errorf("tests = %q, want [TestGenerated]", e.Tests)
	}
	if e.TestCount != len(e.Tests) {
		t.Errorf("test_count = %d, want %d", e.TestCount, len(e.Tests))
	}

	// The index is written to disk, not only held in memory: it is what a
	// teammate's checkout reads.
	body, err := os.ReadFile(tracker.Path(root))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(body), `"TestGenerated"`) {
		t.Errorf("tracker file has no index:\n%s", body)
	}
}

// TestGenerateStopsOnMissingHarness checks that a fault every behavior shares is
// reported once and stops the run, rather than failing each behavior in turn.
func TestGenerateStopsOnMissingHarness(t *testing.T) {
	root := fakeProject(t, 4)
	cfg := "version: 1\nharness:\n  name: fake\n  command: katana-no-such-agent\nbehaviors:\n  - path: behaviors\n"
	if err := os.WriteFile(filepath.Join(root, "katana.yaml"), []byte(cfg), 0o644); err != nil {
		t.Fatal(err)
	}

	err := runGenerate([]string{"--dir", root, "--jobs", "3"})
	if err == nil {
		t.Fatal("a missing harness executable should fail the run")
	}
	if !strings.Contains(err.Error(), "katana-no-such-agent") {
		t.Errorf("error should name the missing executable, got: %v", err)
	}
	// The failure is the harness, not the behaviors, so none are counted failed.
	if strings.Contains(err.Error(), "failed to generate") {
		t.Errorf("error should be the harness fault itself, got: %v", err)
	}
}
