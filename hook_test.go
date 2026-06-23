package main

import (
	"strings"
	"testing"
)

// withFakeTmux points the registry at a temp dir, fakes being inside tmux, and
// captures every tmux command line. paneIsActive queries return "00" (inactive)
// so notifications fire.
func withFakeTmux(t *testing.T) *[]string {
	t.Helper()
	baseDirOverride = t.TempDir()
	t.Cleanup(func() { baseDirOverride = "" })
	t.Setenv("TMUX", "/tmp/fake,1,0")

	var calls []string
	prev := tmuxRunner
	tmuxRunner = func(args ...string) (string, error) {
		calls = append(calls, strings.Join(args, " "))
		if len(args) >= 1 && args[0] == "display-message" && contains(args, "#{pane_active}#{window_active}") {
			return "00", nil
		}
		return "", nil
	}
	t.Cleanup(func() { tmuxRunner = prev })
	return &calls
}

func contains(ss []string, want string) bool {
	for _, s := range ss {
		if s == want {
			return true
		}
	}
	return false
}

func calledWith(calls []string, substr string) bool {
	for _, c := range calls {
		if strings.Contains(c, substr) {
			return true
		}
	}
	return false
}

func TestHookNotificationThenStop(t *testing.T) {
	calls := withFakeTmux(t)

	handleHook(hookEvent{HookEventName: "Notification", SessionID: "s1", Cwd: "/home/u/proj", Message: "Approve edit?"}, "%4", 1000)

	rec, ok := loadSessionRec("s1")
	if !ok || rec.Pane != "%4" || !rec.Needs {
		t.Fatalf("after Notification: %+v ok=%v", rec, ok)
	}
	if !calledWith(*calls, "set-option -t %4 -w @ai_status needs") {
		t.Errorf("expected attention set, calls=%v", *calls)
	}
	if !calledWith(*calls, "display-message ⚠") {
		t.Errorf("expected needs toast, calls=%v", *calls)
	}

	handleHook(hookEvent{HookEventName: "Stop", SessionID: "s1", Cwd: "/home/u/proj"}, "%4", 2000)
	rec, _ = loadSessionRec("s1")
	if rec.Needs {
		t.Errorf("Stop should clear Needs: %+v", rec)
	}
	if !calledWith(*calls, "set-option -t %4 -wu @ai_status") {
		t.Errorf("expected attention cleared, calls=%v", *calls)
	}
	if !calledWith(*calls, "display-message ✓") {
		t.Errorf("expected finished toast, calls=%v", *calls)
	}
}

func TestHookIdleNudgeIsNotNeeds(t *testing.T) {
	calls := withFakeTmux(t)
	handleHook(hookEvent{HookEventName: "Notification", SessionID: "s4", Cwd: "/p",
		Message: "Claude is waiting for your input"}, "%9", 1000)

	rec, _ := loadSessionRec("s4")
	if rec.Needs {
		t.Errorf("idle nudge must not mark needs-attention: %+v", rec)
	}
	if calledWith(*calls, "display-message ⚠") {
		t.Errorf("idle nudge must not post a needs toast, calls=%v", *calls)
	}
}

func TestHookStopHookActiveIgnored(t *testing.T) {
	calls := withFakeTmux(t)
	handleHook(hookEvent{HookEventName: "Stop", SessionID: "s2", StopHookActive: true}, "%1", 1000)
	if calledWith(*calls, "display-message") {
		t.Errorf("intermediate Stop must not notify, calls=%v", *calls)
	}
}

func TestHookSessionEndRemoves(t *testing.T) {
	withFakeTmux(t)
	handleHook(hookEvent{HookEventName: "SessionStart", SessionID: "s3"}, "%2", 1000)
	if _, ok := loadSessionRec("s3"); !ok {
		t.Fatal("SessionStart should record")
	}
	handleHook(hookEvent{HookEventName: "SessionEnd", SessionID: "s3"}, "%2", 2000)
	if _, ok := loadSessionRec("s3"); ok {
		t.Error("SessionEnd should remove record")
	}
}
