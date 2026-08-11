package ui

import "strings"

// blocks are the eighth-height block characters, shortest first. A sparkline is
// one of them per sample, which is as much of a chart as a status line has room
// for.
var blocks = []rune("▁▂▃▄▅▆▇█")

// Cell renders one sample of a sparkline: fraction is 0 to 1, and anything
// above zero gets at least the shortest block, so a run where one case passed
// never looks like a run where none did.
func Cell(fraction float64) string {
	switch {
	case fraction <= 0:
		return string(blocks[0])
	case fraction >= 1:
		return string(blocks[len(blocks)-1])
	}
	i := int(fraction * float64(len(blocks)))
	if i >= len(blocks) {
		i = len(blocks) - 1
	}
	if i < 1 {
		i = 1
	}
	return string(blocks[i])
}

// Spark renders a sparkline from fractions between 0 and 1.
func Spark(fractions []float64) string {
	var b strings.Builder
	for _, f := range fractions {
		b.WriteString(Cell(f))
	}
	return b.String()
}

// Bar renders a horizontal bar of the given width, filled to fraction.
func Bar(fraction float64, width int) string {
	if width <= 0 {
		return ""
	}
	if fraction < 0 {
		fraction = 0
	}
	if fraction > 1 {
		fraction = 1
	}
	filled := int(fraction*float64(width) + 0.5)
	// A result that is not quite nothing keeps a sliver of bar, and one that is
	// not quite everything keeps a gap: rounding must not turn 99% into a full
	// bar or 1% into an empty one.
	if filled == 0 && fraction > 0 {
		filled = 1
	}
	if filled == width && fraction < 1 {
		filled = width - 1
	}
	return strings.Repeat("█", filled) + strings.Repeat("░", width-filled)
}

// Segment is one run of a stacked bar: how many units it covers and how it is
// drawn.
type Segment struct {
	N     int
	Style Style
	Rune  rune
}

// StackedBar renders counts side by side in one bar of the given width — passed,
// failed and skipped cases in the proportions the run had them. Every segment
// with a count keeps at least one column, so a single failure among hundreds of
// passes is still visible, which is the only reason to look at the bar.
func StackedBar(p Printer, width int, segs ...Segment) string {
	total := 0
	for _, s := range segs {
		total += s.N
	}
	if width <= 0 || total == 0 {
		return strings.Repeat("░", max(width, 0))
	}

	cols := make([]int, len(segs))
	used := 0
	for i, s := range segs {
		if s.N == 0 {
			continue
		}
		n := s.N * width / total
		if n == 0 {
			n = 1
		}
		cols[i] = n
		used += n
	}
	// Rounding down and then rounding every non-empty segment up to one column
	// can overshoot; the widest segment gives the columns back.
	for used > width {
		widest, at := 0, -1
		for i, n := range cols {
			if n > widest {
				widest, at = n, i
			}
		}
		if at < 0 || widest <= 1 {
			break
		}
		cols[at]--
		used--
	}

	var b strings.Builder
	for i, s := range segs {
		if cols[i] == 0 {
			continue
		}
		r := s.Rune
		if r == 0 {
			r = '█'
		}
		b.WriteString(p.Paint(strings.Repeat(string(r), cols[i]), s.Style))
	}
	if used < width {
		b.WriteString(p.Dim(strings.Repeat("░", width-used)))
	}
	return b.String()
}
