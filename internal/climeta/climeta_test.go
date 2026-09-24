package climeta

import (
	"os"
	"path/filepath"
	"testing"
)

func TestPackageMetadata(t *testing.T) {
	repository := t.TempDir()
	binDir := filepath.Join(repository, "bin")
	if err := os.Mkdir(binDir, 0o755); err != nil {
		t.Fatal(err)
	}
	packageJSON := filepath.Join(repository, "package.json")
	if err := os.WriteFile(packageJSON, []byte(`{"version":"0.19.0"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	// A plain path, never a file URL: on Windows the URL round trip turned
	// C:\x\graft.exe into \C:\x\graft.exe.
	executable := filepath.Join(binDir, "graft.exe")

	if got, want := ResolvePackageJSONPath(executable), packageJSON; got != want {
		t.Errorf("ResolvePackageJSONPath(%q) = %q, want %q", executable, got, want)
	}
	got, err := ReadCurrentVersion(executable)
	if err != nil {
		t.Fatalf("ReadCurrentVersion(%q) error = %v", executable, err)
	}
	if got != "0.19.0" {
		t.Errorf("ReadCurrentVersion(%q) = %q, want %q", executable, got, "0.19.0")
	}
}
