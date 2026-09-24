package sourcefiles

import (
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"testing"
)

// TestWalkSkipsUnreadableDirectory checks that one directory the walk cannot
// read costs only its own files, not the whole listing.
func TestWalkSkipsUnreadableDirectory(t *testing.T) {
	if runtime.GOOS == "windows" || os.Geteuid() == 0 {
		t.Skip("directory permissions do not deny reads here")
	}
	root := t.TempDir()
	writeSourcefilesTestFile(t, root, "src/a.ts", "a")
	writeSourcefilesTestFile(t, root, "locked/b.ts", "b")
	locked := filepath.Join(root, "locked")
	if err := os.Chmod(locked, 0); err != nil {
		t.Fatalf("Chmod(%q) error = %v, want nil", locked, err)
	}
	t.Cleanup(func() { _ = os.Chmod(locked, 0o755) }) // let t.TempDir remove it
	files, err := Walk(root, Options{Extensions: []string{".ts"}})
	if err != nil {
		t.Fatalf("Walk(%q) error = %v, want nil", root, err)
	}
	got := make([]string, len(files))
	for index, file := range files {
		got[index] = file.Rel
	}
	if want := []string{"src/a.ts"}; !slices.Equal(got, want) {
		t.Errorf("Walk(%q) = %v, want %v", root, got, want)
	}
}

// TestWalkReportsGitFailureInsideARepository checks that a work tree git
// cannot list is an error, not a silent walk that ignores .gitignore.
func TestWalkReportsGitFailureInsideARepository(t *testing.T) {
	root := t.TempDir()
	initSourcefilesTestRepo(t, root)
	writeSourcefilesTestFile(t, root, ".git/index", "not an index")
	writeSourcefilesTestFile(t, root, ".gitignore", "ignored.ts\n")
	writeSourcefilesTestFile(t, root, "ignored.ts", "ignored")
	for _, options := range []Options{
		{Extensions: []string{".ts"}},
		{Extensions: []string{".ts"}, FollowNestedRepos: true},
	} {
		if files, err := Walk(root, options); err == nil {
			t.Errorf("Walk(%q, %#v) = (%d files, nil), want a git error", root, options, len(files))
		}
	}
}
