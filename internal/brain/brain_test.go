package brain

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/h0rn3t/Graft/internal/graph"
)

func TestCacheIsStaleAtTheTTLEdges(t *testing.T) {
	now := time.UnixMilli(100_000_000)
	rules := []graph.BrainRule{{Rule: "rule"}}
	checked := now.Add(-time.Minute).UnixMilli()
	tests := []struct {
		name    string
		cache   RulesCache
		present bool
		want    bool
	}{
		{name: "missing cache", want: true},
		{name: "populated at ttl", cache: RulesCache{FetchedAt: now.Add(-RulesTTL).UnixMilli(), Rules: rules}, present: true},
		{name: "populated past ttl", cache: RulesCache{FetchedAt: now.Add(-RulesTTL - time.Millisecond).UnixMilli(), Rules: rules}, present: true, want: true},
		{name: "empty at its short ttl", cache: RulesCache{FetchedAt: now.Add(-EmptyRulesTTL).UnixMilli()}, present: true},
		{name: "empty past its short ttl", cache: RulesCache{FetchedAt: now.Add(-EmptyRulesTTL - time.Millisecond).UnixMilli()}, present: true, want: true},
		{name: "a recent attempt outweighs an old fetch", cache: RulesCache{FetchedAt: 0, CheckedAt: &checked, Rules: rules}, present: true},
	}
	for _, tt := range tests {
		if got := CacheIsStale(tt.cache, tt.present, now); got != tt.want {
			t.Errorf("CacheIsStale(%s) = %v, want %v", tt.name, got, tt.want)
		}
	}
}

func TestRenderSectionGroupsAndCaps(t *testing.T) {
	rules := []graph.BrainRule{
		{Symbol: "repository", Rule: "Repository rule"},
		{Symbol: "src/z.ts#first", Rule: "Z rule"},
		{Symbol: "src/a.ts#first", Rule: "A rule", SourceURL: "https://example.test/a"},
	}
	for i := range 38 {
		rules = append(rules, graph.BrainRule{Symbol: "src/z.ts#extra", Rule: fmt.Sprintf("Z extra %d", i)})
	}
	got := RenderSection(rules, "repo")
	for _, want := range []string{
		"Mined from repo's own history",
		"what established it.\n\n- Repository rule\n\n### src/a.ts\n- A rule (https://example.test/a)\n\n### src/z.ts\n- Z rule\n",
		"- Z extra 36\n\n_1 more rules apply to specific symbols; graft attaches those to each `graft ask` answer._",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("RenderSection() = %q, want it to contain %q", got, want)
		}
	}
	if strings.HasSuffix(got, "\n") {
		t.Errorf("RenderSection() ends with a newline; the TypeScript renderer trims it")
	}
}

func TestPullWritesSectionHostsOnlyAndKeepsUserText(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer secret" || r.URL.Path != "/api/public/brains/brain/rules/anchors" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		_, _ = w.Write([]byte(`{"anchors":[{"rule_id":"r1","symbol":"src/api.ts#ready","rule":"Keep UTC"}]}`)) // the test reads the result off disk
	}))
	defer server.Close()
	t.Setenv("GRAFT_BRAIN_URL", server.URL)
	t.Setenv("GRAFT_BRAIN_ID", "")
	t.Setenv("GRAFT_BRAIN_TOKEN", "")
	t.Setenv("GRAFT_DIR", "")
	repo := t.TempDir()
	for _, dir := range []string{".github", ".cursor"} {
		if err := os.MkdirAll(filepath.Join(repo, dir), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	agents := "# Notes\r\n\r\n<!-- graft:brain:start -->\r\nold\r\n<!-- graft:brain:end -->\r\nEnd\r\n"
	if err := os.WriteFile(filepath.Join(repo, "AGENTS.md"), []byte(agents), 0o644); err != nil {
		t.Fatal(err)
	}
	// Copilot's instruction file path is a directory, so its write must be skipped.
	if err := os.MkdirAll(filepath.Join(repo, ".github", "copilot-instructions.md"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := WriteLink(repo, Link{BrainID: "brain", Token: "secret"}); err != nil {
		t.Fatal(err)
	}
	result, linked, err := Pull(context.Background(), repo, t.TempDir(), []string{"agents", "copilot", "cursor"}, time.UnixMilli(1000))
	if err != nil || !linked || result.RuleCount != 1 {
		t.Fatalf("Pull() = (%+v, %v, %v), want one rule", result, linked, err)
	}
	if len(result.Writes) != 1 || result.Writes[0].ID != "agents" {
		t.Errorf("Pull() writes = %+v, want only AGENTS.md (Copilot unwritable, Cursor owned)", result.Writes)
	}
	got, err := os.ReadFile(filepath.Join(repo, "AGENTS.md"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(string(got), "# Notes\r\n\r\n<!-- graft:brain:start -->\r\n## House rules") || !strings.HasSuffix(string(got), "<!-- graft:brain:end -->\r\nEnd\r\n") {
		t.Errorf("AGENTS.md = %q, want the block replaced with CRLF user text kept", got)
	}
	if _, err := os.Stat(filepath.Join(repo, ".cursor", "rules", "graft.mdc")); err == nil {
		t.Errorf("Pull() wrote Cursor's owned rule file")
	}
	cache, err := os.ReadFile(RulesCachePath(repo))
	if err != nil || string(cache) != `{"brainId":"brain","fetchedAt":1000,"rules":[{"ruleId":"r1","symbol":"src/api.ts#ready","fingerprint":"","rule":"Keep UTC"}]}` {
		t.Errorf("rules cache = %s (%v), want the compact TypeScript layout", cache, err)
	}

	t.Setenv("GRAFT_BRAIN_URL", "http://127.0.0.1:1")
	result, _, err = Pull(context.Background(), repo, t.TempDir(), nil, time.UnixMilli(2000))
	if err != nil || result.Warning == "" {
		t.Errorf("Pull() with the brain down = (%+v, %v), want a warning and no error", result, err)
	}
}
