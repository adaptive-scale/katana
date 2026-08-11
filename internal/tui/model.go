package tui

import (
	"bytes"
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/adaptive-scale/katana/internal/config"
	"github.com/adaptive-scale/katana/internal/history"
	"github.com/adaptive-scale/katana/internal/plan"
	"github.com/adaptive-scale/katana/internal/results"
	"github.com/adaptive-scale/katana/internal/suite"
	"github.com/adaptive-scale/katana/internal/tracker"
	"github.com/adaptive-scale/katana/internal/ui"
)

// view is which screen is in front.
type view int

const (
	viewList view = iota
	viewDetail
	viewOutput
	viewHelp
)

// model is everything on screen and everything behind it. It is only ever
// touched from the event loop's own goroutine; a run in flight talks to it
// through channels and its own locked buffer.
type model struct {
	cfg *config.Config
	p   ui.Printer

	// The project as it was last read from disk. Every run reloads it, which is
	// what keeps the list honest after a test changes something.
	tracker *tracker.Tracker
	items   []plan.Item
	res     *results.Results
	hist    *history.History
	loadErr error

	view   view
	sel    int // the behavior the list is on
	offset int // the first behavior drawn, for a list taller than the screen
	scroll int // how far the detail or output view is scrolled

	// running is a run in flight, with the buffer its output is going into.
	running   bool
	runLabel  string
	runStart  time.Time
	live      *liveOutput
	cancelRun context.CancelFunc
	done      chan outcome

	// last is the result of the run that just finished, kept so its output can
	// be read after the fact.
	last     *suite.Result
	lastErr  error
	message  string
	messages int

	w, h    int
	spinner int
	quit    bool
}

// outcome is a finished run, on its way back to the event loop.
type outcome struct {
	res *suite.Result
	err error
}

func newModel(cfg *config.Config, p ui.Printer, w, h int) *model {
	m := &model{cfg: cfg, p: p, w: w, h: h, done: make(chan outcome, 1)}
	m.reload()
	return m
}

// reload re-reads the tracker, the last run's results and the history. It is
// called at startup and after every run, so what the UI shows is what `katana
// status` would say if it were asked at that moment.
func (m *model) reload() {
	m.loadErr = nil

	t, err := tracker.Load(m.cfg.Root)
	if err != nil {
		m.loadErr = err
		return
	}
	items, err := plan.Build(m.cfg, t, nil)
	if err != nil {
		m.loadErr = err
		return
	}
	res, err := results.Load(m.cfg.Root)
	if err != nil {
		// A results file that cannot be read costs the pass counts, not the
		// list, so it is reported in the message line and stepped over.
		m.message = "katana: " + err.Error()
		res = &results.Results{}
	}
	hist, err := history.Load(m.cfg.Root)
	if err != nil {
		m.message = "katana: " + err.Error()
		hist = &history.History{}
	}

	m.tracker, m.items, m.res, m.hist = t, items, res, hist
	if m.sel >= len(m.items) {
		m.sel = max(len(m.items)-1, 0)
	}
}

// current is the behavior the list is on.
func (m *model) current() (plan.Item, bool) {
	if m.sel < 0 || m.sel >= len(m.items) {
		return plan.Item{}, false
	}
	return m.items[m.sel], true
}

// tally is how one behavior's recorded cases stand.
func (m *model) tally(it plan.Item) results.Tally {
	entry, ok := m.tracker.Get(it.Source)
	if !ok {
		return results.Tally{}
	}
	return m.res.Tally(entry.Tests)
}

// startRun runs the tests for one behavior, or the whole suite when it is nil.
// The run happens on its own goroutine; the UI stays live, showing the output as
// the runner produces it.
func (m *model) startRun(target *suite.Target, label string) {
	if m.running {
		m.message = "a run is already going; press x to stop it"
		return
	}
	if strings.TrimSpace(m.cfg.Test.Command) == "" {
		m.message = "no test.command set in katana.yaml, so there is nothing to run"
		return
	}

	ctx, cancel := context.WithCancel(context.Background())
	m.cancelRun = cancel
	m.running = true
	m.runLabel = label
	m.runStart = time.Now()
	m.live = newLiveOutput()
	m.last, m.lastErr = nil, nil
	m.view = viewOutput
	m.scroll = 0
	m.message = ""

	req := suite.Request{
		Only:    target,
		PerCase: true,
		Stdout:  m.live,
		Stderr:  m.live,
	}
	go func() {
		res, err := suite.Run(ctx, m.cfg, req)
		m.done <- outcome{res: res, err: err}
	}()
}

// finishRun records what a run produced and reloads the project from it, so the
// list behind the output view is already up to date when the user goes back to
// it. This is the whole point of running from here: the status is never stale
// by more than the run that just happened.
func (m *model) finishRun(o outcome) {
	m.running = false
	m.cancelRun = nil
	m.last, m.lastErr = o.res, o.err

	if o.err != nil {
		m.message = "run failed: " + o.err.Error()
		return
	}
	if _, err := o.res.Record(m.cfg.Root, m.tracker); err != nil {
		m.message = "recording the run: " + err.Error()
	}
	m.reload()

	scope := "the suite"
	if o.res.Scope != "" {
		scope = o.res.Scope
	}
	verdict := "passed"
	if !o.res.OK() {
		verdict = fmt.Sprintf("failed (exit %d)", o.res.ExitCode)
	}
	pass, fail, skip := o.res.Counts()
	if o.res.Parsed {
		m.message = fmt.Sprintf("%s %s in %s — %d passed, %d failed, %d skipped",
			scope, verdict, o.res.Duration.Round(time.Millisecond), pass, fail, skip)
	} else {
		m.message = fmt.Sprintf("%s %s in %s (no per-case results in its output)",
			scope, verdict, o.res.Duration.Round(time.Millisecond))
	}
	for _, note := range o.res.Notes {
		m.message += " · " + note
	}
}

// stopRun cancels a run in flight. The runner is killed, and whatever it printed
// before it died stays on screen.
func (m *model) stopRun() {
	if !m.running || m.cancelRun == nil {
		return
	}
	m.cancelRun()
	m.message = "stopping the run…"
}

// runTarget builds the run for the selected behavior, if it has tests to run.
func (m *model) runSelected() {
	it, ok := m.current()
	if !ok {
		return
	}
	target, ok := suite.TargetFor(m.tracker, it)
	if !ok {
		m.message = it.Source + " has not been generated yet — run `katana generate` first"
		return
	}
	m.startRun(target, it.Source)
}

// liveOutput is where a running suite's output goes. The runner writes to it
// from its own goroutine while the event loop reads it to draw, so it is locked,
// and it keeps only the tail: the UI shows the end of the output, and holding a
// megabyte of it to draw twenty lines would be a waste.
type liveOutput struct {
	mu      sync.Mutex
	lines   []string
	partial bytes.Buffer
}

// liveLines is how much of a run's output is kept for the view.
const liveLines = 2000

func newLiveOutput() *liveOutput { return &liveOutput{} }

func (l *liveOutput) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()

	for _, b := range p {
		switch b {
		case '\n':
			l.push(l.partial.String())
			l.partial.Reset()
		case '\r':
			// A progress line rewriting itself: what came before it on the line
			// is not what the runner wants shown.
			l.partial.Reset()
		default:
			l.partial.WriteByte(b)
		}
	}
	return len(p), nil
}

func (l *liveOutput) push(line string) {
	l.lines = append(l.lines, ui.Strip(line))
	if n := len(l.lines) - liveLines; n > 0 {
		l.lines = append([]string(nil), l.lines[n:]...)
	}
}

// snapshot is the output so far, including the line still being written.
func (l *liveOutput) snapshot() []string {
	l.mu.Lock()
	defer l.mu.Unlock()

	out := make([]string, len(l.lines), len(l.lines)+1)
	copy(out, l.lines)
	if l.partial.Len() > 0 {
		out = append(out, ui.Strip(l.partial.String()))
	}
	return out
}
