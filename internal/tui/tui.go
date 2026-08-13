// Package tui is katana's full-screen terminal interface: the behaviors a
// project has, what state each one is in, how its tests last did, and a way to
// run them there and then.
//
// It exists because the three things a person does with katana between edits —
// look at what is out of date, run a behavior's tests, look at what that did —
// are three commands and a lot of re-reading. Here they are one screen, and a
// run updates it the moment it finishes.
package tui

import (
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/adaptive-scale/katana/internal/config"
	"github.com/adaptive-scale/katana/internal/ui"
)

// tick is how often the UI wakes up on its own: often enough that a spinner
// turns and a running suite's output appears as it is written, rarely enough
// that an idle katana is not something a laptop notices.
const tick = 120 * time.Millisecond

// Run puts the UI on the terminal and returns when the user leaves it.
func Run(cfg *config.Config) error {
	scr, err := newScreen(os.Stdin, os.Stdout)
	if err != nil {
		return err
	}
	// The terminal is given back whatever happens, including a panic on the way
	// out: a process that dies in raw mode leaves the shell unusable.
	defer scr.close()

	m := newModel(cfg, ui.For(os.Stdout), scr.w, scr.h)

	keys := make(chan key, 16)
	go readKeys(scr.in, keys)

	ticker := time.NewTicker(tick)
	defer ticker.Stop()

	for !m.quit {
		scr.draw(m.render())

		select {
		case k, ok := <-keys:
			if !ok {
				// The terminal closed under us.
				return nil
			}
			m.handle(k)
		case o := <-m.done:
			m.finishRun(o)
		case <-ticker.C:
			if scr.resized() {
				m.w, m.h = scr.w, scr.h
			}
			if m.running {
				m.spinner++
			}
		}
	}
	return nil
}

// Snapshot renders one frame to w and returns, for a terminal katana cannot
// take over — a pipe, a CI log, a screenshot. It is the same rendering the UI
// draws, so what it shows is what would be on screen.
func Snapshot(cfg *config.Config, w io.Writer, width, height int) error {
	m := newModel(cfg, ui.For(w), width, height)
	if m.loadErr != nil {
		return m.loadErr
	}
	for _, line := range m.render() {
		if _, err := fmt.Fprintln(w, strings.TrimRight(line, " ")); err != nil {
			return err
		}
	}
	return nil
}

// handle acts on one key press. The bindings are the same in every view where
// they mean the same thing: r runs, esc goes back, q leaves.
func (m *model) handle(k key) {
	// A run in flight owns the keys that would otherwise start another one.
	switch k.kind {
	case keyCtrlC:
		if m.running {
			m.stopRun()
			return
		}
		m.quit = true
		return
	case keyNone:
		return
	}

	switch m.view {
	case viewList:
		m.handleList(k)
	case viewDetail:
		m.handleDetail(k)
	case viewOutput:
		m.handleOutput(k)
	case viewHelp:
		m.handleHelp(k)
	}
}

func (m *model) handleList(k key) {
	switch k.kind {
	case keyUp:
		m.move(-1)
	case keyDown:
		m.move(1)
	case keyPageUp:
		m.move(-m.listBody())
	case keyPageDown:
		m.move(m.listBody())
	case keyHome:
		m.sel = 0
	case keyEnd:
		m.sel = max(len(m.items)-1, 0)
	case keyEnter, keyRight:
		if _, ok := m.current(); ok {
			m.view, m.scroll = viewDetail, 0
		}
	case keyRune:
		switch k.r {
		case 'j':
			m.move(1)
		case 'k':
			m.move(-1)
		case 'g':
			m.sel = 0
		case 'G':
			m.sel = max(len(m.items)-1, 0)
		case 'l':
			if _, ok := m.current(); ok {
				m.view, m.scroll = viewDetail, 0
			}
		default:
			m.common(k.r)
		}
	}
}

func (m *model) handleDetail(k key) {
	switch k.kind {
	case keyEsc, keyLeft:
		m.view, m.scroll = viewList, 0
	case keyUp:
		m.scroll = max(m.scroll-1, 0)
	case keyDown:
		m.scroll++
	case keyPageUp:
		m.scroll = max(m.scroll-10, 0)
	case keyPageDown:
		m.scroll += 10
	case keyRune:
		switch k.r {
		case 'h':
			m.view, m.scroll = viewList, 0
		case 'j':
			m.scroll++
		case 'k':
			m.scroll = max(m.scroll-1, 0)
		case 'n':
			// Straight on to the next behavior, without going back to the list.
			m.move(1)
			m.scroll = 0
		case 'p':
			m.move(-1)
			m.scroll = 0
		default:
			m.common(k.r)
		}
	}
}

func (m *model) handleOutput(k key) {
	switch k.kind {
	case keyEsc, keyLeft:
		m.view, m.scroll = viewList, 0
	case keyUp:
		m.scroll = max(m.scroll-1, 0)
	case keyDown:
		m.scroll++
	case keyPageUp:
		m.scroll = max(m.scroll-10, 0)
	case keyPageDown:
		m.scroll += 10
	case keyRune:
		switch k.r {
		case 'j':
			m.scroll++
		case 'k':
			m.scroll = max(m.scroll-1, 0)
		default:
			m.common(k.r)
		}
	}
}

func (m *model) handleHelp(k key) {
	switch k.kind {
	case keyEsc, keyEnter, keyLeft:
		m.view = viewList
	case keyRune:
		switch k.r {
		case '?', 'h':
			m.view = viewList
		default:
			m.common(k.r)
		}
	}
}

// common is the keys that mean the same thing wherever they are pressed.
func (m *model) common(r rune) {
	switch r {
	case 'q':
		if m.running {
			m.message = "a run is going — press x to stop it, then q to leave"
			return
		}
		m.quit = true
	case 'r':
		if m.view == viewOutput && m.last != nil && m.last.Scope == "" {
			m.startRun(nil, "the whole suite")
			return
		}
		m.runSelected()
	case 'a':
		m.startRun(nil, "the whole suite")
	case 'x':
		m.stopRun()
	case 'o':
		if m.live != nil {
			m.view, m.scroll = viewOutput, 0
		} else {
			m.message = "nothing has been run from here yet"
		}
	case 'u':
		m.reload()
		m.message = "reloaded"
	case '?':
		m.view = viewHelp
	}
}

// move walks the selection through the list, stopping at either end rather than
// wrapping: a list that jumps from the last row to the first hides how long it
// is.
func (m *model) move(by int) {
	if len(m.items) == 0 {
		return
	}
	m.sel = clamp(m.sel+by, 0, len(m.items)-1)
}
