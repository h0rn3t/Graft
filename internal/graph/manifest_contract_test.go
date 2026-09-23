package graph

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestWriteManifestContract(t *testing.T) {
	tests := []struct {
		name     string
		manifest Manifest
		want     string
	}{
		{
			name:     "empty rosters are arrays",
			manifest: Manifest{Version: ManifestVersion, Model: "test", RepoDigest: "empty"},
			want:     "{\n  \"version\": 1,\n  \"model\": \"test\",\n  \"repoDigest\": \"empty\",\n  \"files\": [],\n  \"nodes\": []\n}\n",
		},
		{
			name: "source and node roster",
			manifest: Manifest{
				Version: ManifestVersion, Model: "test", RepoDigest: "digest",
				Files: []SourceRef{{Path: "src/a.ts", Hash: "abc"}},
				Nodes: []ManifestNode{{Slug: "a", Name: "A", Type: "service", Sources: []string{"src/a.ts"}, SourcesDigest: "abc"}},
			},
			want: "{\n  \"version\": 1,\n  \"model\": \"test\",\n  \"repoDigest\": \"digest\",\n  \"files\": [\n    {\n      \"path\": \"src/a.ts\",\n      \"hash\": \"abc\"\n    }\n  ],\n  \"nodes\": [\n    {\n      \"slug\": \"a\",\n      \"name\": \"A\",\n      \"type\": \"service\",\n      \"sources\": [\n        \"src/a.ts\"\n      ],\n      \"sourcesDigest\": \"abc\"\n    }\n  ]\n}\n",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			outDir := t.TempDir()
			if err := WriteManifest(outDir, tt.manifest); err != nil {
				t.Fatalf("WriteManifest(%q, manifest) error = %v, want nil", outDir, err)
			}
			got, err := os.ReadFile(filepath.Join(outDir, "manifest.json"))
			if err != nil {
				t.Fatal(err)
			}
			if string(got) != tt.want {
				t.Errorf("WriteManifest(%q, manifest) = %q, want %q", outDir, got, tt.want)
			}
		})
	}
}

func TestWriteManifestFailureLeavesExistingTarget(t *testing.T) {
	outDir := t.TempDir()
	path := filepath.Join(outDir, "manifest.json")
	if err := os.Mkdir(path, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(path, "sentinel"), []byte("keep"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := WriteManifest(outDir, Manifest{Version: ManifestVersion}); err == nil {
		t.Errorf("WriteManifest(%q, manifest) error = nil, want error", outDir)
	}
	got, err := os.ReadFile(filepath.Join(path, "sentinel"))
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "keep" {
		t.Errorf("WriteManifest(%q, manifest) sentinel = %q, want keep", outDir, got)
	}
	entries, err := os.ReadDir(outDir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || !strings.HasPrefix(entries[0].Name(), "manifest.json") {
		t.Errorf("WriteManifest(%q, manifest) entries = %v, want only manifest.json", outDir, entries)
	}
}
