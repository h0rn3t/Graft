package main

import (
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/NanoNets/context-graph-engine/internal/graph"
	"github.com/NanoNets/context-graph-engine/internal/savings"
)

var hookANSIPattern = regexp.MustCompile(`\x1b\[[0-9;]*m`)

func stripHookANSI(value string) string {
	return hookANSIPattern.ReplaceAllString(value, "")
}

func TestHookStatuslineFormatting(t *testing.T) {
	tests := []struct {
		name       string
		stats      *hookStats
		session    *sessionState
		context    *int
		contains   []string
		notContain []string
	}{
		{name: "not built", contains: []string{"not built", "graft build"}},
		{
			name:     "empty graph",
			stats:    &hookStats{Languages: []string{}},
			contains: []string{"0 nodes / 0 edges", "✓ synced"},
			notContain: []string{
				"not built",
				"graft build",
			},
		},
		{
			name: "size freshness context last",
			stats: &hookStats{
				NodeCount:  319,
				EdgeCount:  730,
				StaleCount: 4,
				Dirty:      true,
				LastFile:   new("pkce.ts"),
			},
			context:  new(34),
			contains: []string{"319 nodes / 730 edges", "⚠ 4 stale", "ctx 34%", "last: pkce.ts"},
		},
		{
			name:     "syncing overrides stale",
			stats:    &hookStats{NodeCount: 1, Dirty: true, Syncing: true},
			contains: []string{"syncing…"},
		},
		{
			name:     "clean",
			stats:    &hookStats{NodeCount: 1},
			contains: []string{"✓ synced"},
		},
		{
			name:    "billed savings include dollars",
			stats:   &hookStats{NodeCount: 1},
			session: &sessionState{SavedTokens: 100_000, InputCostMicros: new(600_000), InputTokensBilled: new(1_000_000)},
			contains: []string{
				"~100,000 tok saved · ~$0.06",
			},
		},
		{
			name:       "unbilled savings omit dollars",
			stats:      &hookStats{NodeCount: 1},
			session:    &sessionState{SavedTokens: 100_000},
			contains:   []string{"~100,000 tok saved"},
			notContain: []string{"$"},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			lines := renderHookStatusline(test.stats, test.session, test.context)
			got := stripHookANSI(strings.Join(lines, "\n"))
			for _, want := range test.contains {
				if !strings.Contains(got, want) {
					t.Errorf("renderHookStatusline() = %q, want substring %q", got, want)
				}
			}
			for _, unwanted := range test.notContain {
				if strings.Contains(got, unwanted) {
					t.Errorf("renderHookStatusline() = %q, want no substring %q", got, unwanted)
				}
			}
		})
	}
}

func TestHookBlastRadiusFormatting(t *testing.T) {
	wiring := graph.GraphV1{
		Nodes: []graph.NodeV1{
			{ID: "src/pkce.ts#verify", Name: "verify", Path: "src/pkce.ts"},
			{ID: "src/client.ts#exchange", Name: "exchange", Path: "src/client.ts"},
			{ID: "src/pkce.ts#gen", Name: "gen", Path: "src/pkce.ts"},
		},
		Edges: []graph.EdgeV1{
			{Source: "src/client.ts#exchange", Target: "src/pkce.ts#verify", Relation: "calls"},
			{Source: "src/pkce.ts#gen", Target: "src/pkce.ts#verify", Relation: "calls"},
		},
	}
	got := stripHookANSI(formatHookBlastRadius(wiring, "/abs/repo/src/pkce.ts", 8))
	if !strings.Contains(got, "blast radius for pkce.ts") || !strings.Contains(got, "calls ← exchange (client.ts)") {
		t.Errorf("formatHookBlastRadius() = %q, want external caller label", got)
	}
	if strings.Contains(got, "gen (pkce.ts)") {
		t.Errorf("formatHookBlastRadius() = %q, want same-file edge excluded", got)
	}
	if got := formatHookBlastRadius(wiring, "/abs/repo/src/unknown.ts", 8); got != "" {
		t.Errorf("formatHookBlastRadius(unknown) = %q, want empty", got)
	}
}

func TestHookRetrievalFormatting(t *testing.T) {
	inline := &graph.AskResult{Hits: []graph.AskHit{{
		Kind: "symbol", Title: "verify", Pointer: "src/pkce.ts:L1-L4", Snippet: "s", Code: "a\nb", Score: 1,
	}, {
		Kind: "symbol", Title: "gen", Pointer: "src/pkce.ts:L6-L9", Snippet: "s", Score: 0.8,
	}}}
	got := stripHookANSI(formatHookRetrieval(inline, 5))
	if !strings.Contains(got, "retrieved context, read these spans") || !strings.Contains(got, "```\na\nb\n```") {
		t.Errorf("formatHookRetrieval(inline pack) = %q, want inlined code", got)
	}
	if got := stripHookANSI(formatHookRetrieval(inline, 1)); strings.Contains(got, "gen:") {
		t.Errorf("formatHookRetrieval(inline pack, 1) = %q, want cap at one hit", got)
	}

	saved := &graph.AskResult{
		Saved: &graph.AskSavings{Files: 1, BaselineChars: 8000},
		Hits:  []graph.AskHit{{Kind: "symbol", Title: "verify", Pointer: "src/pkce.ts:L1-L4", Snippet: "s", Code: "a\nb", Score: 1}},
	}
	if got := stripHookANSI(formatHookRetrieval(saved, 5)); hookANSIPattern.MatchString(got) {
		t.Errorf("formatHookRetrieval(saved) = %q, want plain text", got)
	} else if !strings.Contains(got, "tokens saved ≈ ") || !regexp.MustCompile(`\([0-9]+%\)`).MatchString(got) {
		t.Errorf("formatHookRetrieval(saved) = %q, want savings line with percentage", got)
	}
	if got := formatHookRetrieval(&graph.AskResult{}, 5); got != "" {
		t.Errorf("formatHookRetrieval(no hits) = %q, want empty", got)
	}
}

func TestHookRelevantRetrievalGate(t *testing.T) {
	base := func() graph.AskResult {
		return graph.AskResult{
			Mode:     "lexical",
			Coverage: new(1.0),
			Hits: []graph.AskHit{
				{Title: "verify", Pointer: "src/pkce.ts:L1-L4", Score: 1},
				{Title: "gen", Pointer: "src/pkce.ts:L6-L9", Score: 0.8},
			},
		}
	}
	fresh := func() *sessionState {
		return &sessionState{PerAgentQuery: map[string]string{}, InjectedPointers: []string{}}
	}

	session := fresh()
	if got := relevantHookRetrieval(&graph.AskResult{Mode: "structural", Hits: base().Hits}, session, 3); got == "" {
		t.Fatal("relevantHookRetrieval(structural) = empty, want retrieval")
	}

	session = fresh()
	strong := base()
	strong.Coverage = new(0.2)
	strong.CoverageStrong = new(0.45)
	if got := relevantHookRetrieval(&strong, session, 3); !strings.Contains(got, "verify") {
		t.Errorf("relevantHookRetrieval(strong name) = %q, want verify", got)
	}

	session = fresh()
	weak := base()
	weak.Coverage = new(0.1649)
	weak.CoverageStrong = new(0.0329)
	got := relevantHookRetrieval(&weak, session, 3)
	if !strings.Contains(got, "no strong match") || !strings.Contains(got, "0.03") {
		t.Errorf("relevantHookRetrieval(weak) = %q, want measured nudge", got)
	}
	if len(session.InjectedPointers) != 0 {
		t.Errorf("relevantHookRetrieval(weak) recorded %v, want no pointers", session.InjectedPointers)
	}

	session = fresh()
	weak.Coverage = new(0.1)
	weak.CoverageStrong = new(0.0)
	for range 2 {
		if got := relevantHookRetrieval(&weak, session, 3); got == "" {
			t.Fatal("relevantHookRetrieval(weak nudge) = empty before cap")
		}
	}
	if got := relevantHookRetrieval(&weak, session, 3); got != "" {
		t.Errorf("relevantHookRetrieval(weak nudge) = %q after cap, want empty", got)
	}
	if session.Nudges != 2 {
		t.Errorf("relevantHookRetrieval(weak nudge) nudges = %d, want 2", session.Nudges)
	}

	session = fresh()
	result := base()
	if relevantHookRetrieval(&result, session, 3) == "" {
		t.Fatal("relevantHookRetrieval(first prompt) = empty, want retrieval")
	}
	if !slices.Equal(session.InjectedPointers, []string{"src/pkce.ts:L1-L4", "src/pkce.ts:L6-L9"}) {
		t.Errorf("relevantHookRetrieval(first prompt) pointers = %v", session.InjectedPointers)
	}
	if got := relevantHookRetrieval(&result, session, 3); got != "" {
		t.Errorf("relevantHookRetrieval(repeated prompt) = %q, want empty", got)
	}
	result.Hits = []graph.AskHit{
		{Title: "verify", Pointer: "src/pkce.ts:L1-L4"},
		{Title: "exchange", Pointer: "src/client.ts:L2-L8"},
	}
	got = relevantHookRetrieval(&result, session, 3)
	if !strings.Contains(got, "exchange") || strings.Contains(got, "verify") {
		t.Errorf("relevantHookRetrieval(one fresh hit) = %q, want only exchange", got)
	}
}

func TestHookOrientationAndSubagentFormatting(t *testing.T) {
	index := strings.Repeat("X", 3000)
	got := stripHookANSI(formatHookOrientation(index, 1500, ""))
	if !strings.Contains(got, "repo map") || !strings.Contains(got, "reach for graft first") || !strings.Contains(got, "Already know the file or symbol to change?") || !strings.Contains(got, "Refactor, rename, or multi-file change?") {
		t.Errorf("formatHookOrientation() = %q, want required guidance", got)
	}
	if strings.Contains(got, "graft impact") || savings.Length(got) >= 4000 {
		t.Errorf("formatHookOrientation() length/content = %d/%q, want trimmed current guidance", savings.Length(got), got)
	}
	note := "⚠ graft's index may be ahead of your working tree: 3 of 40 indexed files are not on disk"
	got = stripHookANSI(formatHookOrientation("repo index", 1500, note))
	if strings.Index(got, "ahead of your working tree") > strings.Index(got, "reach for graft first") {
		t.Errorf("formatHookOrientation(stale) = %q, want banner first", got)
	}
	if strings.Contains(stripHookANSI(formatHookOrientation("repo index", 1500, "")), "ahead of your working tree") {
		t.Error("formatHookOrientation(fresh) contains stale banner")
	}

	got = stripHookANSI(renderHookSubagent("Explore", &sessionState{PerAgentQuery: map[string]string{"Explore": "pkce flow"}}))
	if !strings.Contains(got, "Explore") || !strings.Contains(got, "pkce flow") {
		t.Errorf("renderHookSubagent() = %q, want agent and query", got)
	}
	if got := stripHookANSI(renderHookSubagent("Plan", nil)); !strings.Contains(got, "Plan") {
		t.Errorf("renderHookSubagent(nil) = %q, want agent", got)
	}
}

func TestHookStatuslineResolution(t *testing.T) {
	t.Run("cache wins", func(t *testing.T) {
		root := t.TempDir()
		writeHookTestGraph(t, root, graph.GraphV1{Meta: graph.GraphMeta{Version: 1, NodeCount: 42, EdgeCount: 100, Languages: []string{"typescript"}}})
		if err := writeHookStats(root, hookStats{NodeCount: 7, EdgeCount: 3, Dirty: true, StaleCount: 4}); err != nil {
			t.Fatalf("writeHookStats() error = %v, want nil", err)
		}
		got := resolveHookStats(root)
		if got == nil || got.NodeCount != 7 || got.StaleCount != 4 {
			t.Errorf("resolveHookStats(cache) = %#v, want dirty cache", got)
		}
	})

	t.Run("graph fallback", func(t *testing.T) {
		root := t.TempDir()
		writeHookTestGraph(t, root, graph.GraphV1{
			Meta:  graph.GraphMeta{Version: 1, NodeCount: 42, EdgeCount: 100, Languages: []string{"typescript"}},
			Nodes: []graph.NodeV1{{ID: "a", SummaryState: "ready"}, {ID: "b", SummaryState: "pending"}},
		})
		got := resolveHookStats(root)
		if got == nil || got.NodeCount != 42 || got.ReadyCount != 1 || got.Dirty {
			t.Errorf("resolveHookStats(graph) = %#v, want graph stats", got)
		}
	})

	t.Run("empty graph is built", func(t *testing.T) {
		root := t.TempDir()
		writeHookTestGraph(t, root, graph.GraphV1{Meta: graph.GraphMeta{Version: 1}})
		got := resolveHookStats(root)
		if got == nil || got.NodeCount != 0 || strings.Contains(strings.Join(renderHookStatusline(got, nil, nil), ""), "not built") {
			t.Errorf("resolveHookStats(empty graph) = %#v, want built empty graph", got)
		}
	})

	t.Run("zero cache without graph is missing", func(t *testing.T) {
		root := t.TempDir()
		if err := writeHookStats(root, emptyHookStats()); err != nil {
			t.Fatalf("writeHookStats() error = %v, want nil", err)
		}
		if got := resolveHookStats(root); got != nil {
			t.Errorf("resolveHookStats(zero cache only) = %#v, want nil", got)
		}
	})
}

func TestHookIndexFreshness(t *testing.T) {
	root := t.TempDir()
	out := filepath.Join(root, "graft")
	if err := os.MkdirAll(filepath.Join(root, "src"), 0o755); err != nil {
		t.Fatalf("os.MkdirAll(src) error = %v, want nil", err)
	}
	if err := os.WriteFile(filepath.Join(root, "src", "present.go"), []byte("package main\n"), 0o644); err != nil {
		t.Fatalf("os.WriteFile(present.go) error = %v, want nil", err)
	}
	if err := graph.WriteFingerprint(out, graph.ExtractorID, map[string]graph.FingerprintFile{
		"src/present.go": {},
		"src/missing.go": {},
	}, nil); err != nil {
		t.Fatalf("graph.WriteFingerprint() error = %v, want nil", err)
	}
	freshness := hookIndexFreshness(root)
	if freshness == nil || freshness.Missing != 1 || freshness.Total != 2 {
		t.Errorf("hookIndexFreshness() = %#v, want one missing of two", freshness)
	}
	banner := hookStaleBanner(freshness)
	if !strings.Contains(banner, "ahead of your working tree") || !strings.Contains(banner, "graft grep") {
		t.Errorf("hookStaleBanner() = %q, want actionable banner", banner)
	}
	if got := hookStaleBanner(&hookFreshness{Missing: 0, Total: 2}); got != "" {
		t.Errorf("hookStaleBanner(fresh) = %q, want empty", got)
	}
	if got := hookIndexFreshness(t.TempDir()); got != nil {
		t.Errorf("hookIndexFreshness(no graph) = %#v, want nil", got)
	}
}

func TestHookTruncateUTF16(t *testing.T) {
	if got := hookTruncateUTF16("a😀b", 2); got != "a" {
		t.Errorf("hookTruncateUTF16(a😀b, 2) = %q, want a without split surrogate", got)
	}
}

func writeHookTestGraph(t *testing.T, root string, wiring graph.GraphV1) {
	t.Helper()
	out := filepath.Join(root, "graft")
	if _, err := graph.Write(wiring, out); err != nil {
		t.Fatalf("graph.Write(%q) error = %v, want nil", out, err)
	}
}
