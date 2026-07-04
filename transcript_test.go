package main

import (
	"testing"
	"time"
)

// limitLine mirrors the real synthetic assistant entry Claude Code writes when
// a usage limit is hit (error + apiErrorStatus 429 + human-readable text).
func limitLine(text, ts string) string {
	return `{"type":"assistant","timestamp":"` + ts + `","error":"rate_limit","isApiErrorMessage":true,"apiErrorStatus":429,` +
		`"message":{"role":"assistant","model":"<synthetic>","content":[{"type":"text","text":"` + text + `"}]}}`
}

func userLine(text string) string {
	return `{"type":"user","message":{"role":"user","content":"` + text + `"}}`
}

func TestParseLimitResetClock(t *testing.T) {
	berlin, _ := time.LoadLocation("Europe/Berlin")
	ref := time.Date(2026, 7, 3, 20, 32, 1, 0, time.UTC) // 22:32 in Berlin

	got := parseLimitReset("You've hit your session limit · resets 10:50pm (Europe/Berlin)", ref)
	want := time.Date(2026, 7, 3, 22, 50, 0, 0, berlin)
	if !got.Equal(want) {
		t.Errorf("same-day reset: got %v, want %v", got, want)
	}

	// A wall time already past at ref rolls to the next day.
	got = parseLimitReset("You've hit your session limit · resets 10:40am (Europe/Berlin)", ref)
	want = time.Date(2026, 7, 4, 10, 40, 0, 0, berlin)
	if !got.Equal(want) {
		t.Errorf("next-day reset: got %v, want %v", got, want)
	}

	// 12am/12pm map to 0/12 hours.
	got = parseLimitReset("resets limit resets 12am (UTC)", ref)
	want = time.Date(2026, 7, 4, 0, 0, 0, 0, time.UTC)
	if !got.Equal(want) {
		t.Errorf("12am: got %v, want %v", got, want)
	}
}

func TestParseLimitResetEpoch(t *testing.T) {
	got := parseLimitReset("Claude AI usage limit reached|1751600000", time.Now())
	if !got.Equal(time.Unix(1751600000, 0)) {
		t.Errorf("epoch reset: got %v", got)
	}
}

func TestParseLimitResetIgnoresServerRateLimit(t *testing.T) {
	got := parseLimitReset("API Error: Server is temporarily limiting requests (not your usage limit) · Rate limited", time.Now())
	if !got.IsZero() {
		t.Errorf("server rate limit should not parse as a usage limit, got %v", got)
	}
}

func TestScanTailLimited(t *testing.T) {
	now := time.Date(2026, 7, 3, 20, 32, 5, 0, time.UTC)
	data := userLine("fix the tests") + "\n" +
		limitLine("You've hit your session limit · resets 10:50pm (Europe/Berlin)", "2026-07-03T20:32:01.458Z") + "\n"

	prompt, reset := scanTail([]byte(data), now)
	if prompt != "fix the tests" {
		t.Errorf("prompt: got %q", prompt)
	}
	berlin, _ := time.LoadLocation("Europe/Berlin")
	want := time.Date(2026, 7, 3, 22, 50, 0, 0, berlin)
	if !reset.Equal(want) {
		t.Errorf("reset: got %v, want %v", reset, want)
	}
}

func TestScanTailLimitClearedByNewerMessage(t *testing.T) {
	// Once anything follows the limit error, the session has moved on.
	data := limitLine("You've hit your session limit · resets 10:50pm (Europe/Berlin)", "2026-07-03T20:32:01.458Z") + "\n" +
		userLine("continue") + "\n"

	prompt, reset := scanTail([]byte(data), time.Now())
	if prompt != "continue" {
		t.Errorf("prompt: got %q", prompt)
	}
	if !reset.IsZero() {
		t.Errorf("reset should be cleared by a newer message, got %v", reset)
	}
}

func TestScanTailSkipsNonMessageLines(t *testing.T) {
	// Trailing summary/system lines must not mask the limit error.
	data := limitLine("You've hit your session limit · resets 10:50pm (Europe/Berlin)", "2026-07-03T20:32:01.458Z") + "\n" +
		`{"type":"summary","summary":"some summary","leafUuid":"x"}` + "\n"

	_, reset := scanTail([]byte(data), time.Now())
	if reset.IsZero() {
		t.Error("summary line after the error should not clear the limit")
	}
}
