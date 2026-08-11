package tui

import (
	"io"
	"unicode/utf8"
)

// keyKind is a key press, reduced to the ones the UI acts on.
type keyKind int

const (
	// keyNone is a sequence katana has no use for: a mouse report, a function
	// key, an Alt chord. It is decoded so the bytes are consumed and then
	// ignored, rather than acted on as the keys it is made of.
	keyNone keyKind = iota
	keyRune
	keyUp
	keyDown
	keyLeft
	keyRight
	keyEnter
	keyEsc
	keyTab
	keyHome
	keyEnd
	keyPageUp
	keyPageDown
	keyCtrlC
	keyBackspace
)

type key struct {
	kind keyKind
	r    rune
}

// readKeys decodes key presses from a terminal in raw mode and sends them on.
// It returns when the input ends, closing the channel, which is how the loop
// learns the terminal went away.
//
// Escape sequences are read as they arrive rather than by waiting for a timeout:
// a terminal sends the three bytes of an arrow key in one write, and a bare
// escape arrives on its own, so a chunk that begins with escape and holds
// nothing else is the Escape key.
func readKeys(r io.Reader, out chan<- key) {
	defer close(out)

	buf := make([]byte, 128)
	for {
		n, err := r.Read(buf)
		if n > 0 {
			for _, k := range decode(buf[:n]) {
				out <- k
			}
		}
		if err != nil {
			return
		}
	}
}

// decode turns one read from the terminal into the keys it holds.
func decode(b []byte) []key {
	var keys []key
	for len(b) > 0 {
		k, used := decodeOne(b)
		if used == 0 {
			break
		}
		keys = append(keys, k)
		b = b[used:]
	}
	return keys
}

func decodeOne(b []byte) (key, int) {
	switch b[0] {
	case 0x1b:
		return decodeEscape(b)
	case '\r', '\n':
		return key{kind: keyEnter}, 1
	case '\t':
		return key{kind: keyTab}, 1
	case 0x03:
		return key{kind: keyCtrlC}, 1
	case 0x7f, 0x08:
		return key{kind: keyBackspace}, 1
	}
	r, size := utf8.DecodeRune(b)
	if r == utf8.RuneError && size <= 1 {
		return key{kind: keyRune, r: rune(b[0])}, 1
	}
	return key{kind: keyRune, r: r}, size
}

// decodeEscape reads the CSI and SS3 sequences a terminal sends for the keys
// that have no character of their own.
func decodeEscape(b []byte) (key, int) {
	if len(b) == 1 {
		return key{kind: keyEsc}, 1
	}
	if b[1] != '[' && b[1] != 'O' {
		// Alt-something. katana binds nothing to it, and reading it as the two
		// keys it is made of would act on the letter, so it is dropped whole.
		_, size := utf8.DecodeRune(b[1:])
		return key{kind: keyNone}, 1 + size
	}
	if len(b) == 2 {
		return key{kind: keyEsc}, 2
	}

	// A CSI sequence runs to its first letter or tilde.
	end := 2
	for end < len(b) {
		c := b[end]
		if (c >= 'A' && c <= 'Z') || (c >= 'a' && c <= 'z') || c == '~' {
			break
		}
		end++
	}
	if end >= len(b) {
		return key{kind: keyEsc}, len(b)
	}

	body := string(b[2:end])
	switch b[end] {
	case 'A':
		return key{kind: keyUp}, end + 1
	case 'B':
		return key{kind: keyDown}, end + 1
	case 'C':
		return key{kind: keyRight}, end + 1
	case 'D':
		return key{kind: keyLeft}, end + 1
	case 'H':
		return key{kind: keyHome}, end + 1
	case 'F':
		return key{kind: keyEnd}, end + 1
	case '~':
		switch body {
		case "1", "7":
			return key{kind: keyHome}, end + 1
		case "4", "8":
			return key{kind: keyEnd}, end + 1
		case "5":
			return key{kind: keyPageUp}, end + 1
		case "6":
			return key{kind: keyPageDown}, end + 1
		}
	}
	return key{kind: keyNone}, end + 1
}
