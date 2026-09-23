package graph

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func TestLoadWorkspaceGraphsContract(t *testing.T) {
	base := t.TempDir()
	root := filepath.Join(base, "parent")
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatalf("MkdirAll(%q) error = %v, want nil", root, err)
	}
	writeWorkspaceRepo(t, root, "alpha", true)
	writeWorkspaceRepo(t, root, "zeta", true)
	writeWorkspaceRepo(t, base, "outside", true)
	corruptGraph := filepath.Join(root, "corrupt", "graft")
	if err := os.MkdirAll(filepath.Join(corruptGraph, GraphDir), 0o755); err != nil {
		t.Fatalf("MkdirAll(%q) error = %v, want nil", filepath.Join(corruptGraph, GraphDir), err)
	}
	if err := os.WriteFile(WiringPath(corruptGraph), []byte("{"), 0o644); err != nil {
		t.Fatalf("WriteFile(%q) error = %v, want nil", WiringPath(corruptGraph), err)
	}
	contextDir := filepath.Join(root, "workspace-context")
	if err := os.MkdirAll(contextDir, 0o755); err != nil {
		t.Fatalf("MkdirAll(%q) error = %v, want nil", contextDir, err)
	}
	index := `{"version":1,"children":["zeta","missing","../outside","corrupt","alpha"]}`
	if err := os.WriteFile(filepath.Join(contextDir, "workspace.json"), []byte(index), 0o644); err != nil {
		t.Fatalf("WriteFile(%q) error = %v, want nil", filepath.Join(contextDir, "workspace.json"), err)
	}

	got := LoadWorkspaceGraphs(root, contextDir)
	if len(got.Loaded) != 2 || got.Loaded[0].Name != "alpha" || got.Loaded[1].Name != "zeta" {
		t.Errorf("LoadWorkspaceGraphs(%q, %q).Loaded = %#v, want alpha and zeta in sorted order", root, contextDir, got.Loaded)
	}
	if !slices.Equal(got.Missing, []string{"../outside", "corrupt", "missing"}) {
		t.Errorf("LoadWorkspaceGraphs(%q, %q).Missing = %v, want [../outside corrupt missing]", root, contextDir, got.Missing)
	}
}

func TestLoadWorkspaceGraphsDiscoveryFallbackContract(t *testing.T) {
	for _, tt := range []struct {
		name  string
		index string
	}{
		{name: "missing index"},
		{name: "malformed index", index: "{"},
		{name: "unsupported version", index: `{"version":2,"children":["absent"]}`},
		{name: "null children", index: `{"version":1,"children":null}`},
	} {
		t.Run(tt.name, func(t *testing.T) {
			root := t.TempDir()
			for _, name := range []string{"alpha", "beta", "node_modules", ".hidden", "vendor"} {
				writeWorkspaceRepo(t, root, name, false)
			}
			contextDir := filepath.Join(root, "graft")
			if err := os.MkdirAll(filepath.Join(root, ".graft"), 0o755); err != nil {
				t.Fatalf("MkdirAll(%q) error = %v, want nil", filepath.Join(root, ".graft"), err)
			}
			if err := os.WriteFile(filepath.Join(root, ".graft", "config.json"), []byte(`{"includeDirs":["vendor"]}`), 0o644); err != nil {
				t.Fatalf("WriteFile(%q) error = %v, want nil", filepath.Join(root, ".graft", "config.json"), err)
			}
			if tt.index != "" {
				if err := os.MkdirAll(contextDir, 0o755); err != nil {
					t.Fatalf("MkdirAll(%q) error = %v, want nil", contextDir, err)
				}
				if err := os.WriteFile(filepath.Join(contextDir, "workspace.json"), []byte(tt.index), 0o644); err != nil {
					t.Fatalf("WriteFile(%q) error = %v, want nil", filepath.Join(contextDir, "workspace.json"), err)
				}
			}

			got := LoadWorkspaceGraphs(root, "")
			if len(got.Loaded) != 0 || !slices.Equal(got.Missing, []string{"alpha", "beta", "vendor"}) {
				t.Errorf("LoadWorkspaceGraphs(%q, %q) = %#v, want missing [alpha beta vendor]", root, "", got)
			}
		})
	}
}

func TestLoadWorkspaceGraphsEmptyManifestContract(t *testing.T) {
	root := t.TempDir()
	writeWorkspaceRepo(t, root, "discoverable", false)
	outDir := filepath.Join(root, "graft")
	if err := os.MkdirAll(outDir, 0o755); err != nil {
		t.Fatalf("MkdirAll(%q) error = %v, want nil", outDir, err)
	}
	if err := os.WriteFile(filepath.Join(outDir, "workspace.json"), []byte(`{"version":1,"children":[]}`), 0o644); err != nil {
		t.Fatalf("WriteFile(%q) error = %v, want nil", filepath.Join(outDir, "workspace.json"), err)
	}

	got := LoadWorkspaceGraphs(root, "")
	if got.Loaded == nil || got.Missing == nil || len(got.Loaded) != 0 || len(got.Missing) != 0 {
		t.Errorf("LoadWorkspaceGraphs(%q, %q) = %#v, want initialized empty slices from an empty index", root, "", got)
	}
}

func TestWorkspaceBuildChildrenContract(t *testing.T) {
	root := t.TempDir()
	writeWorkspaceRepo(t, root, "zeta", false)
	writeWorkspaceRepo(t, root, "alpha", false)
	children, ok := WorkspaceBuildChildren(root, "")
	if !ok || !slices.Equal(children, []string{"alpha", "zeta"}) {
		t.Errorf("WorkspaceBuildChildren(%q, empty context) = (%v, %t), want sorted workspace children", root, children, ok)
	}
	if err := os.Mkdir(filepath.Join(root, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	if children, ok := WorkspaceBuildChildren(root, ""); ok || children != nil {
		t.Errorf("WorkspaceBuildChildren(%q, empty context) with own .git = (%v, %t), want not a workspace", root, children, ok)
	}
	contextDir := filepath.Join(root, "graft")
	if err := WriteWorkspace(contextDir, []string{"zeta"}); err != nil {
		t.Fatal(err)
	}
	if children, ok := WorkspaceBuildChildren(root, contextDir); !ok || !slices.Equal(children, []string{"alpha", "zeta"}) {
		t.Errorf("WorkspaceBuildChildren(%q, %q) with an index = (%v, %t), want newly discovered children", root, contextDir, children, ok)
	}
}

func TestWriteAndClearWorkspaceParentContract(t *testing.T) {
	contextDir := t.TempDir()
	if err := WriteWorkspace(contextDir, []string{"web", "api"}); err != nil {
		t.Fatalf("WriteWorkspace(%q, children) error = %v", contextDir, err)
	}
	data, err := os.ReadFile(filepath.Join(contextDir, "workspace.json"))
	if err != nil {
		t.Fatal(err)
	}
	want := "{\n  \"version\": 1,\n  \"children\": [\n    \"api\",\n    \"web\"\n  ]\n}\n"
	if string(data) != want {
		t.Errorf("WriteWorkspace(%q, children) = %q, want %q", contextDir, data, want)
	}
	for _, rel := range []string{".graph/wiring.json", ".cache/session.json", "old-card.md"} {
		path := filepath.Join(contextDir, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte("old"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := ClearWorkspaceParent(contextDir); err != nil {
		t.Fatalf("ClearWorkspaceParent(%q) error = %v", contextDir, err)
	}
	entries, err := os.ReadDir(contextDir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Name() != "workspace.json" {
		t.Errorf("ClearWorkspaceParent(%q) entries = %v, want only workspace.json", contextDir, entries)
	}
}

func TestLoadWorkspaceGraphsUsesLastDuplicateKeyContract(t *testing.T) {
	root := t.TempDir()
	if _, err := Write(GraphV1{}, filepath.Join(root, "alpha", "graft")); err != nil {
		t.Fatalf("Write(GraphV1{}, %q) error = %v, want nil", filepath.Join(root, "alpha", "graft"), err)
	}
	outDir := filepath.Join(root, "graft")
	if err := os.MkdirAll(outDir, 0o755); err != nil {
		t.Fatalf("MkdirAll(%q) error = %v, want nil", outDir, err)
	}
	index := `{"version":2,"children":["ignored"],"version":1,"children":["alpha"]}`
	if err := os.WriteFile(filepath.Join(outDir, "workspace.json"), []byte(index), 0o644); err != nil {
		t.Fatalf("WriteFile(%q) error = %v, want nil", filepath.Join(outDir, "workspace.json"), err)
	}

	got := LoadWorkspaceGraphs(root, "")
	if len(got.Loaded) != 1 || got.Loaded[0].Name != "alpha" || len(got.Missing) != 0 {
		t.Errorf("LoadWorkspaceGraphs(%q, %q) with duplicate JSON keys = %#v, want only alpha loaded", root, "", got)
	}
}

func TestLoadWorkspaceGraphsBoundsMetadataContract(t *testing.T) {
	root := t.TempDir()
	writeWorkspaceRepo(t, root, "alpha", false)
	writeWorkspaceRepo(t, root, "vendor", false)
	if err := os.MkdirAll(filepath.Join(root, ".graft"), 0o755); err != nil {
		t.Fatalf("MkdirAll(%q) error = %v, want nil", filepath.Join(root, ".graft"), err)
	}
	if err := os.WriteFile(filepath.Join(root, ".graft", "config.json"), []byte(`{"includeDirs":["vendor"]}`+strings.Repeat(" ", 1<<20)), 0o644); err != nil {
		t.Fatalf("WriteFile(%q) oversized settings error = %v, want nil", filepath.Join(root, ".graft", "config.json"), err)
	}
	outDir := filepath.Join(root, "graft")
	if err := os.MkdirAll(outDir, 0o755); err != nil {
		t.Fatalf("MkdirAll(%q) error = %v, want nil", outDir, err)
	}
	if err := os.WriteFile(filepath.Join(outDir, "workspace.json"), []byte(`{"version":1,"children":[]}`+strings.Repeat(" ", 1<<20)), 0o644); err != nil {
		t.Fatalf("WriteFile(%q) oversized index error = %v, want nil", filepath.Join(outDir, "workspace.json"), err)
	}

	got := LoadWorkspaceGraphs(root, "")
	if !slices.Equal(got.Missing, []string{"alpha"}) {
		t.Errorf("LoadWorkspaceGraphs(%q, %q) with oversized metadata = %#v, want discovery without oversized includeDirs", root, "", got)
	}
}

func TestFederateMapWorkspaceContract(t *testing.T) {
	root := t.TempDir()
	writeWorkspaceRepo(t, root, "alpha", true)
	writeWorkspaceRepo(t, root, "zeta", true)
	outDir := filepath.Join(root, "graft")
	if err := os.MkdirAll(outDir, 0o755); err != nil {
		t.Fatalf("MkdirAll(%q) error = %v, want nil", outDir, err)
	}
	index := `{"version":1,"children":["zeta","missing","alpha"]}`
	if err := os.WriteFile(filepath.Join(outDir, "workspace.json"), []byte(index), 0o644); err != nil {
		t.Fatalf("WriteFile(%q) error = %v, want nil", filepath.Join(outDir, "workspace.json"), err)
	}

	got := FederateMap(root, "", RepoMapOptions{MaxDirs: 2})
	alpha := strings.Index(got, "## alpha/")
	zeta := strings.Index(got, "## zeta/")
	if alpha < 0 || zeta <= alpha {
		t.Errorf("FederateMap(%q, %q, %#v) = %q, want sorted alpha and zeta sections", root, "", RepoMapOptions{MaxDirs: 2}, got)
	}
	if !strings.HasPrefix(got, "workspace map — 2 repo(s)\n") || !strings.Contains(got, "2 of 3 workspace repos have graphs; run graft build to cover missing") {
		t.Errorf("FederateMap(%q, %q, %#v) = %q, want the loaded total and missing-graph coverage note", root, "", RepoMapOptions{MaxDirs: 2}, got)
	}
}

func writeWorkspaceRepo(t *testing.T, root, name string, withGraph bool) {
	t.Helper()
	repo := filepath.Join(root, name)
	if err := os.MkdirAll(filepath.Join(repo, ".git"), 0o755); err != nil {
		t.Fatalf("MkdirAll(%q) error = %v, want nil", filepath.Join(repo, ".git"), err)
	}
	if !withGraph {
		return
	}
	graph := GraphV1{
		Meta: GraphMeta{Version: 1, NodeCount: 2, EdgeCount: 1, Languages: []string{"typescript"}},
		Nodes: []NodeV1{
			{ID: "src/app.ts", Name: "app.ts", Kind: "file", Path: "src/app.ts", Span: "L1-L1", Exported: true, Origin: "ast", BodyHash: "file-hash", SummaryState: "pending"},
			{ID: "src/app.ts#worker", Name: "worker", Kind: "function", Path: "src/app.ts", Span: "L1-L1", Exported: true, Origin: "ast", BodyHash: "worker-hash", SummaryState: "pending"},
		},
		Edges: []EdgeV1{{Source: "src/app.ts", Target: "src/app.ts#worker", Relation: "contains", Confidence: "extracted"}},
	}
	if _, err := Write(graph, filepath.Join(repo, "graft")); err != nil {
		t.Fatalf("Write(workspace graph %q) error = %v, want nil", name, err)
	}
}
