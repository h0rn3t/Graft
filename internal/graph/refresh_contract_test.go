package graph

import (
	"errors"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	"github.com/h0rn3t/Graft/internal/sourcefiles"
)

func TestEnsureFreshGraphRefreshesDriftContract(t *testing.T) {
	t.Setenv("GRAFT_NO_REFRESH", "false")
	root := t.TempDir()
	outDir := filepath.Join(root, "graft")
	options := RefreshOptions{Source: sourcefiles.Options{
		OutDir: outDir, Extensions: []string{".ts"}, OnlyDirs: []string{"src"},
	}}
	writeRefreshSource(t, root, "src/app.ts", "export function before() {}")
	buildAndWriteRefreshGraph(t, root, options.Source)
	writeRefreshSource(t, root, "outside/untouched.ts", "export function outside() {}")
	writeRefreshSource(t, root, "src/app.ts", "export function after() {}")

	got := EnsureFreshGraph(root, options)
	if !got.Refreshed || got.Drift == nil || !slices.Equal(got.Drift.Changed, []string{"src/app.ts"}) {
		t.Fatalf("EnsureFreshGraph(%q, %#v) = %#v, want refreshed with src/app.ts changed", root, options, got)
	}
	wiring, err := Read(WiringPath(outDir))
	if err != nil {
		t.Fatalf("Read(%q) after EnsureFreshGraph error = %v, want nil", WiringPath(outDir), err)
	}
	if !slices.ContainsFunc(wiring.Nodes, func(node NodeV1) bool { return node.ID == "src/app.ts#after" }) {
		t.Errorf("EnsureFreshGraph(%q, %#v) wiring nodes = %v, want src/app.ts#after", root, options, wiring.Nodes)
	}
	fingerprint, err := ReadFingerprint(outDir, ExtractorID)
	if err != nil {
		t.Fatalf("ReadFingerprint(%q, %q) after refresh error = %v, want nil", outDir, ExtractorID, err)
	}
	if fingerprint == nil || fingerprint.Files["src/app.ts"].Hash != sourcefiles.Hash("export function after() {}") ||
		!slices.Equal(fingerprint.OnlyDirs, options.Source.OnlyDirs) || len(fingerprint.Files) != 1 {
		t.Errorf("ReadFingerprint(%q, %q) = %#v, want the refreshed source hash", outDir, ExtractorID, fingerprint)
	}
	if note := RefreshNote(got); !strings.HasPrefix(note, "[graft] refreshed the graph (1 file changed) before answering") {
		t.Errorf("RefreshNote(%#v) = %q, want the one-file refresh note", got, note)
	}
}

func TestSelectedWalkAgreesAcrossBuildProbeAndRefreshContract(t *testing.T) {
	t.Setenv("GRAFT_NO_REFRESH", "false")
	t.Setenv("GRAFT_REFRESH", "hash")
	root := t.TempDir()
	outDir := filepath.Join(root, "graft")
	writeRefreshSource(t, root, ".graft/config.json", `{"includeDirs":["vendor"]}`)
	writeRefreshSource(t, root, "src/app.ts", "export function app() {}\n")
	writeRefreshSource(t, root, "vendor/code.ts", "export function before() {}\n")
	writeRefreshSource(t, root, "src/other.rb", "def other; end\n")
	buildAndWriteRefreshGraph(t, root, sourcefiles.Options{OutDir: outDir})
	fingerprint, err := ReadFingerprint(outDir, ExtractorID)
	if err != nil || fingerprint == nil || len(fingerprint.Files) != 2 {
		t.Fatalf("ReadFingerprint(%q) = %#v, %v, want src/app.ts and vendor/code.ts", outDir, fingerprint, err)
	}
	drift, err := ProbeDrift(root, outDir, ExtractorID, sourcefiles.Options{})
	if err != nil || drift == nil || len(drift.Added)+len(drift.Changed)+len(drift.Removed) != 0 {
		t.Errorf("ProbeDrift(%q) = %#v, %v, want clean selected file set", root, drift, err)
	}
	if result := EnsureFreshGraph(root, RefreshOptions{}); result.Refreshed || result.Note != "" {
		t.Errorf("EnsureFreshGraph(%q) = %#v, want clean selected file set", root, result)
	}
	writeRefreshSource(t, root, "vendor/code.ts", "export function after() {}\n")
	drift, err = ProbeDrift(root, outDir, ExtractorID, sourcefiles.Options{})
	if err != nil || drift == nil || !slices.Equal(drift.Changed, []string{"vendor/code.ts"}) {
		t.Errorf("ProbeDrift(%q) = %#v, %v, want changed vendor/code.ts only", root, drift, err)
	}
	if result := EnsureFreshGraph(root, RefreshOptions{}); !result.Refreshed {
		t.Errorf("EnsureFreshGraph(%q) = %#v, want included vendor refresh", root, result)
	}
}

func TestEnsureFreshChildrenContract(t *testing.T) {
	type repoFixture struct {
		path, before, after, wantName string
	}
	for _, tt := range []struct {
		name        string
		children    []string
		noRefresh   string
		wantNote    string
		wantRefresh bool
		symlink     string
		symlinkTo   string
		repos       []repoFixture
	}{
		{name: "nil children", noRefresh: "false"},
		{
			name: "clean child", noRefresh: "false", children: []string{"api"},
			repos: []repoFixture{{path: "api", before: "export function ready() {}\n", wantName: "ready"}},
		},
		{
			name: "one changed child", noRefresh: "false", children: []string{"api"}, wantRefresh: true,
			wantNote: "[graft] refreshed the graph (? files changed) before answering — refreshed api (1 file changed) before answering",
			repos:    []repoFixture{{path: "api", before: "export function before() {}\n", after: "export function after() {}\n", wantName: "after"}},
		},
		{
			name: "multiple changed children retain order", noRefresh: "false", children: []string{"web", "api"}, wantRefresh: true,
			wantNote: "[graft] refreshed the graph (? files changed) before answering — refreshed web, api (2 files changed) before answering",
			repos: []repoFixture{
				{path: "web", before: "export function before() {}\n", after: "export function after() {}\n", wantName: "after"},
				{path: "api", before: "export function before() {}\n", after: "export function after() {}\n", wantName: "after"},
			},
		},
		{
			name: "environment disabled", noRefresh: "1", children: []string{"api"},
			repos: []repoFixture{{path: "api", before: "export function before() {}\n", after: "export function after() {}\n", wantName: "before"}},
		},
		{
			name: "parent traversal rejected", noRefresh: "false", children: []string{"../outside"},
			repos: []repoFixture{{path: "../outside", before: "export function before() {}\n", after: "export function after() {}\n", wantName: "before"}},
		},
		{
			name: "symlink child rejected", noRefresh: "false", children: []string{"outside"}, symlink: "outside", symlinkTo: "../outside",
			repos: []repoFixture{{path: "../outside", before: "export function before() {}\n", after: "export function after() {}\n", wantName: "before"}},
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv("GRAFT_NO_REFRESH", tt.noRefresh)
			t.Setenv("GRAFT_REFRESH", "hash")
			base := t.TempDir()
			root := filepath.Join(base, "workspace")
			if err := os.MkdirAll(root, 0o755); err != nil {
				t.Fatalf("MkdirAll(%q) error = %v, want nil", root, err)
			}
			for _, repo := range tt.repos {
				repoRoot := filepath.Join(root, repo.path)
				outDir := filepath.Join(repoRoot, "graft")
				options := sourcefiles.Options{OutDir: outDir, Extensions: []string{".ts"}}
				writeRefreshSource(t, repoRoot, "src/app.ts", repo.before)
				buildAndWriteRefreshGraph(t, repoRoot, options)
				if repo.after != "" {
					writeRefreshSource(t, repoRoot, "src/app.ts", repo.after)
				}
			}
			if tt.symlink != "" {
				if err := os.Symlink(tt.symlinkTo, filepath.Join(root, tt.symlink)); err != nil {
					t.Skipf("Symlink(%q, %q) error = %v", tt.symlinkTo, filepath.Join(root, tt.symlink), err)
				}
			}

			got := EnsureFreshChildren(root, tt.children)
			if got.Refreshed != tt.wantRefresh || RefreshNote(got) != tt.wantNote {
				t.Errorf("EnsureFreshChildren(%q, %q) = %#v, RefreshNote = %q; want Refreshed = %t, RefreshNote = %q", root, tt.children, got, RefreshNote(got), tt.wantRefresh, tt.wantNote)
			}
			for _, repo := range tt.repos {
				outDir := filepath.Join(root, repo.path, "graft")
				wiring, err := Read(WiringPath(outDir))
				if err != nil {
					t.Fatalf("Read(%q) after EnsureFreshChildren error = %v, want nil", WiringPath(outDir), err)
				}
				if !slices.ContainsFunc(wiring.Nodes, func(node NodeV1) bool { return node.Name == repo.wantName }) {
					t.Errorf("EnsureFreshChildren(%q, %q) graph nodes = %v, want %q", root, tt.children, wiring.Nodes, repo.wantName)
				}
			}
		})
	}
}

func TestEnsureFreshChildrenExcludesOtherLanguagesContract(t *testing.T) {
	t.Setenv("GRAFT_NO_REFRESH", "false")
	t.Setenv("GRAFT_REFRESH", "hash")
	root := t.TempDir()
	childRoot := filepath.Join(root, "api")
	outDir := filepath.Join(childRoot, "graft")
	options := sourcefiles.Options{OutDir: outDir}
	writeRefreshSource(t, childRoot, "src/app.ts", "export function ready() {}\n")
	buildAndWriteRefreshGraph(t, childRoot, options)
	writeRefreshSource(t, childRoot, "src/tool.zig", "fn unsupported() void {}\n")

	got := EnsureFreshChildren(root, []string{"api"})
	if got.Refreshed || RefreshNote(got) != "" {
		t.Errorf("EnsureFreshChildren(%q, [api]) = %#v, want no refresh for excluded source", root, got)
	}
}

func TestEnsureFreshGraphNoOpContract(t *testing.T) {
	root := t.TempDir()
	outDir := filepath.Join(root, "graft")
	options := RefreshOptions{Source: sourcefiles.Options{OutDir: outDir, Extensions: []string{".ts"}}}
	writeRefreshSource(t, root, "src/app.ts", "export function ready() {}")
	buildAndWriteRefreshGraph(t, root, options.Source)

	got := EnsureFreshGraph(root, options)
	if got.Refreshed || got.Drift != nil || got.Note != "" || RefreshNote(got) != "" {
		t.Errorf("EnsureFreshGraph(%q, %#v) on a clean graph = %#v, want a silent no-op", root, options, got)
	}
}

func TestEnsureFreshGraphDisabledContract(t *testing.T) {
	for _, tt := range []struct {
		name     string
		disabled bool
		env      string
	}{
		{name: "option", disabled: true},
		{name: "environment", env: "1"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv("GRAFT_NO_REFRESH", tt.env)
			root := t.TempDir()
			outDir := filepath.Join(root, "graft")
			options := RefreshOptions{Disabled: tt.disabled, Source: sourcefiles.Options{OutDir: outDir}}
			got := EnsureFreshGraph(root, options)
			if got.Refreshed || got.Note != "" {
				t.Errorf("EnsureFreshGraph(%q, %#v) = %#v, want disabled no-op", root, options, got)
			}
			if _, err := os.Stat(outDir); !os.IsNotExist(err) {
				t.Errorf("EnsureFreshGraph(%q, %#v) created output directory; Stat error = %v, want not-exist", root, options, err)
			}
		})
	}
}

func TestEnsureFreshGraphMissingGraphContract(t *testing.T) {
	root := t.TempDir()
	outDir := filepath.Join(root, "graft")
	options := RefreshOptions{Source: sourcefiles.Options{OutDir: outDir, Extensions: []string{".ts"}}}
	writeRefreshSource(t, root, "src/app.ts", "export function unindexed() {}")

	got := EnsureFreshGraph(root, options)
	if got.Refreshed || got.Note != "" {
		t.Errorf("EnsureFreshGraph(%q, %#v) without a graph = %#v, want a silent no-op", root, options, got)
	}
	if _, err := os.Stat(WiringPath(outDir)); !os.IsNotExist(err) {
		t.Errorf("EnsureFreshGraph(%q, %#v) created wiring; Stat error = %v, want not-exist", root, options, err)
	}
}

func TestEnsureFreshGraphSeedsGitWorktreeContract(t *testing.T) {
	t.Setenv("GRAFT_NO_REFRESH", "false")
	t.Setenv("GRAFT_NO_SEED", "false")
	main, root := newRefreshGitWorktree(t, "export function before() {}")
	mainCache := filepath.Join(main, "graft", ".cache")
	for name, text := range map[string]string{
		"ask-index.json": "ask index",
		"unrelated.json": "unrelated cache",
		".sync.lock":     "parent lock",
	} {
		if err := os.WriteFile(filepath.Join(mainCache, name), []byte(text), 0o644); err != nil {
			t.Fatalf("WriteFile(%q) error = %v, want nil", filepath.Join(mainCache, name), err)
		}
	}

	options := RefreshOptions{Source: sourcefiles.Options{Extensions: []string{".ts"}}}
	got := EnsureFreshGraph(root, options)
	main, err := filepath.EvalSymlinks(main)
	if err != nil {
		t.Fatalf("EvalSymlinks(%q) error = %v, want nil", main, err)
	}
	wantNote := "copied the graph from the main checkout (" + main + ")"
	if got.Refreshed || got.Note != wantNote {
		t.Fatalf("EnsureFreshGraph(%q, %#v) = %#v, want a seeded clean graph with note %q", root, options, got, wantNote)
	}
	graph, err := Read(WiringPath(filepath.Join(root, "graft")))
	if err != nil {
		t.Fatalf("Read(%q) after worktree seed error = %v, want nil", WiringPath(filepath.Join(root, "graft")), err)
	}
	if !slices.ContainsFunc(graph.Nodes, func(node NodeV1) bool { return node.Name == "before" }) {
		t.Errorf("Read(%q) nodes = %v, want the parent graph's before function", WiringPath(filepath.Join(root, "graft")), graph.Nodes)
	}
	for _, name := range []string{"ask-index.json", "extract." + ExtractorID + ".json", "fingerprint." + ExtractorID + ".json"} {
		path := filepath.Join(root, "graft", ".cache", name)
		if _, err := os.Stat(path); err != nil {
			t.Errorf("Stat(%q) after worktree seed error = %v, want copied sidecar", path, err)
		}
	}
	for _, name := range []string{"unrelated.json", ".sync.lock"} {
		path := filepath.Join(root, "graft", ".cache", name)
		if _, err := os.Stat(path); !errors.Is(err, fs.ErrNotExist) {
			t.Errorf("Stat(%q) after worktree seed error = %v, want no copied file", path, err)
		}
	}

	writeRefreshSource(t, root, "src/app.ts", "export function after() {}")
	got = EnsureFreshGraph(root, options)
	if !got.Refreshed || got.Drift == nil || !slices.Equal(got.Drift.Changed, []string{"src/app.ts"}) {
		t.Fatalf("EnsureFreshGraph(%q, %#v) after source change = %#v, want one changed file refreshed", root, options, got)
	}
	graph, err = Read(WiringPath(filepath.Join(root, "graft")))
	if err != nil {
		t.Fatalf("Read(%q) after worktree refresh error = %v, want nil", WiringPath(filepath.Join(root, "graft")), err)
	}
	if !slices.ContainsFunc(graph.Nodes, func(node NodeV1) bool { return node.Name == "after" }) {
		t.Errorf("Read(%q) nodes after refresh = %v, want the after function", WiringPath(filepath.Join(root, "graft")), graph.Nodes)
	}
}

func TestEnsureFreshGraphSeedBusyLockContract(t *testing.T) {
	t.Setenv("GRAFT_NO_REFRESH", "false")
	t.Setenv("GRAFT_NO_SEED", "false")
	_, root := newRefreshGitWorktree(t, "export function parentOnly() {}")
	cacheDir := filepath.Join(root, "graft", ".cache")
	if err := os.MkdirAll(cacheDir, 0o755); err != nil {
		t.Fatalf("MkdirAll(%q) error = %v, want nil", cacheDir, err)
	}
	lockPath := filepath.Join(cacheDir, ".sync.lock")
	if err := os.WriteFile(lockPath, []byte("held"), 0o644); err != nil {
		t.Fatalf("WriteFile(%q) error = %v, want nil", lockPath, err)
	}
	options := RefreshOptions{Source: sourcefiles.Options{Extensions: []string{".ts"}}}
	want := "another process is still copying the graph into this worktree — retry the query in a moment"
	if got := EnsureFreshGraph(root, options); got != (RefreshResult{Note: want}) {
		t.Errorf("EnsureFreshGraph(%q, %#v) with a held seed lock = %#v, want note %q", root, options, got, want)
	}
	if _, err := os.Stat(WiringPath(filepath.Join(root, "graft"))); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("Stat(%q) with a held seed lock error = %v, want no graph", WiringPath(filepath.Join(root, "graft")), err)
	}
}

func TestEnsureFreshGraphWorktreeSeedGatesContract(t *testing.T) {
	for _, tt := range []struct {
		name         string
		noSeed       bool
		customOutDir bool
	}{
		{name: "GRAFT_NO_SEED", noSeed: true},
		{name: "custom output directory", customOutDir: true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv("GRAFT_NO_REFRESH", "false")
			t.Setenv("GRAFT_NO_SEED", "false")
			main, root := newRefreshGitWorktree(t, "export function parentOnly() {}")
			options := RefreshOptions{Source: sourcefiles.Options{Extensions: []string{".ts"}}}
			if tt.noSeed {
				t.Setenv("GRAFT_NO_SEED", "1")
			}
			if tt.customOutDir {
				options.Source.OutDir = filepath.Join(root, "custom")
			}
			got := EnsureFreshGraph(root, options)
			if got != (RefreshResult{}) {
				t.Errorf("EnsureFreshGraph(%q, %#v) = %#v, want no-op without a graph", root, options, got)
			}
			if _, err := os.Stat(WiringPath(filepath.Join(root, "graft"))); !errors.Is(err, fs.ErrNotExist) {
				t.Errorf("Stat(%q) after disabled seed from %q error = %v, want no worktree graph", WiringPath(filepath.Join(root, "graft")), main, err)
			}
		})
	}
}

func TestEnsureFreshGraphMissingFingerprintContract(t *testing.T) {
	root := t.TempDir()
	outDir := filepath.Join(root, "graft")
	options := RefreshOptions{Source: sourcefiles.Options{OutDir: outDir, Extensions: []string{".ts"}}}
	writeRefreshSource(t, root, "src/app.ts", "export function firstBuild() {}")
	if _, err := Write(GraphV1{}, outDir); err != nil {
		t.Fatalf("Write(GraphV1{}, %q) error = %v, want nil", outDir, err)
	}

	got := EnsureFreshGraph(root, options)
	if !got.Refreshed || got.Drift != nil {
		t.Fatalf("EnsureFreshGraph(%q, %#v) with a missing fingerprint = %#v, want one refresh with unknown drift", root, options, got)
	}
	if fingerprint, err := ReadFingerprint(outDir, ExtractorID); err != nil || fingerprint == nil || len(fingerprint.Files) != 1 {
		t.Errorf("ReadFingerprint(%q, %q) after refresh = %#v, %v, want one file", outDir, ExtractorID, fingerprint, err)
	}
	got = EnsureFreshGraph(root, options)
	if got.Refreshed || got.Note != "" {
		t.Errorf("EnsureFreshGraph(%q, %#v) after first fingerprint = %#v, want a silent no-op", root, options, got)
	}
}

func TestEnsureFreshGraphOnlyDirsContract(t *testing.T) {
	root := t.TempDir()
	outDir := filepath.Join(root, "graft")
	options := RefreshOptions{Source: sourcefiles.Options{
		OutDir: outDir, Extensions: []string{".ts"}, OnlyDirs: []string{"src"},
	}}
	writeRefreshSource(t, root, "src/app.ts", "export function scoped() {}")
	buildAndWriteRefreshGraph(t, root, options.Source)
	writeRefreshSource(t, root, "outside/new.ts", "export function outside() {}")

	got := EnsureFreshGraph(root, options)
	if got.Refreshed || got.Note != "" {
		t.Errorf("EnsureFreshGraph(%q, %#v) with an out-of-scope edit = %#v, want a silent no-op", root, options, got)
	}
}

func TestEnsureFreshGraphUnsupportedSourceContract(t *testing.T) {
	root := t.TempDir()
	outDir := filepath.Join(root, "graft")
	base := sourcefiles.Options{OutDir: outDir, Extensions: []string{".ts"}}
	writeRefreshSource(t, root, "src/app.ts", "export function retained() {}")
	buildAndWriteRefreshGraph(t, root, base)
	beforeGraph, err := os.ReadFile(WiringPath(outDir))
	if err != nil {
		t.Fatalf("ReadFile(%q) before refresh error = %v", WiringPath(outDir), err)
	}
	beforeFingerprint, err := os.ReadFile(filepath.Join(outDir, ".cache", "fingerprint."+ExtractorID+".json"))
	if err != nil {
		t.Fatalf("ReadFile(%q) before refresh error = %v", filepath.Join(outDir, ".cache", "fingerprint."+ExtractorID+".json"), err)
	}
	writeRefreshSource(t, root, "src/unsupported.zig", "fn unsupported() void {}\n")
	options := RefreshOptions{Source: sourcefiles.Options{OutDir: outDir, Extensions: []string{".ts", ".zig"}}}

	got := EnsureFreshGraph(root, options)
	if got.Refreshed || !strings.Contains(got.Note, "unsupported") {
		t.Fatalf("EnsureFreshGraph(%q, %#v) with unsupported source = %#v, want explicit limitation", root, options, got)
	}
	afterGraph, err := os.ReadFile(WiringPath(outDir))
	if err != nil {
		t.Fatalf("ReadFile(%q) after refresh error = %v", WiringPath(outDir), err)
	}
	afterFingerprint, err := os.ReadFile(filepath.Join(outDir, ".cache", "fingerprint."+ExtractorID+".json"))
	if err != nil {
		t.Fatalf("ReadFile(%q) after refresh error = %v", filepath.Join(outDir, ".cache", "fingerprint."+ExtractorID+".json"), err)
	}
	if !slices.Equal(beforeGraph, afterGraph) || !slices.Equal(beforeFingerprint, afterFingerprint) {
		t.Errorf("EnsureFreshGraph(%q, %#v) changed graph or fingerprint despite unsupported source", root, options)
	}
}

func TestEnsureFreshGraphUnsupportedBaselineContract(t *testing.T) {
	root := t.TempDir()
	outDir := filepath.Join(root, "graft")
	options := sourcefiles.Options{OutDir: outDir, Extensions: []string{".ts", ".zig"}}
	writeRefreshSource(t, root, "src/app.ts", "export function retained() {}")
	writeRefreshSource(t, root, "src/unsupported.zig", "fn unsupported() void {}\n")
	built, err := BuildGraph(root, options)
	if err != nil {
		t.Fatalf("BuildGraph(%q, %#v) error = %v, want nil", root, options, err)
	}
	if len(built.Unsupported) != 1 {
		t.Fatalf("BuildGraph(%q, %#v) unsupported = %v, want src/unsupported.zig", root, options, built.Unsupported)
	}
	if _, err := Write(built.Graph, outDir); err != nil {
		t.Fatalf("Write(BuildGraph(%q), %q) error = %v, want nil", root, outDir, err)
	}
	if err := WriteFingerprint(outDir, ExtractorID, built.Fingerprints, nil); err != nil {
		t.Fatalf("WriteFingerprint(%q, %q, files, nil) error = %v, want nil", outDir, ExtractorID, err)
	}
	before, err := os.ReadFile(WiringPath(outDir))
	if err != nil {
		t.Fatalf("ReadFile(%q) before refresh error = %v", WiringPath(outDir), err)
	}

	got := EnsureFreshGraph(root, RefreshOptions{Source: options})
	if got.Refreshed || !strings.Contains(got.Note, "unsupported") || got.Drift == nil ||
		len(got.Drift.Changed)+len(got.Drift.Added)+len(got.Drift.Removed) != 0 {
		t.Fatalf("EnsureFreshGraph(%q, %#v) with clean unsupported baseline = %#v, want limitation without treating tree as clean", root, options, got)
	}
	after, err := os.ReadFile(WiringPath(outDir))
	if err != nil {
		t.Fatalf("ReadFile(%q) after refresh error = %v", WiringPath(outDir), err)
	}
	if !slices.Equal(before, after) {
		t.Errorf("EnsureFreshGraph(%q, %#v) overwrote the graph while an adapter was unsupported", root, options)
	}
}

func TestEnsureFreshGraphUndecodableBaselineContract(t *testing.T) {
	root := t.TempDir()
	outDir := filepath.Join(root, "graft")
	options := sourcefiles.Options{OutDir: outDir, Extensions: []string{".ts"}}
	path := filepath.Join(root, "src", "app.ts")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("MkdirAll(%q) error = %v", filepath.Dir(path), err)
	}
	if err := os.WriteFile(path, []byte{0xfe, 0xff, 0x00, 'x'}, 0o644); err != nil {
		t.Fatalf("WriteFile(%q, undecodable source) error = %v", path, err)
	}
	built, err := BuildGraph(root, options)
	if err != nil {
		t.Fatalf("BuildGraph(%q, %#v) error = %v, want nil", root, options, err)
	}
	if !slices.Equal(built.Unsupported, []string{"src/app.ts"}) || built.Fingerprints["src/app.ts"].Hash != "" {
		t.Fatalf("BuildGraph(%q, %#v) unsupported/hash = (%v, %q), want undecodable src/app.ts and empty hash", root, options, built.Unsupported, built.Fingerprints["src/app.ts"].Hash)
	}
	if _, err := Write(built.Graph, outDir); err != nil {
		t.Fatalf("Write(BuildGraph(%q), %q) error = %v, want nil", root, outDir, err)
	}
	if err := WriteFingerprint(outDir, ExtractorID, built.Fingerprints, nil); err != nil {
		t.Fatalf("WriteFingerprint(%q, %q, files, nil) error = %v, want nil", outDir, ExtractorID, err)
	}

	got := EnsureFreshGraph(root, RefreshOptions{Source: options})
	if got.Refreshed || !strings.Contains(got.Note, "unsupported") || got.Drift == nil ||
		len(got.Drift.Changed)+len(got.Drift.Added)+len(got.Drift.Removed) != 0 {
		t.Errorf("EnsureFreshGraph(%q, %#v) with an undecodable baseline = %#v, want limitation without claiming drift or clean", root, options, got)
	}
	writeRefreshSource(t, root, "src/app.ts", "export function recovered() {}")
	got = EnsureFreshGraph(root, RefreshOptions{Source: options})
	if !got.Refreshed || got.Drift == nil || !slices.Equal(got.Drift.Changed, []string{"src/app.ts"}) {
		t.Fatalf("EnsureFreshGraph(%q, %#v) after decoding recovers = %#v, want refreshed src/app.ts", root, options, got)
	}
	wiring, err := Read(WiringPath(outDir))
	if err != nil {
		t.Fatalf("Read(%q) after decoding recovery error = %v, want nil", WiringPath(outDir), err)
	}
	if !slices.ContainsFunc(wiring.Nodes, func(node NodeV1) bool { return node.ID == "src/app.ts#recovered" }) {
		t.Errorf("EnsureFreshGraph(%q, %#v) nodes after decoding recovery = %v, want src/app.ts#recovered", root, options, wiring.Nodes)
	}
}

func TestEnsureFreshGraphBusyLockContract(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		root := t.TempDir()
		outDir := filepath.Join(root, "graft")
		options := RefreshOptions{Source: sourcefiles.Options{OutDir: outDir, Extensions: []string{".ts"}}}
		writeRefreshSource(t, root, "src/app.ts", "export function before() {}")
		buildAndWriteRefreshGraph(t, root, options.Source)
		writeRefreshSource(t, root, "src/app.ts", "export function after() {}")
		lockPath := filepath.Join(outDir, ".cache", ".sync.lock")
		if err := os.MkdirAll(filepath.Dir(lockPath), 0o755); err != nil {
			t.Fatalf("MkdirAll(%q) error = %v", filepath.Dir(lockPath), err)
		}
		if err := os.WriteFile(lockPath, []byte("held"), 0o644); err != nil {
			t.Fatalf("WriteFile(%q) error = %v", lockPath, err)
		}
		result := make(chan RefreshResult, 1)
		go func() { result <- EnsureFreshGraph(root, options) }()
		synctest.Wait()
		synctest.Sleep(2 * time.Second)
		synctest.Wait()
		got := <-result
		if got.Refreshed || got.Note != "a graph rebuild is already in flight — answering from the current graph" || got.Drift == nil {
			t.Errorf("EnsureFreshGraph(%q, %#v) with a held lock = %#v, want bounded stale fallback", root, options, got)
		}
	})
}

func TestEnsureFreshGraphReprobesAfterLockContract(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		root := t.TempDir()
		outDir := filepath.Join(root, "graft")
		sourceOptions := sourcefiles.Options{OutDir: outDir, Extensions: []string{".ts"}}
		options := RefreshOptions{Source: sourceOptions}
		writeRefreshSource(t, root, "src/app.ts", "export function before() {}")
		buildAndWriteRefreshGraph(t, root, sourceOptions)
		writeRefreshSource(t, root, "src/app.ts", "export function after() {}")
		lockPath := filepath.Join(outDir, ".cache", ".sync.lock")
		if err := os.WriteFile(lockPath, []byte("held"), 0o644); err != nil {
			t.Fatalf("WriteFile(%q) error = %v", lockPath, err)
		}
		result := make(chan RefreshResult, 1)
		go func() { result <- EnsureFreshGraph(root, options) }()
		synctest.Wait()
		buildAndWriteRefreshGraph(t, root, sourceOptions)
		if err := os.Remove(lockPath); err != nil {
			t.Fatalf("Remove(%q) error = %v", lockPath, err)
		}
		synctest.Wait()
		got := <-result
		if got.Refreshed || got.Note != "" {
			t.Errorf("EnsureFreshGraph(%q, %#v) after another build = %#v, want a no-op after re-probe", root, options, got)
		}
	})
}

func TestAcquireGraphLockReclaimsStaleContract(t *testing.T) {
	cache := t.TempDir()
	lockPath := filepath.Join(cache, ".sync.lock")
	if err := os.WriteFile(lockPath, []byte("stale"), 0o644); err != nil {
		t.Fatalf("WriteFile(%q) error = %v", lockPath, err)
	}
	stale := time.Now().Add(-6 * time.Minute)
	if err := os.Chtimes(lockPath, stale, stale); err != nil {
		t.Fatalf("Chtimes(%q, %v) error = %v", lockPath, stale, err)
	}
	got, err := AcquireLock(cache)
	if err != nil {
		t.Fatalf("AcquireLock(%q) error = %v, want nil", cache, err)
	}
	if !got {
		t.Fatalf("AcquireLock(%q) = false, want stale lock reclaimed", cache)
	}
	ReleaseLock(cache)
}

func TestRefreshNoteContract(t *testing.T) {
	for _, tt := range []struct {
		name string
		got  RefreshResult
		want string
	}{
		{name: "silent no-op", want: ""},
		{name: "soft error", got: RefreshResult{Note: "graph refresh skipped: disk error"}, want: "[graft] graph refresh skipped: disk error"},
		{name: "one changed file", got: RefreshResult{Refreshed: true, Drift: &Drift{Changed: []string{"src/app.ts"}}}, want: "[graft] refreshed the graph (1 file changed) before answering"},
		{name: "unknown drift count", got: RefreshResult{Refreshed: true}, want: "[graft] refreshed the graph (? files changed) before answering"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if got := RefreshNote(tt.got); got != tt.want {
				t.Errorf("RefreshNote(%#v) = %q, want %q", tt.got, got, tt.want)
			}
		})
	}
}

func writeRefreshSource(t *testing.T, root, rel, source string) {
	t.Helper()
	path := filepath.Join(root, rel)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("MkdirAll(%q) error = %v", filepath.Dir(path), err)
	}
	if err := os.WriteFile(path, []byte(source), 0o644); err != nil {
		t.Fatalf("WriteFile(%q) error = %v", path, err)
	}
}

func newRefreshGitWorktree(t *testing.T, source string) (main, worktree string) {
	t.Helper()
	base := t.TempDir()
	main = filepath.Join(base, "main")
	worktree = filepath.Join(base, "worktree")
	if err := os.MkdirAll(main, 0o755); err != nil {
		t.Fatalf("MkdirAll(%q) error = %v, want nil", main, err)
	}
	runGit := func(args ...string) {
		command := exec.Command("git", append([]string{"-C", main}, args...)...)
		output, err := command.CombinedOutput()
		if err != nil {
			t.Fatalf("git -C %q %v = %q, error %v, want success", main, args, output, err)
		}
	}
	runGit("init", "--quiet")
	runGit("config", "user.name", "Graft Test")
	runGit("config", "user.email", "graft@example.invalid")
	writeRefreshSource(t, main, "src/app.ts", source)
	runGit("add", "src/app.ts")
	runGit("commit", "--quiet", "-m", "base")
	buildAndWriteRefreshGraph(t, main, sourcefiles.Options{
		OutDir: filepath.Join(main, "graft"), Extensions: []string{".ts"},
	})
	runGit("worktree", "add", "--quiet", "-b", "feature", worktree)
	return main, worktree
}

func buildAndWriteRefreshGraph(t *testing.T, root string, opts sourcefiles.Options) {
	t.Helper()
	result, err := BuildGraph(root, opts)
	if err != nil {
		t.Fatalf("BuildGraph(%q, %#v) error = %v, want nil", root, opts, err)
	}
	if len(result.Unsupported) > 0 || len(result.Errors) > 0 {
		t.Fatalf("BuildGraph(%q, %#v) coverage = unsupported %v, errors %v, want complete test fixture", root, opts, result.Unsupported, result.Errors)
	}
	if _, err := Write(result.Graph, opts.OutDir); err != nil {
		t.Fatalf("Write(BuildGraph(%q), %q) error = %v, want nil", root, opts.OutDir, err)
	}
	if err := WriteFingerprint(opts.OutDir, ExtractorID, result.Fingerprints, opts.OnlyDirs); err != nil {
		t.Fatalf("WriteFingerprint(%q, %q, files, %v) error = %v, want nil", opts.OutDir, ExtractorID, opts.OnlyDirs, err)
	}
}
