package fsutil

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestWriteFileAtomic(t *testing.T) {
	t.Run("creates the file and its parents with perm", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "a", "b", "config.json")
		if err := WriteFileAtomic(path, []byte("new"), 0o644); err != nil {
			t.Fatalf("WriteFileAtomic(%q) error = %v, want nil", path, err)
		}
		assertFile(t, path, "new", 0o644)
		assertNoTemporaries(t, filepath.Dir(path))
	})
	t.Run("keeps the mode of an existing file", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "secret.json")
		writeFile(t, path, "old", 0o600)
		if err := WriteFileAtomic(path, []byte("new"), 0o644); err != nil {
			t.Fatalf("WriteFileAtomic(%q) error = %v, want nil", path, err)
		}
		assertFile(t, path, "new", 0o600)
	})
	t.Run("writes through a symlink to its target", func(t *testing.T) {
		dir := t.TempDir()
		target := filepath.Join(dir, "dotfiles", "settings.json")
		writeFile(t, target, "old", 0o644)
		link := filepath.Join(dir, "settings.json")
		if err := os.Symlink(target, link); err != nil {
			t.Skipf("os.Symlink unavailable: %v", err)
		}
		if err := WriteFileAtomic(link, []byte("new"), 0o644); err != nil {
			t.Fatalf("WriteFileAtomic(%q) error = %v, want nil", link, err)
		}
		assertFile(t, target, "new", 0o644)
		if info, err := os.Lstat(link); err != nil || info.Mode()&os.ModeSymlink == 0 {
			t.Errorf("os.Lstat(%q) = %v, %v, want a symlink that still exists", link, info, err)
		}
	})
}

func TestWriteFileAtomicIn(t *testing.T) {
	t.Run("refuses a directory symlink that leaves the root", func(t *testing.T) {
		repo, outside := t.TempDir(), t.TempDir()
		if err := os.Symlink(outside, filepath.Join(repo, "helpers")); err != nil {
			t.Skipf("os.Symlink unavailable: %v", err)
		}
		root := openRoot(t, repo)
		if err := WriteFileAtomicIn(root, filepath.Join("helpers", "hook.cjs"), []byte("x"), 0o755); err == nil {
			t.Errorf("WriteFileAtomicIn(helpers/hook.cjs) error = nil, want an escape error")
		}
		if _, err := os.Stat(filepath.Join(outside, "hook.cjs")); !errors.Is(err, os.ErrNotExist) {
			t.Errorf("os.Stat(outside/hook.cjs) error = %v, want not exist", err)
		}
	})
	t.Run("replaces a file symlink instead of writing through it", func(t *testing.T) {
		repo, outside := t.TempDir(), t.TempDir()
		victim := filepath.Join(outside, "victim")
		writeFile(t, victim, "precious", 0o600)
		if err := os.Symlink(victim, filepath.Join(repo, "hook.cjs")); err != nil {
			t.Skipf("os.Symlink unavailable: %v", err)
		}
		root := openRoot(t, repo)
		if err := WriteFileAtomicIn(root, "hook.cjs", []byte("shim"), 0o755); err != nil {
			t.Fatalf("WriteFileAtomicIn(hook.cjs) error = %v, want nil", err)
		}
		assertFile(t, victim, "precious", 0o600)
		assertFile(t, filepath.Join(repo, "hook.cjs"), "shim", 0o755)
	})
}

func TestLock(t *testing.T) {
	path := filepath.Join(t.TempDir(), "cache", ".sync.lock")
	release, err := Lock(t.Context(), path)
	if err != nil {
		t.Fatalf("Lock(%q) error = %v, want nil", path, err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 100*time.Millisecond)
	defer cancel()
	if _, err := Lock(ctx, path); !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("Lock(%q) while held error = %v, want %v", path, err, context.DeadlineExceeded)
	}
	release()
	again, err := Lock(t.Context(), path)
	if err != nil {
		t.Fatalf("Lock(%q) after release error = %v, want nil", path, err)
	}
	again()
}

func writeFile(t *testing.T, path, content string, perm os.FileMode) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		t.Fatalf("os.MkdirAll(%q) error = %v", filepath.Dir(path), err)
	}
	if err := os.WriteFile(path, []byte(content), perm); err != nil {
		t.Fatalf("os.WriteFile(%q) error = %v", path, err)
	}
	if err := os.Chmod(path, perm); err != nil {
		t.Fatalf("os.Chmod(%q) error = %v", path, err)
	}
}

func openRoot(t *testing.T, dir string) *os.Root {
	t.Helper()
	root, err := os.OpenRoot(dir)
	if err != nil {
		t.Fatalf("os.OpenRoot(%q) error = %v", dir, err)
	}
	t.Cleanup(func() { _ = root.Close() })
	return root
}

func assertFile(t *testing.T, path, want string, wantPerm os.FileMode) {
	t.Helper()
	data, err := os.ReadFile(path) //nolint:gosec // G304: the test reads its own temp files
	if err != nil {
		t.Fatalf("os.ReadFile(%q) error = %v", path, err)
	}
	if string(data) != want {
		t.Errorf("os.ReadFile(%q) = %q, want %q", path, data, want)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("os.Stat(%q) error = %v", path, err)
	}
	if got := info.Mode().Perm(); got != wantPerm && os.PathSeparator == '/' {
		t.Errorf("os.Stat(%q).Mode().Perm() = %v, want %v", path, got, wantPerm)
	}
}

func assertNoTemporaries(t *testing.T, dir string) {
	t.Helper()
	matches, err := filepath.Glob(filepath.Join(dir, "*.tmp"))
	if err != nil || len(matches) > 0 {
		t.Errorf("filepath.Glob(%q) = %v, %v, want no temporary files", dir, matches, err)
	}
}
