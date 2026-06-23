package main

import "testing"

func TestGhosttyTerminalsParsing(t *testing.T) {
	orig := osa
	defer func() { osa = orig }()
	osa = func(string) (string, error) {
		return "id-1\t/Users/x/dev/a\ttmux\n" +
			"id-2\t/Users/x/dev/b\t✳ fix the bug\n" +
			"\n" + // blank line is skipped
			"id-3\t/Users/x/dev/c\t~\n", nil
	}
	terms := ghosttyTerminals()
	if len(terms) != 3 {
		t.Fatalf("want 3 terminals, got %d: %+v", len(terms), terms)
	}
	if terms[1].id != "id-2" || terms[1].cwd != "/Users/x/dev/b" || terms[1].name != "✳ fix the bug" {
		t.Fatalf("bad parse of row 2: %+v", terms[1])
	}
}

func TestGhosttyTerminalsNotRunning(t *testing.T) {
	orig := osa
	defer func() { osa = orig }()
	osa = func(string) (string, error) { return "", nil } // `is running` false → empty
	if terms := ghosttyTerminals(); terms != nil {
		t.Fatalf("want nil when Ghostty not running, got %+v", terms)
	}
}

func TestResolveGhostty(t *testing.T) {
	terms := []ghosttyTerm{
		{id: "shell", cwd: "/dev/a", name: "~"},
		{id: "claude", cwd: "/dev/a", name: "✳ working"}, // same cwd, but the Claude one
		{id: "other", cwd: "/dev/b", name: "✳ elsewhere"},
	}
	cases := []struct {
		cwd, want string
	}{
		{"/dev/a", "claude"}, // ✳-titled surface wins over the plain shell at the same cwd
		{"/dev/b", "other"},  // single match
		{"/dev/none", ""},    // no surface there
		{"", ""},             // no cwd to match on
	}
	for _, c := range cases {
		if got := resolveGhostty(c.cwd, terms); got != c.want {
			t.Errorf("resolveGhostty(%q) = %q, want %q", c.cwd, got, c.want)
		}
	}
}

func TestResolveGhosttyFallbackToPlainShell(t *testing.T) {
	// No ✳ surface at the cwd → fall back to whatever surface is there.
	terms := []ghosttyTerm{{id: "shell", cwd: "/dev/a", name: "~"}}
	if got := resolveGhostty("/dev/a", terms); got != "shell" {
		t.Errorf("want fallback to 'shell', got %q", got)
	}
}
