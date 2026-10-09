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

func TestResolveGoImportsByAssumedName(t *testing.T) {
	files := map[string]string{
		"kit/kit.go": "package kit\n\nfunc New() int { return 1 }\n",
		"cmd/main.go": "package main\n\nimport (\n\t\"example.com/kit/v2\"\n\t\"github.com/mattn/go-sqlite3\"\n" +
			"\t\"gopkg.in/yaml.v3\"\n\t\"k8s.io/klog/v2\"\n)\n\n" +
			"func main() {\n\tkit.New()\n\tyaml.Unmarshal(nil, nil)\n\tsqlite3.Version()\n\tklog.Info(\"x\")\n}\n",
	}
	edges, unresolved := resolveFiles(t, files, []goModule{{module: "example.com/app", dir: "."}, {module: "example.com/kit/v2", dir: "kit"}})
	if want := (EdgeV1{Source: "cmd/main.go#main", Target: "kit/kit.go#New", Relation: "calls", Confidence: "inferred"}); !slices.Contains(edges, want) {
		t.Errorf("resolveEdges(kit, main) has no %+v", want)
	}
	if want := (UnresolvedCalls{ExternalPackage: 3}); unresolved != want {
		t.Errorf("resolveEdges(kit, main) unresolved = %+v, want %+v", unresolved, want)
	}
}

func TestResolveGoMethodSetRules(t *testing.T) {
	files := map[string]string{
		"io.go": "package rw\n\n" +
			"type Reader interface{ Read() (int, error) }\n\n" +
			"type Closer interface{ Close() error }\n\n" +
			"type Flusher interface {\n\tClose() error\n\tFlush()\n}\n\n" +
			"type Both interface {\n\tCloser\n\tFlusher\n}\n",
		"types.go": "package rw\n\n" +
			"type One struct{}\n\nfunc (One) Read() int { return 0 }\n\n" +
			"type Two struct{}\n\nfunc (Two) Read() (n int, err error) { return 0, nil }\n\n" +
			"type A struct{}\n\nfunc (A) Close() error { return nil }\n\n" +
			"type B struct{}\n\nfunc (B) Close() error { return nil }\n\n" +
			"type Tied struct {\n\tA\n\tB\n}\n\n" +
			"type Deep struct{ B }\n\n" +
			"type Shallow struct {\n\tA\n\tDeep\n}\n\n" +
			"type OnlyFlush struct{}\n\nfunc (OnlyFlush) Flush() {}\n\n" +
			"type File struct{ A }\n\nfunc (File) Flush() {}\n",
	}
	edges, _ := resolveFiles(t, files, nil)
	implements := func(source, target string) bool {
		return slices.Contains(edges, EdgeV1{Source: source, Target: target, Relation: "implements", Confidence: "inferred"})
	}
	for _, tc := range []struct {
		source, target string
		want           bool
		why            string
	}{
		{"types.go#Two", "io.go#Reader", true, "two results match (int, error)"},
		{"types.go#One", "io.go#Reader", false, "one result does not match two"},
		{"types.go#A", "io.go#Closer", true, "its own Close"},
		{"types.go#Tied", "io.go#Closer", false, "A and B promote Close at the same depth"},
		{"types.go#Shallow", "io.go#Closer", true, "A's Close is shallower than Deep's B.Close"},
		{"types.go#A.Close", "io.go#Closer.Close", true, "the shallower method implements Close"},
		{"types.go#File", "io.go#Both", true, "Both requires Close once, from either interface"},
		{"types.go#OnlyFlush", "io.go#Both", false, "Close shared by Closer and Flusher is still required"},
	} {
		if got := implements(tc.source, tc.target); got != tc.want {
			t.Errorf("resolveEdges(io.go, types.go) %s implements %s = %t, want %t: %s", tc.source, tc.target, got, tc.want, tc.why)
		}
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
