package climeta

import (
	"net/url"
	"os"
	"path/filepath"
	"testing"
)

func TestPackageMetadata(t *testing.T) {
	repository := t.TempDir()
	sourceDir := filepath.Join(repository, "src")
	if err := os.Mkdir(sourceDir, 0o755); err != nil {
		t.Fatal(err)
	}
	packageJSON := filepath.Join(repository, "package.json")
	if err := os.WriteFile(packageJSON, []byte(`{"version":"0.19.0"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	moduleURL := (&url.URL{Scheme: "file", Path: filepath.ToSlash(filepath.Join(sourceDir, "cli.ts"))}).String()

	if got, want := ResolvePackageJSONPath(moduleURL), packageJSON; got != want {
		t.Errorf("ResolvePackageJSONPath(%q) = %q, want %q", moduleURL, got, want)
	}
	got, err := ReadCurrentVersion(moduleURL)
	if err != nil {
		t.Fatalf("ReadCurrentVersion(%q) error = %v", moduleURL, err)
	}
	if got != "0.19.0" {
		t.Errorf("ReadCurrentVersion(%q) = %q, want %q", moduleURL, got, "0.19.0")
	}
}
