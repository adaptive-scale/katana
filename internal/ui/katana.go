package ui

import (
	"fmt"
	"time"

	"github.com/adaptive-scale/katana/internal/coverage"
	"github.com/adaptive-scale/katana/internal/history"
	"github.com/adaptive-scale/katana/internal/report"
	"github.com/adaptive-scale/katana/internal/results"
	"github.com/adaptive-scale/katana/internal/tracker"
)

// This file is what katana's own values look like on a terminal. It lives with
// the rest of the rendering because the command line and the terminal UI must
// agree: a behavior that is green in one is green in the other, and a mark that
// means "not run" means it in both.

// StatusStyle is the colour a behavior's condition is stated in. Green is the
// only state that needs nothing done, yellow is work the next `katana generate`
// will do, red is a generated file that is no longer there, and magenta is a
// file katana did not write and will not overwrite.
func StatusStyle(s tracker.Status) Style {
	switch s {
	case tracker.StatusUpToDate:
		return Green
	case tracker.StatusNew:
		return Cyan
	case tracker.StatusBehaviorChanged, tracker.StatusConfigChanged:
		return Yellow
	case tracker.StatusOutputMissing:
		return Red
	case tracker.StatusOutputModified, tracker.StatusOutputUntracked:
		return Magenta
	}
	return Grey
}

// StatusText colours a behavior's status.
func (p Printer) StatusText(s tracker.Status) string {
	return p.Paint(s.String(), StatusStyle(s))
}

// StatusCell colours a status padded to a fixed width, for a list that lines up
// without being a table. The padding goes on before the colour, so the escape
// sequences are never counted as part of the column.
func (p Printer) StatusCell(s tracker.Status, width int) string {
	return p.Paint(Pad(s.String(), width), StatusStyle(s))
}

// CaseMark is how one test case fared in the last run: a bullet when that run
// has nothing to say about it, so an unrun case never reads as a failed one.
func (p Printer) CaseMark(status results.Status, known bool) string {
	if !known {
		return p.Dim("•")
	}
	switch status {
	case report.StatusFail:
		return p.Red("✗")
	case report.StatusSkip:
		return p.Yellow("○")
	}
	return p.Green("✓")
}

// PassedText renders a tally as passed-out-of-mapped, coloured by what it says:
// green when every case the run covered passed, red when any failed, yellow
// when it passed as far as it went but did not cover everything, and dim when
// the run had nothing to say about the behavior at all.
func (p Printer) PassedText(t results.Tally) string {
	if t.Known() == 0 {
		return p.Dim("-")
	}
	s := fmt.Sprintf("%d/%d", t.Pass, t.Total())
	switch {
	case t.Fail > 0:
		return p.Red(s)
	case t.Pass == t.Total():
		return p.Green(s)
	}
	return p.Yellow(s)
}

// RunSpark draws one column per run, tallest for a run that passed entirely and
// coloured by whether anything failed in it. It is the smallest honest chart of
// how the suite has been doing: the shape shows the trend, the colour shows
// whether the latest run is one to worry about.
func (p Printer) RunSpark(runs []history.Run) string {
	out := ""
	for _, r := range runs {
		out += p.Paint(Cell(r.Rate()), outcomeStyle(r.Pass, r.Fail, r.Skip, r.Total() > 0))
	}
	return out
}

// BehaviorSpark is RunSpark for one behavior's share of each run.
func (p Printer) BehaviorSpark(source string, runs []history.Run) string {
	out := ""
	for _, r := range runs {
		b, ok := r.Find(source)
		if !ok {
			continue
		}
		rate, known := b.Rate()
		out += p.Paint(Cell(rate), outcomeStyle(b.Pass, b.Fail, b.Skip, known))
	}
	return out
}

// CoverageSpark draws total statement coverage for each observation. Its shape
// is the percentage trend and its colour uses the same conventional thresholds
// as the coverage table.
func (p Printer) CoverageSpark(runs []coverage.Run) string {
	out := ""
	for _, r := range runs {
		pct := r.Percent()
		style := Red
		if pct >= 80 {
			style = Green
		} else if pct >= 50 {
			style = Yellow
		}
		out += p.Paint(Cell(pct/100), style)
	}
	return out
}

// HistoryBar is one run drawn as a bar of its cases: passed, failed and skipped
// side by side, in the proportions the run had them.
func (p Printer) HistoryBar(pass, fail, skip, width int) string {
	return StackedBar(p, width,
		Segment{N: pass, Style: Green},
		Segment{N: fail, Style: Red, Rune: '▓'},
		Segment{N: skip, Style: Yellow, Rune: '▒'},
	)
}

func outcomeStyle(pass, fail, skip int, known bool) Style {
	switch {
	case !known:
		return Grey
	case fail > 0:
		return Red
	case skip > 0:
		return Yellow
	case pass > 0:
		return Green
	}
	return Grey
}

// Age renders a timestamp as how long ago it was, which is what a reader of the
// tracker actually wants to know. Anything older than a week is a date instead:
// "63d ago" takes longer to place than the day it happened.
func Age(ts time.Time) string {
	if ts.IsZero() {
		return "-"
	}
	d := time.Since(ts)
	switch {
	case d < 0:
		// A clock that moved backwards, or a tracker written on another machine.
		return ts.Local().Format("2006-01-02")
	case d < time.Minute:
		return "just now"
	case d < time.Hour:
		return fmt.Sprintf("%dm ago", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh ago", int(d.Hours()))
	case d < 7*24*time.Hour:
		return fmt.Sprintf("%dd ago", int(d.Hours()/24))
	default:
		return ts.Local().Format("2006-01-02")
	}
}
