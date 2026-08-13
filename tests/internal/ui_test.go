package internal

import (
	"bytes"
	"errors"
	"io"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/adaptive-scale/katana/internal/coverage"
	"github.com/adaptive-scale/katana/internal/history"
	"github.com/adaptive-scale/katana/internal/report"
	"github.com/adaptive-scale/katana/internal/results"
	"github.com/adaptive-scale/katana/internal/tracker"
	"github.com/adaptive-scale/katana/internal/ui"
)

func TestUIColorModesTrimCaseAndRejectUnknownValues(t *testing.T) {
	for _, tc := range []struct {
		s    string
		want ui.Mode
	}{
		{"", ui.Auto}, {" AUTO ", ui.Auto}, {"force", ui.Always}, {"YES", ui.Always}, {"true", ui.Always},
		{"never", ui.Never}, {"NO", ui.Never}, {"false", ui.Never},
	} {
		if got, err := ui.ParseMode(tc.s); err != nil || got != tc.want {
			t.Errorf("ParseMode(%q) = %v, %v", tc.s, got, err)
		}
	}
	got, err := ui.ParseMode(" strange ")
	if got != ui.Auto || err == nil || err.Error() != `invalid colour mode " strange " (want auto, always or never)` {
		t.Fatalf("bad invalid mode result: %v, %v", got, err)
	}
}

func TestUIColorEnvironmentAndPrinterBoundaries(t *testing.T) {
	old := os.Getenv("NO_COLOR")
	oldForce := os.Getenv("CLICOLOR_FORCE")
	oldTerm := os.Getenv("TERM")
	defer func() {
		os.Setenv("NO_COLOR", old)
		os.Setenv("CLICOLOR_FORCE", oldForce)
		os.Setenv("TERM", oldTerm)
		ui.SetMode(ui.Auto)
	}()
	var b bytes.Buffer
	ui.SetMode(ui.Always)
	if !ui.For(&b).Enabled() {
		t.Error("always must color pipes")
	}
	ui.SetMode(ui.Never)
	if ui.For(&b).Enabled() {
		t.Error("never must not color")
	}
	ui.SetMode(ui.Auto)
	os.Setenv("NO_COLOR", "1")
	os.Setenv("CLICOLOR_FORCE", "1")
	if ui.For(&b).Enabled() {
		t.Error("NO_COLOR wins")
	}
	os.Setenv("NO_COLOR", "")
	os.Setenv("CLICOLOR_FORCE", "0")
	os.Setenv("TERM", "dumb")
	if ui.For(&b).Enabled() {
		t.Error("dumb terminal must not color")
	}
	os.Setenv("TERM", "xterm")
	os.Setenv("CLICOLOR_FORCE", "1")
	if !ui.For(&b).Enabled() {
		t.Error("force must color")
	}
	if ui.IsTerminal(&b) || ui.IsTerminal(struct{ io.Writer }{}) {
		t.Error("non-terminal detection failed")
	}
	if ui.For(&b).Paint("", ui.Red) != "" {
		t.Error("non-terminal and empty paint boundary failed")
	}
}

func TestUIColorTextUtilitiesHandleAnsiRunesPaddingAndTruncation(t *testing.T) {
	s := "\x1b[31m世界\x1b[0m!"
	if ui.Strip(s) != "世界!" || ui.Width(s) != 3 {
		t.Fatalf("strip/width failed")
	}
	if ui.Pad("x", 3) != "x  " || ui.Pad("xxxx", 2) != "xxxx" || ui.PadLeft("x", 3) != "  x" {
		t.Fatal("padding failed")
	}
	for _, tc := range []struct {
		s    string
		n    int
		want string
	}{{"abc", 0, ""}, {"abc", 3, "abc"}, {"abcdef", 1, "…"}, {"abcdef", 4, "abc…"}} {
		if got := ui.Truncate(tc.s, tc.n); got != tc.want {
			t.Errorf("truncate=%q want %q", got, tc.want)
		}
	}
}

func TestUIColorPaintJoinsStylesAndLeavesEmptyOrUnstyledTextAlone(t *testing.T) {
	ui.SetMode(ui.Always)
	defer ui.SetMode(ui.Auto)
	p := ui.For(&bytes.Buffer{})
	if got := p.Paint("x", ui.Bold, ui.Red); got != "\x1b[1;31mx\x1b[0m" {
		t.Fatalf("paint=%q", got)
	}
	if p.Paint("", ui.Red) != "" || p.Paint("x") != "x" || ui.Plain().Paint("x", ui.Red) != "x" {
		t.Fatal("paint boundaries")
	}
}

func TestUIColorAutomaticForceAndTerminalBoundaries(t *testing.T) {
	oldNo, oldForce, oldTerm := os.Getenv("NO_COLOR"), os.Getenv("CLICOLOR_FORCE"), os.Getenv("TERM")
	defer func() {
		os.Setenv("NO_COLOR", oldNo)
		os.Setenv("CLICOLOR_FORCE", oldForce)
		os.Setenv("TERM", oldTerm)
		ui.SetMode(ui.Auto)
	}()
	ui.SetMode(ui.Auto)
	var b bytes.Buffer
	os.Setenv("NO_COLOR", "")
	os.Setenv("CLICOLOR_FORCE", "yes")
	if !ui.For(&b).Enabled() {
		t.Fatal("nonzero force should enable color")
	}
	os.Setenv("CLICOLOR_FORCE", "0")
	os.Setenv("TERM", "xterm")
	if ui.For(&b).Enabled() {
		t.Fatal("a pipe is not a terminal")
	}
	os.Setenv("NO_COLOR", "1")
	if ui.For(&b).Enabled() {
		t.Fatal("NO_COLOR should disable color")
	}
}

func TestUIStatusMarksTalliesAndSparklines(t *testing.T) {
	p := ui.Plain()
	for _, tc := range []struct {
		s    tracker.Status
		want ui.Style
	}{{tracker.StatusUpToDate, ui.Green}, {tracker.StatusNew, ui.Cyan}, {tracker.StatusBehaviorChanged, ui.Yellow}, {tracker.StatusConfigChanged, ui.Yellow}, {tracker.StatusOutputMissing, ui.Red}, {tracker.StatusOutputModified, ui.Magenta}, {tracker.StatusOutputUntracked, ui.Magenta}} {
		if ui.StatusStyle(tc.s) != tc.want {
			t.Errorf("status style %v", tc.s)
		}
	}
	if p.CaseMark("", false) != "•" || p.CaseMark(report.StatusFail, true) != "✗" || p.CaseMark(report.StatusSkip, true) != "○" || p.CaseMark(report.StatusPass, true) != "✓" {
		t.Fatal("case marks")
	}
	if p.PassedText(results.Tally{}) != "-" || ui.Strip(p.PassedText(results.Tally{Pass: 2})) != "2/2" || ui.Strip(p.PassedText(results.Tally{Pass: 1, Unknown: 1})) != "1/2" || ui.Strip(p.PassedText(results.Tally{Pass: 1, Fail: 1})) != "1/2" {
		t.Fatal("tallies")
	}
	if ui.Cell(0) != "▁" || ui.Cell(-1) != "▁" || ui.Cell(1) != "█" || ui.Cell(.01) != "▂" {
		t.Fatal("spark cell boundaries")
	}
}

func TestUIUnknownStatusIsGreyAndKnownDisplayColorsFollowTheirOutcomes(t *testing.T) {
	if ui.StatusStyle(tracker.Status(99)) != ui.Grey {
		t.Fatal("unknown status")
	}
	ui.SetMode(ui.Always)
	defer ui.SetMode(ui.Auto)
	p := ui.For(&bytes.Buffer{})
	if !strings.Contains(p.CaseMark(report.StatusFail, true), "\x1b[31m") || !strings.Contains(p.CaseMark(report.StatusSkip, true), "\x1b[33m") || !strings.Contains(p.CaseMark(report.StatusPass, true), "\x1b[32m") || !strings.Contains(p.CaseMark("", false), "\x1b[90m") {
		t.Fatal("case mark colors")
	}
	if !strings.Contains(p.PassedText(results.Tally{Pass: 1, Unknown: 1}), "\x1b[33m") || !strings.Contains(p.PassedText(results.Tally{Pass: 1, Fail: 1}), "\x1b[31m") || !strings.Contains(p.PassedText(results.Tally{Pass: 2}), "\x1b[32m") {
		t.Fatal("tally colors")
	}
}

func TestUIHistorySparklinesUseOutcomeColorsAndBehaviorScope(t *testing.T) {
	p := ui.Plain()
	runs := []history.Run{{Pass: 2}, {Pass: 1, Fail: 1}, {Skip: 1}, {}}
	if got := p.RunSpark(runs); got != "██▁▁" {
		t.Errorf("run spark=%q", got)
	}
	if got := p.BehaviorSpark("a", []history.Run{{Behaviors: []history.Behavior{{Source: "a", Pass: 1}}}, {Behaviors: []history.Behavior{{Source: "b", Pass: 1}}}}); got != "█" {
		t.Errorf("behavior spark=%q", got)
	}
	ui.SetMode(ui.Always)
	defer ui.SetMode(ui.Auto)
	got := ui.For(&bytes.Buffer{}).RunSpark([]history.Run{{Pass: 1, Fail: 1}, {Skip: 1}, {Pass: 1}})
	if !strings.Contains(got, "\x1b[31m") || !strings.Contains(got, "\x1b[33m") || !strings.Contains(got, "\x1b[32m") {
		t.Errorf("outcome colors=%q", got)
	}
}

func TestUICoverageSparklineUsesMeasuredRatesAndThresholdColors(t *testing.T) {
	p := ui.Plain()
	runs := []coverage.Run{{Files: []coverage.File{{Statements: 10, Covered: 4}}}, {Files: []coverage.File{{Statements: 10, Covered: 5}}}, {Files: []coverage.File{{Statements: 10, Covered: 8}}}}
	if got := p.CoverageSpark(runs); got != "▄▅▇" {
		t.Fatalf("coverage spark=%q", got)
	}
	ui.SetMode(ui.Always)
	defer ui.SetMode(ui.Auto)
	got := ui.For(&bytes.Buffer{}).CoverageSpark(runs)
	if !strings.Contains(got, "\x1b[31m") || !strings.Contains(got, "\x1b[33m") || !strings.Contains(got, "\x1b[32m") {
		t.Fatalf("coverage colors=%q", got)
	}
}

func TestUIBarsClampRoundAndStackSegments(t *testing.T) {
	if ui.Bar(-1, 3) != "░░░" || ui.Bar(2, 3) != "███" || ui.Bar(.01, 3) != "█░░" || ui.Bar(.99, 3) != "██░" || ui.Bar(.5, 0) != "" {
		t.Fatal("bar boundaries")
	}
	if got := ui.StackedBar(ui.Plain(), 5, ui.Segment{N: 1}, ui.Segment{N: 9, Rune: '▓'}); ui.Width(got) != 5 || !strings.Contains(got, "▓") {
		t.Fatalf("stacked bar=%q", got)
	}
	if got := ui.StackedBar(ui.Plain(), 3); got != "░░░" {
		t.Fatalf("empty stack=%q", got)
	}
}

func TestUITableShapesAlignsHighlightsAndWritesUntilError(t *testing.T) {
	if len(ui.NewTable().Lines(ui.Plain(), 0)) != 0 {
		t.Fatal("empty table should have no lines")
	}
	table := ui.NewTable("H").Row("longer").Row().RightAlign(0).Highlight(0).MaxWidth(20)
	lines := table.Lines(ui.Plain(), 20)
	if len(lines) != 5 || !strings.Contains(lines[1], "H") {
		t.Fatal("table shape")
	}
	if ui.Width(lines[2]) != ui.Width(lines[3]) {
		t.Fatal("rows not aligned")
	}
	var buf bytes.Buffer
	if err := table.Render(&buf, ui.Plain()); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(buf.String(), "…") {
		t.Fatal("long cells should truncate")
	}
	if err := ui.NewTable("x").Row("y").Render(errWriter{}, ui.Plain()); !errors.Is(err, errStop) {
		t.Fatal("writer error not returned")
	}
}

func TestUITableUsesAllColumnsPadsRowsAndHonorsSelectionAndAlignment(t *testing.T) {
	table := ui.NewTable("A").Row("x", "long").Row("y", "z").RightAlign(1).Highlight(1)
	lines := table.Lines(ui.Plain(), 0)
	if len(lines) != 5 || !strings.Contains(lines[1], "A") || !strings.Contains(lines[2], "long") || !strings.Contains(lines[3], " y ") {
		t.Fatalf("table columns/rows=%q", lines)
	}
	if strings.Contains(lines[3], "\x1b[") {
		t.Fatal("plain selected table unexpectedly colored")
	}
	if ui.Width(lines[2]) != ui.Width(lines[3]) {
		t.Fatal("table rows have different widths")
	}
	if got := ui.NewTable().Row("abcdefghi").MaxWidth(12).Lines(ui.Plain(), 12); !strings.Contains(strings.Join(got, "\n"), "…") {
		t.Fatal("max width should truncate cells")
	}
}

var errStop = errors.New("stop")

type errWriter struct{}

func (errWriter) Write([]byte) (int, error) { return 0, errStop }

func TestUIAgeUsesRelativeUnitsAndDates(t *testing.T) {
	now := time.Now()
	if ui.Age(time.Time{}) != "-" || ui.Age(now.Add(-10*time.Second)) != "just now" || ui.Age(now.Add(-2*time.Hour)) != "2h ago" || ui.Age(now.Add(-8*24*time.Hour)) != now.Add(-8*24*time.Hour).Local().Format("2006-01-02") || ui.Age(now.Add(time.Hour)) != now.Add(time.Hour).Local().Format("2006-01-02") {
		t.Fatal("age formatting")
	}
}

func TestUITerminalWritersWithoutTerminalReturnZeroSize(t *testing.T) {
	var b bytes.Buffer
	if ui.TerminalWidth(&b) != 0 {
		t.Fatal("pipe width")
	}
	if w, h, ok := ui.TerminalSize(os.Stdout); ok || w != 0 || h != 0 {
		t.Fatal("non-terminal size")
	}
}
