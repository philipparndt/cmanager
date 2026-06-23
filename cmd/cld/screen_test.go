package main

import (
	"strings"
	"testing"

	"github.com/hinshun/vt10x"
)

// stripSGR removes ANSI SGR escapes so we can assert on the visible text.
func stripSGR(s string) string {
	var b strings.Builder
	inEsc := false
	for _, r := range s {
		switch {
		case r == '\x1b':
			inEsc = true
		case inEsc && r == 'm':
			inEsc = false
		case inEsc:
		default:
			b.WriteRune(r)
		}
	}
	return b.String()
}

func TestRenderColorScreen(t *testing.T) {
	vt := vt10x.New(vt10x.WithSize(20, 2))
	// red "HI" on row 0, bright-green bold "OK" via truecolor on row 1.
	vt.Write([]byte("\x1b[31mHI\x1b[0m\r\n\x1b[1m\x1b[38;2;0;255;0mOK\x1b[0m"))

	out := renderColorScreen(vt)

	if !strings.Contains(out, "\x1b[") {
		t.Fatalf("expected ANSI escapes in output, got %q", out)
	}
	// Visible text must survive. The cursor sits just past "OK", drawn as a
	// reverse-video blank, so row 1 carries a trailing cursor cell.
	lines := strings.Split(stripSGR(out), "\n")
	if len(lines) < 2 || lines[0] != "HI" || strings.TrimRight(lines[1], " ") != "OK" {
		t.Fatalf("unexpected visible text %q (lines %q)", stripSGR(out), lines)
	}
	// The cursor must be rendered as a reverse-video (SGR 7) cell.
	if !strings.Contains(out, "7m") {
		t.Errorf("missing reverse-video cursor cell in %q", out)
	}
	// The 31 (red) and a 38;2 truecolor sequence should both appear.
	if !strings.Contains(out, "31") {
		t.Errorf("missing red fg code in %q", out)
	}
	if !strings.Contains(out, "38;2;0;255;0") {
		t.Errorf("missing truecolor fg code in %q", out)
	}
}

func TestRenderFaint(t *testing.T) {
	vt := vt10x.New(vt10x.WithSize(20, 1))
	// Dim/faint gray text, as claude renders prompt suggestions.
	vt.Write([]byte("\x1b[2mhint\x1b[0m"))

	out := renderColorScreen(vt)
	if !strings.Contains(out, "2m") {
		t.Fatalf("expected SGR 2 (faint) in output, got %q", out)
	}
	if !strings.Contains(stripSGR(out), "hint") {
		t.Errorf("faint text lost: %q", stripSGR(out))
	}
}

func TestColorCodes(t *testing.T) {
	cases := []struct {
		c    vt10x.Color
		bg   bool
		want string
	}{
		{vt10x.Red, false, "31"},
		{vt10x.Red, true, "41"},
		{vt10x.LightRed, false, "91"},
		{200, false, "38;5;200"},
		{200, true, "48;5;200"},
		{vt10x.Color(0x00ff00), false, "38;2;0;255;0"},
	}
	for _, tc := range cases {
		got := strings.Join(colorCodes(tc.c, tc.bg), ";")
		if got != tc.want {
			t.Errorf("colorCodes(%d, bg=%v) = %q, want %q", tc.c, tc.bg, got, tc.want)
		}
	}
}
