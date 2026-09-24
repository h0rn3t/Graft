package main

import (
	"context"
	"testing"

	"github.com/h0rn3t/Graft/internal/graph"
)

func TestMCPGoParityGoldensMatchGo(t *testing.T) {
	for _, name := range []string{"full-session", "workspace-root", "no-graph", "worktree"} {
		t.Run(name, func(t *testing.T) {
			golden := loadGolden(t, "mcp-go-parity/"+name)
			runtime := newGoldenRuntime(t, golden)
			runGoldenMCP(t, &runtime, golden)
		})
	}
}

func TestMCPServerGoldensMatchGo(t *testing.T) {
	for _, name := range []string{"retrieval-tools", "workspace-refresh", "workspace-federation", "legacy-aliases-built", "legacy-aliases-bare"} {
		t.Run(name, func(t *testing.T) {
			golden := loadGolden(t, "mcp-server/"+name)
			runtime := newGoldenRuntime(t, golden)
			steps := make([]goldenStep, len(golden.Messages))
			for i, message := range golden.Messages {
				object, ok := message.(map[string]any)
				if !ok {
					t.Fatalf("loadGolden(%q) message %d = %#v, want object", name, i, message)
				}
				id, ok := object["id"].(float64)
				if !ok {
					t.Fatalf("loadGolden(%q) message %d id = %#v, want number", name, i, object["id"])
				}
				steps[i] = goldenStep{Send: message, Reply: true, ID: int(id)}
			}
			golden.Steps = steps
			runGoldenMCP(t, &runtime, golden)
		})
	}
}

func TestHostsGoldensMatchGo(t *testing.T) {
	names := goldenNames(t, "hosts")
	first := loadGolden(t, names[0])
	runtime := newGoldenRuntime(t, first)
	for _, name := range names {
		t.Run(name, func(t *testing.T) {
			golden := loadGolden(t, name)
			runtime.runCLI(t, golden)
		})
	}
}

func TestUpkeepGoldensMatchGo(t *testing.T) {
	golden := loadGolden(t, "upkeep/stale-wiring")
	runtime := newGoldenRuntime(t, golden)
	message := golden.Messages[0]
	id := message.(map[string]any)["id"].(float64)
	golden.Steps = []goldenStep{{Send: message, Reply: true, ID: int(id)}}
	runGoldenMCP(t, &runtime, golden)
}

func TestHookGoldensMatchGo(t *testing.T) {
	for _, name := range goldenNames(t, "hooks") {
		t.Run(name, func(t *testing.T) {
			ask, check, startSync := hookAsk, hookCheck, hookStartSync
			t.Cleanup(func() { hookAsk, hookCheck, hookStartSync = ask, check, startSync })
			golden := loadGolden(t, name)
			runtime := newGoldenRuntime(t, golden)
			runtime.normalizeMS = true
			home := hookHomeDir
			hookHomeDir = func() string { return t.TempDir() }
			t.Cleanup(func() { hookHomeDir = home })
			switch name {
			case "hooks/02-prompt":
				hookAsk = func(context.Context, string, string, string, string) (graph.AskResult, bool) {
					coverage := 1.0
					return graph.AskResult{
						Query: "auth verification", Mode: "lexical", Coverage: &coverage, CoverageStrong: &coverage,
						Hits: []graph.AskHit{{Kind: "symbol", Title: "verify", Pointer: "src/auth.ts:L1-L4", Snippet: "function verify() {}", Score: 1}},
					}, true
				}
			case "hooks/03-post-edit", "hooks/06-post-edit-sync":
				hookCheck = func(string, string) int { return 1 }
			}
			if name == "hooks/06-post-edit-sync" {
				hookStartSync = func(string) bool { return true }
			}
			runtime.runCLI(t, golden)
		})
	}
}
