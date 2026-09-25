package main

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/h0rn3t/Graft/internal/graph"
)

func TestHookPromptDeduplicatesContentPerAgent(t *testing.T) {
	root := t.TempDir()
	ask := hookAsk
	t.Cleanup(func() { hookAsk = ask })
	code := "func verify() bool { return true }"
	sourceHash := "original"
	hookAsk = func(context.Context, string, string, string, string) (graph.AskResult, bool) {
		return graph.AskResult{Hits: []graph.AskHit{{
			Title: "verify", Pointer: "auth.go:L1-L1", Code: code, SourceHash: sourceHash,
		}}}, true
	}
	tests := []struct {
		name       string
		agentID    string
		agentName  string
		code       string
		sourceHash string
		want       bool
	}{
		{name: "first prompt", want: true},
		{name: "same content", want: false},
		{name: "same span changed body", code: "func verify() bool { return false }", want: true},
		{name: "repeated changed body", want: false},
		{name: "body changed outside excerpt", sourceHash: "changed", want: true},
		{name: "same full body revision", want: false},
		{name: "first agent", agentID: "a", agentName: "Explore", want: true},
		{name: "same agent", agentID: "a", agentName: "Explore", want: false},
		{name: "different agent same name", agentID: "b", agentName: "Explore", want: true},
		{name: "named agent fallback", agentName: "Reviewer", want: true},
		{name: "same named agent fallback", agentName: "Reviewer", want: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.code != "" {
				code = tt.code
			}
			if tt.sourceHash != "" {
				sourceHash = tt.sourceHash
			}
			input := hookInput{
				"session_id": "shared", "prompt": "verify the authentication flow",
				"agent_id": tt.agentID, "agent": map[string]any{"name": tt.agentName},
			}
			var out bytes.Buffer
			handleHookPrompt(t.Context(), input, root, &out, io.Discard)
			if got := out.Len() > 0; got != tt.want {
				t.Errorf("handleHookPrompt(agent=%q, name=%q, code=%q) injected = %t, want %t; output=%q", tt.agentID, tt.agentName, code, got, tt.want, out.String())
			}
		})
	}
}

func TestAskHookGraphIncludesBodyRevision(t *testing.T) {
	root := t.TempDir()
	contextDir := hookContextDir(root)
	source := "package auth\nfunc verify() bool {\n" + strings.Repeat("\t// unchanged detail\n", 9) + "\treturn true\n}\n"
	if err := os.WriteFile(filepath.Join(root, "auth.go"), []byte(source), 0o644); err != nil {
		t.Fatalf("os.WriteFile(auth.go) = %v, want nil", err)
	}
	wiring := graph.GraphV1{Nodes: []graph.NodeV1{{
		ID: "auth.go#verify", Name: "verify", Kind: "function", Path: "auth.go", Span: "L2-L13",
		Signature: new("func verify() bool"), BodyHash: "full-body-revision",
	}}}
	if _, err := graph.Write(wiring, contextDir); err != nil {
		t.Fatalf("graph.Write(auth graph) = %v, want nil", err)
	}
	result, ok := askHookGraph(t.Context(), root, contextDir, "verify", "")
	if !ok || len(result.Hits) == 0 {
		t.Fatalf("askHookGraph(verify) = (%#v, %t), want a hit", result, ok)
	}
	hit := result.Hits[0]
	if hit.SourceHash == "" || !strings.Contains(hit.Code, "func verify()") {
		t.Errorf("askHookGraph(verify) hit = %#v, want full body revision and inline code", hit)
	}
	if err := os.WriteFile(filepath.Join(root, "auth.go"), []byte(strings.ReplaceAll(source, "return true", "return false")), 0o644); err != nil {
		t.Fatalf("os.WriteFile(changed auth.go) = %v, want nil", err)
	}
	changed, ok := askHookGraph(t.Context(), root, contextDir, "verify", "")
	if !ok || len(changed.Hits) == 0 {
		t.Fatalf("askHookGraph(changed verify) = (%#v, %t), want a hit", changed, ok)
	}
	if got := changed.Hits[0]; got.SourceHash == hit.SourceHash || got.Code != hit.Code {
		t.Errorf("askHookGraph(edit outside excerpt) hit = %#v, want changed hash and unchanged excerpt %q", got, hit.Code)
	}
}

func TestHookSessionStartPermitsContextReinjection(t *testing.T) {
	root := t.TempDir()
	t.Setenv("CLAUDE_PROJECT_DIR", root)
	ask, home := hookAsk, hookHomeDir
	t.Cleanup(func() { hookAsk, hookHomeDir = ask, home })
	homeDir := t.TempDir()
	hookHomeDir = func() string { return homeDir }
	hookAsk = func(context.Context, string, string, string, string) (graph.AskResult, bool) {
		return graph.AskResult{Hits: []graph.AskHit{{
			Title: "verify", Pointer: "auth.go:L1-L1", Code: "func verify() {}",
		}}}, true
	}
	input := hookInput{"session_id": "compact", "prompt": "verify the authentication flow"}
	handleHookPrompt(t.Context(), input, root, io.Discard, io.Discard)
	if err := updateHookSession(root, "compact", func(session *sessionState) bool {
		session.GraftReads = 7
		session.SearchNudges = 2
		return true
	}); err != nil {
		t.Fatalf("updateHookSession(compact) = %v, want nil", err)
	}
	runHook(t.Context(), "session-start", "", strings.NewReader(`{"session_id":"compact","source":"compact"}`), io.Discard, io.Discard)
	var out bytes.Buffer
	handleHookPrompt(t.Context(), input, root, &out, io.Discard)
	if out.Len() == 0 {
		t.Error("handleHookPrompt(after compact session-start) = empty, want retrieved context")
	}
	session := readHookSession(root, "compact")
	if session.GraftReads != 7 || session.SearchNudges != 2 {
		t.Errorf("readHookSession(after compact) counters = (%d, %d), want (7, 2)", session.GraftReads, session.SearchNudges)
	}
}

func TestHookLegacyPointerStatePermitsContentReinjection(t *testing.T) {
	root := t.TempDir()
	path := hookSessionPath(root, "legacy")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(`{"graftReads":7,"injectedPointers":["auth.go:L1-L1"]}`), 0o644); err != nil {
		t.Fatal(err)
	}
	session := readHookSession(root, "legacy")
	result := graph.AskResult{Hits: []graph.AskHit{{Title: "verify", Pointer: "auth.go:L1-L1", Code: "func verify() {}"}}}
	if got := relevantHookRetrieval(&result, &session, 3, ""); got == "" {
		t.Error("relevantHookRetrieval(legacy pointers without revisions) = empty, want retrieved context")
	}
	if session.GraftReads != 7 {
		t.Errorf("readHookSession(legacy) GraftReads = %d, want 7", session.GraftReads)
	}
}

func TestHookRetrievalHistoryIsBounded(t *testing.T) {
	session := emptySessionState()
	for i := range 45 {
		result := graph.AskResult{Hits: []graph.AskHit{{Title: "verify", Pointer: fmt.Sprintf("auth.go:L%d-L%d", i, i), Code: "func verify() {}"}}}
		if got := relevantHookRetrieval(&result, &session, 3, ""); got == "" {
			t.Fatalf("relevantHookRetrieval(pointer %q) = empty, want retrieved context", result.Hits[0].Pointer)
		}
	}
	result := graph.AskResult{Hits: []graph.AskHit{{Title: "verify", Pointer: "auth.go:L0-L0", Code: "func verify() {}"}}}
	if got := relevantHookRetrieval(&result, &session, 3, ""); got == "" {
		t.Error("relevantHookRetrieval(evicted pointer) = empty, want retrieved context")
	}
	if got := len(session.InjectedPointers); got != 40 {
		t.Errorf("relevantHookRetrieval(46 hits) history length = %d, want 40", got)
	}
	if got := len(session.InjectedRevisions); got != 40 {
		t.Errorf("relevantHookRetrieval(46 hits) revision history length = %d, want 40", got)
	}
}
