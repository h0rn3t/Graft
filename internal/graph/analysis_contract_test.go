package graph

import (
	"fmt"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/h0rn3t/Graft/internal/sourcefiles"
)

// buildAnalysisRepo writes files under a fresh root and builds its graph.
func buildAnalysisRepo(t *testing.T, files map[string]string) (string, GraphV1) {
	t.Helper()
	root := t.TempDir()
	for rel, source := range files {
		writeGrepFile(t, root, rel, source)
	}
	opts := sourcefiles.Options{OutDir: filepath.Join(root, "graft")}
	built, err := BuildGraph(root, opts)
	if err != nil {
		t.Fatalf("BuildGraph(%q) error = %v, want nil", root, err)
	}
	return root, built.Graph
}

func TestShortestPathDispatchesThroughInterfaces(t *testing.T) {
	nodes := []NodeV1{{ID: "a#A"}, {ID: "b#B"}, {ID: "i#I.M"}, {ID: "t#T.M"}, {ID: "d#D"}}
	graph := GraphV1{Nodes: nodes, Edges: []EdgeV1{
		{Source: "a#A", Target: "b#B", Relation: "calls", Confidence: "extracted"},
		{Source: "a#A", Target: "i#I.M", Relation: "calls", Confidence: "inferred"},
		{Source: "t#T.M", Target: "i#I.M", Relation: "implements", Confidence: "inferred"},
		{Source: "t#T.M", Target: "d#D", Relation: "calls", Confidence: "extracted"},
	}}
	want := []PathStep{
		{From: "a#A", To: "i#I.M", Relation: "calls", Confidence: "inferred"},
		{From: "i#I.M", To: "t#T.M", Relation: "implements", Confidence: "inferred", Dispatch: true},
		{From: "t#T.M", To: "d#D", Relation: "calls", Confidence: "extracted"},
	}
	if got := ShortestPath(graph, nodes[:1], nodes[4:], 10); !reflect.DeepEqual(got, want) {
		t.Errorf("ShortestPath(A, D, 10) = %+v, want %+v", got, want)
	}
	if got := ShortestPath(graph, nodes[:1], nodes[4:], 2); got != nil {
		t.Errorf("ShortestPath(A, D, 2) = %+v, want nil: the path is 3 steps", got)
	}
	if got := ShortestPath(graph, nodes[4:], nodes[:1], 10); got != nil {
		t.Errorf("ShortestPath(D, A, 10) = %+v, want nil: edges are walked forward", got)
	}
}

func TestDependencyCycles(t *testing.T) {
	graph := GraphV1{
		Nodes: []NodeV1{
			{ID: "x/a.ts", Kind: "file", Path: "x/a.ts"}, {ID: "x/b.ts", Kind: "file", Path: "x/b.ts"},
			{ID: "y/c.ts", Kind: "file", Path: "y/c.ts"}, {ID: "y/d.ts", Kind: "file", Path: "y/d.ts"},
			{ID: "x/a.ts#f", Kind: "function", Path: "x/a.ts"}, {ID: "y/c.ts#g", Kind: "function", Path: "y/c.ts"},
			{ID: "tests/e.ts", Kind: "file", Path: "tests/e.ts"},
		},
		Edges: []EdgeV1{
			{Source: "x/a.ts", Target: "x/b.ts", Relation: "imports"},
			{Source: "x/b.ts", Target: "y/c.ts", Relation: "imports"},
			{Source: "y/c.ts", Target: "x/a.ts", Relation: "imports"},
			{Source: "y/c.ts", Target: "y/d.ts", Relation: "imports"},
			{Source: "y/d.ts", Target: "tests/e.ts", Relation: "imports"}, // test code is left out
			{Source: "tests/e.ts", Target: "y/d.ts", Relation: "imports"},
			{Source: "x/a.ts#f", Target: "y/c.ts#g", Relation: "calls"},
		},
	}
	files, err := DependencyCycles(graph, "file", "")
	if err != nil {
		t.Fatalf("DependencyCycles(file) error = %v, want nil", err)
	}
	wantFiles := []Cycle{{Members: []string{"x/a.ts", "x/b.ts", "y/c.ts"}, Loop: []CycleHop{
		{From: "x/a.ts", To: "x/b.ts", Evidence: graph.Edges[0]},
		{From: "x/b.ts", To: "y/c.ts", Evidence: graph.Edges[1]},
		{From: "y/c.ts", To: "x/a.ts", Evidence: graph.Edges[2]},
	}}}
	if !reflect.DeepEqual(files, wantFiles) {
		t.Errorf("DependencyCycles(file) = %+v, want %+v", files, wantFiles)
	}
	dirs, err := DependencyCycles(graph, "dir", "")
	if err != nil {
		t.Fatalf("DependencyCycles(dir) error = %v, want nil", err)
	}
	wantDirs := []Cycle{{Members: []string{"x", "y"}, Loop: []CycleHop{
		{From: "x", To: "y", Evidence: graph.Edges[1]},
		{From: "y", To: "x", Evidence: graph.Edges[2]},
	}}}
	if !reflect.DeepEqual(dirs, wantDirs) {
		t.Errorf("DependencyCycles(dir) = %+v, want %+v", dirs, wantDirs)
	}
	if _, err := DependencyCycles(graph, "file", "missing/"); err == nil {
		t.Errorf("DependencyCycles(file, in missing/) error = nil, want an unindexed-prefix error")
	}
}

func TestMostComplexAndHotspots(t *testing.T) {
	function := func(id, path string, complexity int) NodeV1 {
		return NodeV1{ID: id, Name: id[len(path)+1:], Kind: "function", Path: path, Span: "L1-L2", Complexity: &complexity}
	}
	graph := GraphV1{Nodes: []NodeV1{
		{ID: "a.go", Kind: "file", Path: "a.go"}, {ID: "b.go", Kind: "file", Path: "b.go"},
		function("a.go#f", "a.go", 5), function("a.go#g", "a.go", 2), function("b.go#h", "b.go", 9),
		function("testdata/c.go#x", "testdata/c.go", 50),
	}}
	functions, err := MostComplex(graph, "")
	if err != nil {
		t.Fatalf("MostComplex() error = %v, want nil", err)
	}
	var names []string
	for _, node := range functions {
		names = append(names, node.Name)
	}
	if want := []string{"h", "f", "g"}; !reflect.DeepEqual(names, want) {
		t.Errorf("MostComplex() = %q, want %q (testdata excluded)", names, want)
	}
	spots, err := Hotspots(graph, map[string]int{"a.go": 10, "b.go": 2, "testdata/c.go": 99}, "")
	if err != nil {
		t.Fatalf("Hotspots() error = %v, want nil", err)
	}
	want := []Hotspot{
		{Path: "a.go", Commits: 10, Complexity: 7, Score: 78, Top: graph.Nodes[2]},
		{Path: "b.go", Commits: 2, Complexity: 9, Score: 20, Top: graph.Nodes[4]},
	}
	if !reflect.DeepEqual(spots, want) {
		t.Errorf("Hotspots() = %+v, want %+v", spots, want)
	}
}

func TestFindDeadCode(t *testing.T) {
	root, graph := buildAnalysisRepo(t, map[string]string{
		"app.py": "def used():\n    pass\n\n" +
			"def caller():\n    used()\n\n" +
			"def _orphan():\n    pass\n\n" +
			"def exported_orphan():\n    pass\n\n" +
			"def mentioned():\n    pass\n\n" +
			"@app.get(\n    \"/x\",\n)\ndef decorated():\n    pass\n\n" +
			"def _recursive():\n    _recursive()\n\n" +
			"class K:\n    def __init__(self):\n        pass\n\n" +
			"    def dynamic(self):\n        pass\n\n" +
			"def main():\n    x = get()\n    x.dynamic()\n    print(\"mentioned\")\n",
		"tests/test_app.py": "def helper():\n    pass\n",
	})
	dead, err := FindDeadCode(graph, root, "")
	if err != nil {
		t.Fatalf("FindDeadCode() error = %v, want nil", err)
	}
	got := make([]string, 0, len(dead))
	for _, symbol := range dead {
		got = append(got, fmt.Sprintf("%s %s", symbol.Confidence, symbol.Node.Name))
	}
	want := []string{
		"high _orphan", "high _recursive",
		"medium caller", "medium exported_orphan",
		"low mentioned", "low decorated", "low dynamic",
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("FindDeadCode() = %q, want %q", got, want)
	}
}

func TestFindRoutesGoFrameworks(t *testing.T) {
	root, graph := buildAnalysisRepo(t, map[string]string{
		"go.mod": "module example.com/app\n",
		"main.go": "package main\n\n" +
			"func main() {\n" + // 3
			"\tapp := fiber.New()\n" +
			"\tapp.Get(\"/users/:id\", getUser)\n" + // 5
			"\tapp.All(\"/any\", getUser)\n" +
			"\tapp.Add(fiber.MethodPut, \"/users/:id\", update)\n" + // 7
			"\tapp.Add(\"DELETE\", \"/users/:id\", update)\n" +
			"\tapp.Connect(\"/tunnel\", getUser)\n" + // 9
			"\tapi := app.Group(\"/api\")\n" +
			"\tv1 := api.Group(\"/v1\")\n" + // 11
			"\tv1.Get(\"/health\", func(c *fiber.Ctx) error { return nil })\n" +
			"\tapp.Group(\"/admin\").Post(\"/login\", getUser)\n" + // 13
			"\tapp.Use(\"/static\", getUser)\n" +
			"}\n\n" +
			"func chiRoutes(r chi.Router) {\n" + // 17
			"\tr.Route(\"/articles\", func(r chi.Router) {\n" +
			"\t\tr.Get(\"/\", getUser)\n" + // 19
			"\t\tr.Method(\"PATCH\", \"/{id}\", getUser)\n" +
			"\t})\n" + // 21
			"\tr.Get(\"/ping\", getUser)\n" +
			"}\n\n" + // 23
			"func ginRoutes(g *gin.Engine) {\n" + // 25
			"\tg.Handle(\"GET\", \"/ready\", getUser)\n" +
			"\tapi.Get(\"/leak\", getUser)\n" + // 27: main's api group does not reach here
			"}\n\n" +
			"func getUser(c *fiber.Ctx) error { return nil }\n" + // 30
			"func update(c *fiber.Ctx) error { return nil }\n",
	})
	routes, err := FindRoutes(graph, root, "")
	if err != nil {
		t.Fatalf("FindRoutes() error = %v, want nil", err)
	}
	got := make([]string, 0, len(routes))
	for _, route := range routes {
		handler := route.HandlerText
		if route.Handler != nil {
			handler = route.Handler.ID
		}
		got = append(got, fmt.Sprintf("%s %s :%d %s", route.Method, route.Path, route.Line, handler))
	}
	want := []string{
		"POST /admin/login :13 main.go#getUser",
		"ANY /any :6 main.go#getUser",
		"GET /api/v1/health :12 inline function",
		"GET /articles :19 main.go#getUser",
		"PATCH /articles/{id} :20 main.go#getUser",
		"GET /leak :27 main.go#getUser",
		"GET /ping :22 main.go#getUser",
		"GET /ready :26 main.go#getUser",
		"CONNECT /tunnel :9 main.go#getUser",
		"DELETE /users/:id :8 main.go#update",
		"GET /users/:id :5 main.go#getUser",
		"PUT /users/:id :7 main.go#update",
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("FindRoutes(fiber, chi, gin) =\n%q\nwant\n%q", got, want)
	}
}

func TestFindRoutes(t *testing.T) {
	root, graph := buildAnalysisRepo(t, map[string]string{
		"go.mod": "module example.com/app\n",
		"server/routes.go": "package server\n\nfunc routes() {\n" +
			"\tmux.HandleFunc(\"GET /users/{id}\", getUser)\n" +
			"\tr.POST(`/users`, func(c *gin.Context) {})\n" +
			"\tr.Route(\"/admin\", func(r chi.Router) {})\n}\n\n" +
			"func getUser() {}\n",
		"api/app.py": "@app.route(\"/items\", methods=[\"GET\", \"POST\"])\ndef items():\n    pass\n\n" +
			"@router.delete(\"/items/{id}\")\ndef drop(id):\n    pass\n",
		"web/server.ts": "app.get('/health', health);\nrouter.post(`/login`, (req, res) => {});\n" +
			"axios.get('/api/users', handler);\nmap.get('/k');\n" +
			"function health() {}\n" +
			"@Controller('orders')\nclass Orders {\n  @Get(':id')\n  find() {}\n}\n",
		"src/Api.java": "@RequestMapping(\"/api\")\nclass Api {\n  @GetMapping(\"/x\")\n  void x() {}\n" +
			"  @RequestMapping(value = \"/y\", method = RequestMethod.POST)\n  void y() {}\n}\n",
	})
	routes, err := FindRoutes(graph, root, "")
	if err != nil {
		t.Fatalf("FindRoutes() error = %v, want nil", err)
	}
	got := make([]string, 0, len(routes))
	for _, route := range routes {
		handler := route.HandlerText
		if route.Handler != nil {
			handler = route.Handler.ID
		}
		got = append(got, fmt.Sprintf("%s %s %s:%d %s", route.Method, route.Path, route.File, route.Line, handler))
	}
	want := []string{
		"GET /api/x src/Api.java:3 src/Api.java#Api.x",
		"POST /api/y src/Api.java:5 src/Api.java#Api.y",
		"GET /health web/server.ts:1 web/server.ts#health",
		"GET|POST /items api/app.py:1 api/app.py#items",
		"DELETE /items/{id} api/app.py:5 api/app.py#drop",
		"POST /login web/server.ts:2 inline function",
		"GET /orders/:id web/server.ts:8 web/server.ts#Orders.find",
		"POST /users server/routes.go:5 inline function",
		"GET /users/{id} server/routes.go:4 server/routes.go#getUser",
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("FindRoutes() =\n%q\nwant\n%q", got, want)
	}
}
