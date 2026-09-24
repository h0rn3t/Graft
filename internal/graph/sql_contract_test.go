package graph

import (
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"
	"unicode/utf16"

	"github.com/h0rn3t/Graft/internal/sourcefiles"
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
		wantEdges    bool
	}{
		{"anonymous", "-- report\nSELECT * FROM app.users;\n", true},
		{"unterminated string", "CREATE TYPE app.status AS ENUM ('active);", false},
		{"missing name", "CREATE TABLE (id bigint);", false},
		{"unclosed parenthesis", "CREATE TABLE app.users (id bigint;", false},
		{"unmatched parenthesis", "SELECT 1) FROM app.users;", false},
		{"unclosed dollar quote", "CREATE FUNCTION app.f() RETURNS int AS $$ SELECT 1;", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := extractFile("query.sql", tc.source)
			if err != nil {
				t.Fatalf("extractFile(%q, %q) error = %v, want nil", "query.sql", tc.source, err)
			}
			if len(got.nodes) != 1 || got.nodes[0].ID != "query.sql" || got.nodes[0].BodyText == nil || *got.nodes[0].BodyText == "" {
				t.Errorf("extractFile(%q, %q) nodes = %#v, want searchable file node only", "query.sql", tc.source, got.nodes)
			}
			if gotEdges := len(got.rawEdges) > 0; gotEdges != tc.wantEdges {
				t.Errorf("extractFile(%q, %q) raw edges = %#v, want edges %t", "query.sql", tc.source, got.rawEdges, tc.wantEdges)
			}
		})
	}
}

func TestSQLDialectAndRecoveryContract(t *testing.T) {
	cases := []struct {
		name, source   string
		wantIDs        []string
		wantRefs       []string
		wantLimitation string
	}{
		{
			name:    "build placeholder schema",
			source:  "CREATE OR REPLACE FUNCTION @extschema@.gapfill(ts int) RETURNS int AS '@MODULE_PATHNAME@', 'fn' LANGUAGE C;\n",
			wantIDs: []string{"q.sql#@extschema@.gapfill"},
		},
		{
			name:    "psql variable name",
			source:  "CREATE TABLE :TEST_TABLE (id int);\nCREATE MATERIALIZED VIEW :'CAGG' AS SELECT 1;\nCREATE TABLE app.teams (id int);\n",
			wantIDs: []string{"q.sql#app.teams"},
		},
		{
			name:    "psql meta-commands",
			source:  "\\set ON_ERROR_STOP 1\n\\if :flag\nCREATE TABLE app.teams (id int);\n\\endif\n",
			wantIDs: []string{"q.sql#app.teams"},
		},
		{
			name:    "cast is not a psql variable",
			source:  "CREATE TABLE app.teams (id int DEFAULT 1::int);\n",
			wantIDs: []string{"q.sql#app.teams"},
		},
		{
			name: "template branches",
			source: "{# grouped ( #}\nSELECT * FROM (\n{% if by_team %}\n  SELECT team_id FROM app.teams GROUP BY (team_id\n" +
				"{%- elif by_user -%}\n  SELECT id FROM app.users GROUP BY (id\n{% else %}\n  SELECT 1 FROM (VALUES (1\n{% endif %}\n)) sub;\n" +
				"CREATE TABLE {{ table }} (id int);\nCREATE TABLE app.audit (id int);\n",
			wantIDs:  []string{"q.sql#app.audit"},
			wantRefs: []string{"app.teams", "app.users"},
		},
		{
			name:           "malformed statements",
			source:         "CREATE TABLE app.broken (id int;\nCREATE TABLE app.teams (id int);\nSELECT 1) FROM app.users;\nCREATE TABLE (id int);\nCREATE VIEW app.v AS SELECT 1;\n",
			wantIDs:        []string{"q.sql#app.teams", "q.sql#app.v"},
			wantLimitation: "q.sql: 3 of 5 SQL statements not indexed (unclosed SQL parenthesis at byte 0)",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := extractFile("q.sql", tc.source)
			if err != nil {
				t.Fatalf("extractFile(%q, %q) error = %v, want nil", "q.sql", tc.source, err)
			}
			var ids []string
			for _, node := range got.nodes[1:] {
				ids = append(ids, node.ID)
			}
			if !slices.Equal(ids, tc.wantIDs) {
				t.Errorf("extractFile(%q, %q) symbol ids = %q, want %q", "q.sql", tc.source, ids, tc.wantIDs)
			}
			if got.limitation != tc.wantLimitation {
				t.Errorf("extractFile(%q, %q) limitation = %q, want %q", "q.sql", tc.source, got.limitation, tc.wantLimitation)
			}
			var refs []string
			for _, edge := range got.rawEdges {
				refs = append(refs, edge.name)
			}
			if !slices.Equal(refs, tc.wantRefs) {
				t.Errorf("extractFile(%q, %q) references = %q, want %q", "q.sql", tc.source, refs, tc.wantRefs)
			}
		})
	}
}

func TestSQLQuoteEscapesContract(t *testing.T) {
	cases := []struct {
		name, source string
	}{
		{"backslash escaped quote", "INSERT INTO app.people VALUES ('O\\'Brien');\nCREATE TABLE app.teams (id bigint);\n"},
		{"doubled quote", "INSERT INTO app.people VALUES ('O''Brien');\nCREATE TABLE app.teams (id bigint);\n"},
		{"literal trailing backslash", "SELECT 'C:\\';\nCREATE TABLE app.teams (id bigint);\n"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := extractFile("seed.sql", tc.source)
			if err != nil {
				t.Fatalf("extractFile(%q, %q) error = %v, want nil", "seed.sql", tc.source, err)
			}
			if !slices.ContainsFunc(got.nodes, func(node NodeV1) bool { return node.ID == "seed.sql#app.teams" && node.Span == "L2-L2" }) {
				t.Errorf("extractFile(%q, %q) nodes = %#v, want seed.sql#app.teams at L2-L2", "seed.sql", tc.source, got.nodes)
			}
		})
	}
}

func TestSQLCharsCountUTF16Units(t *testing.T) {
	const source = "-- café 😀\nSELECT 1;\n"
	got, err := extractFile("query.sql", source)
	if err != nil {
		t.Fatalf("extractFile(%q, %q) error = %v, want nil", "query.sql", source, err)
	}
	if want := len(utf16.Encode([]rune(source))); got.nodes[0].Chars == nil || *got.nodes[0].Chars != want {
		t.Errorf("extractFile(%q, %q) file chars = %v, want %d", "query.sql", source, got.nodes[0].Chars, want)
	}
}

func TestBuildGraphDegradesUnparsableSQLFile(t *testing.T) {
	root := t.TempDir()
	for name, source := range map[string]string{
		"broken.sql": "INSERT INTO app.notes VALUES ('it''s \\');\nSELECT 1);\n",
		"schema.sql": "CREATE TABLE app.users (id bigint);\n",
	} {
		if err := os.WriteFile(filepath.Join(root, name), []byte(source), 0o644); err != nil {
			t.Fatalf("WriteFile(%q) error = %v", name, err)
		}
	}
	opts := sourcefiles.Options{OutDir: filepath.Join(root, "graft")}
	built, err := BuildGraph(root, opts)
	if err != nil {
		t.Fatalf("BuildGraph(%q, %#v) error = %v, want nil", root, opts, err)
	}
	if len(built.Errors) != 0 {
		t.Errorf("BuildGraph(%q, %#v) errors = %q, want none", root, opts, built.Errors)
	}
	if len(built.Limitations) != 1 || !strings.HasPrefix(built.Limitations[0], "broken.sql: ") {
		t.Errorf("BuildGraph(%q, %#v) limitations = %q, want one for broken.sql", root, opts, built.Limitations)
	}
	replayed, err := BuildGraph(root, opts)
	if err != nil || replayed.Reused != 2 || !slices.Equal(replayed.Limitations, built.Limitations) {
		t.Errorf("BuildGraph(%q, %#v) replay = (reused %d, limitations %q, %v), want 2 reused and %q", root, opts, replayed.Reused, replayed.Limitations, err, built.Limitations)
	}
	for _, id := range []string{"broken.sql", "schema.sql", "schema.sql#app.users"} {
		if !slices.ContainsFunc(built.Graph.Nodes, func(node NodeV1) bool { return node.ID == id }) {
			t.Errorf("BuildGraph(%q, %#v) nodes = %#v, want %q", root, opts, built.Graph.Nodes, id)
		}
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
