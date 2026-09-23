package graph

import (
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/NanoNets/context-graph-engine/internal/sourcefiles"
)

func TestSQLExtractorContract(t *testing.T) {
	const source = "CREATE TABLE app.teams (id bigint PRIMARY KEY);\n" +
		"CREATE TABLE app.users (id bigint, team_id bigint REFERENCES app.teams(id));\n" +
		"CREATE VIEW app.active_users AS SELECT u.id FROM app.users u JOIN app.teams t ON t.id = u.team_id;\n" +
		"CREATE TYPE app.status AS ENUM ('active', 'inactive');\n" +
		"CREATE FUNCTION app.count_users() RETURNS bigint LANGUAGE sql AS $$ SELECT count(*) FROM app.users; $$;\n" +
		"CREATE PROCEDURE app.noop() LANGUAGE sql AS $$ SELECT 1; $$;\n"
	got, err := extractFile("db/schema.SQL", source)
	if err != nil {
		t.Fatalf("extractFile(%q, source) error = %v, want nil", "db/schema.SQL", err)
	}
	want := []struct {
		id   string
		kind Kind
	}{
		{"db/schema.SQL", "file"},
		{"db/schema.SQL#app.teams", "type"},
		{"db/schema.SQL#app.users", "type"},
		{"db/schema.SQL#app.active_users", "type"},
		{"db/schema.SQL#app.status", "type"},
		{"db/schema.SQL#app.count_users", "function"},
		{"db/schema.SQL#app.noop", "function"},
	}
	if len(got.nodes) != len(want) {
		t.Fatalf("extractFile(%q, source) node count = %d, want %d", "db/schema.SQL", len(got.nodes), len(want))
	}
	for i, node := range got.nodes {
		if node.ID != want[i].id || node.Kind != want[i].kind || node.Path != "db/schema.SQL" || node.BodyHash == "" || node.Span == "" || node.Signature == nil && i > 0 || node.BodyText == nil {
			t.Errorf("extractFile(%q, source) node[%d] = %#v, want ID %q and kind %q with path, hash, span", "db/schema.SQL", i, node, want[i].id, want[i].kind)
		}
	}
	resolved := resolveEdges(got.nodes, got.rawEdges, nil)
	for _, edge := range []EdgeV1{
		{Source: "db/schema.SQL#app.users", Target: "db/schema.SQL#app.teams", Relation: "references", Confidence: "extracted"},
		{Source: "db/schema.SQL#app.active_users", Target: "db/schema.SQL#app.users", Relation: "references", Confidence: "extracted"},
		{Source: "db/schema.SQL#app.active_users", Target: "db/schema.SQL#app.teams", Relation: "references", Confidence: "extracted"},
		{Source: "db/schema.SQL#app.count_users", Target: "db/schema.SQL#app.users", Relation: "references", Confidence: "extracted"},
	} {
		if !slices.Contains(resolved, edge) {
			t.Errorf("resolveEdges(extractFile(%q)) = %#v, want %#v", "db/schema.SQL", resolved, edge)
		}
	}
}

func TestSQLAnonymousAndInvalidContract(t *testing.T) {
	cases := []struct {
		name, source string
		wantErr      bool
	}{
		{"anonymous", "-- report\nSELECT * FROM app.users;\n", false},
		{"unterminated string", "CREATE TYPE app.status AS ENUM ('active);", true},
		{"missing name", "CREATE TABLE (id bigint);", true},
		{"unclosed parenthesis", "CREATE TABLE app.users (id bigint;", true},
		{"unclosed dollar quote", "CREATE FUNCTION app.f() RETURNS int AS $$ SELECT 1;", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := extractFile("query.sql", tc.source)
			if (err != nil) != tc.wantErr {
				t.Fatalf("extractFile(%q, %q) error = %v, want error %t", "query.sql", tc.source, err, tc.wantErr)
			}
			if !tc.wantErr && (len(got.nodes) != 1 || got.nodes[0].BodyText == nil || !strings.Contains(*got.nodes[0].BodyText, "SELECT")) {
				t.Errorf("extractFile(%q, %q) nodes = %#v, want searchable file node only", "query.sql", tc.source, got.nodes)
			}
		})
	}
}

func TestSQLReferenceResolutionContract(t *testing.T) {
	files := map[string]string{
		"db/app.sql":   "CREATE TABLE app.users (id bigint);\nCREATE VIEW app.active AS SELECT * FROM app.users;\n",
		"db/audit.sql": "CREATE TABLE audit.users (id bigint);\n",
		"db/query.sql": "SELECT * FROM users;\nSELECT * FROM app.users;\n",
	}
	nodes := make([]NodeV1, 0)
	raw := make([]rawEdge, 0)
	for _, file := range []string{"db/app.sql", "db/audit.sql", "db/query.sql"} {
		got, err := extractFile(file, files[file])
		if err != nil {
			t.Fatalf("extractFile(%q, source) error = %v", file, err)
		}
		nodes = append(nodes, got.nodes...)
		raw = append(raw, got.rawEdges...)
	}
	edges := resolveEdges(nodes, raw, nil)
	want := EdgeV1{Source: "db/query.sql", Target: "db/app.sql#app.users", Relation: "references", Confidence: "inferred"}
	if !slices.Contains(edges, want) {
		t.Errorf("resolveEdges(SQL files) = %#v, want %#v", edges, want)
	}
	for _, edge := range edges {
		if edge.Target == "db/audit.sql#audit.users" {
			t.Errorf("resolveEdges(SQL files) = %#v, want ambiguous unqualified users unresolved", edges)
		}
	}
}

func TestSQLBuildCacheContract(t *testing.T) {
	root := t.TempDir()
	file := filepath.Join(root, "schema.SQL")
	if err := os.WriteFile(file, []byte("CREATE TABLE app.users (id bigint);\n"), 0o644); err != nil {
		t.Fatalf("WriteFile(%q) error = %v", file, err)
	}
	fixed := time.Unix(1_700_000_000, 0)
	if err := os.Chtimes(file, fixed, fixed); err != nil {
		t.Fatalf("Chtimes(%q) error = %v", file, err)
	}
	opts := sourcefiles.Options{OutDir: filepath.Join(root, "graft")}
	first, err := BuildGraph(root, opts)
	if err != nil {
		t.Fatalf("BuildGraph(%q, %#v) error = %v", root, opts, err)
	}
	second, err := BuildGraph(root, opts)
	if err != nil {
		t.Fatalf("BuildGraph(%q, %#v) second error = %v", root, opts, err)
	}
	if first.Parsed != 1 || second.Reused != 1 || !reflect.DeepEqual(first.Graph, second.Graph) {
		t.Errorf("BuildGraph(%q, %#v) = (parsed %d, reused %d, equal %t), want (1, 1, true)", root, opts, first.Parsed, second.Reused, reflect.DeepEqual(first.Graph, second.Graph))
	}
	if err := os.WriteFile(file, []byte("CREATE TABLE app.teams (id bigint);\n"), 0o644); err != nil {
		t.Fatalf("WriteFile(%q) replacement error = %v", file, err)
	}
	if err := os.Chtimes(file, fixed, fixed); err != nil {
		t.Fatalf("Chtimes(%q) replacement error = %v", file, err)
	}
	changed, err := BuildGraph(root, opts)
	if err != nil {
		t.Fatalf("BuildGraph(%q, %#v) after same-stat edit error = %v", root, opts, err)
	}
	if changed.Parsed != 1 || changed.Reused != 0 || !slices.ContainsFunc(changed.Graph.Nodes, func(node NodeV1) bool { return node.ID == "schema.SQL#app.teams" }) {
		t.Errorf("BuildGraph(%q, %#v) after same-stat edit = %#v, want freshly parsed app.teams", root, opts, changed)
	}
	if err := os.Remove(file); err != nil {
		t.Fatalf("Remove(%q) error = %v", file, err)
	}
	removed, err := BuildGraph(root, opts)
	if err != nil {
		t.Fatalf("BuildGraph(%q, %#v) after removal error = %v", root, opts, err)
	}
	if len(removed.Graph.Nodes) != 0 || len(removed.Fingerprints) != 0 {
		t.Errorf("BuildGraph(%q, %#v) after removal = %#v, want no SQL nodes or fingerprints", root, opts, removed)
	}
}

func TestSQLNamesDoNotResolveAcrossLanguageFamilies(t *testing.T) {
	nodes := []NodeV1{
		{ID: "main.ts#run", Name: "run", Kind: "function", Path: "main.ts", Origin: "ast"},
		{ID: "schema.sql#foo", Name: "foo", Kind: "function", Path: "schema.sql", Origin: "sql"},
	}
	raw := []rawEdge{{source: "main.ts#run", relation: "calls", name: "foo", file: "main.ts"}}
	if got := resolveEdges(nodes, raw, nil); len(got) != 0 {
		t.Errorf("resolveEdges(TypeScript call, SQL function) = %#v, want no cross-language call", got)
	}
}

func TestSQLGraphOrderingContract(t *testing.T) {
	root := t.TempDir()
	for _, item := range []struct{ file, source string }{
		{"z.sql", "CREATE VIEW app.active AS SELECT * FROM app.users;\n"},
		{"a.sql", "CREATE TABLE app.users (id bigint);\n"},
	} {
		path := filepath.Join(root, item.file)
		if err := os.WriteFile(path, []byte(item.source), 0o644); err != nil {
			t.Fatalf("WriteFile(%q) error = %v", path, err)
		}
	}
	opts := sourcefiles.Options{OutDir: filepath.Join(root, "graft")}
	first, err := BuildGraph(root, opts)
	if err != nil {
		t.Fatalf("BuildGraph(%q, %#v) error = %v", root, opts, err)
	}
	second, err := BuildGraph(root, opts)
	if err != nil {
		t.Fatalf("BuildGraph(%q, %#v) cached error = %v", root, opts, err)
	}
	if err := os.RemoveAll(filepath.Join(opts.OutDir, ".cache")); err != nil {
		t.Fatalf("RemoveAll(cache) error = %v", err)
	}
	third, err := BuildGraph(root, opts)
	if err != nil {
		t.Fatalf("BuildGraph(%q, %#v) cold replay error = %v", root, opts, err)
	}
	if !reflect.DeepEqual(first.Graph, second.Graph) || !reflect.DeepEqual(first.Graph, third.Graph) {
		t.Errorf("BuildGraph(%q, %#v) graphs differ across cold, cached, and cold replay", root, opts)
	}
	if len(first.Graph.Nodes) != 4 || first.Graph.Nodes[0].ID != "a.sql" || first.Graph.Nodes[2].ID != "z.sql" {
		t.Errorf("BuildGraph(%q, %#v) nodes = %#v, want deterministic path order", root, opts, first.Graph.Nodes)
	}
}
