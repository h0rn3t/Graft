package graph

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestResolveEdgesContract(t *testing.T) {
	method := func(path, owner, name string) NodeV1 {
		return NodeV1{ID: path + "#" + owner + "." + name, Name: name, Kind: "method", Path: path, Owner: &owner}
	}
	file := func(path string) NodeV1 { return NodeV1{ID: path, Name: filepath.Base(path), Kind: "file", Path: path} }
	class := func(path, name string) NodeV1 {
		return NodeV1{ID: path + "#" + name, Name: name, Kind: "class", Path: path}
	}
	function := func(path, name string) NodeV1 {
		return NodeV1{ID: path + "#" + name, Name: name, Kind: "function", Path: path}
	}
	nodes := []NodeV1{
		file("a.ts"), class("a.ts", "Base"), method("a.ts", "Base", "ping"), class("a.ts", "Child"),
		file("b.ts"), method("b.ts", "Svc", "run"), method("c.ts", "Svc", "run"), file("c.ts"),
		function("b.ts", "helper"), function("main.go", "helper"), file("main.go"),
		file("dir/index.ts"), file("pkg/util/u.go"), file("pkg/util/a.go"),
	}
	tests := []struct {
		name    string
		raw     rawEdge
		modules []goModule
		want    []EdgeV1
	}{
		{
			name: "typed member resolves in the same file",
			raw:  rawEdge{source: "a.ts#Base", relation: "calls", name: "ping", viaMember: true, recvType: "Base", file: "a.ts"},
			want: []EdgeV1{{Source: "a.ts#Base", Target: "a.ts#Base.ping", Relation: "calls", Confidence: "extracted"}},
		},
		{
			name: "typed member walks the extends chain",
			raw:  rawEdge{source: "a.ts#Child", relation: "calls", name: "ping", viaMember: true, recvType: "Child", file: "a.ts"},
			want: []EdgeV1{{Source: "a.ts#Child", Target: "a.ts#Base.ping", Relation: "calls", Confidence: "extracted"}},
		},
		{
			name: "ambiguous owner across other files is dropped",
			raw:  rawEdge{source: "a.ts#Base", relation: "calls", name: "run", viaMember: true, recvType: "Svc", file: "a.ts"},
		},
		{
			name: "member call without a receiver type is dropped",
			raw:  rawEdge{source: "a.ts#Base", relation: "calls", name: "ping", viaMember: true, file: "a.ts"},
		},
		{
			name: "bare call never crosses a language family",
			raw:  rawEdge{source: "a.ts#Base", relation: "calls", name: "helper", file: "a.ts"},
			want: []EdgeV1{{Source: "a.ts#Base", Target: "b.ts#helper", Relation: "calls", Confidence: "inferred"}},
		},
		{
			name: "relative import with a trailing slash does not reach an index file",
			raw:  rawEdge{source: "a.ts", relation: "imports", specifier: "./dir/", file: "a.ts"},
			want: []EdgeV1{{Source: "a.ts", Target: "./dir/", Relation: "imports", Confidence: "extracted"}},
		},
		{
			name: "relative import resolves to an index file",
			raw:  rawEdge{source: "a.ts", relation: "imports", specifier: "./dir", file: "a.ts"},
			want: []EdgeV1{{Source: "a.ts", Target: "dir/index.ts", Relation: "imports", Confidence: "extracted"}},
		},
		{
			name:    "go import resolves to the lowest file id in the package",
			raw:     rawEdge{source: "main.go", relation: "imports", specifier: "example.com/app/pkg/util", file: "main.go"},
			modules: []goModule{{module: "example.com/app", dir: "."}},
			want:    []EdgeV1{{Source: "main.go", Target: "pkg/util/a.go", Relation: "imports", Confidence: "extracted"}},
		},
		{
			name: "unresolved base keeps its name",
			raw:  rawEdge{source: "a.ts#Child", relation: "extends", name: "External", file: "a.ts"},
			want: []EdgeV1{{Source: "a.ts#Child", Target: "External", Relation: "extends", Confidence: "inferred"}},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			raw := []rawEdge{{source: "a.ts#Child", relation: "extends", name: "Base", file: "a.ts"}, tt.raw}
			got := resolveEdges(nodes, raw, tt.modules)[1:]
			if len(got) == 0 && len(tt.want) == 0 {
				return
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("resolveEdges(%+v) = %+v, want %+v", tt.raw, got, tt.want)
			}
		})
	}
}

func TestDiscoverScopesContract(t *testing.T) {
	root := t.TempDir()
	for _, rel := range []string{"package.json", "packages/a/package.json", "packages/a/src/x.ts", "packages/b/go.mod", "packages/b/y.go", "tools/deep/er/go.mod", "tools/deep/er/z.go", "lib/package.json", "lib/w.ts"} {
		path := filepath.Join(root, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
			t.Fatal(err)
		}
		content := ""
		if rel == "package.json" {
			content = `{"workspaces":["packages/*"]}`
		}
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	files := []string{"packages/a/src/x.ts", "packages/b/y.go", "tools/deep/er/z.go", "lib/w.ts"}
	got := discoverScopes(root, files)
	want := []ScopeV1{
		{Prefix: "packages/a", Label: "packages/a", Markers: []string{"package.json"}},
		{Prefix: "packages/b", Label: "packages/b", Markers: []string{"go.mod"}},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("discoverScopes(%q, %q) = %+v, want workspace scopes only (root package.json demoted, lib ignored, deep go.mod depth-guarded)", root, files, got)
	}

	thin := applyMinSubstanceGuard(got, []NodeV1{{Kind: "function", Path: "packages/a/src/x.ts"}})
	if !reflect.DeepEqual(thin, []ScopeV1{{Prefix: "", Label: "", Markers: []string{}}}) {
		t.Errorf("applyMinSubstanceGuard(%+v, one node) = %+v, want the canonical root scope", got, thin)
	}
}
