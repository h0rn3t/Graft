package hosts

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/h0rn3t/Graft/internal/fsutil"
)

// errOutsideRepo refuses a write through a symlink that leaves the repository.
var errOutsideRepo = errors.New("symlink points outside the repository")

// files performs graft's reads and writes for one repository. A path inside
// the repository goes through an os.Root opened on it, so a symlink the
// repository commits cannot redirect a write, chmod, or removal outside it.
// Any other path is a machine-wide target under home, replaced atomically in
// place so a user's dotfile symlink keeps pointing at its target.
type files struct {
	repo    string
	home    string
	root    *os.Root
	rootErr error
}

func openFiles(repo, home string) *files {
	if abs, err := filepath.Abs(repo); err == nil {
		repo = abs
	}
	root, err := os.OpenRoot(repo)
	return &files{repo: repo, home: home, root: root, rootErr: err}
}

func (f *files) close() {
	if f.root != nil {
		_ = f.root.Close() // read-only handle on the directory; nothing to flush
	}
}

// inRepo returns path relative to the repository when it lies inside it.
func (f *files) inRepo(path string) (string, bool) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", false
	}
	rel, err := filepath.Rel(f.repo, abs)
	if err != nil || !filepath.IsLocal(rel) {
		return "", false
	}
	return rel, true
}

// repoName resolves path inside the repository, reporting ok=false for a
// machine-wide path.
func (f *files) repoName(path string) (string, bool, error) {
	rel, ok := f.inRepo(path)
	if !ok {
		return "", false, nil
	}
	if f.rootErr != nil {
		return "", true, f.rootErr
	}
	return rel, true, nil
}

// resolve follows a symlink at name while it stays inside the repository, so
// a committed AGENTS.md -> docs/AGENTS.md link keeps its layout; a link that
// leaves the repository is refused.
func (f *files) resolve(name string) (string, error) {
	for range 16 {
		info, err := f.root.Lstat(name)
		if err != nil || info.Mode()&fs.ModeSymlink == 0 {
			return name, nil // a missing or regular file is written in place
		}
		target, err := f.root.Readlink(name)
		if err != nil {
			return "", err
		}
		if filepath.IsAbs(target) {
			rel, ok := f.inRepo(target)
			if !ok {
				return "", fmt.Errorf("%s: %w", filepath.Join(f.repo, name), errOutsideRepo)
			}
			name = rel
			continue
		}
		name = filepath.Join(filepath.Dir(name), target)
		if !filepath.IsLocal(name) {
			return "", fmt.Errorf("%s: %w", filepath.Join(f.repo, name), errOutsideRepo)
		}
	}
	return "", fmt.Errorf("%s: too many levels of symbolic links", filepath.Join(f.repo, name))
}

func (f *files) readFile(path string) ([]byte, error) {
	name, ok, err := f.repoName(path)
	if err != nil {
		return nil, err
	}
	if !ok {
		return os.ReadFile(path) //nolint:gosec // G304: graft's own machine-wide config paths
	}
	if name, err = f.resolve(name); err != nil {
		return nil, err
	}
	return f.root.ReadFile(name)
}

// writeFile atomically replaces path with data; a new file gets perm and an
// existing one keeps its mode.
func (f *files) writeFile(path string, data []byte, perm fs.FileMode) error {
	name, ok, err := f.repoName(path)
	if err != nil {
		return err
	}
	if !ok {
		return fsutil.WriteFileAtomic(path, data, perm)
	}
	if name, err = f.resolve(name); err != nil {
		return err
	}
	return fsutil.WriteFileAtomicIn(f.root, name, data, perm)
}

func (f *files) stat(path string) (fs.FileInfo, error) {
	name, ok, err := f.repoName(path)
	if err != nil {
		return nil, err
	}
	if !ok {
		return os.Stat(path)
	}
	if name, err = f.resolve(name); err != nil {
		return nil, err
	}
	return f.root.Stat(name)
}

func (f *files) lstat(path string) (fs.FileInfo, error) {
	name, ok, err := f.repoName(path)
	if err != nil {
		return nil, err
	}
	if !ok {
		return os.Lstat(path)
	}
	return f.root.Lstat(name)
}

func (f *files) chmod(path string, mode fs.FileMode) error {
	name, ok, err := f.repoName(path)
	if err != nil {
		return err
	}
	if !ok {
		return os.Chmod(path, mode)
	}
	if name, err = f.resolve(name); err != nil {
		return err
	}
	return f.root.Chmod(name, mode)
}

// remove deletes path itself; a symlink is removed, not its target.
func (f *files) remove(path string) error {
	name, ok, err := f.repoName(path)
	if err != nil {
		return err
	}
	if !ok {
		return os.Remove(path)
	}
	return f.root.Remove(name)
}

func (f *files) removeAll(path string) error {
	name, ok, err := f.repoName(path)
	if err != nil {
		return err
	}
	if !ok {
		return os.RemoveAll(path)
	}
	return f.root.RemoveAll(name)
}

// pruneEmptyDirs removes dir and its empty ancestors after a removal left
// them empty. It stops below the repository root for a repository path, and
// below the host's own directory under home (~/.codex, ~/.claude) or home
// itself for a machine-wide one, so neither is ever removed.
func (f *files) pruneEmptyDirs(dir string) {
	if name, ok := f.inRepo(dir); ok {
		f.pruneRepoDirs(name)
		return
	}
	rel, err := filepath.Rel(f.home, dir)
	if err != nil || !filepath.IsLocal(rel) {
		return
	}
	first, _, _ := strings.Cut(filepath.ToSlash(rel), "/")
	stop := filepath.Join(f.home, filepath.FromSlash(first))
	for {
		below, err := filepath.Rel(stop, dir)
		if err != nil || below == "." || !filepath.IsLocal(below) {
			return
		}
		info, err := os.Lstat(dir)
		if err != nil || !info.IsDir() || os.Remove(dir) != nil {
			return
		}
		dir = filepath.Dir(dir)
	}
}

func (f *files) pruneRepoDirs(name string) {
	if f.root == nil {
		return
	}
	for name != "." {
		info, err := f.root.Lstat(name)
		if err != nil || !info.IsDir() || f.root.Remove(name) != nil {
			return
		}
		name = filepath.Dir(name)
	}
}
