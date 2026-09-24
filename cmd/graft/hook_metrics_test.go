package main

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/h0rn3t/Graft/internal/savings"
)

func TestClassifyHookToolUse(t *testing.T) {
	tests := []struct {
		name    string
		tool    string
		command string
		want    string
	}{
		{name: "canonical MCP", tool: "graft_find_code", want: hookToolGraft},
		{name: "colon MCP", tool: "MCP:graft_find_code", want: hookToolGraft},
		{name: "host MCP", tool: "mcp__graft__graft_find_code", want: hookToolGraft},
		{name: "repo map MCP", tool: "graft_repo_map", want: hookToolGraft},
		{name: "read", tool: "Read", want: hookToolSource},
		{name: "grep", tool: "Grep", want: hookToolSource},
		{name: "glob", tool: "Glob", want: hookToolSource},
		{name: "search", tool: "Search", want: hookToolSource},
		{name: "graft shell", tool: "Bash", command: `graft ask "how does auth work"`, want: hookToolGraft},
		{name: "npx shell", tool: "Shell", command: "npx -y @nanonets/graft callers foo", want: hookToolGraft},
		{name: "ordinary shell", tool: "Bash", command: "ls -la"},
		{name: "substring path", tool: "Bash", command: "./mygraft/run.sh"},
		{name: "MCP lookalike", tool: "mygraft_search"},
		{name: "MCP wrong server", tool: "mcp__other__graft_helper"},
		{name: "write", tool: "Write"},
		{name: "empty", tool: ""},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := classifyHookToolUse(test.tool, test.command); got != test.want {
				t.Errorf("classifyHookToolUse(%q, %q) = %q, want %q", test.tool, test.command, got, test.want)
			}
		})
	}
	if !isMCPToolName("MCP:graft_find_code") || !isMCPToolName("mcp__graft__graft_find_code") || isMCPToolName("Read") || isMCPToolName("Shell") {
		t.Error("isMCPToolName() did not preserve the server-prefix contract")
	}
}

func TestParseHookSavings(t *testing.T) {
	if got := parseHookSavings("nothing here"); got != 0 {
		t.Errorf("parseHookSavings(no footer) = %d, want 0", got)
	}
	got := parseHookSavings("a\n[graft] tokens saved ≈ 2,181 (89%) — …\nb\n[graft] tokens saved ≈ 1000 — …")
	if got != 3181 {
		t.Errorf("parseHookSavings(multiple) = %d, want 3181", got)
	}
}

func TestRecordHookToolUse(t *testing.T) {
	root := t.TempDir()
	for _, use := range []hookToolUse{
		{Kind: hookToolGraft, SavedTokens: 500},
		{Kind: hookToolGraft},
		{Kind: hookToolSource},
	} {
		if err := recordHookToolUse(root, "s1", use); err != nil {
			t.Fatalf("recordHookToolUse(%#v) error = %v, want nil", use, err)
		}
	}
	session := readHookSession(root, "s1")
	if session.GraftReads != 2 || session.SourceReads != 1 || session.SavedTokens != 500 || session.TurnUsedGraft == nil || !*session.TurnUsedGraft {
		t.Errorf("readHookSession(after uses) = %#v, want counters and turn flag", session)
	}

	empty := t.TempDir()
	if err := recordHookToolUse(empty, "s1", hookToolUse{}); err != nil {
		t.Fatalf("recordHookToolUse(no-op) error = %v, want nil", err)
	}
	if _, err := os.Stat(filepath.Join(hookSessionDir(empty), "s1.json")); !os.IsNotExist(err) {
		t.Errorf("recordHookToolUse(no-op) created a session file, stat error = %v", err)
	}

	if err := recordHookToolUse(root, "s1", hookToolUse{Kind: hookToolGraft, Host: "cursor"}); err != nil {
		t.Fatalf("recordHookToolUse(host) error = %v, want nil", err)
	}
	if err := recordHookToolUse(root, "s1", hookToolUse{Kind: hookToolSource, Host: "claude-code"}); err != nil {
		t.Fatalf("recordHookToolUse(second host) error = %v, want nil", err)
	}
	session = readHookSession(root, "s1")
	if session.Host == nil || *session.Host != "cursor" {
		t.Errorf("readHookSession(host) = %#v, want first host retained", session.Host)
	}
}

func TestLatestHookSessionAndStats(t *testing.T) {
	root := t.TempDir()
	if _, _, ok := latestHookSession(root); ok {
		t.Fatal("latestHookSession(empty) reported a session")
	}
	writeHookStatsTestSession(t, root, "old", sessionState{GraftReads: 1, SourceReads: 9}, time.Now().Add(-time.Minute))
	writeHookStatsTestSession(t, root, "new", sessionState{GraftReads: 8, SourceReads: 2}, time.Now())
	id, session, ok := latestHookSession(root)
	if !ok || id != "new" || session.GraftReads != 8 {
		t.Errorf("latestHookSession() = (%q, %#v, %t), want new", id, session, ok)
	}

	query := "where is auth"
	session.LastQuery = &query
	session.SavedTokens = 12_345
	session.InputCostMicros = new(600_000)
	session.InputTokensBilled = new(1_000_000)
	got := formatHookSessionStats(id, &session)
	for _, want := range []string{"session new", "graft reads:   8", "source reads:  2", "80% graft", "12,345", "~<$0.01", "where is auth"} {
		if !strings.Contains(got, want) {
			t.Errorf("formatHookSessionStats() = %q, want substring %q", got, want)
		}
	}
	if got := formatHookSessionStats("", nil); !strings.Contains(got, "no session recorded yet") {
		t.Errorf("formatHookSessionStats(nil) = %q, want empty state", got)
	}
}

func TestHookPriceTable(t *testing.T) {
	tests := []struct {
		model string
		want  float64
		ok    bool
	}{
		{model: "claude-opus-5", want: 5, ok: true},
		{model: "claude-opus-4-8", want: 5, ok: true},
		{model: "claude-sonnet-5", want: 2, ok: true},
		{model: "claude-sonnet-4-6", want: 3, ok: true},
		{model: "claude-haiku-4-5", want: 1, ok: true},
		{model: "claude-fable-5-1", want: 10, ok: true},
		{model: "gpt-5"},
	}
	for _, test := range tests {
		got, ok := inputUSDPerMtok(test.model)
		if got != test.want || ok != test.ok {
			t.Errorf("inputUSDPerMtok(%q) = (%v, %t), want (%v, %t)", test.model, got, ok, test.want, test.ok)
		}
	}
	fresh := hookUsage{Model: "claude-opus-5", Input: 1_000_000}
	if got, ok := turnInputCostMicros(fresh); !ok || got != 5_000_000 {
		t.Errorf("turnInputCostMicros(fresh) = (%d, %t), want (5000000, true)", got, ok)
	}
	cacheWrite := hookUsage{Model: "claude-opus-5", CacheCreate: 1_000_000}
	if got, ok := turnInputCostMicros(cacheWrite); !ok || got != 6_250_000 {
		t.Errorf("turnInputCostMicros(cache write) = (%d, %t), want (6250000, true)", got, ok)
	}
	cacheRead := hookUsage{Model: "claude-opus-5", CacheRead: 1_000_000}
	if got, ok := turnInputCostMicros(cacheRead); !ok || got != 500_000 {
		t.Errorf("turnInputCostMicros(cache read) = (%d, %t), want (500000, true)", got, ok)
	}
	if _, ok := turnInputCostMicros(hookUsage{Model: "future", Input: 100}); ok {
		t.Error("turnInputCostMicros(unpriced model) reported a cost")
	}
	if got := turnInputTokens(hookUsage{Input: 10, CacheCreate: 20, CacheRead: 30}); got != 60 {
		t.Errorf("turnInputTokens() = %d, want 60", got)
	}
	if got := savings.FormatDollars(0.005); got != "<$0.01" {
		t.Errorf("savings.FormatDollars(0.005) = %q, want <$0.01", got)
	}
}

func writeHookStatsTestSession(t *testing.T, root, id string, session sessionState, modified time.Time) {
	t.Helper()
	if err := writeHookSession(root, id, session); err != nil {
		t.Fatalf("writeHookSession(%q, %q) error = %v, want nil", root, id, err)
	}
	if !modified.IsZero() {
		path := filepath.Join(hookSessionDir(root), id+".json")
		if err := os.Chtimes(path, modified, modified); err != nil {
			t.Fatalf("os.Chtimes(%q) error = %v, want nil", path, err)
		}
	}
}

func TestHookSessionPointerOrder(t *testing.T) {
	root := t.TempDir()
	older := sessionState{PerAgentQuery: map[string]string{}, InjectedPointers: []string{"old"}}
	newer := sessionState{PerAgentQuery: map[string]string{}, InjectedPointers: []string{"new"}}
	writeHookStatsTestSession(t, root, "older", older, time.Now().Add(-time.Minute))
	writeHookStatsTestSession(t, root, "newer", newer, time.Now())
	_, session, ok := latestHookSession(root)
	if !ok || !slices.Equal(session.InjectedPointers, []string{"new"}) {
		t.Errorf("latestHookSession() = (%#v, %t), want newer pointer", session, ok)
	}
}
