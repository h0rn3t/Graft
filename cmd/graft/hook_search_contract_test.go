package main

import (
	"bytes"
	jsonv2 "encoding/json/v2"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/h0rn3t/Graft/internal/graph"
)

// runToolSavingsHook runs the Claude Code PostToolUse hook for one tool call
// and returns the context it adds for the model, or "" when it adds none.
func runToolSavingsHook(t *testing.T, root, session, tool string, toolInput map[string]any) string {
	t.Helper()
	stdin, err := jsonv2.Marshal(map[string]any{"session_id": session, "cwd": root, "tool_name": tool, "tool_input": toolInput, "tool_response": "ok"})
	if err != nil {
		t.Fatal(err)
	}
	var stdout, stderr bytes.Buffer
	if status := runWithInput([]string{"_hook", "tool-savings"}, bytes.NewReader(stdin), &stdout, &stderr); status != 0 {
		t.Fatalf("runWithInput(tool-savings, %s %v) status = %d, want 0; stderr = %q", tool, toolInput, status, stderr.String())
	}
	if stdout.Len() == 0 {
		return ""
	}
	var output struct {
		HookSpecificOutput struct {
			HookEventName     string `json:"hookEventName"`
			AdditionalContext string `json:"additionalContext"`
		} `json:"hookSpecificOutput"`
	}
	if err := jsonv2.Unmarshal(stdout.Bytes(), &output); err != nil || output.HookSpecificOutput.HookEventName != "PostToolUse" {
		t.Fatalf("tool-savings(%s %v) stdout = %q, want PostToolUse context (%v)", tool, toolInput, stdout.String(), err)
	}
	return output.HookSpecificOutput.AdditionalContext
}

func indexedHookRepo(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	wiring := graph.WiringPath(filepath.Join(root, "graft"))
	if err := os.MkdirAll(filepath.Dir(wiring), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(wiring, []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, "internal", "graph"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CLAUDE_PROJECT_DIR", root)
	t.Setenv("GRAFT_DIR", "")
	return root
}

func TestHookSearchNudgeContract(t *testing.T) {
	root := indexedHookRepo(t)
	cases := []struct {
		name, tool string
		input      map[string]any
		want       string
	}{
		{"recursive grep", "Bash", map[string]any{"command": `grep -rn "fooBar" --include='*.go' .`}, `graft_find_all {"pattern":"fooBar"}`},
		{"ripgrep in a subtree", "Bash", map[string]any{"command": `rg -i 'a\.b' internal/graph`}, `graft_find_all {"pattern":"a\\.b","ignore_case":true,"in":"internal/graph"}`},
		{"basic regex alternation", "Bash", map[string]any{"command": `grep -rn 'Foo\|Bar' .`}, `graft_find_all {"pattern":"Foo|Bar"}`},
		{"fixed string cluster", "Bash", map[string]any{"command": `grep -rnF 'a.b' .`}, `graft_find_all {"pattern":"a.b","fixed":true}`},
		{"pattern after -e", "Bash", map[string]any{"command": `grep -r -e needle -- internal`}, `graft_find_all {"pattern":"needle","in":"internal"}`},
		{"after cd", "Bash", map[string]any{"command": `cd internal && grep -rn foo graph`}, `graft_find_all {"pattern":"foo","in":"internal/graph"}`},
		{"git grep", "Bash", map[string]any{"command": `git grep -n Handler`}, `graft_find_all {"pattern":"Handler"}`},
		{"grep tool", "Grep", map[string]any{"pattern": "Foo", "path": filepath.Join(root, "internal"), "-i": true}, `graft_find_all {"pattern":"Foo","ignore_case":true,"in":"internal"}`},
		{"pipeline filter", "Bash", map[string]any{"command": `git log --oneline | grep fix`}, ""},
		{"one file", "Bash", map[string]any{"command": `grep -n foo internal/graph/x.go`}, ""},
		{"outside the repo", "Bash", map[string]any{"command": `grep -rn foo /usr/include`}, ""},
		{"graft cards", "Bash", map[string]any{"command": `grep -rn foo graft/`}, ""},
		{"graft itself", "Bash", map[string]any{"command": `graft grep foo`}, ""},
		{"not a search", "Bash", map[string]any{"command": `go test ./...`}, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := runToolSavingsHook(t, root, "case-"+strings.ReplaceAll(tc.name, " ", "-"), tc.tool, tc.input)
			if tc.want == "" && got != "" || !strings.Contains(got, tc.want) {
				t.Errorf("tool-savings(%s %v) context = %q, want %q", tc.tool, tc.input, got, tc.want)
			}
		})
	}
}

func TestHookSearchNudgeIsCappedPerSession(t *testing.T) {
	root := indexedHookRepo(t)
	input := map[string]any{"command": "grep -rn foo ."}
	for call := 1; call <= hookSearchNudgeLimit+1; call++ {
		got := runToolSavingsHook(t, root, "capped", "Bash", input)
		if want := call <= hookSearchNudgeLimit; (got != "") != want {
			t.Errorf("tool-savings(call %d of one session) context = %q, want context %t", call, got, want)
		}
	}
}

func TestHookSearchNudgeNeedsAGraph(t *testing.T) {
	root := t.TempDir()
	t.Setenv("CLAUDE_PROJECT_DIR", root)
	t.Setenv("GRAFT_DIR", "")
	if got := runToolSavingsHook(t, root, "bare", "Bash", map[string]any{"command": "grep -rn foo ."}); got != "" {
		t.Errorf("tool-savings(grep in a repo without a graph) context = %q, want none", got)
	}
}
