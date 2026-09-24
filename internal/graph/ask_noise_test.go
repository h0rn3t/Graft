package graph

import "testing"

func TestAskPrefersProductionOverCopies(t *testing.T) {
	for _, path := range []string{
		"cmd/graft/testdata/frozen/cache.go",
		"fixtures/cache.go",
		"src/__fixtures__/cache.ts",
		"generated/cache.go",
		"src/__generated__/cache.ts",
		"vendor/example/cache.go",
		"src/cache.gen.go",
		"src/cache.generated.ts",
		"src/cache.pb.go",
		"src/zz_generated.cache.go",
		"src/cache_test.go",
	} {
		t.Run(path, func(t *testing.T) {
			t.Parallel()
			wiring := GraphV1{Nodes: []NodeV1{
				{ID: "copy", Name: "QueryCache", Kind: "function", Path: path, Span: "L1-L3"},
				{ID: "production", Name: "QueryCache", Kind: "function", Path: "src/cache.go", Span: "L1-L3"},
			}}
			result, err := Ask(wiring, "QueryCache", AskOptions{})
			if err != nil {
				t.Fatalf("Ask(QueryCache, copy=%q) error = %v, want nil", path, err)
			}
			if len(result.Hits) != 2 {
				t.Fatalf("Ask(QueryCache, copy=%q).Hits = %v, want both matches", path, result.Hits)
			}
			if result.Hits[0].Pointer != "src/cache.go:L1-L3" {
				t.Errorf("Ask(QueryCache, copy=%q).Hits[0] = %v, want production first", path, result.Hits[0])
			}
			if result.Hits[1].Pointer != path+":L1-L3" {
				t.Errorf("Ask(QueryCache, copy=%q).Hits[1] = %v, want copy retained", path, result.Hits[1])
			}
		})
	}
}

func TestAskExplicitCopyQueries(t *testing.T) {
	for _, tt := range []struct{ query, path string }{
		{query: "QueryCache tests", path: "tests/cache.go"},
		{query: "QueryCache testdata", path: "cmd/graft/testdata/cache.go"},
		{query: "QueryCache fixture", path: "fixtures/cache.go"},
		{query: "QueryCache generated", path: "generated/cache.go"},
		{query: "QueryCache vendor", path: "vendor/example/cache.go"},
	} {
		t.Run(tt.query, func(t *testing.T) {
			t.Parallel()
			wiring := GraphV1{Nodes: []NodeV1{
				{ID: "copy", Name: "QueryCache", Kind: "function", Path: tt.path, Span: "L1-L3"},
				{ID: "production", Name: "QueryCache", Kind: "function", Path: "src/cache.go", Span: "L1-L3"},
			}}
			result, err := Ask(wiring, tt.query, AskOptions{NoGraphRank: true})
			if err != nil {
				t.Fatalf("Ask(%q) error = %v, want nil", tt.query, err)
			}
			if len(result.Hits) == 0 {
				t.Fatalf("Ask(%q).Hits = %v, want explicit copy match", tt.query, result.Hits)
			}
			if result.Hits[0].Pointer != tt.path+":L1-L3" || result.Hits[0].Score != 1 {
				t.Errorf("Ask(%q).Hits[0] = %v, want %s:L1-L3 at full score 1", tt.query, result.Hits[0], tt.path)
			}
		})
	}
}

func TestAskExplicitCopyScopes(t *testing.T) {
	for _, tt := range []struct{ scope, path string }{
		{scope: "tests", path: "tests/cache.go"},
		{scope: "cmd/graft/testdata/", path: "cmd/graft/testdata/cache.go"},
		{scope: "fixtures", path: "fixtures/cache.go"},
		{scope: "src/__fixtures__", path: "src/__fixtures__/cache.ts"},
		{scope: "generated", path: "generated/cache.go"},
		{scope: "src/__generated__/client", path: "src/__generated__/client/cache.ts"},
		{scope: "vendor/example", path: "vendor/example/cache.go"},
		{scope: "src/cache.gen.go", path: "src/cache.gen.go"},
		{scope: "src/cache_test.go", path: "src/cache_test.go"},
	} {
		t.Run(tt.scope, func(t *testing.T) {
			t.Parallel()
			wiring := GraphV1{Nodes: []NodeV1{
				{ID: "copy", Name: "QueryCache", Kind: "function", Path: tt.path, Span: "L1-L3"},
				{ID: "production", Name: "QueryCache", Kind: "function", Path: "src/cache.go", Span: "L1-L3"},
			}}
			result, err := Ask(wiring, "QueryCache", AskOptions{In: tt.scope, NoGraphRank: true})
			if err != nil {
				t.Fatalf("Ask(QueryCache, In=%q) error = %v, want nil", tt.scope, err)
			}
			if len(result.Hits) != 1 {
				t.Fatalf("Ask(QueryCache, In=%q).Hits = %v, want one copy match", tt.scope, result.Hits)
			}
			if result.Hits[0].Pointer != tt.path+":L1-L3" || result.Hits[0].Score != 1 {
				t.Errorf("Ask(QueryCache, In=%q).Hits[0] = %v, want %s:L1-L3 at full score 1", tt.scope, result.Hits[0], tt.path)
			}
		})
	}
}
