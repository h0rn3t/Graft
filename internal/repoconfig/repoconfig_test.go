package repoconfig

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/h0rn3t/Graft/internal/jsonjs"
)

func writeFile(t *testing.T, path, content string, mode os.FileMode) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("MkdirAll(%q) error = %v, want nil", filepath.Dir(path), err)
	}
	if err := os.WriteFile(path, []byte(content), mode); err != nil {
		t.Fatalf("WriteFile(%q) error = %v, want nil", path, err)
	}
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile(%q) error = %v, want nil", path, err)
	}
	return string(data)
}

func TestReadDistinguishesMissingFromMalformed(t *testing.T) {
	tests := []struct {
		name      string
		content   *string
		wantNil   bool
		wantError bool
	}{
		{name: "missing", wantNil: true},
		{name: "valid", content: new(`{"followSubmodules":true}`)},
		{name: "trailing comma", content: new(`{"followSubmodules":true,}`), wantNil: true, wantError: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			root := t.TempDir()
			if tt.content != nil {
				writeFile(t, Path(root), *tt.content, 0o644)
			}
			got, err := Read(root)
			if (err != nil) != tt.wantError {
				t.Errorf("Read(%q) error = %v, want error = %t", root, err, tt.wantError)
			}
			if (got == nil) != tt.wantNil {
				t.Errorf("Read(%q) = %v, want nil = %t", root, got, tt.wantNil)
			}
		})
	}
}

func TestPatchRefusesMalformedConfig(t *testing.T) {
	root := t.TempDir()
	const broken = `{"includeDirs":["vendor"],"followSubmodules":true,}`
	writeFile(t, Path(root), broken, 0o644)
	if err := Patch(root, []Field{{Key: "followNestedRepos", Value: true}}); err == nil {
		t.Errorf("Patch(%q) over malformed JSON error = nil, want an error", root)
	}
	if got := readFile(t, Path(root)); got != broken {
		t.Errorf("Patch(%q) config = %q, want untouched %q", root, got, broken)
	}
}

func TestPatchKeepsModeAndIgnoresDir(t *testing.T) {
	root := t.TempDir()
	writeFile(t, Path(root), `{"includeDirs":["vendor"]}`, 0o600)
	writeFile(t, filepath.Join(root, ".gitignore"), "node_modules/\n", 0o644)
	if err := Patch(root, []Field{{Key: "followSubmodules", Value: true}}); err != nil {
		t.Fatalf("Patch(%q) error = %v, want nil", root, err)
	}
	const want = "{\n  \"includeDirs\": [\n    \"vendor\"\n  ],\n  \"followSubmodules\": true\n}"
	if got := readFile(t, Path(root)); got != want {
		t.Errorf("Patch(%q) config = %q, want %q", root, got, want)
	}
	if runtime.GOOS != "windows" {
		info, err := os.Stat(Path(root))
		if err != nil {
			t.Fatalf("Stat(%q) error = %v, want nil", Path(root), err)
		}
		if info.Mode().Perm() != 0o600 {
			t.Errorf("Patch(%q) mode = %o, want 600 kept", root, info.Mode().Perm())
		}
	}
	const wantIgnore = "node_modules/\n\n# graft's local repository settings — not committed.\n/.graft/\n"
	if got := readFile(t, filepath.Join(root, ".gitignore")); got != wantIgnore {
		t.Errorf("Patch(%q) .gitignore = %q, want %q", root, got, wantIgnore)
	}
}

// TestPatchLeavesUnreadableGitignore checks that a .gitignore that cannot be
// read is reported rather than treated as empty and overwritten.
func TestPatchLeavesUnreadableGitignore(t *testing.T) {
	root := t.TempDir()
	gitignore := filepath.Join(root, ".gitignore")
	if err := os.Mkdir(gitignore, 0o755); err != nil {
		t.Fatalf("Mkdir(%q) error = %v, want nil", gitignore, err)
	}
	if err := Patch(root, []Field{{Key: "followSubmodules", Value: jsonjs.Value(true)}}); err == nil {
		t.Errorf("Patch(%q) with unreadable .gitignore error = nil, want an error", root)
	}
	if info, err := os.Stat(gitignore); err != nil || !info.IsDir() {
		t.Errorf("Patch(%q) .gitignore = (%v, %v), want the directory untouched", root, info, err)
	}
	if _, err := os.Stat(Path(root)); !os.IsNotExist(err) {
		t.Errorf("Patch(%q) config stat error = %v, want not written", root, err)
	}
}
