package contextcheck

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/NanoNets/context-graph-engine/internal/graph"
)

func TestCheckContract(t *testing.T) {
	tests := []struct {
		name    string
		setup   func(t *testing.T, root string)
		options Options
		want    Result
	}{
		{
			name:  "missing manifest",
			setup: func(t *testing.T, root string) {},
			want:  Result{Missing: true},
		},
		{
			name: "invalid manifest is missing",
			setup: func(t *testing.T, root string) {
				t.Helper()
				if err := os.MkdirAll(filepath.Join(root, "graft"), 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(root, "graft", "manifest.json"), []byte("{"), 0o644); err != nil {
					t.Fatal(err)
				}
			},
			want: Result{Missing: true},
		},
		{
			name: "null manifest is missing",
			setup: func(t *testing.T, root string) {
				t.Helper()
				writeSourceBytes(t, root, filepath.Join("graft", "manifest.json"), []byte("null"))
			},
			want: Result{Missing: true},
		},
		{
			name: "clean graph",
			setup: func(t *testing.T, root string) {
				t.Helper()
				writeSource(t, root, "app.ts", "export const app = 1;\n")
				writeManifest(t, root, graph.Manifest{
					Version: 1,
					Files:   []graph.SourceRef{{Path: "app.ts", Hash: hashText("export const app = 1;\n")}},
					Nodes:   []graph.ManifestNode{{Slug: "app", SourcesDigest: "digest"}},
				})
				writeNode(t, root, "app.md", "---\nslug: app\nsources_digest: digest\n---\n")
				writeNode(t, root, "app-card.md", "# app.ts\n")
			},
			want: Result{OK: true},
		},
		{
			name: "changed removed and uncovered",
			setup: func(t *testing.T, root string) {
				t.Helper()
				writeSource(t, root, "changed.ts", "new\n")
				writeSource(t, root, "new.ts", "new file\n")
				writeManifest(t, root, graph.Manifest{
					Version: 1,
					Files: []graph.SourceRef{
						{Path: "changed.ts", Hash: hashText("old\n")},
						{Path: "removed.ts", Hash: hashText("gone\n")},
					},
				})
			},
			want: Result{
				ContentDrift: []ContentDrift{{Path: "changed.ts", From: hashText("old\n")[:8], To: hashText("new\n")[:8]}},
				Removed:      []string{"removed.ts"},
				Coverage:     []string{"new.ts"},
			},
		},
		{
			name: "utf16le source uses decoded hash",
			setup: func(t *testing.T, root string) {
				t.Helper()
				writeUTF16LE(t, root, "utf16.ts", "export const value = 1;\n")
				writeManifest(t, root, graph.Manifest{
					Version: 1,
					Files:   []graph.SourceRef{{Path: "utf16.ts", Hash: hashText("export const value = 1;\n")}},
				})
			},
			options: Options{Extensions: []string{".ts"}},
			want:    Result{OK: true},
		},
		{
			name: "invalid utf8 uses Node replacement subparts",
			setup: func(t *testing.T, root string) {
				t.Helper()
				writeSourceBytes(t, root, "invalid.ts", []byte{0xe2, 0x82, 0xff})
				writeManifest(t, root, graph.Manifest{
					Version: 1,
					Files:   []graph.SourceRef{{Path: "invalid.ts", Hash: hashText("��")}},
				})
			},
			options: Options{Extensions: []string{".ts"}},
			want:    Result{OK: true},
		},
		{
			name: "skip directories unless explicitly included",
			setup: func(t *testing.T, root string) {
				t.Helper()
				writeSource(t, root, "main.ts", "main\n")
				writeSource(t, root, filepath.Join("build", "generated.ts"), "generated\n")
				writeSource(t, root, filepath.Join("vendor", "third-party.ts"), "vendor\n")
				writeManifest(t, root, graph.Manifest{
					Version: 1,
					Files: []graph.SourceRef{
						{Path: "main.ts", Hash: hashText("main\n")},
						{Path: "build/generated.ts", Hash: hashText("generated\n")},
					},
				})
			},
			options: Options{IncludeDirs: []string{"build"}},
			want:    Result{OK: true},
		},
		{
			name: "index drift",
			setup: func(t *testing.T, root string) {
				t.Helper()
				writeManifest(t, root, graph.Manifest{
					Version: 1,
					Nodes: []graph.ManifestNode{
						{Slug: "changed", SourcesDigest: "new-digest"},
						{Slug: "missing", SourcesDigest: "digest"},
					},
				})
				writeNode(t, root, "changed.md", "---\nslug: changed\nsources_digest: old-digest\n---\n")
				writeNode(t, root, "notes.md", "# hand-written notes\n")
			},
			want: Result{
				IndexDrift: []string{
					"changed: frontmatter digest ≠ manifest",
					"notes: node file not in manifest",
					"missing: in manifest but node file missing",
				},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			root := t.TempDir()
			tt.setup(t, root)
			got, err := Check(root, tt.options)
			if err != nil {
				t.Fatalf("Check(%q) error = %v, want nil", root, err)
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("Check(%q) = %#v, want %#v", root, got, tt.want)
			}
		})
	}
}

func writeSource(t *testing.T, root, name, content string) {
	t.Helper()
	path := filepath.Join(root, name)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func writeUTF16LE(t *testing.T, root, name, content string) {
	t.Helper()
	data := []byte{0xff, 0xfe}
	for _, r := range content {
		data = append(data, byte(r), byte(r>>8))
	}
	writeSourceBytes(t, root, name, data)
}

func writeSourceBytes(t *testing.T, root, name string, data []byte) {
	t.Helper()
	path := filepath.Join(root, name)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}
}

func writeManifest(t *testing.T, root string, manifest graph.Manifest) {
	t.Helper()
	data, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	writeSourceBytes(t, root, filepath.Join("graft", "manifest.json"), data)
}

func writeNode(t *testing.T, root, name, content string) {
	t.Helper()
	writeSource(t, filepath.Join(root, "graft"), name, content)
}

func hashText(text string) string {
	sum := sha256.Sum256([]byte(text))
	return hex.EncodeToString(sum[:])
}
