package main

import (
	"bytes"
	"fmt"
	"io"
	"maps"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/h0rn3t/Graft/internal/graph"
)

func TestHookPostEditBlastRadiusIsNotRepeated(t *testing.T) {
	root := t.TempDir()
	t.Setenv("GRAFT_DIR", "")
	t.Setenv("GRAFT_NO_REFRESH", "1")
	writeFixtureFile(t, root, "pkg/lib.go", "package pkg\n\nfunc Helper() {}\n")
	writeFixtureFile(t, root, "pkg/main.go", "package pkg\n\nfunc Run() { Helper() }\n")
	build := func() {
		t.Helper()
		if status := runBuild(callersOptions{command: "build", root: root, rootSet: true}, io.Discard, io.Discard); status != 0 {
			t.Fatalf("runBuild(%q) status = %d, want 0", root, status)
		}
	}
	build()
	edit := func(agentID string) string {
		t.Helper()
		input := hookInput{"tool_input": map[string]any{"file_path": root + "/pkg/lib.go"}}
		if agentID != "" {
			input["agent_id"] = agentID
		}
		var out bytes.Buffer
		handleHookPostEdit(t.Context(), input, root, &out, io.Discard)
		return out.String()
	}

	if got := edit(""); !strings.Contains(got, "blast radius for lib.go") || !strings.Contains(got, "Run") {
		t.Fatalf("handleHookPostEdit(first edit) = %q, want the blast radius naming Run", got)
	}
	if got := edit(""); got != "" {
		t.Errorf("handleHookPostEdit(repeat edit, same dependents) = %q, want nothing", got)
	}
	if got := edit("subagent-1"); !strings.Contains(got, "blast radius for lib.go") {
		t.Errorf("handleHookPostEdit(another agent context) = %q, want the blast radius", got)
	}
	writeFixtureFile(t, root, "pkg/other.go", "package pkg\n\nfunc Other() { Helper() }\n")
	build()
	if got := edit(""); !strings.Contains(got, "Other") {
		t.Errorf("handleHookPostEdit(after dependents changed) = %q, want the blast radius naming Other", got)
	}
}

func TestHookBlastSeenEvictsOldest(t *testing.T) {
	session := emptySessionState()
	if hookBlastSeen(&session, "ctx\x00a.go", "h1") {
		t.Fatal("hookBlastSeen(first a.go) = true, want false")
	}
	if !hookBlastSeen(&session, "ctx\x00a.go", "h1") {
		t.Error("hookBlastSeen(a.go, same hash) = false, want true")
	}
	if hookBlastSeen(&session, "ctx\x00a.go", "h2") {
		t.Error("hookBlastSeen(a.go, changed hash) = true, want false")
	}
	for i := range hookBlastShownLimit {
		hookBlastSeen(&session, fmt.Sprintf("ctx\x00f%d.go", i), "h")
	}
	if len(session.BlastShown) != hookBlastShownLimit {
		t.Errorf("len(BlastShown) after %d more files = %d, want %d", hookBlastShownLimit, len(session.BlastShown), hookBlastShownLimit)
	}
	if hookBlastSeen(&session, "ctx\x00a.go", "h2") {
		t.Error("hookBlastSeen(a.go after eviction) = true, want false")
	}
}

func TestHookPromptInlinesOnlyAStrongTopHit(t *testing.T) {
	coverage := func(value float64) *float64 { return &value }
	hits := func() []graph.AskHit {
		return []graph.AskHit{
			{Kind: "symbol", Title: "verify · function", Pointer: "src/auth.ts:L1-L4", Snippet: "function verify()", Code: "function verify() {\n  return true;\n}", Score: 1},
			{Kind: "symbol", Title: "sign · function", Pointer: "src/auth.ts:L6-L9", Snippet: "function sign()", Code: "function sign() {}", Score: 0.8},
			{Kind: "symbol", Title: "hash · function", Pointer: "src/hash.ts:L1-L3", Snippet: "function hash()", Code: "function hash() {}", Score: 0.5},
		}
	}

	session := emptySessionState()
	strong := graph.AskResult{Mode: "lexical", Hits: hits(), Coverage: coverage(1), CoverageStrong: coverage(1)}
	got := relevantHookRetrieval(&strong, &session, 3, "name:")
	if !strings.Contains(got, "```\nfunction verify() {") {
		t.Errorf("relevantHookRetrieval(strong top) = %q, want the top hit's code inlined", got)
	}
	for _, want := range []string{" 2. sign · function: src/auth.ts:L6-L9 — function sign()", " 3. hash · function: src/hash.ts:L1-L3 — function hash()"} {
		if !strings.Contains(got, want) {
			t.Errorf("relevantHookRetrieval(strong top) = %q, want pointer line %q", got, want)
		}
	}
	if strings.Contains(got, "function sign() {}") || strings.Contains(got, "function hash() {}") {
		t.Errorf("relevantHookRetrieval(strong top) = %q, want no code for the other hits", got)
	}

	session = emptySessionState()
	moderate := graph.AskResult{Mode: "lexical", Hits: hits(), Coverage: coverage(0.8), CoverageStrong: coverage(0.3)}
	got = relevantHookRetrieval(&moderate, &session, 3, "name:")
	if strings.Contains(got, "```") || !strings.Contains(got, " 1. verify · function: src/auth.ts:L1-L4 — function verify()") {
		t.Errorf("relevantHookRetrieval(moderate top) = %q, want pointer lines only", got)
	}
	// The same top hit, strong later, is delivered again with its code.
	got = relevantHookRetrieval(&strong, &session, 3, "name:")
	if !strings.Contains(got, "```\nfunction verify() {") {
		t.Errorf("relevantHookRetrieval(strong after pointer) = %q, want the code delivered", got)
	}
}

func TestHookStopCountsTranscriptToolCalls(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(t.TempDir(), "session.jsonl")
	toolUse := func(uuid string, uses ...string) string {
		content := make([]string, 0, len(uses))
		for _, use := range uses {
			name, command, _ := strings.Cut(use, " ")
			content = append(content, fmt.Sprintf(`{"type":"tool_use","id":"%s-%s","name":%q,"input":{"command":%q}}`, uuid, name, name, command))
		}
		return fmt.Sprintf(`{"type":"assistant","uuid":%q,"message":{"role":"assistant","content":[%s]}}`, uuid, strings.Join(content, ","))
	}
	appendLine := func(text string) {
		t.Helper()
		file, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = file.Close() }()
		if _, err := file.WriteString(text); err != nil {
			t.Fatal(err)
		}
	}
	stop := func(extra hookInput) sessionState {
		t.Helper()
		input := hookInput{"session_id": "s1", "transcript_path": path, "hook_event_name": "Stop"}
		maps.Copy(input, extra)
		handleHookStop(input, root)
		return readHookSession(root, "s1")
	}

	appendLine(toolUse("old", "Read") + "\n")
	seedHookTranscriptOffset(root, "s1", path)
	appendLine(`{"type":"user","message":{"role":"user","content":"q"}}` + "\n")
	appendLine(toolUse("a1", "Read", "Bash graft ask \"auth\"", "mcp__graft__graft_find_code", "Grep", "Edit") + "\n")
	appendLine(`{"type":"assistant","uuid":"a1-text","message":{"role":"assistant","content":[{"type":"text","text":"done"}]}}` + "\n")
	if got := stop(nil); got.GraftReads != 2 || got.SourceReads != 2 || got.GraftTurns == nil || *got.GraftTurns != 1 {
		t.Errorf("handleHookStop(new turn) session = graft %d, source %d, graftTurns %v; want 2, 2, 1 (history before session start not counted)", got.GraftReads, got.SourceReads, got.GraftTurns)
	}
	if got := stop(nil); got.GraftReads != 2 || got.SourceReads != 2 {
		t.Errorf("handleHookStop(no new entries) session = graft %d, source %d; want 2, 2", got.GraftReads, got.SourceReads)
	}
	partial := toolUse("a2", "Read")
	appendLine(partial[:len(partial)/2])
	if got := stop(nil); got.SourceReads != 2 {
		t.Errorf("handleHookStop(partial trailing line) sourceReads = %d, want 2", got.SourceReads)
	}
	appendLine(partial[len(partial)/2:] + "\n")
	if got := stop(nil); got.SourceReads != 3 {
		t.Errorf("handleHookStop(completed line) sourceReads = %d, want 3", got.SourceReads)
	}
	if got := stop(hookInput{"hook_event_name": "SubagentStop", "agent_transcript_path": ""}); got.SourceReads != 3 || got.GraftReads != 2 {
		t.Errorf("handleHookStop(SubagentStop without agent transcript) session = graft %d, source %d; want 2, 3", got.GraftReads, got.SourceReads)
	}
	if err := os.WriteFile(path, []byte(toolUse("b1", "Glob")+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := stop(nil); got.SourceReads != 4 {
		t.Errorf("handleHookStop(shrunk transcript) sourceReads = %d, want 4", got.SourceReads)
	}
}

func TestHookSubagentStopCountsAgentTranscript(t *testing.T) {
	root := t.TempDir()
	dir := t.TempDir()
	main := filepath.Join(dir, "main.jsonl")
	agent := filepath.Join(dir, "agent.jsonl")
	writeFixture := func(path, text string) {
		t.Helper()
		if err := os.WriteFile(path, []byte(text), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	writeFixture(main, "")
	writeFixture(agent, `{"type":"assistant","message":{"role":"assistant","content":[{"type":"tool_use","name":"mcp__graft__graft_find_all","input":{}}]}}`+"\n")
	input := hookInput{"session_id": "s2", "transcript_path": main, "agent_transcript_path": agent, "hook_event_name": "SubagentStop"}
	handleHookStop(input, root)
	handleHookStop(input, root)
	if got := readHookSession(root, "s2"); got.GraftReads != 1 {
		t.Errorf("handleHookStop(SubagentStop twice) graftReads = %d, want 1", got.GraftReads)
	}
}

func TestHookToolUseKeepsNoCounts(t *testing.T) {
	root := t.TempDir()
	for _, input := range []hookInput{
		{"session_id": "s3", "tool_name": "Read", "tool_input": map[string]any{"file_path": "a.go"}},
		{"session_id": "s3", "tool_name": "mcp__graft__graft_find_code", "tool_input": map[string]any{"query": "x"}},
		{"session_id": "s3", "tool_name": "Bash", "tool_input": map[string]any{"command": "graft ask x"}},
	} {
		handleHookToolUse(input, root, io.Discard)
	}
	if got := readHookSession(root, "s3"); got.GraftReads != 0 || got.SourceReads != 0 {
		t.Errorf("handleHookToolUse(Read, graft MCP, graft CLI) session = graft %d, source %d; want 0, 0", got.GraftReads, got.SourceReads)
	}
}
