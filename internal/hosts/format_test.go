package hosts

import (
	"slices"
	"strings"
	"testing"
)

// TestFormatPlanHonorsInitOptions checks the dry-run plan against the flags a
// run obeys: --no-mcp and --no-hooks drop the other hosts' registrations,
// --no-global every machine-wide write, and Claude Code keeps its MCP server
// and hooks either way.
func TestFormatPlanHonorsInitOptions(t *testing.T) {
	repo, home := machine(t)
	plans := PlanInit(repo, home, binLaunch, nil)
	ids := []string{"claude", "cursor", "gemini", "agents"}
	format := func(opts PlanOptions) string { return FormatPlan(plans, ids, repo, home, opts, false) }

	all := format(PlanOptions{MCP: true, Hooks: true, Global: true})
	for _, want := range []string{".cursor/hooks.json", "~/.codex/hooks.json", "~/.claude/settings.json", "your machine"} {
		if !strings.Contains(all, want) {
			t.Errorf("FormatPlan(all options) = %q, want it to contain %q", all, want)
		}
	}
	noHooks := format(PlanOptions{MCP: true, Global: true})
	for _, gone := range []string{".cursor/hooks.json", "~/.codex/hooks.json"} {
		if strings.Contains(noHooks, gone) {
			t.Errorf("FormatPlan(--no-hooks) = %q, want it without %q", noHooks, gone)
		}
	}
	for _, kept := range []string{".cursor/mcp.json", "~/.claude/settings.json"} {
		if !strings.Contains(noHooks, kept) {
			t.Errorf("FormatPlan(--no-hooks) = %q, want it to keep %q", noHooks, kept)
		}
	}
	noMCP := format(PlanOptions{Hooks: true, Global: true})
	for _, gone := range []string{".cursor/mcp.json", "~/.codex/config.toml", "~/.gemini/config/mcp_config.json"} {
		if strings.Contains(noMCP, gone) {
			t.Errorf("FormatPlan(--no-mcp) = %q, want it without %q", noMCP, gone)
		}
	}
	for _, kept := range []string{".mcp.json", "~/.claude.json", ".cursor/hooks.json"} {
		if !strings.Contains(noMCP, kept) {
			t.Errorf("FormatPlan(--no-mcp) = %q, want it to keep %q", noMCP, kept)
		}
	}
	noGlobal := format(PlanOptions{MCP: true, Hooks: true})
	for _, gone := range []string{"your machine", "suppress the out-of-repo writes", "~/.codex/hooks.json", "~/.claude.json"} {
		if strings.Contains(noGlobal, gone) {
			t.Errorf("FormatPlan(--no-global) = %q, want it without %q", noGlobal, gone)
		}
	}
	if !strings.Contains(noGlobal, ".mcp.json") {
		t.Errorf("FormatPlan(--no-global) = %q, want the repo writes kept", noGlobal)
	}
}

func TestFormatInitEpilogueWordmarkLabels(t *testing.T) {
	const (
		version = "  ]             ~ ~             |     |___/             v0.2.0"
		stats   = "   |                           |     3,018 nodes · 7,231 edges"
	)
	tests := []struct {
		name       string
		graphBuilt bool
		want       []string
	}{
		{
			name:       "graph built",
			graphBuilt: true,
			want:       []string{wordmark[7], version, wordmark[9], stats},
		},
		{
			name: "graph missing",
			want: []string{wordmark[7], version, wordmark[9], wordmark[10]},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			lines := strings.Split(FormatInitEpilogue(tt.graphBuilt, 3018, 7231, "0.2.0", false), "\n")
			if got := lines[7:11]; !slices.Equal(got, tt.want) {
				t.Errorf("FormatInitEpilogue(%v, 3018, 7231, %q, false) lines 7-10 = %q, want %q", tt.graphBuilt, "0.2.0", got, tt.want)
			}
		})
	}
}
