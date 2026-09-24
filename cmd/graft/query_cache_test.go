package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/h0rn3t/Graft/internal/graph"
)

func TestQueryCacheFileChanges(t *testing.T) {
	cases := []struct {
		name      string
		file      string
		atomic    bool
		remove    bool
		corrupt   bool
		wantName  string
		wantTerm  string
		wantSame  bool
		wantError bool
	}{
		{name: "unchanged", wantName: "alpha", wantTerm: "alpha", wantSame: true},
		{name: "graph rewrite", file: "graph", wantName: "bravo", wantTerm: "alpha"},
		{name: "graph replacement with preserved metadata", file: "graph", atomic: true, wantName: "bravo", wantTerm: "alpha"},
		{name: "index rewrite", file: "index", wantName: "alpha", wantTerm: "bravo"},
		{name: "index replacement with preserved metadata", file: "index", atomic: true, wantName: "alpha", wantTerm: "bravo"},
		{name: "graph removed", file: "graph", remove: true, wantError: true},
		{name: "graph malformed", file: "graph", corrupt: true, wantError: true},
		{name: "index removed", file: "index", remove: true, wantName: "alpha"},
		{name: "index malformed", file: "index", corrupt: true, wantName: "alpha"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			writeQueryCacheFixture(t, dir, 1)
			var cache queryCache
			before, beforeIndex, err := cache.load(dir)
			if err != nil || beforeIndex == nil {
				t.Fatalf("cache.load(%q) = (%v, %v), want graph and index", dir, beforeIndex, err)
			}
			if tc.file != "" {
				path := graph.WiringPath(dir)
				if tc.file == "index" {
					path = filepath.Join(dir, ".cache", "ask-index.json")
				}
				info, err := os.Stat(path)
				if err != nil {
					t.Fatal(err)
				}
				data, err := os.ReadFile(path)
				if err != nil {
					t.Fatal(err)
				}
				data = bytes.ReplaceAll(data, []byte("alpha"), []byte("bravo"))
				if tc.corrupt {
					data = []byte("{")
				}
				switch {
				case tc.remove:
					if err := os.Remove(path); err != nil {
						t.Fatal(err)
					}
				case tc.atomic:
					replacement := path + ".next"
					if err := os.WriteFile(replacement, data, 0o600); err != nil {
						t.Fatal(err)
					}
					if err := os.Chtimes(replacement, info.ModTime(), info.ModTime()); err != nil {
						t.Fatal(err)
					}
					if err := os.Rename(replacement, path); err != nil {
						t.Fatal(err)
					}
				default:
					if err := os.WriteFile(path, data, 0o600); err != nil {
						t.Fatal(err)
					}
					updated := info.ModTime().Add(time.Second)
					if err := os.Chtimes(path, updated, updated); err != nil {
						t.Fatal(err)
					}
				}
			}
			got, index, err := cache.load(dir)
			if (err != nil) != tc.wantError {
				t.Fatalf("cache.load(%q) error = %v, want error %v", dir, err, tc.wantError)
			}
			if tc.wantError {
				if got != nil || index != nil {
					t.Errorf("cache.load(%q) = (%p, %p), want no stale graph or index", dir, got, index)
				}
				writeQueryCacheFixture(t, dir, 1)
				restored, restoredIndex, err := cache.load(dir)
				if err != nil || restored == before || restoredIndex == nil {
					t.Errorf("cache.load(%q) after repair = (%p, %p, %v), want reloaded graph and index", dir, restored, restoredIndex, err)
				}
				return
			}
			if got.Nodes[0].Name != tc.wantName {
				t.Errorf("cache.load(%q) node name = %q, want %q", dir, got.Nodes[0].Name, tc.wantName)
			}
			if tc.wantTerm == "" {
				if index != nil {
					t.Errorf("cache.load(%q) index = %v, want nil", dir, index)
				}
			} else if index == nil || index.DF[tc.wantTerm] == 0 {
				t.Errorf("cache.load(%q) index = %v, want term %q", dir, index, tc.wantTerm)
			}
			if tc.wantSame && (got != before || index != beforeIndex) {
				t.Errorf("cache.load(%q) pointers = (%p, %p), want (%p, %p)", dir, got, index, before, beforeIndex)
			}
			if before.Nodes[0].Name != "alpha" || beforeIndex.DF["alpha"] == 0 {
				t.Error("cache.load changed a previously returned graph or index")
			}
			if tc.file != "" {
				writeQueryCacheFixture(t, dir, 1)
				restored, restoredIndex, err := cache.load(dir)
				if err != nil || restored.Nodes[0].Name != "alpha" || restoredIndex == nil || restoredIndex.DF["alpha"] == 0 {
					t.Errorf("cache.load(%q) after repair = (%v, %v, %v), want alpha graph and index", dir, restored, restoredIndex, err)
				}
			}
		})
	}
}

func TestQueryCacheConcurrentLoads(t *testing.T) {
	dir := t.TempDir()
	writeQueryCacheFixture(t, dir, 1)
	var cache queryCache
	graphs := make([]*graph.GraphV1, 32)
	indexes := make([]*graph.AskIndex, len(graphs))
	var workers sync.WaitGroup
	for i := range graphs {
		workers.Go(func() {
			var err error
			graphs[i], indexes[i], err = cache.load(dir)
			if err != nil {
				t.Errorf("cache.load(%q) error = %v, want nil", dir, err)
			}
		})
	}
	workers.Wait()
	for i := range graphs {
		if graphs[i] == nil || indexes[i] == nil || graphs[i] != graphs[0] || indexes[i] != indexes[0] {
			t.Errorf("cache.load(%q) call %d pointers = (%p, %p), want (%p, %p)", dir, i, graphs[i], indexes[i], graphs[0], indexes[0])
		}
	}
}

func TestQueryCacheBoundedAndIsolated(t *testing.T) {
	var cache queryCache
	first := t.TempDir()
	writeQueryCacheFixture(t, first, 1)
	before, _, err := cache.load(first)
	if err != nil {
		t.Fatal(err)
	}
	for range 16 {
		dir := t.TempDir()
		writeQueryCacheFixture(t, dir, 1)
		if _, _, err := cache.load(dir); err != nil {
			t.Fatal(err)
		}
	}
	if len(cache.entries) > 16 {
		t.Errorf("cache entries = %d, want at most 16", len(cache.entries))
	}
	after, _, err := cache.load(first)
	if err != nil || after == before {
		t.Errorf("cache.load(%q) after eviction = (%p, %v), want reloaded graph", first, after, err)
	}
	var other queryCache
	isolated, _, err := other.load(first)
	if err != nil || isolated == after {
		t.Errorf("other.load(%q) = (%p, %v), want separate server value", first, isolated, err)
	}
	var uncached *queryCache
	loaded, index, err := uncached.load(first)
	if err != nil || loaded == after || index == nil {
		t.Errorf("nil cache.load(%q) = (%p, %p, %v), want uncached graph and index", first, loaded, index, err)
	}
}

func TestMCPQueryCacheNoRefresh(t *testing.T) {
	t.Setenv("GRAFT_NO_REFRESH", "1")
	root := t.TempDir()
	dir := filepath.Join(root, "graft")
	writeQueryCacheFixture(t, dir, 1)
	var cache queryCache
	args := map[string]any{"query": "alpha", "limit": float64(1)}
	result := mcpCallWithCache(t.Context(), root, dir, "", "graft_find_code", args, &cache)
	if result.isError || !strings.Contains(result.text, "alpha") {
		t.Fatalf("mcpCallWithCache(alpha) = %+v, want alpha result", result)
	}
	if len(cache.entries) != 1 {
		t.Fatalf("mcpCallWithCache(alpha) cached entries = %d, want 1", len(cache.entries))
	}
	wiring := cache.entries[0].wiring
	result = mcpCallWithCache(t.Context(), root, dir, "", "graft_find_code", args, &cache)
	if result.isError || cache.entries[0].wiring != wiring {
		t.Errorf("mcpCallWithCache(alpha) = %+v, want cached graph reused", result)
	}
	if err := os.Remove(graph.WiringPath(dir)); err != nil {
		t.Fatal(err)
	}
	result = mcpCallWithCache(t.Context(), root, dir, "", "graft_find_code", args, &cache)
	if !result.isError || !strings.Contains(result.text, "no graph found") {
		t.Errorf("mcpCallWithCache(alpha) after graph removal = %+v, want missing graph error", result)
	}
}

func writeQueryCacheFixture(t testing.TB, dir string, nodes int) {
	t.Helper()
	wiring := graph.GraphV1{Nodes: make([]graph.NodeV1, nodes)}
	for i := range wiring.Nodes {
		wiring.Nodes[i] = graph.NodeV1{
			ID: "symbol:" + strconv.Itoa(i), Name: "alpha", Kind: "function", Path: "src/service" + strconv.Itoa(i/20) + ".go",
			Span: "L10-L30", Signature: new("func alpha(ctx context.Context) error"),
			BodyText: new("load customer account records validate permissions retry transient database errors and return response"),
		}
		if i > 0 {
			wiring.Edges = append(wiring.Edges, graph.EdgeV1{Source: wiring.Nodes[i].ID, Target: wiring.Nodes[i-1].ID, Relation: "calls"})
		}
	}
	if _, err := graph.Write(wiring, dir); err != nil {
		t.Fatal(err)
	}
	if err := graph.WriteAskIndex(dir, wiring); err != nil {
		t.Fatal(err)
	}
}

func BenchmarkQueryCacheSynthetic2000Nodes(b *testing.B) {
	dir := b.TempDir()
	writeQueryCacheFixture(b, dir, 2000)
	for _, cached := range []bool{false, true} {
		name := "uncached_decode"
		var cache *queryCache
		if cached {
			name = "cached_stat"
			cache = new(queryCache)
		}
		b.Run(name, func(b *testing.B) {
			if _, _, err := cache.load(dir); err != nil {
				b.Fatal(err)
			}
			b.ReportAllocs()
			for b.Loop() {
				if _, _, err := cache.load(dir); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}
