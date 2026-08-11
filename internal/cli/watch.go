package cli

import (
	"fmt"
	"io"
	"os"
	"sync"
	"time"

	"github.com/adaptive-scale/katana/internal/testindex"
)

// testPollInterval is how often a running generation's output file is re-read
// to see which test cases have landed in it. An agent writes a test file over
// seconds or minutes, so this is about answering "what is it writing?" promptly
// rather than about catching every intermediate state; the file is small and
// the read is cheap.
const testPollInterval = 400 * time.Millisecond

// testWatcher narrates test cases while the harness is still working.
//
// katana never sees the agent's reasoning, only the file it leaves behind — but
// that file is written as the agent goes, so re-reading it turns a silent wait
// into a running list of the cases that have arrived. Each name is reported
// once, in the order it appears in the file.
//
// Only what the harness itself wrote is reported. A regeneration starts from
// the tests already on disk, and echoing those back as soon as the watch begins
// would credit the harness with work it has not done.
type testWatcher struct {
	path     string
	language string
	before   string

	mu   sync.Mutex
	w    io.Writer
	seen map[string]bool

	done    chan struct{}
	stopped chan struct{}
}

// watchTests reports test cases to w as they appear in the file at path. The
// watcher must be stopped before anything else writes to w.
func watchTests(w io.Writer, path, language string) *testWatcher {
	before, _ := os.ReadFile(path)
	t := &testWatcher{
		path:     path,
		language: language,
		before:   string(before),
		w:        w,
		seen:     map[string]bool{},
		done:     make(chan struct{}),
		stopped:  make(chan struct{}),
	}
	go t.loop()
	return t
}

func (t *testWatcher) loop() {
	defer close(t.stopped)

	tick := time.NewTicker(testPollInterval)
	defer tick.Stop()
	for {
		select {
		case <-t.done:
			return
		case <-tick.C:
			t.sweep()
		}
	}
}

// stop ends the watch after one last look, so a test case written just before
// the harness exited is still reported as it happened rather than only in the
// summary that follows.
func (t *testWatcher) stop() {
	close(t.done)
	<-t.stopped
	t.sweep()
}

// sweep reports every test case in the file that has not been reported yet.
//
// A file caught mid-write costs nothing: an incomplete declaration matches
// nothing, and the next sweep picks it up. Names are only ever added, so a
// partial read cannot unsay something already narrated.
func (t *testWatcher) sweep() {
	body, err := os.ReadFile(t.path)
	if err != nil || string(body) == t.before {
		// Not written yet, or still exactly the tests that were there before the
		// harness started: nothing of the harness's own to report.
		return
	}

	t.mu.Lock()
	defer t.mu.Unlock()
	for _, name := range testindex.Names(string(body), t.language) {
		if t.seen[name] {
			continue
		}
		t.seen[name] = true
		fmt.Fprintf(t.w, "  test     %s\n", name)
	}
}

// announced reports whether name was narrated while it was being written, so
// the summary afterwards does not list it a second time. A nil watcher has
// announced nothing, which is what a non-verbose run wants.
func (t *testWatcher) announced(name string) bool {
	if t == nil {
		return false
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.seen[name]
}
