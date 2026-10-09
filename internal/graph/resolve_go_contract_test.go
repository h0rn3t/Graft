package graph

import (
	"maps"
	"slices"
	"testing"
)

// resolveFiles extracts each file and resolves the combined graph.
func resolveFiles(t *testing.T, files map[string]string, modules []goModule) ([]EdgeV1, UnresolvedCalls) {
	t.Helper()
	var nodes []NodeV1
	var raw []rawEdge
	for _, rel := range slices.Sorted(maps.Keys(files)) {
		got, err := extractFile(rel, files[rel])
		if err != nil {
			t.Fatalf("extractFile(%q) error = %v, want nil", rel, err)
		}
		nodes = append(nodes, got.nodes...)
		raw = append(raw, got.rawEdges...)
	}
	return resolveEdges(nodes, raw, modules)
}

func TestResolveGoInterfacesAndPackageCalls(t *testing.T) {
	files := map[string]string{
		"store/store.go": "package store\n\n" +
			"type Getter interface{ Get(k string) string }\n\n" +
			"type ReadCloser interface {\n\tGetter\n\tClose() error\n}\n\n" +
			"type hidden interface{ secret() }\n\n" +
			"type Base struct{}\n\nfunc (Base) Close() error { return nil }\n\n" +
			"type Store struct{ Base }\n\n" +
			"func NewStore() *Store { return &Store{} }\n\n" +
			"func (s *Store) Get(k string) string { return k }\n\n" +
			"type Wrong struct{}\n\nfunc (Wrong) Get(a, b string) string { return a }\n",
		"cmd/main.go": "package main\n\nimport (\n\t\"fmt\"\n\t\"example.com/app/store\"\n)\n\n" +
			"type local struct{}\n\nfunc (local) secret() {}\n\n" +
			"func use(g store.Getter) string { return g.Get(\"k\") }\n\n" +
			"func main() {\n\ts := store.NewStore()\n\tfmt.Println(s.Get(\"k\"), use(s))\n\ts.Close()\n}\n",
	}
	edges, unresolved := resolveFiles(t, files, []goModule{{module: "example.com/app", dir: "."}})

	for _, want := range []EdgeV1{
		{Source: "store/store.go#ReadCloser", Target: "store/store.go#Getter", Relation: "extends", Confidence: "extracted"},
		{Source: "store/store.go#Store", Target: "store/store.go#Base", Relation: "extends", Confidence: "extracted"},
		{Source: "store/store.go#Store", Target: "store/store.go#Getter", Relation: "implements", Confidence: "inferred"},
		{Source: "store/store.go#Store.Get", Target: "store/store.go#Getter.Get", Relation: "implements", Confidence: "inferred"},
		{Source: "store/store.go#Store", Target: "store/store.go#ReadCloser", Relation: "implements", Confidence: "inferred"},
		{Source: "store/store.go#Base.Close", Target: "store/store.go#ReadCloser.Close", Relation: "implements", Confidence: "inferred"},
		{Source: "cmd/main.go#main", Target: "store/store.go#NewStore", Relation: "calls", Confidence: "inferred"},
		{Source: "cmd/main.go#main", Target: "store/store.go#Store.Get", Relation: "calls", Confidence: "inferred"},
		{Source: "cmd/main.go#main", Target: "store/store.go#Base.Close", Relation: "calls", Confidence: "inferred"},
		{Source: "cmd/main.go#main", Target: "cmd/main.go#use", Relation: "calls", Confidence: "extracted"},
		{Source: "cmd/main.go#use", Target: "store/store.go#Getter.Get", Relation: "calls", Confidence: "inferred"},
	} {
		if !slices.Contains(edges, want) {
			t.Errorf("resolveEdges(store, main) has no %+v", want)
		}
	}
	for _, unwanted := range []struct{ source, target string }{
		{"store/store.go#Wrong", "store/store.go#Getter"},          // Get takes two arguments, not one
		{"store/store.go#Base", "store/store.go#ReadCloser"},       // Base has Close but no Get
		{"cmd/main.go#local", "store/store.go#hidden"},             // unexported methods stay in their package
		{"store/store.go#Getter", "store/store.go#ReadCloser"},     // interfaces implement nothing
		{"store/store.go#Getter.Get", "store/store.go#Getter.Get"}, // no self loops
	} {
		if slices.ContainsFunc(edges, func(edge EdgeV1) bool {
			return edge.Source == unwanted.source && edge.Target == unwanted.target && edge.Relation == "implements"
		}) {
			t.Errorf("resolveEdges(store, main) has %s implements %s, want none", unwanted.source, unwanted.target)
		}
	}
	if want := (UnresolvedCalls{ExternalPackage: 1}); unresolved != want {
		t.Errorf("resolveEdges(store, main) unresolved = %+v, want %+v", unresolved, want)
	}
}

func TestResolveGoEmbedsExternalInterface(t *testing.T) {
	files := map[string]string{
		"rw.go": "package rw\n\nimport \"io\"\n\n" +
			"type ReadCloser interface {\n\tio.Reader\n\tClose() error\n}\n\n" +
			"type File struct{}\n\nfunc (File) Close() error { return nil }\n",
	}
	edges, _ := resolveFiles(t, files, nil)
	if want := (EdgeV1{Source: "rw.go#ReadCloser", Target: "io.Reader", Relation: "extends", Confidence: "inferred"}); !slices.Contains(edges, want) {
		t.Errorf("resolveEdges(rw.go) has no %+v", want)
	}
	if slices.ContainsFunc(edges, func(edge EdgeV1) bool { return edge.Relation == "implements" }) {
		t.Errorf("resolveEdges(rw.go) = %+v, want no implements: io.Reader's methods are unknown", edges)
	}
}

func TestResolveCountsUnresolvedCalls(t *testing.T) {
	files := map[string]string{
		"a.py": "def helper():\n    pass\n",
		"b.py": "def helper():\n    pass\n",
		"main.py": "class Box:\n    def put(self):\n        pass\n\n" +
			"def run(x):\n    x.go()\n    helper()\n    print()\n    b = Box()\n    b.take()\n",
	}
	_, unresolved := resolveFiles(t, files, nil)
	want := UnresolvedCalls{ReceiverUnknown: 1, MemberNotInGraph: 1, NameAmbiguous: 1, NameNotInGraph: 1}
	if unresolved != want {
		t.Errorf("resolveEdges(a.py, b.py, main.py) unresolved = %+v, want %+v", unresolved, want)
	}
}
