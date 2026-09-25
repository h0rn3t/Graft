package main

import (
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/h0rn3t/Graft/internal/graph"
)

func TestMCPGraphOnlyCache(t *testing.T) {
	t.Setenv("GRAFT_NO_REFRESH", "1")
	for _, tc := range []struct {
		tool string
		args map[string]any
	}{
		{"graft_file_api", map[string]any{"file": "src/service0.go"}},
		{"graft_trace_calls", map[string]any{"symbol": "alpha"}},
		{"graft_repo_map", nil},
		{"graft_find_all", map[string]any{"pattern": "alpha"}},
	} {
		t.Run(tc.tool, func(t *testing.T) {
			root := t.TempDir()
			dir := filepath.Join(root, "graft")
			writeQueryCacheFixture(t, dir, 1)
			var cache queryCache
			result := mcpCallWithCache(t.Context(), root, dir, "", tc.tool, tc.args, &cache)
			if result.isError || len(cache.entries) != 1 {
				t.Fatalf("mcpCallWithCache(%s) = %+v, cache entries = %d, want success and one cached graph", tc.tool, result, len(cache.entries))
			}
			before := cache.entries[0].wiring
			if cache.entries[0].index != nil {
				t.Fatalf("mcpCallWithCache(%s) loaded ask index, want graph only", tc.tool)
			}
			var wg sync.WaitGroup
			for range 8 {
				wg.Go(func() {
					got := mcpCallWithCache(t.Context(), root, dir, "", tc.tool, tc.args, &cache)
					if got.isError || got.text != result.text {
						t.Errorf("concurrent mcpCallWithCache(%s) = %+v, want %+v", tc.tool, got, result)
					}
				})
			}
			wg.Wait()
			if cache.entries[0].wiring != before {
				t.Errorf("mcpCallWithCache(%s) replaced unchanged graph", tc.tool)
			}
			_, index, err := cache.load(dir)
			if err != nil || index == nil || cache.entries[0].wiring != before {
				t.Fatalf("cache.load after %s = (%v, %v), want lazy index and same graph", tc.tool, index, err)
			}
			writeQueryCacheFixture(t, dir, 2)
			got := mcpCallWithCache(t.Context(), root, dir, "", tc.tool, tc.args, &cache)
			if got.isError || cache.entries[0].wiring == before || len(cache.entries[0].wiring.Nodes) != 2 {
				t.Errorf("mcpCallWithCache(%s) after replacement = %+v, want new graph", tc.tool, got)
			}
			if err := os.WriteFile(graph.WiringPath(dir), []byte("{"), 0o600); err != nil {
				t.Fatal(err)
			}
			got = mcpCallWithCache(t.Context(), root, dir, "", tc.tool, tc.args, &cache)
			if !got.isError {
				t.Errorf("mcpCallWithCache(%s) after corruption = %+v, want error", tc.tool, got)
			}
			if err := os.Remove(graph.WiringPath(dir)); err != nil {
				t.Fatal(err)
			}
			got = mcpCallWithCache(t.Context(), root, dir, "", tc.tool, tc.args, &cache)
			if !got.isError || !strings.Contains(got.text, "no graph found") {
				t.Errorf("mcpCallWithCache(%s) after deletion = %+v, want missing graph", tc.tool, got)
			}
		})
	}
}

func BenchmarkMCPGraphOnly(b *testing.B) {
	b.Setenv("GRAFT_NO_REFRESH", "1")
	root := b.TempDir()
	dir := filepath.Join(root, "graft")
	writeQueryCacheFixture(b, dir, 2000)
	for _, tool := range []string{"graft_file_api", "graft_repo_map"} {
		for _, mode := range []string{"off", "on"} {
			b.Run(tool+"/cache="+mode, func(b *testing.B) {
				var cache *queryCache
				if mode == "on" {
					cache = new(queryCache)
				}
				args := map[string]any{"file": "src/service0.go"}
				mcpCallWithCache(b.Context(), root, dir, "", tool, args, cache)
				b.ReportAllocs()
				for b.Loop() {
					result := mcpCallWithCache(b.Context(), root, dir, "", tool, args, cache)
					if result.isError {
						b.Fatal(result.text)
					}
				}
			})
		}
	}
}
