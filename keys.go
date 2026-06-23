package main

import (
	"fmt"

	tea "github.com/charmbracelet/bubbletea"
)

// specialKeySeq maps the non-printable navigation keys to the byte sequences a
// terminal application expects. Printable runes and C0 control keys are handled
// directly in keyToBytes.
var specialKeySeq = map[tea.KeyType]string{
	tea.KeyUp:       "\x1b[A",
	tea.KeyDown:     "\x1b[B",
	tea.KeyRight:    "\x1b[C",
	tea.KeyLeft:     "\x1b[D",
	tea.KeyShiftTab: "\x1b[Z",
	tea.KeyHome:     "\x1b[H",
	tea.KeyEnd:      "\x1b[F",
	tea.KeyPgUp:     "\x1b[5~",
	tea.KeyPgDown:   "\x1b[6~",
	tea.KeyDelete:   "\x1b[3~",
	tea.KeyInsert:   "\x1b[2~",
}

// keyToBytes encodes a bubbletea key event back into the raw terminal bytes it
// came from, so cmanager can forward keystrokes into a managed session. It
// returns nil for keys we don't translate.
func keyToBytes(k tea.KeyMsg) []byte {
	var prefix []byte
	if k.Alt {
		prefix = []byte{0x1b} // alt = ESC-prefixed
	}
	switch {
	case k.Type == tea.KeyRunes:
		return append(prefix, []byte(string(k.Runes))...)
	case k.Type == tea.KeySpace:
		return append(prefix, ' ')
	case k.Type >= 0 && (k.Type <= 31 || k.Type == 127):
		// C0 control keys (incl. enter, tab, esc, backspace): the KeyType
		// value is the byte itself.
		return append(prefix, byte(k.Type))
	}
	if seq, ok := specialKeySeq[k.Type]; ok {
		return append(prefix, []byte(seq)...)
	}
	return nil
}

// mouseToBytes encodes a scroll-wheel event as an SGR mouse report (mode 1006),
// which is what Claude's UI reads. yOffset is the number of header rows drawn
// above the mirrored screen, so the coordinate lands in the session's own
// space. Non-wheel events return nil — we only forward scrolling.
func mouseToBytes(m tea.MouseMsg, yOffset int) []byte {
	var btn int
	switch m.Button {
	case tea.MouseButtonWheelUp:
		btn = 64
	case tea.MouseButtonWheelDown:
		btn = 65
	case tea.MouseButtonWheelLeft:
		btn = 66
	case tea.MouseButtonWheelRight:
		btn = 67
	default:
		return nil
	}
	x := m.X + 1
	y := m.Y - yOffset + 1
	if x < 1 {
		x = 1
	}
	if y < 1 {
		y = 1
	}
	return []byte(fmt.Sprintf("\x1b[<%d;%d;%dM", btn, x, y))
}
