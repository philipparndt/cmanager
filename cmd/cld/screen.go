package main

import (
	"strconv"
	"strings"

	"github.com/hinshun/vt10x"
)

// vt10x glyph attribute bits (the package keeps these unexported, so we mirror
// the ones we care about from its state.go).
const (
	attrUnderline = 1 << (iota + 1)
	attrBold
	_ // attrGfx
	attrItalic
)

// renderColorScreen dumps the VT grid to a string with ANSI styling preserved,
// so cmanager's mirror shows Claude's real colors instead of flat text. It
// follows vt10x's own String() row/column walk but, for each cell, emits the
// SGR sequence for its colors and text attributes.
//
// vt10x has already folded reverse-video (FG/BG pre-swapped) and bold-bright
// promotion into the stored glyph, so we re-emit only bold/italic/underline and
// the literal FG/BG colors — re-emitting reverse would double-swap them.
func renderColorScreen(vt vt10x.Terminal) string {
	vt.Lock()
	defer vt.Unlock()

	cols, rows := vt.Size()
	cpos := vt.Cursor()
	showCur := vt.CursorVisible()
	var b strings.Builder
	var cur cellStyle

	for y := 0; y < rows; y++ {
		// Trim trailing run of blank, default-styled cells to keep snapshots tidy.
		last := -1
		for x := 0; x < cols; x++ {
			if g := vt.Cell(x, y); !isBlank(g) {
				last = x
			}
		}
		// Keep the cursor visible even when it sits in the trimmed blank tail
		// (end of an input line, or an otherwise empty row).
		if showCur && cpos.Y == y && cpos.X > last && cpos.X < cols {
			last = cpos.X
		}
		for x := 0; x <= last; x++ {
			g := vt.Cell(x, y)
			st := styleOf(g)
			// Draw the terminal cursor by inverting its cell so you can see where
			// typing lands while attached. Toggle (not force) reverse so the
			// cursor stays visible even on an already-inverted cell.
			if showCur && cpos.X == x && cpos.Y == y {
				st.reverse = !st.reverse
			}
			if st != cur {
				if cur != (cellStyle{}) {
					b.WriteString("\x1b[0m")
				}
				b.WriteString(st.sgr())
				cur = st
			}
			ch := g.Char
			if ch == 0 {
				ch = ' '
			}
			b.WriteRune(ch)
		}
		if cur != (cellStyle{}) {
			b.WriteString("\x1b[0m")
			cur = cellStyle{}
		}
		b.WriteByte('\n')
	}
	return b.String()
}

// cellStyle is the visible styling of one glyph, normalized so that the default
// (unstyled) cell is the zero value — which lets us collapse runs cheaply.
type cellStyle struct {
	fg, bg                  vt10x.Color
	fgSet, bgSet            bool
	bold, italic, underline bool
	reverse                 bool
	faint                   bool
}

func styleOf(g vt10x.Glyph) cellStyle {
	var s cellStyle
	fg, bg := g.FG, g.BG
	// vt10x folds reverse-video by swapping FG/BG into the glyph. When the cell
	// was reversed against the terminal defaults, that leaves a default sentinel
	// in the "wrong" slot (default-bg as fg, or default-fg as bg) — a color we
	// can't name directly. Reconstruct the reverse flag and unswap, so we emit
	// the underlying color (if any) plus SGR 7 and let the terminal re-invert.
	if fg == vt10x.DefaultBG || bg == vt10x.DefaultFG {
		s.reverse = true
		fg, bg = bg, fg
	}
	if fg < vt10x.DefaultFG {
		s.fg, s.fgSet = fg, true
	}
	if bg < vt10x.DefaultFG {
		s.bg, s.bgSet = bg, true
	}
	s.bold = g.Mode&attrBold != 0
	s.italic = g.Mode&attrItalic != 0
	s.underline = g.Mode&attrUnderline != 0
	s.faint = g.Mode&int16(vt10x.AttrFaint) != 0
	return s
}

// isBlank reports whether a cell is an empty space with no styling, i.e. safe to
// drop from the end of a line.
func isBlank(g vt10x.Glyph) bool {
	return (g.Char == ' ' || g.Char == 0) && styleOf(g) == cellStyle{}
}

// sgr builds the ANSI escape that turns the default style into this one ("" for
// the default style itself).
func (s cellStyle) sgr() string {
	if s == (cellStyle{}) {
		return ""
	}
	var codes []string
	if s.bold {
		codes = append(codes, "1")
	}
	if s.faint {
		codes = append(codes, "2")
	}
	if s.reverse {
		codes = append(codes, "7")
	}
	if s.italic {
		codes = append(codes, "3")
	}
	if s.underline {
		codes = append(codes, "4")
	}
	if s.fgSet {
		codes = append(codes, colorCodes(s.fg, false)...)
	}
	if s.bgSet {
		codes = append(codes, colorCodes(s.bg, true)...)
	}
	return "\x1b[" + strings.Join(codes, ";") + "m"
}

// colorCodes returns the SGR parameters for a color, picking the narrowest
// encoding: 16-color, 256-palette, or 24-bit truecolor. bg selects background.
func colorCodes(c vt10x.Color, bg bool) []string {
	switch {
	case c < 8: // standard ANSI
		base := 30
		if bg {
			base = 40
		}
		return []string{strconv.Itoa(base + int(c))}
	case c < 16: // bright ANSI
		base := 90
		if bg {
			base = 100
		}
		return []string{strconv.Itoa(base + int(c) - 8)}
	case c < 256: // xterm 256-color palette
		lead := "38"
		if bg {
			lead = "48"
		}
		return []string{lead, "5", strconv.Itoa(int(c))}
	default: // packed 24-bit RGB (r<<16 | g<<8 | b)
		lead := "38"
		if bg {
			lead = "48"
		}
		return []string{lead, "2",
			strconv.Itoa(int(c>>16) & 0xff),
			strconv.Itoa(int(c>>8) & 0xff),
			strconv.Itoa(int(c) & 0xff),
		}
	}
}
