package tui

import (
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"github.com/adaptive-scale/katana/internal/history"
	"github.com/adaptive-scale/katana/internal/plan"
	"github.com/adaptive-scale/katana/internal/tracker"
	"github.com/adaptive-scale/katana/internal/ui"
)

// spinnerFrames turns while a suite is running, which is the only thing on
// screen that says katana is still waiting on it rather than stuck.
var spinnerFrames = []string{"⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏"}

// render draws the frame for whichever view is in front.
func (m *model) render() []string {
	switch m.view {
	case viewDetail:
		return m.fit(m.renderDetail())
	case viewOutput:
		return m.fit(m.renderOutput())
	case viewHelp:
		return m.fit(m.renderHelp())
	default:
		return m.fit(m.renderList())
	}
}

// fit is the last word on what a frame may be: nothing wider than the terminal,
// which would wrap and push the rest of the view off the bottom, and nothing
// taller, which would scroll it. The keys always survive — they are the last
// line, and the way out of whatever the view is showing.
func (m *model) fit(lines []string) []string {
	if len(lines) > m.h && m.h > 0 {
		keys := lines[len(lines)-1]
		lines = append(lines[:m.h-1:m.h-1], keys)
	}
	for i, l := range lines {
		lines[i] = ui.Truncate(l, m.w)
	}
	return lines
}

// renderList is the behavior list: every behavior katana knows about, what
// state it is in, how its tests last did, and how they have been doing.
func (m *model) renderList() []string {
	p := m.p
	out := []string{m.titleBar(m.projectLine(), m.countsLine()), ""}

	if m.loadErr != nil {
		return append(out, p.Red("  "+m.loadErr.Error()), "", m.footer(
			"q", "quit", "r", "reload"))
	}
	if len(m.items) == 0 {
		return append(out,
			p.Dim("  no behaviors configured — write one under behaviors/ and run `katana generate`"),
			"", m.footer("q", "quit"))
	}

	// The list scrolls rather than the screen: the title and the keys stay put,
	// and only the rows move under them.
	body := max(m.h-11, 1)
	m.clampOffset(body)
	visible := m.items[m.offset:min(m.offset+body, len(m.items))]

	// A narrow terminal drops columns rather than squeezing every one of them
	// into six characters: a truncated path says less than no path at all.
	showTests, showWhen, showRecent := m.w >= 108, m.w >= 90, m.w >= 72
	head := []string{"STATUS", "BEHAVIOR"}
	if showTests {
		head = append(head, "TESTS")
	}
	counts := len(head) // where CASES and PASSED land, once TESTS is decided
	head = append(head, "CASES", "PASSED")
	if showRecent {
		head = append(head, "RECENT")
	}
	if showWhen {
		head = append(head, "GENERATED")
	}
	table := ui.NewTable(head...).
		RightAlign(counts, counts+1).
		Highlight(m.sel - m.offset).
		MaxWidth(m.w)
	for _, it := range visible {
		entry, mapped := m.tracker.Get(it.Source)
		cases, passed, when, recent := p.Dim("-"), p.Dim("-"), p.Dim("-"), ""
		if mapped {
			cases = fmt.Sprint(caseCount(entry))
			passed = p.PassedText(m.res.Tally(entry.Tests))
			when = ui.Age(entry.GeneratedAt)
			recent = p.BehaviorSpark(it.Source, m.hist.For(it.Source, 12))
		}
		row := []string{p.StatusText(it.Status), it.Source}
		if showTests {
			row = append(row, it.Output)
		}
		row = append(row, cases, passed)
		if showRecent {
			row = append(row, recent)
		}
		if showWhen {
			row = append(row, when)
		}
		table.Row(row...)
	}
	out = append(out, table.Lines(p, m.w)...)
	if len(m.items) > body {
		out = append(out, p.Dim(fmt.Sprintf("  showing %d–%d of %d",
			m.offset+1, m.offset+len(visible), len(m.items))))
	}

	out = append(out, "", m.historyLine(), m.messageLine())
	if m.w < 100 {
		// The hints are the documentation, so they are shortened rather than
		// cut off mid-word by a terminal that cannot hold all of them.
		return append(out, m.footer(
			"↑↓", "select", "⏎", "open", "r", "run", "a", "all", "?", "help", "q", "quit"))
	}
	return append(out, m.footer(
		"↑↓", "select", "enter", "open", "r", "run", "a", "run all",
		"o", "output", "u", "reload", "?", "help", "q", "quit"))
}

// renderDetail is one behavior in full: where it came from, where its tests
// went, how each case last did, and a bar per run of how it has been doing.
func (m *model) renderDetail() []string {
	p := m.p
	it, ok := m.current()
	if !ok {
		return m.renderList()
	}
	entry, mapped := m.tracker.Get(it.Source)

	lines := []string{m.titleBar(p.Bold(it.Source)+p.Dim(" → ")+it.Output, m.countsLine()), ""}
	lines = append(lines,
		field(p, "status", p.StatusText(it.Status)),
		field(p, "stack", it.Stack()),
	)
	if mapped {
		tally := m.res.Tally(entry.Tests)
		lines = append(lines,
			field(p, "generated", fmt.Sprintf("%s by %s", ui.Age(entry.GeneratedAt), entry.Harness)),
			field(p, "cases", fmt.Sprintf("%d mapped, %s passed in the last run",
				caseCount(entry), p.PassedText(tally))),
		)
	} else {
		lines = append(lines, field(p, "generated", p.Yellow("never — run `katana generate`")))
	}

	lines = append(lines, "", p.Bold("  cases"))
	switch {
	case !mapped || len(entry.Tests) == 0:
		lines = append(lines, p.Dim("    no test cases recorded for this behavior"))
	default:
		for _, name := range entry.Tests {
			status, known := m.res.Outcome(name)
			when := p.Dim("never run")
			if ranAt, ok := m.res.CaseRanAt(name); ok {
				when = p.Dim(ui.Age(ranAt))
			}
			lines = append(lines, fmt.Sprintf("    %s %s  %s",
				p.CaseMark(status, known), ui.Pad(name, 40), when))
		}
	}

	lines = append(lines, "", p.Bold("  history")+p.Dim("  — one row per run, newest first"))
	lines = append(lines, m.behaviorHistory(it)...)

	lines = m.scrolled(lines, 1)
	return append(lines, m.messageLine(), m.footer(
		"r", "run these tests", "a", "run all", "o", "output", "esc", "back", "q", "quit"))
}

// behaviorHistory draws a bar per run of how this behavior's cases did in it.
func (m *model) behaviorHistory(it plan.Item) []string {
	p := m.p
	runs := m.hist.For(it.Source, 12)
	if len(runs) == 0 {
		return []string{p.Dim("    nothing recorded yet — the chart fills in as `katana run` is used")}
	}

	width := clamp(m.w-46, 10, 40)
	var out []string
	// Newest first: the run a reader cares about most is the one they can see
	// without counting along the column.
	for i := len(runs) - 1; i >= 0; i-- {
		r := runs[i]
		b, _ := r.Find(it.Source)
		verdict := p.Green("✓")
		if b.Fail > 0 {
			verdict = p.Red("✗")
		} else if b.Known() == 0 {
			verdict = p.Dim("•")
		}
		out = append(out, fmt.Sprintf("    %s %s  %s  %s %s",
			ui.Pad(ui.Age(r.RanAt), 12),
			p.HistoryBar(b.Pass, b.Fail, b.Skip, width),
			ui.PadLeft(fmt.Sprintf("%d/%d", b.Pass, b.Total()), 7),
			verdict,
			p.Dim(runNote(r))))
	}
	return out
}

// runNote says what kind of run a row is, when it is worth saying: a run of one
// behavior is not a verdict on the rest of the suite.
func runNote(r history.Run) string {
	if r.Scope != "" {
		return "this behavior only"
	}
	if r.Millis > 0 {
		return (time.Duration(r.Millis) * time.Millisecond).Round(10 * time.Millisecond).String()
	}
	return ""
}

// renderOutput is the running suite, or the one that just finished: what katana
// ran, how long it has been going, and the tail of what the runner has printed.
func (m *model) renderOutput() []string {
	p := m.p
	title := p.Bold("running") + " " + m.runLabel
	right := ""
	switch {
	case m.running:
		elapsed := time.Since(m.runStart).Round(time.Second)
		title = fmt.Sprintf("%s %s %s", p.Cyan(spinnerFrames[m.spinner%len(spinnerFrames)]),
			p.Bold("running"), m.runLabel)
		right = p.Dim(elapsed.String())
	case m.lastErr != nil:
		title = p.Red("could not run ") + m.runLabel
	case m.last != nil:
		title = p.Bold("ran ") + m.runLabel
		right = m.verdict(m.last.ExitCode) + p.Dim(" · "+m.last.Duration.Round(time.Millisecond).String())
	}

	lines := []string{m.titleBar(title, right), ""}
	if m.last != nil {
		lines = append(lines, p.Dim("  "+m.last.Command), "")
	} else if m.running {
		lines = append(lines, p.Dim("  "+m.cfg.Test.Command), "")
	}

	var body []string
	if m.live != nil {
		body = m.live.snapshot()
	}
	if len(body) == 0 {
		body = []string{p.Dim("    (no output yet)")}
	}

	// A run in flight follows the output; once it is finished the view holds
	// still, so what happened can be read rather than chased.
	room := m.h - len(lines) - 2
	if m.running {
		m.scroll = max(len(body)-room, 0)
	}
	lines = append(lines, window(indent(body, "  "), m.scroll, room)...)

	lines = append(lines, m.messageLine())
	if m.running {
		return append(lines, m.footer("x", "stop", "↑↓", "scroll", "esc", "back"))
	}
	return append(lines, m.footer("↑↓", "scroll", "r", "run again", "esc", "back", "q", "quit"))
}

func (m *model) renderHelp() []string {
	p := m.p
	rows := [][2]string{
		{"↑ ↓ / j k", "move through the behaviors"},
		{"enter / →", "open the selected behavior"},
		{"esc / ←", "back to the list"},
		{"r", "run the selected behavior's tests (the whole suite from the list header)"},
		{"a", "run the whole suite"},
		{"o", "show the output of the last run"},
		{"x", "stop a run in flight"},
		{"u", "reload the tracker, results and history from disk"},
		{"g / G", "first / last behavior"},
		{"?", "this help"},
		{"q / ctrl-c", "quit"},
	}
	lines := []string{m.titleBar(p.Bold("keys"), ""), ""}
	for _, r := range rows {
		lines = append(lines, fmt.Sprintf("  %s  %s", p.Cyan(ui.Pad(r[0], 12)), r[1]))
	}
	lines = append(lines, "",
		p.Dim("  Running from here records the run exactly as `katana run` does: the"),
		p.Dim("  results file, the history behind the charts, and this list are all"),
		p.Dim("  updated the moment a run finishes."))
	return append(lines, "", m.footer("esc", "back", "q", "quit"))
}

// --- pieces ----------------------------------------------------------------

func (m *model) projectLine() string {
	return m.p.Bold("katana") + "  " + m.p.Cyan(filepath.Base(m.cfg.Root))
}

// countsLine is the state of the project in one phrase, for the top right. A
// narrow terminal gets the same facts in fewer words rather than losing the
// line: the counts are what a reader glances up at.
func (m *model) countsLine() string {
	p := m.p
	narrow := m.w < 100

	stale := 0
	for _, it := range m.items {
		if it.Stale() {
			stale++
		}
	}
	s := fmt.Sprintf("%d behavior(s)", len(m.items))
	if narrow {
		s = fmt.Sprintf("%d", len(m.items))
	}
	switch {
	case stale > 0 && narrow:
		s += " · " + p.Yellow(fmt.Sprintf("%d stale", stale))
	case stale > 0:
		s += " · " + p.Yellow(fmt.Sprintf("%d out of date", stale))
	case len(m.items) > 0 && !narrow:
		s += " · " + p.Green("all up to date")
	case len(m.items) > 0:
		s += " · " + p.Green("current")
	}

	if !m.res.Recorded() {
		return s
	}
	c := m.res.Counts()
	if narrow {
		mark := p.Green("✓")
		if !m.res.OK() {
			mark = p.Red("✗")
		}
		s += " · " + mark + " " + ui.Age(m.res.RanAt)
	} else {
		s += " · last run " + ui.Age(m.res.RanAt) + " " + m.verdict(m.res.ExitCode)
	}
	if m.res.PerCase {
		s += p.Dim(fmt.Sprintf(" %d/%d", c.Pass, c.Total()))
	}
	return s
}

func (m *model) verdict(exit int) string {
	if exit == 0 {
		return m.p.Green("passed")
	}
	return m.p.Red(fmt.Sprintf("failed (exit %d)", exit))
}

// historyLine is the whole suite's recent runs, as one column per run.
func (m *model) historyLine() string {
	p := m.p
	runs := m.hist.Recent(m.w / 4)
	if len(runs) == 0 {
		return p.Dim("  history   nothing recorded yet — run the suite to start the chart")
	}
	// The chart holds as many runs as the terminal is wide; the count beside it
	// is every run recorded, so a project that has run a thousand times does not
	// read as one that has run twenty.
	note := fmt.Sprintf("%d run(s), oldest %s", len(runs), ui.Age(runs[0].RanAt))
	if total := m.hist.Totals.Runs; total > len(runs) {
		note = fmt.Sprintf("%d of %d run(s), oldest shown %s", len(runs), total, ui.Age(runs[0].RanAt))
	}
	return fmt.Sprintf("  %s %s  %s", p.Dim("history  "), p.RunSpark(runs), p.Dim(note))
}

// messageLine is what just happened: the result of the last run, or what went
// wrong reading the project.
func (m *model) messageLine() string {
	if m.message == "" {
		return ""
	}
	return "  " + ui.Truncate(m.message, max(m.w-2, 0))
}

// titleBar puts the two halves of a heading at either end of the line.
func (m *model) titleBar(left, right string) string {
	gap := m.w - ui.Width(left) - ui.Width(right) - 2
	if gap < 1 {
		return " " + ui.Truncate(left, max(m.w-2, 0))
	}
	return " " + left + strings.Repeat(" ", gap) + right + " "
}

// footer is the key hints, which are the only documentation a full-screen view
// gets to have.
func (m *model) footer(pairs ...string) string {
	p := m.p
	var parts []string
	for i := 0; i+1 < len(pairs); i += 2 {
		parts = append(parts, p.Cyan(pairs[i])+" "+p.Dim(pairs[i+1]))
	}
	return " " + strings.Join(parts, p.Dim(" · "))
}

func field(p ui.Printer, name, value string) string {
	return "  " + p.Dim(ui.Pad(name, 11)) + value
}

// scrolled applies the view's scroll offset to a body of lines, keeping the
// first keep lines pinned as a heading.
func (m *model) scrolled(lines []string, keep int) []string {
	room := m.h - 2 - keep
	if len(lines)-keep <= room || room < 1 {
		m.scroll = 0
		return lines
	}
	m.scroll = clamp(m.scroll, 0, len(lines)-keep-room)
	return append(lines[:keep], window(lines[keep:], m.scroll, room)...)
}

// window is the slice of lines a view has room for, from offset.
func window(lines []string, offset, room int) []string {
	if room <= 0 || len(lines) == 0 {
		return nil
	}
	offset = clamp(offset, 0, max(len(lines)-room, 0))
	return lines[offset:min(offset+room, len(lines))]
}

func indent(lines []string, with string) []string {
	out := make([]string, len(lines))
	for i, l := range lines {
		out[i] = with + l
	}
	return out
}

// clampOffset keeps the selected row inside the window the list has room for.
func (m *model) clampOffset(body int) {
	if m.sel < m.offset {
		m.offset = m.sel
	}
	if m.sel >= m.offset+body {
		m.offset = m.sel - body + 1
	}
	m.offset = clamp(m.offset, 0, max(len(m.items)-body, 0))
}

// caseCount is how many test cases a tracker entry maps to. The names win where
// there are any: a tracker written by an older katana can carry the count alone.
func caseCount(e tracker.Entry) int {
	if n := len(e.Tests); n > 0 {
		return n
	}
	return e.TestCount
}

func clamp(v, lo, hi int) int {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}
