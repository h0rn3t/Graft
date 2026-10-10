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
	edges, unresolved, _ := resolveEdges(nodes, raw, modules)
	// The cases compare what links to what; TestResolveEdgeLines owns lines.
	for index := range edges {
		edges[index].Line = 0
	}
	return edges, unresolved
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

func TestResolveGoFunctionValues(t *testing.T) {
	files := map[string]string{
		"kit/kit.go": "package kit\n\nfunc Helper() {}\n",
		"app/app.go": "package app\n\nimport \"example.com/m/kit\"\n\n" +
			"type Server struct{}\n\nfunc (s *Server) Serve() {}\n\nfunc (s *Server) Start() { register(s.Serve) }\n\n" +
			"type Command struct {\n\tRunE  func() error\n\tHooks []func()\n}\n\n" +
			"func RunE() error { return nil }\n\nfunc handle() {}\n\nfunc run() error { return nil }\n\n" +
			"func pre() {}\n\nfunc post() {}\n\nfunc onTick() {}\n\nfunc cb() {}\n\n" +
			"var single = onTick\n\nfunc register(f func()) {}\n\n" +
			"func setup(s *Server) {\n\tregister(handle)\n\tc := &Command{RunE: run, Hooks: []func(){pre, post}}\n" +
			"\tf := s.Serve\n\tg := kit.Helper\n\t_, _, _ = c, f, g\n}\n\n" +
			"func factory() func() { return handle }\n\nfunc recur() { register(recur) }\n\n" +
			"func shadow(cb func()) {\n\tregister(cb)\n\tpost := func() {}\n\tregister(post)\n}\n",
	}
	edges, _ := resolveFiles(t, files, []goModule{{module: "example.com/m", dir: "."}})

	reference := func(source, target string) EdgeV1 {
		return EdgeV1{Source: source, Target: target, Relation: "references", Confidence: "inferred"}
	}
	for _, want := range []EdgeV1{
		reference("app/app.go#setup", "app/app.go#handle"),              // call argument
		reference("app/app.go#setup", "app/app.go#run"),                 // keyed element value
		reference("app/app.go#setup", "app/app.go#pre"),                 // literal list element
		reference("app/app.go#setup", "app/app.go#post"),                // literal list element
		reference("app/app.go#setup", "app/app.go#Server.Serve"),        // method value through a typed parameter
		reference("app/app.go#setup", "kit/kit.go#Helper"),              // package function value
		reference("app/app.go#Server.Start", "app/app.go#Server.Serve"), // method value through the receiver
		reference("app/app.go#factory", "app/app.go#handle"),            // returned value
		reference("app/app.go#single", "app/app.go#onTick"),             // package-level var value
		{Source: "app/app.go#setup", Target: "app/app.go#register", Relation: "calls", Confidence: "extracted"},
	} {
		if !slices.Contains(edges, want) {
			t.Errorf("resolveEdges(app, kit) has no %+v", want)
		}
	}
	for _, unwanted := range []struct{ source, target string }{
		{"app/app.go#setup", "app/app.go#RunE"},     // a keyed element's key is a field name
		{"app/app.go#setup", "app/app.go#register"}, // a callee is a call, not a value
		{"app/app.go#recur", "app/app.go#recur"},    // no self references
		{"app/app.go#shadow", "app/app.go#cb"},      // the parameter cb hides the function
		{"app/app.go#shadow", "app/app.go#post"},    // the local post hides the function
	} {
		if slices.Contains(edges, reference(unwanted.source, unwanted.target)) {
			t.Errorf("resolveEdges(app, kit) has %s references %s, want none", unwanted.source, unwanted.target)
		}
	}
}

func TestResolveGoTypeUses(t *testing.T) {
	files := map[string]string{
		"kit/kit.go": "package kit\n\ntype Config struct{}\n\ntype Option func(*Config)\n",
		"app/app.go": "package app\n\nimport \"example.com/m/kit\"\n\n" +
			"type ID int\n\ntype IDs []ID\n\ntype T struct{}\n\ntype Base struct{}\n\ntype Item struct{ id ID }\n\n" +
			"type Store struct {\n\tcfg  kit.Config\n\tnext *Store\n\tbyID map[ID]*Item\n\tBase\n}\n\n" +
			"type Handler func(*Item) error\n\n" +
			"type List[T any] struct{ items []T }\n\nfunc (l *List[T]) Push(x T) {}\n\n" +
			"func (s *Store) Get(id ID) (*Item, error) { return nil, nil }\n\n" +
			"func NewStore(opts ...kit.Option) *Store { return &Store{} }\n\n" +
			"func toID(n int) { _ = ID(n) }\n\nfunc toOption(f func(*kit.Config)) { _ = kit.Option(f) }\n\n" +
			"func cast(v any) { _ = v.(*Item) }\n\nfunc alloc() { _ = new(Base) }\n\n" +
			"func Map[T any](xs []T) []T { return xs }\n\nvar defaultID ID\n",
	}
	edges, _ := resolveFiles(t, files, []goModule{{module: "example.com/m", dir: "."}})

	reference := func(source, target string) EdgeV1 {
		return EdgeV1{Source: source, Target: target, Relation: "references", Confidence: "inferred"}
	}
	for _, want := range []EdgeV1{
		reference("app/app.go#IDs", "app/app.go#ID"),           // underlying type
		reference("app/app.go#Item.id", "app/app.go#ID"),       // field type
		reference("app/app.go#Store.cfg", "kit/kit.go#Config"), // qualified field type
		reference("app/app.go#Store.byID", "app/app.go#Item"),  // map value type
		reference("app/app.go#Handler", "app/app.go#Item"),     // function type parameter
		reference("app/app.go#Store.Get", "app/app.go#ID"),     // parameter type
		reference("app/app.go#Store.Get", "app/app.go#Item"),   // result type
		reference("app/app.go#NewStore", "kit/kit.go#Option"),  // variadic qualified parameter
		reference("app/app.go#NewStore", "app/app.go#Store"),   // result and composite literal
		reference("app/app.go#toID", "app/app.go#ID"),          // conversion
		reference("app/app.go#toOption", "kit/kit.go#Option"),  // qualified conversion
		reference("app/app.go#cast", "app/app.go#Item"),        // type assertion
		reference("app/app.go#alloc", "app/app.go#Base"),       // new(T)
		reference("app/app.go#defaultID", "app/app.go#ID"),     // package-level var type
	} {
		if !slices.Contains(edges, want) {
			t.Errorf("resolveEdges(app, kit) has no %+v", want)
		}
	}
	for _, unwanted := range []struct{ source, target string }{
		{"app/app.go#Store", "app/app.go#Store"},     // no self references
		{"app/app.go#Store", "app/app.go#Base"},      // an embedded field extends, it is no reference
		{"app/app.go#Store.Get", "app/app.go#Store"}, // a receiver owns the method
		{"app/app.go#List.items", "app/app.go#T"},    // the type parameter T hides the type
		{"app/app.go#List.Push", "app/app.go#T"},     // so does a generic receiver's
		{"app/app.go#Map", "app/app.go#T"},           // and a generic function's
	} {
		if slices.Contains(edges, reference(unwanted.source, unwanted.target)) {
			t.Errorf("resolveEdges(app, kit) has %s references %s, want none", unwanted.source, unwanted.target)
		}
	}
}

func TestResolveGoReceiverTyping(t *testing.T) {
	files := map[string]string{
		"kit/kit.go": "package kit\n\n" +
			"type Client struct{}\n\nfunc (c *Client) Do() {}\n\n" +
			"type Base struct{ log *Client }\n\nfunc (b *Base) Ping() {}\n\n" +
			"func NewClient() *Client { return &Client{} }\n",
		"api/api.go": "package api\n\ntype Client struct{}\n\nfunc (c *Client) Do() {}\n",
		"app/app.go": "package app\n\nimport (\n\t\"database/sql\"\n\n\t\"example.com/m/api\"\n\t\"example.com/m/kit\"\n)\n\n" +
			"type Client struct{}\n\nfunc (c *Client) Do() {}\n\n" +
			"type DB struct{}\n\nfunc (d *DB) Query() {}\n\n" +
			"type Service struct {\n\tclient *kit.Client\n\tlocal  Client\n\tinner  *Service\n\tkit.Base\n}\n\n" +
			"func (s *Service) Run() {\n\ts.client.Do()\n\ts.local.Do()\n\ts.inner.inner.client.Do()\n\ts.Base.Ping()\n\ts.Ping()\n}\n\n" +
			"func (s *Service) Promoted() { s.log.Do() }\n\n" +
			"func (s *Service) Value() { register(s.client.Do) }\n\n" +
			"func register(f func()) {}\n\n" +
			"func useKit(c *kit.Client) { c.Do() }\n\n" +
			"func useAPI(c api.Client) { c.Do() }\n\n" +
			"func useSQL(db *sql.DB) { db.Query() }\n\n" +
			"func assert(v any) {\n\tv.(*kit.Client).Do()\n}\n\n" +
			"func assertLocal(v any) { v.(Client).Do() }\n\n" +
			"func bind(v any) {\n\tc, ok := v.(*api.Client)\n\t_ = ok\n\tc.Do()\n}\n\n" +
			"func construct() {\n\tc := kit.NewClient()\n\tc.Do()\n}\n\n" +
			"type Logger interface{ Log() }\n\n" +
			"func shadow(l Logger) {\n\tif l, ok := l.(*kit.Client); ok {\n\t\tl.Do()\n\t}\n\tl.Log()\n}\n",
	}
	edges, _ := resolveFiles(t, files, []goModule{{module: "example.com/m", dir: "."}})

	call := func(source, target string) EdgeV1 {
		return EdgeV1{Source: source, Target: target, Relation: "calls", Confidence: "inferred"}
	}
	for _, want := range []EdgeV1{
		call("app/app.go#Service.Run", "kit/kit.go#Client.Do"),                                                         // field of an imported type
		{Source: "app/app.go#Service.Run", Target: "app/app.go#Client.Do", Relation: "calls", Confidence: "extracted"}, // field of a local type
		call("app/app.go#Service.Run", "kit/kit.go#Base.Ping"),                                                         // embedded field selected by its type name, and promoted
		call("app/app.go#Service.Promoted", "kit/kit.go#Client.Do"),                                                    // field promoted from an embedded type of another package
		call("app/app.go#useKit", "kit/kit.go#Client.Do"),                                                              // parameter of an imported type, though app declares Client.Do
		call("app/app.go#useAPI", "api/api.go#Client.Do"),
		call("app/app.go#assert", "kit/kit.go#Client.Do"),    // type assertion
		call("app/app.go#bind", "api/api.go#Client.Do"),      // comma-ok assertion bound to a variable
		call("app/app.go#construct", "kit/kit.go#Client.Do"), // kit.NewClient makes a kit.Client
		{Source: "app/app.go#assertLocal", Target: "app/app.go#Client.Do", Relation: "calls", Confidence: "extracted"},
		{Source: "app/app.go#shadow", Target: "app/app.go#Logger.Log", Relation: "calls", Confidence: "extracted"},           // l.(T) retypes l in its block only
		{Source: "app/app.go#Service.Value", Target: "kit/kit.go#Client.Do", Relation: "references", Confidence: "inferred"}, // method value through a field
	} {
		if !slices.Contains(edges, want) {
			t.Errorf("resolveEdges(app, kit, api) has no %+v", want)
		}
	}
	for _, unwanted := range []struct{ source, target string }{
		{"app/app.go#useKit", "app/app.go#Client.Do"},
		{"app/app.go#useKit", "api/api.go#Client.Do"},
		{"app/app.go#useAPI", "app/app.go#Client.Do"},
		{"app/app.go#useSQL", "app/app.go#DB.Query"}, // sql.DB is no type of this repository
		{"app/app.go#construct", "app/app.go#Client.Do"},
	} {
		if slices.ContainsFunc(edges, func(edge EdgeV1) bool { return edge.Source == unwanted.source && edge.Target == unwanted.target }) {
			t.Errorf("resolveEdges(app, kit, api) links %s to %s, want no edge", unwanted.source, unwanted.target)
		}
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

func TestResolveGoValueUses(t *testing.T) {
	files := map[string]string{
		"kit/kit.go": "package kit\n\nconst Limit = 3\n\nvar Default = 1\n",
		"app/app.go": "package app\n\nimport \"example.com/m/kit\"\n\n" +
			"type Kind int\n\nconst (\n\tKindA Kind = iota\n\tKindB\n)\n\n" +
			"type Weights map[Kind]int\n\n" +
			"var hits int\n\nvar hook = func() {}\n\n" +
			"type Base struct{ Inner int }\n\n" +
			"type Box struct {\n\tBase\n\tName  string\n\tcache map[string]int\n\tcb    func()\n}\n\n" +
			"func (b *Box) Fill(k string) {\n\tb.Name = \"x\"\n\tb.cache[k] = 1\n\thits++\n\tb.cb()\n\thook()\n}\n\n" +
			"func read(b *Box) int { return b.Inner + kit.Limit + kit.Default + len(b.Name) }\n\n" +
			"func build() []Box {\n\t_ = map[Kind]int{KindA: 1}\n\t_ = Weights{KindB: 2}\n\treturn []Box{{Name: \"a\"}}\n}\n\n" +
			"func shadow() int {\n\thits := 1\n\treturn hits\n}\n\n" +
			"func load() *Box { return nil }\n\n" +
			"func untyped() string {\n\tb := load()\n\treturn b.Name\n}\n",
	}
	edges, _ := resolveFiles(t, files, []goModule{{module: "example.com/m", dir: "."}})

	edge := func(source, relation, target string) EdgeV1 {
		return EdgeV1{Source: "app/app.go#" + source, Target: target, Relation: Relation(relation), Confidence: "inferred"}
	}
	for _, want := range []EdgeV1{
		edge("Box.Fill", "writes", "app/app.go#Box.Name"),  // assigned
		edge("Box.Fill", "writes", "app/app.go#Box.cache"), // stored into through an index
		edge("Box.Fill", "writes", "app/app.go#hits"),      // stepped
		// Called through a field of function type: bound like a method call.
		{Source: "app/app.go#Box.Fill", Target: "app/app.go#Box.cb", Relation: "references", Confidence: "extracted"},
		edge("Box.Fill", "references", "app/app.go#hook"),   // called through a variable
		edge("read", "references", "app/app.go#Base.Inner"), // promoted through an embedded struct
		edge("read", "references", "app/app.go#Box.Name"),   // read
		edge("read", "references", "kit/kit.go#Limit"),      // an imported package's constant
		edge("read", "references", "kit/kit.go#Default"),    // and variable
		edge("build", "references", "app/app.go#KindA"),     // a map literal's key is a value
		edge("build", "references", "app/app.go#KindB"),     // so is a key of a literal of a map type
		edge("build", "writes", "app/app.go#Box.Name"),      // an elided element's key is a field
	} {
		if !slices.Contains(edges, want) {
			t.Errorf("resolveEdges(app, kit) has no %+v", want)
		}
	}
	for _, unwanted := range []EdgeV1{
		edge("shadow", "references", "app/app.go#hits"),                                                          // the local hits hides the variable
		edge("untyped", "references", "app/app.go#Box.Name"),                                                     // b's type is not known
		{Source: "app/app.go#Box.Fill", Target: "app/app.go#Box.cb", Relation: "calls", Confidence: "extracted"}, // a field is read, not called
	} {
		if slices.Contains(edges, unwanted) {
			t.Errorf("resolveEdges(app, kit) has %+v, want none", unwanted)
		}
	}
}

func TestResolveCountsUnresolvedNames(t *testing.T) {
	files := map[string]string{
		"app/lock_unix.go":    "package app\n\nfunc tryLock() bool { return true }\n",
		"app/lock_windows.go": "package app\n\nfunc tryLock() bool { return false }\n",
		"kit/lock_unix.go":    "package kit\n\nfunc Lock() {}\n",
		"kit/lock_windows.go": "package kit\n\nfunc Lock() {}\n",
		"app/app.go": "package app\n\nimport \"example.com/m/kit\"\n\ntype Box struct{ Name string }\n\nfunc (Box) Close() {}\n\n" +
			"func load() *Box { return nil }\n\n" +
			"func run() string {\n\tb := load()\n\tb.Close()\n\t_ = tryLock()\n\tkit.Lock()\n\treturn b.Name + b.Missing\n}\n",
	}
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
	_, _, names := resolveEdges(nodes, raw, []goModule{{module: "example.com/m", dir: "."}})
	want := map[string]UnresolvedName{
		"Close":   {Untyped: 1},   // a call on a receiver of unknown type
		"Name":    {Untyped: 1},   // and a field read on one
		"tryLock": {Ambiguous: 1}, // two definitions of one name
		"Lock":    {Ambiguous: 1}, // and of one name in an imported package
	}
	if !maps.Equal(names, want) {
		t.Errorf("resolveEdges(app) unresolved names = %v, want %v (Missing names no symbol)", names, want)
	}
}

func TestResolveEdgeLines(t *testing.T) {
	const source = "package app\n\nimport \"fmt\"\n\ntype Box struct{ n int }\n\n" +
		"func (b *Box) inc() { b.n++ }\n\n" +
		"func run(b *Box) {\n\tfmt.Println(b.n)\n\tb.inc()\n\tb.inc()\n}\n"
	got, err := extractFile("app.go", source)
	if err != nil {
		t.Fatalf("extractFile(%q, source) error = %v, want nil", "app.go", err)
	}
	edges, _, _ := resolveEdges(got.nodes, got.rawEdges, nil)
	for _, want := range []EdgeV1{
		{Source: "app.go#Box.inc", Target: "app.go#Box.n", Relation: "writes", Confidence: "inferred", Line: 7},
		{Source: "app.go#run", Target: "app.go#Box.n", Relation: "references", Confidence: "inferred", Line: 10},
		// The first of two calls gives the edge its line.
		{Source: "app.go#run", Target: "app.go#Box.inc", Relation: "calls", Confidence: "extracted", Line: 11},
		// contains is no use, and has no line.
		{Source: "app.go#Box", Target: "app.go#Box.n", Relation: "contains", Confidence: "extracted"},
	} {
		if !slices.Contains(edges, want) {
			t.Errorf("resolveEdges(extractFile(%q)) = %+v, want %+v", "app.go", edges, want)
		}
	}
}
