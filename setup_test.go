package main

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestWireSettingsAddsAndIsIdempotent(t *testing.T) {
	exe := "/usr/local/bin/cmanager"

	// Existing settings with an unrelated hook and the OLD bash cmanager hook.
	orig := []byte(`{
	  "model": "opus",
	  "hooks": {
	    "PreToolUse": [],
	    "Notification": [{"hooks":[{"type":"command","command":"bash /home/u/.claude/cmanager/cmanager-hook.sh"}]}]
	  }
	}`)

	out, changed := wireSettings(orig, exe)
	if !changed {
		t.Fatal("expected a change")
	}

	var root map[string]any
	if err := json.Unmarshal(out, &root); err != nil {
		t.Fatalf("output not valid JSON: %v", err)
	}
	if root["model"] != "opus" {
		t.Errorf("unrelated keys must be preserved, got model=%v", root["model"])
	}
	hooks := root["hooks"].(map[string]any)
	for _, ev := range hookEvents {
		groups, _ := hooks[ev].([]any)
		if len(groups) == 0 {
			t.Fatalf("%s not wired", ev)
		}
		// The stale bash hook must be gone; exactly our command remains.
		joined := mustJSON(t, groups)
		if strings.Contains(joined, "cmanager-hook.sh") {
			t.Errorf("%s: stale bash hook not removed: %s", ev, joined)
		}
		if !strings.Contains(joined, exe+" hook") {
			t.Errorf("%s: our command missing: %s", ev, joined)
		}
	}

	// Running again on the result must be a no-op (idempotent).
	if _, changed2 := wireSettings(out, exe); changed2 {
		t.Error("second run should be idempotent")
	}
}

func TestWireTmuxAddsThenReplaces(t *testing.T) {
	exe := "/usr/local/bin/cmanager"

	out, changed := wireTmux("set -g mouse on\n", exe)
	if !changed || !strings.Contains(out, tmuxMarkerStart) || !strings.Contains(out, exe+" pick") {
		t.Fatalf("block not added: %q", out)
	}
	if !strings.Contains(out, "set -g mouse on") {
		t.Error("existing config must be preserved")
	}

	// Idempotent: same exe → no change.
	if _, changed2 := wireTmux(out, exe); changed2 {
		t.Error("second run with same exe should be idempotent")
	}

	// New exe path → block updated in place, not duplicated.
	out3, changed3 := wireTmux(out, "/opt/cmanager")
	if !changed3 {
		t.Fatal("expected update for new exe")
	}
	if strings.Count(out3, tmuxMarkerStart) != 1 {
		t.Errorf("marker block duplicated: %q", out3)
	}
	if !strings.Contains(out3, "/opt/cmanager pick") {
		t.Error("exe path not updated")
	}
}

func mustJSON(t *testing.T, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}
