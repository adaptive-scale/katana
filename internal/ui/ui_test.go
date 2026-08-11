package ui

import (
	"bytes"
	"strings"
	"testing"
)

// TestTableAlignsColouredCells is the reason this package exists: text/tabwriter
// pads by byte length, so a cell carrying colour is padded by the width of its
// escape sequences and the column below it bends. Every rendered line of a
// table must be the same visible width whether or not its cells are coloured.
func TestTableAlignsColouredCells(t *testing.T) {
	colour := Printer{on: true}
	tbl := NewTable("STATUS", "BEHAVIOR", "CASES").RightAlign(2)
	tbl.Row(colour.Green("up to date"), "behaviors/checkout.md", "3")
	tbl.Row(colour.Red("output missing"), "behaviors/login.md", "12")

	lines := tbl.Lines(colour, 0)
	if len(lines) != 6 {
		t.Fatalf("want a rule, a heading, a rule, two rows and a rule; got %d lines:\n%s",
			len(lines), strings.Join(lines, "\n"))
	}
	want := Width(lines[0])
	for i, l := range lines {
		if got := Width(l); got != want {
			t.Errorf("line %d is %d columns wide, want %d:\n%s", i, got, want, l)
		}
	}
	if !strings.Contains(lines[3], "\x1b[32m") {
		t.Errorf("the cell lost its colour: %q", lines[3])
	}
	// The count column is right-aligned, so 3 and 12 end in the same column.
	if !strings.Contains(Strip(lines[3]), " 3 │") || !strings.Contains(Strip(lines[4]), "12 │") {
		t.Errorf("counts are not right-aligned:\n%s\n%s", Strip(lines[3]), Strip(lines[4]))
	}
}

func TestTableRenderPlain(t *testing.T) {
	var buf bytes.Buffer
	tbl := NewTable("A", "B")
	tbl.Row("one", "two")
	if err := tbl.Render(&buf, Plain()); err != nil {
		t.Fatal(err)
	}
	out := buf.String()
	if strings.Contains(out, "\x1b[") {
		t.Errorf("a plain printer must not emit escapes:\n%q", out)
	}
	for _, want := range []string{"│ A   │ B   │", "│ one │ two │", "└─────┴─────┘"} {
		if !strings.Contains(out, want) {
			t.Errorf("output is missing %q:\n%s", want, out)
		}
	}
}

// TestTableMaxWidth covers the narrow terminal: the table gives way rather than
// wrapping, and it gives way in the widest column first.
func TestTableMaxWidth(t *testing.T) {
	tbl := NewTable("SHORT", "LONG").MaxWidth(30)
	tbl.Row("ok", strings.Repeat("x", 60))
	for _, l := range tbl.Lines(Plain(), 30) {
		if Width(l) > 30 {
			t.Errorf("line is %d columns, want at most 30:\n%s", Width(l), l)
		}
	}
	body := tbl.Lines(Plain(), 30)[3]
	if !strings.Contains(body, "…") {
		t.Errorf("the long cell should be truncated with an ellipsis:\n%s", body)
	}
	if !strings.Contains(body, "ok") {
		t.Errorf("the short cell should survive intact:\n%s", body)
	}
}

func TestWidthIgnoresColour(t *testing.T) {
	colour := Printer{on: true}
	if got := Width(colour.Green("abc")); got != 3 {
		t.Errorf("Width = %d, want 3", got)
	}
	if got := Width("✓ ok"); got != 4 {
		t.Errorf("Width = %d, want 4 (runes, not bytes)", got)
	}
}

func TestTruncateDropsColour(t *testing.T) {
	colour := Printer{on: true}
	got := Truncate(colour.Green("abcdefgh"), 4)
	if strings.Contains(got, "\x1b[") {
		t.Errorf("a truncated cell must not end mid-escape: %q", got)
	}
	if got != "abc…" {
		t.Errorf("Truncate = %q, want %q", got, "abc…")
	}
}

func TestPrinterOffIsIdentity(t *testing.T) {
	p := Plain()
	if got := p.Green("hello"); got != "hello" {
		t.Errorf("a disabled printer changed the text: %q", got)
	}
	if p.Enabled() {
		t.Error("Plain() must report colour as off")
	}
}

func TestParseMode(t *testing.T) {
	cases := map[string]Mode{"": Auto, "auto": Auto, "always": Always, "NEVER": Never}
	for in, want := range cases {
		got, err := ParseMode(in)
		if err != nil {
			t.Fatalf("ParseMode(%q): %v", in, err)
		}
		if got != want {
			t.Errorf("ParseMode(%q) = %v, want %v", in, got, want)
		}
	}
	if _, err := ParseMode("sometimes"); err == nil {
		t.Error("an unknown colour mode should be an error")
	}
}

// TestColorModeOverridesDetection keeps --color honest: the flag decides even
// when the destination is a buffer that could never render an escape.
func TestColorModeOverridesDetection(t *testing.T) {
	defer SetMode(Auto)

	SetMode(Never)
	if For(&bytes.Buffer{}).Enabled() {
		t.Error("--color=never must disable colour")
	}
	SetMode(Always)
	if !For(&bytes.Buffer{}).Enabled() {
		t.Error("--color=always must enable colour, pipe or not")
	}
	SetMode(Auto)
	if For(&bytes.Buffer{}).Enabled() {
		t.Error("a buffer is not a terminal, so auto must leave it plain")
	}
}

func TestBar(t *testing.T) {
	cases := []struct {
		fraction float64
		want     string
	}{
		{0, "░░░░"},
		{1, "████"},
		{0.5, "██░░"},
		// Neither end may be rounded away: some passed, and some did not.
		{0.01, "█░░░"},
		{0.99, "███░"},
	}
	for _, c := range cases {
		if got := Bar(c.fraction, 4); got != c.want {
			t.Errorf("Bar(%v, 4) = %q, want %q", c.fraction, got, c.want)
		}
	}
}

func TestSparkKeepsFailuresVisible(t *testing.T) {
	got := Spark([]float64{0, 0.5, 1})
	if []rune(got)[0] != '▁' || []rune(got)[2] != '█' {
		t.Errorf("Spark = %q, want it to run from the shortest block to the tallest", got)
	}
	if n := len([]rune(got)); n != 3 {
		t.Errorf("Spark drew %d cells, want one per sample", n)
	}
}

// TestStackedBarShowsASingleFailure is the point of the histogram: a run that
// almost passed must not be drawn as one that did.
func TestStackedBarShowsASingleFailure(t *testing.T) {
	got := StackedBar(Plain(), 20,
		Segment{N: 199, Style: Green},
		Segment{N: 1, Style: Red, Rune: '▄'},
	)
	if !strings.ContainsRune(got, '▄') {
		t.Errorf("the one failure was rounded away: %q", got)
	}
	if Width(got) != 20 {
		t.Errorf("StackedBar is %d columns, want 20: %q", Width(got), got)
	}
}

func TestStackedBarEmpty(t *testing.T) {
	got := StackedBar(Plain(), 5)
	if got != "░░░░░" {
		t.Errorf("a run with no cases should be an empty bar, got %q", got)
	}
}
