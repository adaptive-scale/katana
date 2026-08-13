package cli

import (
	"fmt"
	"io"
	"os"
	"strings"
	"sync"

	"github.com/adaptive-scale/katana/internal/ui"
)

// operationBar reports both completed work items and the finer-grained cases
// that arrive while an item is still running. On a terminal it owns the current
// line and redraws it in place. Writers returned by writer temporarily clear
// that line, write ordinary output, then restore it, so concurrent progress and
// result output do not corrupt one another.
type operationBar struct {
	mu           sync.Mutex
	w            io.Writer
	verb         string
	total        int
	done         int
	cases        int
	lastLine     string
	interactive  bool
	visible      bool
	visibleWidth int
	lineOpen     bool
	printedLine  string
	batch        string
}

func newOperationBar(w io.Writer, verb string, total int) *operationBar {
	return newOperationBarMode(w, verb, total,
		ui.IsTerminal(w) && os.Getenv("TERM") != "dumb")
}

// newOperationBarMode makes terminal rendering explicit for the unit tests,
// whose bytes.Buffer is deliberately not a terminal.
func newOperationBarMode(w io.Writer, verb string, total int, interactive bool) *operationBar {
	b := &operationBar{w: w, verb: verb, total: total, interactive: interactive}
	b.render()
	return b
}

// writer returns a destination which cooperates with the bar's terminal line.
// Writes to stdout and stderr share the same lock when both wrap this bar.
func (b *operationBar) writer(w io.Writer) io.Writer {
	if !b.interactive {
		return w
	}
	return operationWriter{bar: b, w: w}
}

type operationWriter struct {
	bar *operationBar
	w   io.Writer
}

func (w operationWriter) Write(p []byte) (int, error) {
	w.bar.mu.Lock()
	defer w.bar.mu.Unlock()

	w.bar.eraseLocked()
	n, err := w.w.Write(p)
	// A partial line still belongs to its writer. Leave the bar hidden until a
	// later write completes that line rather than appending progress to it.
	if n > 0 {
		w.bar.lineOpen = p[n-1] != '\n'
		if !w.bar.lineOpen {
			w.bar.drawLocked()
		}
	}
	return n, err
}

func (b *operationBar) addCases(n int) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if n <= 0 {
		return
	}
	b.cases += n
	b.renderLocked()
}

func (b *operationBar) finish() {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.done < b.total {
		b.done++
	}
	b.renderLocked()
}

func (b *operationBar) setCases(n int) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if n < b.cases {
		return
	}
	b.cases = n
	b.done = n
	if b.done > b.total {
		b.total = b.done
	}
	b.renderLocked()
}

// beginBatch announces the test currently being run. The batch line is kept
// separate from the progress line so callers can replace it for the next
// invocation without changing the meaning of the overall bar.
func (b *operationBar) beginBatch(name string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.batch = name
	if !b.interactive {
		fmt.Fprintf(b.w, "  test: %s\n", name)
		return
	}
	b.eraseLocked()
	b.drawLocked()
	fmt.Fprintf(b.w, "\n  test: %s", name)
}

func (b *operationBar) render() {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.renderLocked()
}

func (b *operationBar) renderLocked() {
	line := b.line()
	if line == b.lastLine {
		return
	}
	b.lastLine = line

	if b.interactive {
		b.eraseLocked()
		b.drawLocked()
		return
	}

	// A pipe cannot replace a line. Print the initial state here; stop appends the
	// final one without turning every case in between into permanent log spam.
	if b.printedLine == "" {
		fmt.Fprintln(b.w, line)
		b.printedLine = line
	}
}

func (b *operationBar) line() string {
	const width = 20
	filled := width
	if b.total > 0 {
		filled = b.done * width / b.total
	}
	line := fmt.Sprintf("  %s [%s%s] %d/%d", b.verb,
		strings.Repeat("=", filled), strings.Repeat(".", width-filled), b.done, b.total)
	if b.cases > 0 {
		line += fmt.Sprintf(" — %d case(s)", b.cases)
	}
	return line
}

func (b *operationBar) drawLocked() {
	if b.visible || b.lineOpen || b.lastLine == "" {
		return
	}
	fmt.Fprint(b.w, b.lastLine)
	b.visible = true
	b.visibleWidth = ui.Width(b.lastLine)
}

func (b *operationBar) eraseLocked() {
	if !b.visible {
		return
	}
	// Spaces and carriage returns work without relying on ANSI support. The bar
	// is plain text, so its rune width is also its terminal width here.
	fmt.Fprintf(b.w, "\r%s\r", strings.Repeat(" ", b.visibleWidth))
	b.visible = false
	b.visibleWidth = 0
}

// stop commits the latest state before a summary or error is printed.
func (b *operationBar) stop() {
	b.mu.Lock()
	defer b.mu.Unlock()
	if !b.interactive {
		if b.lastLine != b.printedLine {
			fmt.Fprintln(b.w, b.lastLine)
			b.printedLine = b.lastLine
		}
		return
	}
	if b.lineOpen {
		fmt.Fprintln(b.w)
		b.lineOpen = false
		b.drawLocked()
	}
	if b.visible {
		fmt.Fprintln(b.w)
		b.visible = false
	}
}
