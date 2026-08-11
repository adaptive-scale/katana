package tui

import (
	"bytes"
	"fmt"
	"os"

	"golang.org/x/term"

	"github.com/adaptive-scale/katana/internal/ui"
)

// The escape sequences the UI is built from. They are written by hand rather
// than pulled in with a library: a full-screen list that repaints itself is a
// few sequences' worth of terminal, and katana would rather owe nothing for it.
const (
	altScreenOn  = "\x1b[?1049h"
	altScreenOff = "\x1b[?1049l"
	cursorHide   = "\x1b[?25l"
	cursorShow   = "\x1b[?25h"
	cursorHome   = "\x1b[H"
	clearLine    = "\x1b[K"
	clearBelow   = "\x1b[J"
)

// The size assumed for a terminal that does not report one.
const (
	fallbackWidth  = 80
	fallbackHeight = 24
)

// screen owns the terminal for as long as the UI is up: raw mode so keys arrive
// as they are pressed, and the alternate screen so the shell's scrollback is
// exactly where the user left it when katana exits.
type screen struct {
	in    *os.File
	out   *os.File
	state *term.State
	w, h  int
	buf   bytes.Buffer
}

// newScreen takes over the terminal. The caller must close it, including on the
// way out of a panic: a process that dies in raw mode leaves the shell unusable.
func newScreen(in, out *os.File) (*screen, error) {
	if !ui.IsTerminal(out) {
		return nil, fmt.Errorf("katana tui needs a terminal (stdout is not one); try `katana status`, or `katana tui --snapshot`")
	}
	w, h, ok := ui.TerminalSize(out)
	if !ok {
		// A terminal that will not say how big it is — some ptys report nothing
		// until they are resized. A conservative guess beats refusing to start.
		w, h = fallbackWidth, fallbackHeight
	}
	state, err := term.MakeRaw(int(in.Fd()))
	if err != nil {
		return nil, fmt.Errorf("putting the terminal in raw mode: %w", err)
	}
	s := &screen{in: in, out: out, state: state, w: w, h: h}
	fmt.Fprint(out, altScreenOn+cursorHide)
	return s, nil
}

// close gives the terminal back exactly as it was found.
func (s *screen) close() {
	if s.state == nil {
		return
	}
	fmt.Fprint(s.out, cursorShow+altScreenOff)
	_ = term.Restore(int(s.in.Fd()), s.state)
	s.state = nil
}

// resized re-reads the terminal size and reports whether it changed. The UI
// polls rather than waiting on SIGWINCH, which does not exist everywhere katana
// builds, and a redraw is cheap enough that a poll costs nothing worth saving.
func (s *screen) resized() bool {
	w, h, ok := ui.TerminalSize(s.out)
	if !ok || (w == s.w && h == s.h) {
		return false
	}
	s.w, s.h = w, h
	return true
}

// draw paints a whole frame. Every line is cleared to the right as it is
// written and the rest of the screen after the last one, so a frame that is
// shorter than the one before it leaves nothing of it behind — and because the
// frame is written in one call, the terminal never shows it half-painted.
func (s *screen) draw(lines []string) {
	s.buf.Reset()
	s.buf.WriteString(cursorHome)
	for i, l := range lines {
		if i >= s.h {
			break
		}
		s.buf.WriteString(ui.Truncate(l, s.w))
		s.buf.WriteString(clearLine)
		if i < len(lines)-1 && i < s.h-1 {
			s.buf.WriteString("\r\n")
		}
	}
	s.buf.WriteString(clearBelow)
	s.out.Write(s.buf.Bytes())
}
