package main

import (
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
)

func TestHighlightCols(t *testing.T) {
	line := "hello world"
	out := highlightCols(line, 6, 11, ansi.StringWidth(line)) // "world"

	// Visible text must be unchanged.
	if got := ansi.Strip(out); got != line {
		t.Fatalf("text changed by highlight: %q", got)
	}
	// A reverse-video (SGR 7) sequence must wrap the selected run.
	if !strings.Contains(out, "7m") {
		t.Errorf("expected reverse-video escape in %q", out)
	}
	// The unselected prefix must come before the reverse sequence.
	if i := strings.Index(out, "7m"); i < 0 || !strings.Contains(out[:i], "hello") {
		t.Errorf("prefix not preserved ahead of highlight: %q", out)
	}
}

func TestHighlightColsClamp(t *testing.T) {
	line := "abc"
	// Out-of-range / empty ranges are no-ops, not panics or corruption.
	if got := highlightCols(line, 5, 9, ansi.StringWidth(line)); got != line {
		t.Errorf("out-of-range highlight changed line: %q", got)
	}
	if got := highlightCols(line, 2, 2, ansi.StringWidth(line)); got != line {
		t.Errorf("empty highlight changed line: %q", got)
	}
}

func TestClamp(t *testing.T) {
	cases := []struct{ v, lo, hi, want int }{
		{-1, 0, 5, 0}, {7, 0, 5, 5}, {3, 0, 5, 3},
	}
	for _, c := range cases {
		if got := clamp(c.v, c.lo, c.hi); got != c.want {
			t.Errorf("clamp(%d,%d,%d)=%d want %d", c.v, c.lo, c.hi, got, c.want)
		}
	}
}
