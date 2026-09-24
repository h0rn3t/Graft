package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/h0rn3t/Graft/internal/jsonjs"
	"github.com/h0rn3t/Graft/internal/telemetry"
)

func openHookTelemetry(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("GRAFT_POSTHOG_KEY", "phc_test")
	t.Setenv("GRAFT_POSTHOG_HOST", "http://127.0.0.1:1")
	t.Setenv("DO_NOT_TRACK", "")
	for _, name := range telemetry.CIEnvVars {
		t.Setenv(name, "")
		if err := os.Unsetenv(name); err != nil {
			t.Fatalf("os.Unsetenv(%q) error = %v, want nil", name, err)
		}
	}
	return home
}

func writeHookSessionAt(t *testing.T, root, id string, session sessionState, modified time.Time) {
	t.Helper()
	if err := writeHookSession(root, id, session); err != nil {
		t.Fatalf("writeHookSession(%q, %q) error = %v, want nil", root, id, err)
	}
	if err := os.Chtimes(filepath.Join(hookSessionDir(root), id+".json"), modified, modified); err != nil {
		t.Fatalf("os.Chtimes(%q) error = %v, want nil", id, err)
	}
}

func TestFlushClosedHookSessions(t *testing.T) {
	home := openHookTelemetry(t)
	root := t.TempDir()
	old := time.Now().Add(-hookSessionIdle - time.Minute)
	writeHookSessionAt(t, root, "closed", sessionState{
		PerAgentQuery: map[string]string{}, InjectedPointers: []string{},
		GraftReads: 56, SourceReads: 12, SavedTokens: 7400,
		GraftTurns: new(9), ReportedTurns: new(3), LastQuery: new("private query text"),
	}, old)
	writeHookSessionAt(t, root, "live", sessionState{PerAgentQuery: map[string]string{}, InjectedPointers: []string{}}, time.Now())

	if got := flushClosedHookSessions(root, time.Now(), home, "claude-code"); got != 1 {
		t.Fatalf("flushClosedHookSessions() = %d, want 1", got)
	}
	queued := telemetry.Peek(home)
	if len(queued) != 1 {
		t.Fatalf("telemetry.Peek() = %d events, want 1", len(queued))
	}
	event, ok := queued[0].(*jsonjs.Object)
	if !ok {
		t.Fatalf("telemetry.Peek()[0] = %T, want *jsonjs.Object", queued[0])
	}
	properties, ok := event.Get("properties")
	if !ok {
		t.Fatal("session_summary event has no properties")
	}
	summary, ok := properties.(*jsonjs.Object)
	if !ok {
		t.Fatalf("session_summary properties = %T, want *jsonjs.Object", properties)
	}
	for key, want := range map[string]string{
		"event":                 "session_summary",
		"graft_reads_bucket":    "50-199",
		"source_reads_bucket":   "5-19",
		"saved_tokens_bucket":   "5-20k",
		"graft_turns_bucket":    "5-19",
		"reported_turns_bucket": "1-4",
		"agent_host":            "claude-code",
	} {
		got, present := summary.Get(key)
		if key == "event" {
			got, present = event.Get(key)
		}
		if !present || got != want {
			t.Errorf("session_summary %s = %v, want %q", key, got, want)
		}
	}
	if _, present := summary.Get("node_major"); !present {
		t.Error("session_summary node_major is absent")
	}
	for _, raw := range []string{"graft_reads", "source_reads", "saved_tokens", "graft_turns", "reported_turns"} {
		if _, present := summary.Get(raw); present {
			t.Errorf("session_summary contains raw property %q", raw)
		}
	}
	if line := jsonjs.Stringify(summary, 0); strings.Contains(line, "private query text") {
		t.Errorf("session_summary properties = %s, want no private query text", line)
	}
	closed := readHookSession(root, "closed")
	if closed.Summarized == nil || !*closed.Summarized {
		t.Errorf("readHookSession(closed).Summarized = %#v, want true", closed.Summarized)
	}
	if live := readHookSession(root, "live"); live.Summarized != nil {
		t.Errorf("readHookSession(live).Summarized = %#v, want nil", live.Summarized)
	}
	if got := flushClosedHookSessions(root, time.Now(), home, "claude-code"); got != 0 {
		t.Errorf("second flushClosedHookSessions() = %d, want 0", got)
	}
}

func TestSummarizeHookSessionUsesHostStampAndForce(t *testing.T) {
	home := openHookTelemetry(t)
	root := t.TempDir()
	writeHookSessionAt(t, root, "cursor", sessionState{
		PerAgentQuery: map[string]string{}, InjectedPointers: []string{},
		GraftReads: 8, SourceReads: 2, SavedTokens: 7400, Host: new("cursor"),
	}, time.Now())
	if got := flushClosedHookSessions(root, time.Now(), home, "claude-code"); got != 0 {
		t.Fatalf("flushClosedHookSessions(fresh) = %d, want 0", got)
	}
	if got := summarizeHookSession(root, "cursor", home, "claude-code"); got != 1 {
		t.Fatalf("summarizeHookSession(force) = %d, want 1", got)
	}
	queued := telemetry.Peek(home)
	if len(queued) != 1 || !strings.Contains(jsonjs.Stringify(queued[0], 0), `"agent_host":"cursor"`) {
		t.Errorf("telemetry.Peek(force) = %s, want cursor event", queued)
	}
	if got := summarizeHookSession(root, "cursor", home, "claude-code"); got != 0 {
		t.Errorf("summarizeHookSession(repeat) = %d, want 0", got)
	}
}

func TestFlushClosedHookSessionsDisabled(t *testing.T) {
	home := openHookTelemetry(t)
	telemetry.SetEnabled(home, false)
	root := t.TempDir()
	writeHookSessionAt(t, root, "closed", sessionState{PerAgentQuery: map[string]string{}, InjectedPointers: []string{}}, time.Now().Add(-hookSessionIdle-time.Minute))
	if got := flushClosedHookSessions(root, time.Now(), home, "claude-code"); got != 0 {
		t.Errorf("flushClosedHookSessions(disabled) = %d, want 0", got)
	}
	if queued := telemetry.Peek(home); len(queued) != 0 {
		t.Errorf("telemetry.Peek(disabled) = %d events, want 0", len(queued))
	}
	if session := readHookSession(root, "closed"); session.Summarized != nil {
		t.Errorf("readHookSession(disabled).Summarized = %#v, want nil", session.Summarized)
	}
}
