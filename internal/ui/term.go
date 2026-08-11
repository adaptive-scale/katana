package ui

import (
	"io"
	"os"

	"golang.org/x/term"
)

// TerminalWidth is how many columns the terminal behind w has, or zero when it
// is not a terminal — a pipe has no width, and output going into one should not
// be trimmed to fit a size nobody is looking at.
func TerminalWidth(w io.Writer) int {
	f, ok := w.(*os.File)
	if !ok {
		return 0
	}
	width, _, err := term.GetSize(int(f.Fd()))
	if err != nil || width <= 0 {
		return 0
	}
	return width
}

// TerminalSize is TerminalWidth with the row count, for a full-screen view.
func TerminalSize(f *os.File) (width, height int, ok bool) {
	w, h, err := term.GetSize(int(f.Fd()))
	if err != nil || w <= 0 || h <= 0 {
		return 0, 0, false
	}
	return w, h, true
}
