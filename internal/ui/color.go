// Package ui renders katana's terminal output: colour that switches itself off
// wherever it would not be read by a person, and tables that stay aligned when
// their cells carry it.
package ui

import (
	"fmt"
	"io"
	"os"
	"regexp"
	"strings"
	"unicode/utf8"
)

// Style is one ANSI SGR parameter. Styles combine, so a cell can be bold and
// green without either one having to know about the other.
type Style string

const (
	Bold      Style = "1"
	Dim       Style = "2"
	Italic    Style = "3"
	Underline Style = "4"
	Reverse   Style = "7"

	Red     Style = "31"
	Green   Style = "32"
	Yellow  Style = "33"
	Blue    Style = "34"
	Magenta Style = "35"
	Cyan    Style = "36"
	Grey    Style = "90"
)

// Mode is how colour is decided: from the terminal, or by being told.
type Mode int

const (
	// Auto colours only when the destination is a terminal.
	Auto Mode = iota
	// Always colours whatever the destination is, for a pager that renders it.
	Always
	// Never leaves the output plain.
	Never
)

// ParseMode reads a --color flag value.
func ParseMode(s string) (Mode, error) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "", "auto":
		return Auto, nil
	case "always", "force", "yes", "true":
		return Always, nil
	case "never", "no", "false":
		return Never, nil
	}
	return Auto, fmt.Errorf("invalid colour mode %q (want auto, always or never)", s)
}

// mode is the process-wide override a --color flag sets. It is written once,
// while parsing flags, and only read afterwards.
var mode = Auto

// SetMode overrides colour detection for the rest of the process.
func SetMode(m Mode) { mode = m }

// Printer decides whether the text it is given carries colour. Build one per
// destination — stdout may be a terminal while stderr is a file, and a test that
// captures either wants neither coloured.
type Printer struct{ on bool }

// For builds a printer for a writer, honouring the --color mode and, under
// Auto, whether the writer is a terminal.
func For(w io.Writer) Printer { return Printer{on: colorable(w)} }

// Plain is a printer that never colours, for building strings that are going
// somewhere other than a terminal.
func Plain() Printer { return Printer{} }

// Enabled reports whether this printer colours anything.
func (p Printer) Enabled() bool { return p.on }

// Paint wraps s in the given styles, or returns it untouched when colour is off.
func (p Printer) Paint(s string, styles ...Style) string {
	if !p.on || s == "" || len(styles) == 0 {
		return s
	}
	codes := make([]string, len(styles))
	for i, st := range styles {
		codes[i] = string(st)
	}
	return "\x1b[" + strings.Join(codes, ";") + "m" + s + "\x1b[0m"
}

func (p Printer) Red(s string) string     { return p.Paint(s, Red) }
func (p Printer) Green(s string) string   { return p.Paint(s, Green) }
func (p Printer) Yellow(s string) string  { return p.Paint(s, Yellow) }
func (p Printer) Blue(s string) string    { return p.Paint(s, Blue) }
func (p Printer) Magenta(s string) string { return p.Paint(s, Magenta) }
func (p Printer) Cyan(s string) string    { return p.Paint(s, Cyan) }
func (p Printer) Dim(s string) string     { return p.Paint(s, Grey) }
func (p Printer) Bold(s string) string    { return p.Paint(s, Bold) }

// colorable decides whether output written to w should carry colour.
//
// The environment outranks the terminal in both directions: NO_COLOR is a
// promise katana keeps even on a terminal, and CLICOLOR_FORCE is how a caller
// asks for colour through a pipe it is going to render itself.
func colorable(w io.Writer) bool {
	switch mode {
	case Always:
		return true
	case Never:
		return false
	}
	if os.Getenv("NO_COLOR") != "" {
		return false
	}
	if v := os.Getenv("CLICOLOR_FORCE"); v != "" && v != "0" {
		return true
	}
	if term := os.Getenv("TERM"); term == "dumb" {
		return false
	}
	return IsTerminal(w)
}

// IsTerminal reports whether w is a character device — a terminal someone is
// looking at, rather than a pipe or a file.
func IsTerminal(w io.Writer) bool {
	f, ok := w.(*os.File)
	if !ok {
		return false
	}
	info, err := f.Stat()
	if err != nil {
		return false
	}
	return info.Mode()&os.ModeCharDevice != 0
}

// ansi matches the escape sequences colour is written with, so the width of a
// cell can be measured in what a reader actually sees.
var ansi = regexp.MustCompile(`\x1b\[[0-9;]*m`)

// Strip removes ANSI colour from a string.
func Strip(s string) string { return ansi.ReplaceAllString(s, "") }

// Width is how many columns a string occupies once its colour is discounted.
// Runes are counted rather than bytes, so the marks and arrows katana prints
// measure as the one column they take up.
func Width(s string) int { return utf8.RuneCountInString(Strip(s)) }

// Pad extends s with spaces to n visible columns. A string already that wide is
// returned as it is: a table that cannot fit a cell is better wide than wrong.
func Pad(s string, n int) string {
	if d := n - Width(s); d > 0 {
		return s + strings.Repeat(" ", d)
	}
	return s
}

// PadLeft is Pad for a right-aligned cell.
func PadLeft(s string, n int) string {
	if d := n - Width(s); d > 0 {
		return strings.Repeat(" ", d) + s
	}
	return s
}

// Truncate shortens s to n visible columns, ending in an ellipsis when it had
// to cut. Colour is dropped rather than half-applied: a truncated cell would
// otherwise end mid-escape and bleed its colour into the rest of the line.
func Truncate(s string, n int) string {
	if n <= 0 {
		return ""
	}
	if Width(s) <= n {
		return s
	}
	plain := []rune(Strip(s))
	if n == 1 {
		return "…"
	}
	return string(plain[:n-1]) + "…"
}
