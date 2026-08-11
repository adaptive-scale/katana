package ui

import (
	"fmt"
	"io"
	"strings"
)

// Table lays rows out in ruled columns. Cells may already carry colour: every
// width is measured on the visible text, so a coloured cell lines up with a
// plain one instead of being padded by the length of its escape sequences.
//
// A table is built up and then rendered, because the widest cell is only known
// once the last row is in.
type Table struct {
	header []string
	rows   [][]string
	right  map[int]bool
	// max bounds the rendered width. Zero means no bound.
	max int
	// highlight is the row drawn as selected, or -1 for none.
	highlight int
}

// NewTable starts a table with the given column headings.
func NewTable(header ...string) *Table {
	return &Table{header: header, right: map[int]bool{}, highlight: -1}
}

// Row appends a row. Rows shorter than the header are padded with empty cells,
// and longer ones widen the table, so a caller never has to count.
func (t *Table) Row(cells ...string) *Table {
	t.rows = append(t.rows, cells)
	return t
}

// RightAlign right-aligns the given columns, which is what counts want.
func (t *Table) RightAlign(cols ...int) *Table {
	for _, c := range cols {
		t.right[c] = true
	}
	return t
}

// Highlight draws one row as the selected one, for a table a cursor moves
// through. Pass a negative row to select nothing.
func (t *Table) Highlight(row int) *Table {
	t.highlight = row
	return t
}

// MaxWidth bounds the rendered width in columns. The widest column is the one
// that gives way, since a path or a description is the cell a reader can still
// recognise from its beginning.
func (t *Table) MaxWidth(n int) *Table {
	t.max = n
	return t
}

// Empty reports whether the table has any rows.
func (t *Table) Empty() bool { return len(t.rows) == 0 }

// Render writes the table to w, colouring its rules and headings with p.
func (t *Table) Render(w io.Writer, p Printer) error {
	for _, line := range t.Lines(p, t.max) {
		if _, err := fmt.Fprintln(w, line); err != nil {
			return err
		}
	}
	return nil
}

// Lines renders the table as the lines it is made of, which is what a
// full-screen view needs: it draws them itself, wherever they belong on screen.
//
// maxWidth bounds the total width; zero leaves the table its natural size.
func (t *Table) Lines(p Printer, maxWidth int) []string {
	cols := len(t.header)
	for _, r := range t.rows {
		if len(r) > cols {
			cols = len(r)
		}
	}
	if cols == 0 {
		return nil
	}

	widths := make([]int, cols)
	for i, h := range t.header {
		widths[i] = Width(h)
	}
	for _, r := range t.rows {
		for i, c := range r {
			if n := Width(c); n > widths[i] {
				widths[i] = n
			}
		}
	}
	if maxWidth > 0 {
		shrink(widths, maxWidth-frameWidth(cols))
	}

	rule := func(left, mid, right string) string {
		parts := make([]string, cols)
		for i, wd := range widths {
			parts[i] = strings.Repeat("─", wd+2)
		}
		return p.Dim(left + strings.Join(parts, mid) + right)
	}
	bar := p.Dim("│")

	// A selected row is drawn reversed and without the colours its cells carry:
	// an escape sequence inside the row would end the reversal halfway along it,
	// leaving the selection looking like a rendering fault.
	line := func(cells []string, selected bool, style ...Style) string {
		sep := bar
		if selected {
			sep = "│"
		}
		var b strings.Builder
		b.WriteString(sep)
		for i := 0; i < cols; i++ {
			cell := ""
			if i < len(cells) {
				cell = cells[i]
			}
			if selected {
				cell = Strip(cell)
			}
			cell = Truncate(cell, widths[i])
			if t.right[i] {
				cell = PadLeft(cell, widths[i])
			} else {
				cell = Pad(cell, widths[i])
			}
			if !selected {
				cell = p.Paint(cell, style...)
			}
			b.WriteString(" " + cell + " ")
			b.WriteString(sep)
		}
		if selected {
			return p.Paint(b.String(), Reverse)
		}
		return b.String()
	}

	out := make([]string, 0, len(t.rows)+4)
	out = append(out, rule("┌", "┬", "┐"))
	if len(t.header) > 0 {
		out = append(out, line(t.header, false, Bold))
		out = append(out, rule("├", "┼", "┤"))
	}
	for i, r := range t.rows {
		out = append(out, line(r, i == t.highlight))
	}
	return append(out, rule("└", "┴", "┘"))
}

// frameWidth is what the rules and padding cost: "│ " before every column and
// " │" after the last.
func frameWidth(cols int) int { return cols*3 + 1 }

// shrink narrows the widest columns until the total fits, taking a column at a
// time from whichever is widest so one long path does not squeeze every other
// column into uselessness. Columns are never taken below a legible minimum; a
// table that cannot fit at all is left too wide rather than made unreadable.
func shrink(widths []int, budget int) {
	const min = 6
	if budget <= 0 {
		return
	}
	for total(widths) > budget {
		widest, at := 0, -1
		for i, w := range widths {
			if w > widest {
				widest, at = w, i
			}
		}
		if at < 0 || widest <= min {
			return
		}
		widths[at]--
	}
}

func total(widths []int) int {
	n := 0
	for _, w := range widths {
		n += w
	}
	return n
}
