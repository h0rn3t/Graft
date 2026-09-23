package graph

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"testing"

	"github.com/NanoNets/context-graph-engine/internal/sourcefiles"
)

func TestBuildGraphContract(t *testing.T) {
	root := t.TempDir()
	write := func(rel, source string) {
		t.Helper()
		path := filepath.Join(root, rel)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatalf("MkdirAll(%q) error = %v", filepath.Dir(path), err)
		}
		if err := os.WriteFile(path, []byte(source), 0o644); err != nil {
			t.Fatalf("WriteFile(%q) error = %v", path, err)
		}
	}
	write("src/helper.js", "export function helper() {}")
	write("src/main.ts", "import { helper } from \"./helper.js\";\nexport function run() { return helper; }")
	write("src/main.zig", "fn main() void {}")

	opts := sourcefiles.Options{OutDir: filepath.Join(root, "graft")}
	got, err := BuildGraph(root, opts)
	if err != nil {
		t.Fatalf("BuildGraph(%q, %#v) error = %v, want nil", root, opts, err)
	}
	wantEdges := []EdgeV1{
		{Source: "src/helper.js", Target: "src/helper.js#helper", Relation: "contains", Confidence: "extracted"},
		{Source: "src/main.ts", Target: "src/helper.js", Relation: "imports", Confidence: "extracted"},
		{Source: "src/main.ts", Target: "src/main.ts#run", Relation: "contains", Confidence: "extracted"},
		{Source: "src/main.ts#run", Target: "src/helper.js#helper", Relation: "references", Confidence: "extracted"},
	}
	wantScopes := []ScopeV1{{Prefix: "", Label: "", Markers: []string{}}}
	if got.Graph.Meta.Version != 1 || got.Graph.Meta.NodeCount != 4 || got.Graph.Meta.EdgeCount != len(wantEdges) || !reflect.DeepEqual(got.Graph.Meta.Languages, []string{"javascript", "typescript"}) || len(got.Graph.Nodes) != 4 || !reflect.DeepEqual(got.Graph.Edges, wantEdges) || !reflect.DeepEqual(got.Unsupported, []string{"src/main.zig"}) || len(got.Errors) != 0 || len(got.Limitations) != 0 || got.Graph.Meta.Scopes == nil || !reflect.DeepEqual(*got.Graph.Meta.Scopes, wantScopes) {
		t.Errorf("BuildGraph(%q, %#v) = %#v, want GraphV1 with 4 nodes, 4 edges, TS/JS coverage, root scope, no limitations, and unsupported src/main.zig", root, opts, got)
	}
	if invariants := CheckInvariants(got.Graph); len(invariants.Problems) > 0 {
		t.Errorf("CheckInvariants(BuildGraph(%q)) = %v, want no problems", root, invariants.Problems)
	}
	path, err := Write(got.Graph, filepath.Join(root, "graft"))
	if err != nil {
		t.Fatalf("Write(BuildGraph(%q), %q) error = %v, want nil", root, filepath.Join(root, "graft"), err)
	}
	first, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile(%q) error = %v", path, err)
	}
	again, err := BuildGraph(root, opts)
	if err != nil {
		t.Fatalf("BuildGraph(%q, %#v) second call error = %v, want nil", root, opts, err)
	}
	if !reflect.DeepEqual(got.Graph, again.Graph) {
		t.Errorf("BuildGraph(%q, %#v) second graph = %#v, want %#v", root, opts, again.Graph, got.Graph)
	}
	if _, err := Write(again.Graph, filepath.Join(root, "graft")); err != nil {
		t.Fatalf("Write(second BuildGraph(%q), %q) error = %v, want nil", root, filepath.Join(root, "graft"), err)
	}
	second, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile(%q) second read error = %v", path, err)
	}
	if !bytes.Equal(first, second) {
		t.Errorf("Write(BuildGraph(%q)) output changed between identical builds", root)
	}
}

func TestBuildGraphFingerprintContract(t *testing.T) {
	root := t.TempDir()
	outDir := filepath.Join(root, "graft")
	path := filepath.Join(root, "src", "app.ts")
	source := "export function run() {}"
	unsupportedPath := filepath.Join(root, "src", "app.zig")
	unsupportedSource := "const app = 1;\n"
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("MkdirAll(%q) error = %v", filepath.Dir(path), err)
	}
	if err := os.WriteFile(path, []byte(source), 0o644); err != nil {
		t.Fatalf("WriteFile(%q) error = %v", path, err)
	}
	if err := os.WriteFile(unsupportedPath, []byte(unsupportedSource), 0o644); err != nil {
		t.Fatalf("WriteFile(%q) error = %v", unsupportedPath, err)
	}
	opts := sourcefiles.Options{OutDir: outDir, Extensions: []string{".ts", ".zig"}, OnlyDirs: []string{"src"}}
	result, err := BuildGraph(root, opts)
	if err != nil {
		t.Fatalf("BuildGraph(%q, %#v) error = %v, want nil", root, opts, err)
	}
	if !slices.Equal(result.Unsupported, []string{"src/app.zig"}) {
		t.Errorf("BuildGraph(%q, %#v) unsupported = %v, want [src/app.zig]", root, opts, result.Unsupported)
	}
	fingerprint, err := ReadFingerprint(outDir, ExtractorID)
	if err != nil {
		t.Fatalf("ReadFingerprint(%q, %q) error = %v, want nil", outDir, ExtractorID, err)
	}
	if fingerprint != nil {
		t.Fatalf("ReadFingerprint(%q, %q) = %#v after BuildGraph, want nil", outDir, ExtractorID, fingerprint)
	}
	files, err := sourcefiles.Walk(root, opts)
	if err != nil {
		t.Fatalf("Walk(%q, %#v) error = %v, want nil", root, opts, err)
	}
	if len(files) != 2 || len(result.Fingerprints) != 2 {
		t.Errorf("BuildGraph(%q, %#v) fingerprints = %v for files %v, want both sources", root, opts, result.Fingerprints, files)
	}
	wantHashes := map[string]string{
		"src/app.ts":  sourcefiles.Hash(source),
		"src/app.zig": sourcefiles.Hash(unsupportedSource),
	}
	for _, file := range files {
		got := result.Fingerprints[file.Rel]
		want := FingerprintFile{Size: file.Size, MTimeMS: file.MTimeMS, Hash: wantHashes[file.Rel]}
		if got != want {
			t.Errorf("BuildGraph(%q, %#v) fingerprint[%q] = %#v, want %#v", root, opts, file.Rel, got, want)
		}
	}
	if err := os.Remove(unsupportedPath); err != nil {
		t.Fatalf("Remove(%q) error = %v", unsupportedPath, err)
	}
	result, err = BuildGraph(root, opts)
	if err != nil {
		t.Fatalf("BuildGraph(%q, %#v) after unsupported file removal error = %v, want nil", root, opts, err)
	}
	if len(result.Fingerprints) != 1 {
		t.Fatalf("BuildGraph(%q, %#v) fingerprints after removal = %v, want only src/app.ts", root, opts, result.Fingerprints)
	}
	if _, err := Write(result.Graph, outDir); err != nil {
		t.Fatalf("Write(BuildGraph(%q), %q) error = %v, want nil", root, outDir, err)
	}
	if err := WriteFingerprint(outDir, ExtractorID, result.Fingerprints, opts.OnlyDirs); err != nil {
		t.Fatalf("WriteFingerprint(%q, %q, files, %v) error = %v, want nil", outDir, ExtractorID, opts.OnlyDirs, err)
	}
	fingerprint, err = ReadFingerprint(outDir, ExtractorID)
	if err != nil {
		t.Fatalf("ReadFingerprint(%q, %q) after write error = %v, want nil", outDir, ExtractorID, err)
	}
	if fingerprint == nil || !slices.Equal(fingerprint.OnlyDirs, opts.OnlyDirs) || len(fingerprint.Files) != 1 {
		t.Fatalf("ReadFingerprint(%q, %q) = %#v, want src/app.ts and onlyDirs %v", outDir, ExtractorID, fingerprint, opts.OnlyDirs)
	}
	drift, err := ProbeDrift(root, outDir, ExtractorID, opts)
	if err != nil {
		t.Fatalf("ProbeDrift(%q, %q, %q, %#v) error = %v, want nil", root, outDir, ExtractorID, opts, err)
	}
	if drift == nil || len(drift.Added)+len(drift.Changed)+len(drift.Removed) != 0 {
		t.Errorf("ProbeDrift(%q, %q, %q, %#v) = %#v, want clean fingerprint", root, outDir, ExtractorID, opts, drift)
	}
}

func TestBuildGraphExtractionCacheContract(t *testing.T) {
	root := t.TempDir()
	outDir := filepath.Join(root, "graft")
	path := filepath.Join(root, "src", "app.ts")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("MkdirAll(%q) error = %v", filepath.Dir(path), err)
	}
	if err := os.WriteFile(path, []byte("export function oldName() {}"), 0o644); err != nil {
		t.Fatalf("WriteFile(%q) error = %v", path, err)
	}
	opts := sourcefiles.Options{OutDir: outDir, Extensions: []string{".ts"}}

	first, err := BuildGraph(root, opts)
	if err != nil {
		t.Fatalf("BuildGraph(%q, %#v) error = %v, want nil", root, opts, err)
	}
	if first.Parsed != 1 || first.Reused != 0 {
		t.Errorf("BuildGraph(%q, %#v) cache counts = (%d, %d), want (1, 0)", root, opts, first.Parsed, first.Reused)
	}
	cachePath := filepath.Join(outDir, ".cache", "extract."+ExtractorID+".json")
	data, err := os.ReadFile(cachePath)
	if err != nil {
		t.Fatalf("ReadFile(%q) error = %v, want a written extraction cache", cachePath, err)
	}
	var cache struct {
		Version   int                        `json:"version"`
		Extractor string                     `json:"extractor"`
		Files     map[string]json.RawMessage `json:"files"`
	}
	if err := json.Unmarshal(data, &cache); err != nil {
		t.Fatalf("Unmarshal(%q) error = %v, want valid JSON", cachePath, err)
	}
	if cache.Version != extractCacheVersion || cache.Extractor != ExtractorID || len(cache.Files) != 1 {
		t.Errorf("extract cache = (version %d, extractor %q, files %v), want (1, %q, [src/app.ts])", cache.Version, cache.Extractor, cache.Files, ExtractorID)
	}

	second, err := BuildGraph(root, opts)
	if err != nil {
		t.Fatalf("BuildGraph(%q, %#v) second call error = %v, want nil", root, opts, err)
	}
	if second.Parsed != 0 || second.Reused != 1 || !reflect.DeepEqual(first.Graph, second.Graph) {
		t.Errorf("BuildGraph(%q, %#v) second result = (parsed %d, reused %d, graph equal %t), want (0, 1, true)", root, opts, second.Parsed, second.Reused, reflect.DeepEqual(first.Graph, second.Graph))
	}

	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("Stat(%q) error = %v", path, err)
	}
	if err := os.WriteFile(path, []byte("export function newName() {}"), 0o644); err != nil {
		t.Fatalf("WriteFile(%q) changed source error = %v", path, err)
	}
	if err := os.Chtimes(path, info.ModTime(), info.ModTime()); err != nil {
		t.Fatalf("Chtimes(%q, %v) error = %v", path, info.ModTime(), err)
	}
	changed, err := os.Stat(path)
	if err != nil {
		t.Fatalf("Stat(%q) changed source error = %v", path, err)
	}
	if changed.Size() != info.Size() || !changed.ModTime().Equal(info.ModTime()) {
		t.Fatalf("source stats after same-length edit = (%d, %v), want (%d, %v)", changed.Size(), changed.ModTime(), info.Size(), info.ModTime())
	}
	third, err := BuildGraph(root, opts)
	if err != nil {
		t.Fatalf("BuildGraph(%q, %#v) changed source error = %v, want nil", root, opts, err)
	}
	if third.Parsed != 1 || third.Reused != 0 || third.Graph.Nodes[1].Name != "newName" {
		t.Errorf("BuildGraph(%q, %#v) after same-stat edit = (parsed %d, reused %d, name %q), want (1, 0, %q)", root, opts, third.Parsed, third.Reused, third.Graph.Nodes[1].Name, "newName")
	}
}

func TestBuildGraphExtractionCacheFailureContract(t *testing.T) {
	for _, tt := range []struct {
		name    string
		sidecar string
	}{
		{name: "malformed JSON", sidecar: "{"},
		{name: "wrong version", sidecar: `{"version":99,"extractor":"go-v2","files":{}}`},
	} {
		t.Run(tt.name, func(t *testing.T) {
			root := t.TempDir()
			outDir := filepath.Join(root, "graft")
			cacheDir := filepath.Join(outDir, ".cache")
			if err := os.MkdirAll(cacheDir, 0o755); err != nil {
				t.Fatalf("MkdirAll(%q) error = %v", cacheDir, err)
			}
			cachePath := filepath.Join(cacheDir, "extract."+ExtractorID+".json")
			if err := os.WriteFile(cachePath, []byte(tt.sidecar), 0o644); err != nil {
				t.Fatalf("WriteFile(%q) error = %v", cachePath, err)
			}
			path := filepath.Join(root, "app.ts")
			if err := os.WriteFile(path, []byte("export function run() {}"), 0o644); err != nil {
				t.Fatalf("WriteFile(%q) error = %v", path, err)
			}
			opts := sourcefiles.Options{OutDir: outDir, Extensions: []string{".ts"}}
			got, err := BuildGraph(root, opts)
			if err != nil {
				t.Fatalf("BuildGraph(%q, %#v) error = %v, want nil", root, opts, err)
			}
			if got.Parsed != 1 || got.Reused != 0 || len(got.Graph.Nodes) != 2 {
				t.Errorf("BuildGraph(%q, %#v) = (parsed %d, reused %d, nodes %d), want (1, 0, 2)", root, opts, got.Parsed, got.Reused, len(got.Graph.Nodes))
			}
		})
	}

	root := t.TempDir()
	outDir := filepath.Join(root, "graft")
	path := filepath.Join(root, "app.ts")
	if err := os.WriteFile(path, []byte("export function run() {}"), 0o644); err != nil {
		t.Fatalf("WriteFile(%q) error = %v", path, err)
	}
	opts := sourcefiles.Options{OutDir: outDir, Extensions: []string{".ts"}}
	if _, err := BuildGraph(root, opts); err != nil {
		t.Fatalf("BuildGraph(%q, %#v) seed cache error = %v", root, opts, err)
	}
	cachePath := filepath.Join(outDir, ".cache", "extract."+ExtractorID+".json")
	data, err := os.ReadFile(cachePath)
	if err != nil {
		t.Fatalf("ReadFile(%q) error = %v", cachePath, err)
	}
	var encoded map[string]any
	if err := json.Unmarshal(data, &encoded); err != nil {
		t.Fatalf("Unmarshal(%q) error = %v", cachePath, err)
	}
	files, ok := encoded["files"].(map[string]any)
	if !ok {
		t.Fatalf("extract cache files = %T, want map", encoded["files"])
	}
	entry, ok := files["app.ts"].(map[string]any)
	if !ok {
		t.Fatalf("extract cache entry = %T, want map", files["app.ts"])
	}
	entry["rawEdges"] = "invalid"
	data, err = json.Marshal(encoded)
	if err != nil {
		t.Fatalf("Marshal(%q cache) error = %v", cachePath, err)
	}
	if err := os.WriteFile(cachePath, data, 0o644); err != nil {
		t.Fatalf("WriteFile(%q) malformed cache error = %v", cachePath, err)
	}
	got, err := BuildGraph(root, opts)
	if err != nil {
		t.Fatalf("BuildGraph(%q, %#v) malformed cache error = %v, want nil", root, opts, err)
	}
	if got.Parsed != 1 || got.Reused != 0 || len(got.Graph.Edges) != 1 {
		t.Errorf("BuildGraph(%q, %#v) partially malformed cache = (parsed %d, reused %d, edges %d), want (1, 0, 1)", root, opts, got.Parsed, got.Reused, len(got.Graph.Edges))
	}

	root = t.TempDir()
	outDir = filepath.Join(root, "graft")
	if err := os.WriteFile(outDir, []byte("file blocks cache directory"), 0o644); err != nil {
		t.Fatalf("WriteFile(%q) error = %v", outDir, err)
	}
	if err := os.WriteFile(filepath.Join(root, "app.ts"), []byte("export function run() {}"), 0o644); err != nil {
		t.Fatalf("WriteFile(%q) source error = %v", filepath.Join(root, "app.ts"), err)
	}
	opts = sourcefiles.Options{OutDir: outDir, Extensions: []string{".ts"}}
	got, err = BuildGraph(root, opts)
	if err != nil {
		t.Fatalf("BuildGraph(%q, %#v) cache write failure = %v, want nil", root, opts, err)
	}
	if got.Parsed != 1 || len(got.Errors) != 0 {
		t.Errorf("BuildGraph(%q, %#v) after cache write failure = (parsed %d, errors %v), want (1, none)", root, opts, got.Parsed, got.Errors)
	}
}
