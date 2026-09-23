package sourcefiles

import (
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func TestReadContract(t *testing.T) {
	tests := []struct {
		name    string
		data    []byte
		missing bool
		want    string
		wantOK  bool
		wantErr bool
	}{
		{name: "empty input", data: nil, wantOK: true},
		{name: "utf8", data: []byte("café"), want: "café", wantOK: true},
		{name: "invalid utf8", data: []byte{0xff, 'a'}, want: "�a", wantOK: true},
		{name: "utf8 invalid continuation subparts", data: []byte{0xe2, 0x82, 0xff}, want: "��", wantOK: true},
		{name: "utf8 overlong sequence", data: []byte{0xe0, 0x80, 0x80}, want: "���", wantOK: true},
		{name: "utf8 surrogate sequence", data: []byte{0xed, 0xa0, 0x80}, want: "���", wantOK: true},
		{name: "utf8 out of range sequence", data: []byte{0xf4, 0x90, 0x80, 0x80}, want: "����", wantOK: true},
		{name: "utf16le", data: []byte{0xff, 0xfe, 'h', 0, 'i', 0}, want: "hi", wantOK: true},
		{name: "utf16le odd byte", data: []byte{0xff, 0xfe, 'h', 0, 0xff}, want: "h", wantOK: true},
		{name: "utf16be unsupported", data: []byte{0xfe, 0xff, 0, 'h'}, wantOK: false},
		{name: "missing file", missing: true, wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "source.ts")
			if !tt.missing {
				if err := os.WriteFile(path, tt.data, 0o644); err != nil {
					t.Fatalf("WriteFile(%q) error = %v", path, err)
				}
			}
			got, gotOK, err := Read(path)
			if (err != nil) != tt.wantErr {
				t.Errorf("Read(%q) error = %v, want error presence %t", path, err, tt.wantErr)
			}
			if got != tt.want || gotOK != tt.wantOK {
				t.Errorf("Read(%q) = (%q, %t), want (%q, %t)", path, got, gotOK, tt.want, tt.wantOK)
			}
		})
	}
}

func TestWalkContract(t *testing.T) {
	setupTree := func(t *testing.T, root string) {
		t.Helper()
		writeSourcefilesTestFile(t, root, "src/a.ts", "a")
		writeSourcefilesTestFile(t, root, "src/a/c.ts", "c")
		writeSourcefilesTestFile(t, root, "src/ab/b.ts", "b")
		writeSourcefilesTestFile(t, root, "vendor/lib/c.ts", "c")
		writeSourcefilesTestFile(t, root, "graft/cache.ts", "cache")
		writeSourcefilesTestFile(t, root, ".hidden/d.ts", "d")
		writeSourcefilesTestFile(t, root, "src/readme.md", "text")
	}
	tests := []struct {
		name    string
		setup   func(t *testing.T, root string)
		options Options
		want    []string
	}{
		{
			name:  "extension and output directory filters",
			setup: setupTree,
			options: Options{
				OutDir:     "graft",
				Extensions: []string{"TS"},
			},
			// Depth-first in directory order, as the TypeScript filesystem walk visits.
			want: []string{"src/a/c.ts", "src/a.ts", "src/ab/b.ts"},
		},
		{
			name:  "only-dir prefix is segment-aware",
			setup: setupTree,
			options: Options{
				Extensions: []string{".ts"},
				OnlyDirs:   []string{"src/a"},
			},
			want: []string{"src/a/c.ts"},
		},
		{
			name:  "included directory overrides default skip",
			setup: setupTree,
			options: Options{
				Extensions:  []string{".ts"},
				IncludeDirs: []string{"vendor"},
				OnlyDirs:    []string{"vendor/lib"},
			},
			want: []string{"vendor/lib/c.ts"},
		},
		{
			name: "persisted include directory applies without an option",
			setup: func(t *testing.T, root string) {
				setupTree(t, root)
				writeSourcefilesTestFile(t, root, ".graft/config.json", `{"includeDirs":["vendor"]}`)
			},
			options: Options{Extensions: []string{".ts"}, OnlyDirs: []string{"vendor/lib"}},
			want:    []string{"vendor/lib/c.ts"},
		},
		{
			name:  "file size limit",
			setup: setupTree,
			options: Options{
				Extensions:   []string{".ts"},
				MaxFileBytes: 1,
				OnlyDirs:     []string{"src"},
			},
			// Depth-first in directory order, as the TypeScript filesystem walk visits.
			want: []string{"src/a/c.ts", "src/a.ts", "src/ab/b.ts"},
		},
		{
			name: "tracked untracked and ignored git files",
			setup: func(t *testing.T, root string) {
				t.Helper()
				initSourcefilesTestRepo(t, root)
				writeSourcefilesTestFile(t, root, "tracked.ts", "tracked")
				runSourcefilesTestGit(t, "-C", root, "add", "tracked.ts")
				runSourcefilesTestGit(t, "-C", root, "commit", "-m", "tracked")
				writeSourcefilesTestFile(t, root, "loose.ts", "loose")
				writeSourcefilesTestFile(t, root, ".gitignore", "ignored.ts\n")
				writeSourcefilesTestFile(t, root, "ignored.ts", "ignored")
			},
			options: Options{Extensions: []string{".ts"}},
			want:    []string{"loose.ts", "tracked.ts"},
		},
		{
			name: "nested repository excluded by default",
			setup: func(t *testing.T, root string) {
				t.Helper()
				initSourcefilesTestRepo(t, root)
				nested := filepath.Join(root, "nested")
				initSourcefilesTestRepo(t, nested)
				writeSourcefilesTestFile(t, nested, "child.ts", "child")
				runSourcefilesTestGit(t, "-C", nested, "add", "child.ts")
				runSourcefilesTestGit(t, "-C", nested, "commit", "-m", "child")
				writeSourcefilesTestFile(t, root, "main.ts", "main")
			},
			options: Options{Extensions: []string{".ts"}},
			want:    []string{"main.ts"},
		},
		{
			name: "nested repository opt in",
			setup: func(t *testing.T, root string) {
				t.Helper()
				initSourcefilesTestRepo(t, root)
				nested := filepath.Join(root, "nested")
				initSourcefilesTestRepo(t, nested)
				writeSourcefilesTestFile(t, nested, "child.ts", "child")
				runSourcefilesTestGit(t, "-C", nested, "add", "child.ts")
				runSourcefilesTestGit(t, "-C", nested, "commit", "-m", "child")
				writeSourcefilesTestFile(t, root, "main.ts", "main")
			},
			options: Options{Extensions: []string{".ts"}, FollowNestedRepos: true},
			want:    []string{"main.ts", "nested/child.ts"},
		},
		{
			name: "persisted nested repository choice applies without an option",
			setup: func(t *testing.T, root string) {
				initSourcefilesTestRepo(t, root)
				nested := filepath.Join(root, "nested")
				initSourcefilesTestRepo(t, nested)
				writeSourcefilesTestFile(t, nested, "child.ts", "child")
				runSourcefilesTestGit(t, "-C", nested, "add", "child.ts")
				runSourcefilesTestGit(t, "-C", nested, "commit", "-m", "child")
				writeSourcefilesTestFile(t, root, "main.ts", "main")
				writeSourcefilesTestFile(t, root, ".graft/config.json", `{"followNestedRepos":true}`)
			},
			options: Options{Extensions: []string{".ts"}},
			want:    []string{"main.ts", "nested/child.ts"},
		},
		{
			name: "submodule excluded by default",
			setup: func(t *testing.T, root string) {
				t.Helper()
				source := filepath.Join(filepath.Dir(root), "submodule-source")
				initSourcefilesTestRepo(t, source)
				writeSourcefilesTestFile(t, source, "child.ts", "child")
				runSourcefilesTestGit(t, "-C", source, "add", "child.ts")
				runSourcefilesTestGit(t, "-C", source, "commit", "-m", "child")
				initSourcefilesTestRepo(t, root)
				runSourcefilesTestGit(t, "-c", "protocol.file.allow=always", "-C", root, "submodule", "add", source, "sub")
				runSourcefilesTestGit(t, "-C", root, "commit", "-am", "add submodule")
			},
			options: Options{Extensions: []string{".ts"}},
			want:    []string{},
		},
		{
			name: "submodule opt in",
			setup: func(t *testing.T, root string) {
				t.Helper()
				source := filepath.Join(filepath.Dir(root), "submodule-source")
				initSourcefilesTestRepo(t, source)
				writeSourcefilesTestFile(t, source, "child.ts", "child")
				runSourcefilesTestGit(t, "-C", source, "add", "child.ts")
				runSourcefilesTestGit(t, "-C", source, "commit", "-m", "child")
				initSourcefilesTestRepo(t, root)
				runSourcefilesTestGit(t, "-c", "protocol.file.allow=always", "-C", root, "submodule", "add", source, "sub")
				runSourcefilesTestGit(t, "-C", root, "commit", "-am", "add submodule")
			},
			options: Options{Extensions: []string{".ts"}, FollowSubmodules: true},
			want:    []string{"sub/child.ts"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			root := t.TempDir()
			tt.setup(t, root)
			files, err := Walk(root, tt.options)
			if err != nil {
				t.Fatalf("Walk(%q, %#v) error = %v, want nil", root, tt.options, err)
			}
			got := make([]string, len(files))
			for index, file := range files {
				got[index] = file.Rel
			}
			if !slices.Equal(got, tt.want) {
				t.Errorf("Walk(%q, %#v) = %v, want %v", root, tt.options, got, tt.want)
			}
		})
	}
}

func writeSourcefilesTestFile(t *testing.T, root, rel, content string) {
	t.Helper()
	path := filepath.Join(root, rel)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("MkdirAll(%q) error = %v", filepath.Dir(path), err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("WriteFile(%q) error = %v", path, err)
	}
}

func initSourcefilesTestRepo(t *testing.T, root string) {
	t.Helper()
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatalf("MkdirAll(%q) error = %v", root, err)
	}
	runSourcefilesTestGit(t, "-C", root, "init", "-q")
	runSourcefilesTestGit(t, "-C", root, "config", "user.name", "Sourcefiles Test")
	runSourcefilesTestGit(t, "-C", root, "config", "user.email", "sourcefiles@example.test")
}

func runSourcefilesTestGit(t *testing.T, args ...string) {
	t.Helper()
	output, err := exec.Command("git", args...).CombinedOutput()
	if err != nil {
		t.Fatalf("git %s error = %v, output = %s", strings.Join(args, " "), err, output)
	}
}
