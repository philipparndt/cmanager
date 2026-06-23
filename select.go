package main

import (
	"strings"

	"github.com/charmbracelet/x/ansi"
)

// Mouse text selection. The selection is stored as two screen cells (start and
// end of the drag); both the copied text and the on-screen highlight are
// derived from the current View, so they always match what's visible.

// orderedSel returns the selection corners in reading order (top-left first).
func (m model) orderedSel() (x0, y0, x1, y1 int) {
	x0, y0, x1, y1 = m.selStartX, m.selStartY, m.selEndX, m.selEndY
	if y1 < y0 || (y1 == y0 && x1 < x0) {
		x0, y0, x1, y1 = x1, y1, x0, y0
	}
	return
}

// selectedText extracts the plain text under the current selection from the
// (un-highlighted) view, cell-accurately and ANSI-free, ready for the clipboard.
func (m model) selectedText() string {
	x0, y0, x1, y1 := m.orderedSel()
	lines := strings.Split(m.baseView(), "\n")
	if len(lines) == 0 {
		return ""
	}
	y0 = clamp(y0, 0, len(lines)-1)
	y1 = clamp(y1, 0, len(lines)-1)

	plain := func(i int) string { return ansi.Strip(lines[i]) }

	if y0 == y1 {
		l, r := x0, x1
		if r < l {
			l, r = r, l
		}
		return ansi.Cut(plain(y0), l, r+1)
	}

	var b strings.Builder
	first := plain(y0)
	b.WriteString(strings.TrimRight(ansi.Cut(first, x0, ansi.StringWidth(first)), " "))
	for y := y0 + 1; y < y1; y++ {
		b.WriteByte('\n')
		b.WriteString(strings.TrimRight(plain(y), " "))
	}
	b.WriteByte('\n')
	b.WriteString(ansi.Cut(plain(y1), 0, x1+1))
	return b.String()
}

// applyHighlight overlays a reverse-video block on the selected cells of the
// rendered view. It cuts each affected line around the selection so the rest of
// the line keeps its original colors.
func (m model) applyHighlight(view string) string {
	x0, y0, x1, y1 := m.orderedSel()
	lines := strings.Split(view, "\n")
	if len(lines) == 0 {
		return view
	}
	y0 = clamp(y0, 0, len(lines)-1)
	y1 = clamp(y1, 0, len(lines)-1)

	for y := y0; y <= y1; y++ {
		w := ansi.StringWidth(lines[y])
		var l, r int
		switch {
		case y0 == y1:
			l, r = x0, x1
			if r < l {
				l, r = r, l
			}
			r++
		case y == y0:
			l, r = x0, w
		case y == y1:
			l, r = 0, x1+1
		default:
			l, r = 0, w
		}
		lines[y] = highlightCols(lines[y], l, r, w)
	}
	return strings.Join(lines, "\n")
}

// highlightCols reverse-videos cells [l, r) of one rendered line, preserving the
// styling of the unselected remainder.
func highlightCols(line string, l, r, w int) string {
	if l < 0 {
		l = 0
	}
	if r > w {
		r = w
	}
	if r <= l {
		return line
	}
	left := ansi.Cut(line, 0, l)
	mid := ansi.Strip(ansi.Cut(line, l, r))
	right := ansi.Cut(line, r, w)
	// Raw reverse-video around the plain selected run: independent of lipgloss's
	// color-profile detection, and the reset lets `right` re-establish its own
	// styling (ansi.Cut re-emits the active SGR at the cut point).
	return left + "\x1b[7m" + mid + "\x1b[0m" + right
}

func clamp(v, lo, hi int) int {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}
