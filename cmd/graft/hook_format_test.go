package main

import (
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/h0rn3t/Graft/internal/graph"
	"github.com/h0rn3t/Graft/internal/savings"
)

var hookANSIPattern = regexp.MustCompile(`\x1b\[[0-9;]*m`)

func stripHookANSI(value string) string {
	return hookANSIPattern.ReplaceAllString(value, "")
}

func TestHookStatuslineFormatting(t *testing.T) {
	tests := []struct {
		name       string
		stats      *hookStats
		repos      []graph.WorkspaceChildSize
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
		{
			name:  "workspace sums repos and sizes the edited one",
			stats: &hookStats{NodeCount: 5, EdgeCount: 3, LastFile: new("api/src/app.ts")},
			repos: []graph.WorkspaceChildSize{
				{Name: "api", Built: true, NodeCount: 2, EdgeCount: 1},
				{Name: "web", Built: true, NodeCount: 3, EdgeCount: 2},
			},
			context:    new(34),
			contains:   []string{"2 repos · 5 nodes / 3 edges · ✓ synced", "ctx 34% · api: 2 nodes / 1 edges · last: app.ts"},
			notContain: []string{"not built", "web:"},
		},
		{
			name:  "workspace names a few repos without a graph",
			stats: &hookStats{NodeCount: 2, EdgeCount: 1},
			repos: []graph.WorkspaceChildSize{
				{Name: "api", Built: true, NodeCount: 2, EdgeCount: 1},
				{Name: "auth"}, {Name: "cart"}, {Name: "mail"}, {Name: "web"},
			},
			contains: []string{"5 repos", "⚠ 4 not built: auth, cart, mail +1 · run graft build"},
		},
		{
			name:     "workspace file outside every repo",
			stats:    &hookStats{NodeCount: 2, EdgeCount: 1, LastFile: new("notes.md")},
			repos:    []graph.WorkspaceChildSize{{Name: "api", Built: true, NodeCount: 2, EdgeCount: 1}},
			contains: []string{"1 repos", "last: notes.md"},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			lines := renderHookStatusline(test.stats, test.repos, test.session, test.context)
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
	if !strings.Contains(got, "retrieved context: cite these spans") || !strings.Contains(got, "```\na\nb\n```") {
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
	} else if strings.Contains(got, "tokens saved") || strings.Contains(got, "this pack") {
		t.Errorf("formatHookRetrieval(saved) = %q, want retrieval without a savings banner", got)
	}
	if got := formatHookRetrieval(&graph.AskResult{}, 5); got != "" {
		t.Errorf("formatHookRetrieval(no hits) = %q, want empty", got)
	}
}

func TestHookRelevantRetrievalGate(t *testing.T) {
	base := func() graph.AskResult {
		return graph.AskResult{
			Query:    "verify the pkce code",
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
	if got := relevantHookRetrieval(&graph.AskResult{Mode: "structural", Hits: base().Hits}, session, 3, ""); got == "" {
		t.Fatal("relevantHookRetrieval(structural) = empty, want retrieval")
	}

	session = fresh()
	strong := base()
	strong.Coverage = new(0.2)
	strong.CoverageStrong = new(0.45)
	if got := relevantHookRetrieval(&strong, session, 3, ""); !strings.Contains(got, "verify") {
		t.Errorf("relevantHookRetrieval(strong name) = %q, want verify", got)
	}

	session = fresh()
	weak := base()
	weak.Coverage = new(0.1649)
	weak.CoverageStrong = new(0.0329)
	if got := relevantHookRetrieval(&weak, session, 3, ""); got != "" {
		t.Errorf("relevantHookRetrieval(weak) = %q, want empty", got)
	}
	if len(session.InjectedPointers) != 0 || session.Nudges != 0 {
		t.Errorf("relevantHookRetrieval(weak) recorded pointers %v, nudges %d, want none", session.InjectedPointers, session.Nudges)
	}

	// The prompt must share a word of three or more letters with the top
	// hit's name, whatever the coverage says.
	for _, tc := range []struct {
		name, query, title string
		coverage, strong   float64
		want               bool
	}{
		{name: "shared name word", query: "почини verify в pkce", title: "verify · function", coverage: 1, strong: 1, want: true},
		{name: "top hit not named", query: "why does EnsureFreshGraph skip", title: "refreshBeforeQuery · function", coverage: 0.6, strong: 0.3, want: false},
		{name: "identifier part", query: "how is the fresh graph kept", title: "EnsureFreshGraph · function", coverage: 0.6, strong: 0.5, want: true},
		{name: "body word only", query: "запусти замер haiku", title: "inputUSDPerMtok · function", coverage: 1, strong: 0, want: false},
		{name: "two-letter word", query: "сохрани в RESEARCH.md", title: "mdText · function", coverage: 0.34, strong: 0.34, want: false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			result := base()
			result.Query, result.Coverage, result.CoverageStrong = tc.query, new(tc.coverage), new(tc.strong)
			result.Hits[0].Title = tc.title
			got := relevantHookRetrieval(&result, fresh(), 3, "")
			if (got != "") != tc.want {
				t.Errorf("relevantHookRetrieval(%q, top %q) = %q, want injected %t", tc.query, tc.title, got, tc.want)
			}
		})
	}

	session = fresh()
	result := base()
	if relevantHookRetrieval(&result, session, 3, "") == "" {
		t.Fatal("relevantHookRetrieval(first prompt) = empty, want retrieval")
	}
	if !slices.Equal(session.InjectedPointers, []string{"src/pkce.ts:L1-L4", "src/pkce.ts:L6-L9"}) {
		t.Errorf("relevantHookRetrieval(first prompt) pointers = %v", session.InjectedPointers)
	}
	if got := relevantHookRetrieval(&result, session, 3, ""); got != "" {
		t.Errorf("relevantHookRetrieval(repeated prompt) = %q, want empty", got)
	}
	result.Hits = []graph.AskHit{
		{Title: "verify", Pointer: "src/pkce.ts:L1-L4"},
		{Title: "exchange", Pointer: "src/client.ts:L2-L8"},
	}
	got := relevantHookRetrieval(&result, session, 3, "")
	if !strings.Contains(got, "exchange") || strings.Contains(got, "verify") {
		t.Errorf("relevantHookRetrieval(one fresh hit) = %q, want only exchange", got)
	}
}

func TestHookOrientationAndSubagentFormatting(t *testing.T) {
	index := strings.Repeat("X", 3000)
	got := stripHookANSI(formatHookOrientation(index, 1500, ""))
	if !strings.Contains(got, "repo map") || !strings.Contains(got, "reach for graft first") || !strings.Contains(got, "Already know the symbol?") || !strings.Contains(got, "Refactor, rename, or multi-file change?") {
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
		got, _ := resolveHookStats(root)
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
		got, _ := resolveHookStats(root)
		if got == nil || got.NodeCount != 42 || got.ReadyCount != 1 || got.Dirty {
			t.Errorf("resolveHookStats(graph) = %#v, want graph stats", got)
		}
	})

	t.Run("empty graph is built", func(t *testing.T) {
		root := t.TempDir()
		writeHookTestGraph(t, root, graph.GraphV1{Meta: graph.GraphMeta{Version: 1}})
		got, _ := resolveHookStats(root)
		if got == nil || got.NodeCount != 0 || strings.Contains(strings.Join(renderHookStatusline(got, nil, nil, nil), ""), "not built") {
			t.Errorf("resolveHookStats(empty graph) = %#v, want built empty graph", got)
		}
	})

	t.Run("zero cache without graph is missing", func(t *testing.T) {
		root := t.TempDir()
		if err := writeHookStats(root, emptyHookStats()); err != nil {
			t.Fatalf("writeHookStats() error = %v, want nil", err)
		}
		if got, _ := resolveHookStats(root); got != nil {
			t.Errorf("resolveHookStats(zero cache only) = %#v, want nil", got)
		}
	})

	t.Run("workspace sums repo graphs under the root's freshness", func(t *testing.T) {
		t.Setenv("GRAFT_DIR", "")
		root := t.TempDir()
		writeHookTestGraph(t, filepath.Join(root, "api"), graph.GraphV1{Meta: graph.GraphMeta{Version: 1, NodeCount: 2, EdgeCount: 1}})
		writeHookTestGraph(t, filepath.Join(root, "web"), graph.GraphV1{Meta: graph.GraphMeta{Version: 1, NodeCount: 3, EdgeCount: 2}})
		if err := graph.WriteWorkspace(filepath.Join(root, "graft"), []string{"web", "missing", "api"}); err != nil {
			t.Fatalf("graph.WriteWorkspace() error = %v, want nil", err)
		}
		if err := writeHookStats(root, hookStats{NodeCount: 99, Dirty: true, LastFile: new("api/src/app.ts")}); err != nil {
			t.Fatalf("writeHookStats() error = %v, want nil", err)
		}
		got, repos := resolveHookStats(root)
		if got == nil || got.NodeCount != 5 || got.EdgeCount != 3 || !got.Dirty || got.LastFile == nil {
			t.Errorf("resolveHookStats(workspace) stats = %#v, want summed repo graphs with the root's dirty flag and last file", got)
		}
		wantRepos := []graph.WorkspaceChildSize{
			{Name: "api", Built: true, NodeCount: 2, EdgeCount: 1},
			{Name: "missing"},
			{Name: "web", Built: true, NodeCount: 3, EdgeCount: 2},
		}
		if !slices.Equal(repos, wantRepos) {
			t.Errorf("resolveHookStats(workspace) repos = %v, want %v", repos, wantRepos)
		}
	})

	t.Run("workspace without a repo graph is not built", func(t *testing.T) {
		t.Setenv("GRAFT_DIR", "")
		root := t.TempDir()
		if err := graph.WriteWorkspace(filepath.Join(root, "graft"), []string{"api"}); err != nil {
			t.Fatalf("graph.WriteWorkspace() error = %v, want nil", err)
		}
		if got, repos := resolveHookStats(root); got != nil || len(repos) != 1 {
			t.Errorf("resolveHookStats(unbuilt workspace) = (%#v, %v), want (nil, one repo)", got, repos)
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

func TestHookRetrievalBodyOmitsDoc(t *testing.T) {
	hits := []graph.AskHit{{Kind: "symbol", Title: "load · function", Pointer: "a.go:L2-L2", Snippet: "func load() error", Doc: "Reads the persisted settings from disk."}}
	got := hookRetrievalBody(hits)
	if want := "\n 1. load · function: a.go:L2-L2 — func load() error"; !strings.HasSuffix(got, want) || strings.Contains(got, hits[0].Doc) {
		t.Errorf("hookRetrievalBody(documented hit) = %q, want it to end with %q and carry no doc line", got, want)
	}
}
