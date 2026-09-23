package graph

import (
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/NanoNets/context-graph-engine/internal/sourcefiles"
)

func TestFingerprintReadContract(t *testing.T) {
	tests := []struct {
		name      string
		extractor string
		data      string
		missing   bool
		want      bool
	}{
		{name: "missing sidecar", extractor: "v1", missing: true},
		{name: "malformed json", extractor: "v1", data: "{"},
		{name: "wrong version", extractor: "v1", data: `{"version":2,"extractor":"v1","files":{}}`},
		{name: "wrong extractor", extractor: "v1", data: `{"version":1,"extractor":"other","files":{}}`},
		{name: "null files", extractor: "v1", data: `{"version":1,"extractor":"v1","files":null}`},
		{name: "malformed file tuple", extractor: "v1", data: `{"version":1,"extractor":"v1","files":{"a.ts":[1,1]}}`},
		{name: "valid sidecar tuple", extractor: "v1", data: `{"version":1,"extractor":"v1","files":{"a.ts":[1,1,"hash"]}}`, want: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			outDir := t.TempDir()
			if !tt.missing {
				path, err := FingerprintPath(outDir, tt.extractor)
				if err != nil {
					t.Fatalf("FingerprintPath(%q, %q) error = %v", outDir, tt.extractor, err)
				}
				if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
					t.Fatalf("MkdirAll(%q) error = %v", filepath.Dir(path), err)
				}
				if err := os.WriteFile(path, []byte(tt.data), 0o644); err != nil {
					t.Fatalf("WriteFile(%q) error = %v", path, err)
				}
			}
			got, err := ReadFingerprint(outDir, tt.extractor)
			if err != nil {
				t.Fatalf("ReadFingerprint(%q, %q) error = %v, want nil", outDir, tt.extractor, err)
			}
			if (got != nil) != tt.want {
				t.Errorf("ReadFingerprint(%q, %q) = %#v, want present %t", outDir, tt.extractor, got, tt.want)
			}
		})
	}
}

func TestFingerprintWriteContract(t *testing.T) {
	outDir := t.TempDir()
	files := map[string]FingerprintFile{
		"src/app.ts": {Size: 7, MTimeMS: 1.5, Hash: "hash"},
	}
	if err := WriteFingerprint(outDir, "v1", files, []string{"src"}); err != nil {
		t.Fatalf("WriteFingerprint(%q, %q, files, [src]) error = %v, want nil", outDir, "v1", err)
	}
	path, err := FingerprintPath(outDir, "v1")
	if err != nil {
		t.Fatalf("FingerprintPath(%q, %q) error = %v, want nil", outDir, "v1", err)
	}
	gotBytes, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile(%q) error = %v", path, err)
	}
	wantJSON := `{"version":1,"extractor":"v1","files":{"src/app.ts":[7,1.5,"hash"]},"onlyDirs":["src"]}`
	if string(gotBytes) != wantJSON {
		t.Errorf("WriteFingerprint(%q) = %s, want %s", path, gotBytes, wantJSON)
	}
	got, err := ReadFingerprint(outDir, "v1")
	if err != nil {
		t.Fatalf("ReadFingerprint(%q, %q) error = %v, want nil", outDir, "v1", err)
	}
	if got == nil || got.Version != 1 || got.Extractor != "v1" || got.Files["src/app.ts"] != files["src/app.ts"] || !slices.Equal(got.OnlyDirs, []string{"src"}) {
		t.Errorf("ReadFingerprint(%q, %q) = %#v, want round-trip fingerprint", outDir, "v1", got)
	}
	emptyDir := filepath.Join(outDir, "empty")
	if err := WriteFingerprint(emptyDir, "v1", nil, nil); err != nil {
		t.Fatalf("WriteFingerprint(%q, %q, nil, nil) error = %v, want nil", emptyDir, "v1", err)
	}
	emptyPath, err := FingerprintPath(emptyDir, "v1")
	if err != nil {
		t.Fatalf("FingerprintPath(%q, %q) error = %v, want nil", emptyDir, "v1", err)
	}
	emptyBytes, err := os.ReadFile(emptyPath)
	if err != nil {
		t.Fatalf("ReadFile(%q) error = %v", emptyPath, err)
	}
	if got, want := string(emptyBytes), `{"version":1,"extractor":"v1","files":{}}`; got != want {
		t.Errorf("WriteFingerprint(%q, empty files) = %s, want %s", emptyPath, got, want)
	}
}

func TestFingerprintPathRejectsSeparators(t *testing.T) {
	for _, extractor := range []string{"../outside", `sub\\dir`} {
		if got, err := FingerprintPath(t.TempDir(), extractor); err == nil || got != "" {
			t.Errorf("FingerprintPath(outDir, %q) = (%q, %v), want empty path and error", extractor, got, err)
		}
	}
}

func TestProbeDriftContract(t *testing.T) {
	tests := []struct {
		name    string
		setup   func(t *testing.T, root, outDir string)
		env     string
		wantNil bool
		want    Drift
	}{
		{
			name:    "missing fingerprint is unknown",
			setup:   func(t *testing.T, root, outDir string) {},
			wantNil: true,
		},
		{
			name: "clean source",
			setup: func(t *testing.T, root, outDir string) {
				writeFingerprintContractFile(t, root, "src/a.ts", "same")
				writeFingerprintContract(t, root, outDir, nil)
			},
			want: Drift{Changed: []string{}, Added: []string{}, Removed: []string{}},
		},
		{
			name: "changed source",
			setup: func(t *testing.T, root, outDir string) {
				writeFingerprintContractFile(t, root, "src/a.ts", "old")
				writeFingerprintContract(t, root, outDir, nil)
				writeFingerprintContractFile(t, root, "src/a.ts", "new content")
			},
			want: Drift{Changed: []string{"src/a.ts"}, Added: []string{}, Removed: []string{}},
		},
		{
			name: "added and removed paths are sorted",
			setup: func(t *testing.T, root, outDir string) {
				writeFingerprintContractFile(t, root, "z.ts", "z")
				writeFingerprintContractFile(t, root, "a.ts", "a")
				writeFingerprintContract(t, root, outDir, nil)
				if err := os.Remove(filepath.Join(root, "a.ts")); err != nil {
					t.Fatalf("Remove(%q) error = %v", filepath.Join(root, "a.ts"), err)
				}
				writeFingerprintContractFile(t, root, "b.ts", "b")
			},
			want: Drift{Changed: []string{}, Added: []string{"b.ts"}, Removed: []string{"a.ts"}},
		},
		{
			name: "stored onlyDirs excludes out-of-scope source",
			setup: func(t *testing.T, root, outDir string) {
				writeFingerprintContractFile(t, root, "src/a.ts", "same")
				writeFingerprintContract(t, root, outDir, []string{"src"})
				writeFingerprintContractFile(t, root, "outside.ts", "new")
			},
			want: Drift{Changed: []string{}, Added: []string{}, Removed: []string{}},
		},
		{
			name: "stat fast path trusts matching size and mtime",
			setup: func(t *testing.T, root, outDir string) {
				writeSameStatFingerprintContract(t, root, outDir)
			},
			want: Drift{Changed: []string{}, Added: []string{}, Removed: []string{}},
		},
		{
			name: "hash mode detects same-size same-mtime changes",
			setup: func(t *testing.T, root, outDir string) {
				writeSameStatFingerprintContract(t, root, outDir)
			},
			env:  "hash",
			want: Drift{Changed: []string{"src/a.ts"}, Added: []string{}, Removed: []string{}},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv("GRAFT_REFRESH", tt.env)
			root := t.TempDir()
			outDir := filepath.Join(root, "graft")
			tt.setup(t, root, outDir)
			got, err := ProbeDrift(root, outDir, "v1", sourcefiles.Options{Extensions: []string{".ts"}})
			if err != nil {
				t.Fatalf("ProbeDrift(%q, %q, %q) error = %v, want nil", root, outDir, "v1", err)
			}
			if tt.wantNil {
				if got != nil {
					t.Errorf("ProbeDrift(%q, %q, %q) = %#v, want nil", root, outDir, "v1", got)
				}
				return
			}
			if got == nil || !slices.Equal(got.Changed, tt.want.Changed) ||
				!slices.Equal(got.Added, tt.want.Added) || !slices.Equal(got.Removed, tt.want.Removed) {
				t.Errorf("ProbeDrift(%q, %q, %q) = %#v, want %#v", root, outDir, "v1", got, tt.want)
			}
		})
	}
}

func writeFingerprintContractFile(t *testing.T, root, rel, content string) {
	t.Helper()
	path := filepath.Join(root, rel)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("MkdirAll(%q) error = %v", filepath.Dir(path), err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("WriteFile(%q) error = %v", path, err)
	}
}

func writeFingerprintContract(t *testing.T, root, outDir string, onlyDirs []string) {
	t.Helper()
	opts := sourcefiles.Options{OutDir: outDir, Extensions: []string{".ts"}, OnlyDirs: onlyDirs}
	files, err := sourcefiles.Walk(root, opts)
	if err != nil {
		t.Fatalf("Walk(%q, %#v) error = %v", root, opts, err)
	}
	prints := make(map[string]FingerprintFile, len(files))
	for _, file := range files {
		text, ok, err := sourcefiles.Read(file.Abs)
		if err != nil || !ok {
			t.Fatalf("Read(%q) = (%q, %t, %v), want readable source", file.Abs, text, ok, err)
		}
		prints[file.Rel] = FingerprintFile{Size: file.Size, MTimeMS: file.MTimeMS, Hash: sourcefiles.Hash(text)}
	}
	if err := WriteFingerprint(outDir, "v1", prints, onlyDirs); err != nil {
		t.Fatalf("WriteFingerprint(%q, %q, files, %v) error = %v", outDir, "v1", onlyDirs, err)
	}
}

func writeSameStatFingerprintContract(t *testing.T, root, outDir string) {
	t.Helper()
	path := filepath.Join(root, "src", "a.ts")
	writeFingerprintContractFile(t, root, "src/a.ts", "aaaa")
	fixed := time.Unix(1_700_000_000, 0)
	if err := os.Chtimes(path, fixed, fixed); err != nil {
		t.Fatalf("Chtimes(%q) error = %v", path, err)
	}
	writeFingerprintContract(t, root, outDir, nil)
	if err := os.WriteFile(path, []byte("bbbb"), 0o644); err != nil {
		t.Fatalf("WriteFile(%q) error = %v", path, err)
	}
	if err := os.Chtimes(path, fixed, fixed); err != nil {
		t.Fatalf("Chtimes(%q) error = %v", path, err)
	}
}
