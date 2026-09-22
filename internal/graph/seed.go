// Package graph builds and queries Graft's persisted symbol graph.
package graph

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// mainWorktreeRoot resolves a linked worktree's primary checkout from its Git metadata.
func mainWorktreeRoot(root string) (string, bool) {
	root, err := filepath.Abs(root)
	if err != nil {
		return "", false
	}
	dotGit := filepath.Join(root, ".git")
	info, err := os.Stat(dotGit)
	if err != nil || info.IsDir() {
		return "", false
	}
	data, err := os.ReadFile(dotGit)
	if err != nil {
		return "", false
	}
	for line := range strings.SplitSeq(string(data), "\n") {
		target, ok := strings.CutPrefix(line, "gitdir:")
		if !ok {
			continue
		}
		gitDir := strings.TrimSpace(target)
		if gitDir == "" {
			return "", false
		}
		if !filepath.IsAbs(gitDir) {
			gitDir = filepath.Join(root, gitDir)
		}
		gitDir = filepath.Clean(gitDir)
		if filepath.Base(filepath.Dir(gitDir)) != "worktrees" {
			return "", false
		}
		common, err := os.ReadFile(filepath.Join(gitDir, "commondir"))
		if err != nil {
			return "", false
		}
		commonDir := strings.TrimSpace(string(common))
		if commonDir == "" {
			return "", false
		}
		if !filepath.IsAbs(commonDir) {
			commonDir = filepath.Join(gitDir, commonDir)
		}
		commonDir = filepath.Clean(commonDir)
		if filepath.Base(commonDir) != ".git" {
			return "", false
		}
		info, err := os.Stat(commonDir)
		if err != nil || !info.IsDir() {
			return "", false
		}
		main := filepath.Dir(commonDir)
		if main == root {
			return "", false
		}
		return main, true
	}
	return "", false
}

// seedGraphFromWorktree copies the main checkout's graph into a linked worktree.
func seedGraphFromWorktree(root, outDir string) (string, bool, error) {
	value := os.Getenv("GRAFT_NO_SEED")
	if value != "" && value != "0" && value != "false" {
		return "", false, nil
	}
	cacheDir := filepath.Join(outDir, ".cache")
	locked, err := waitForGraphLock(cacheDir)
	if err != nil {
		return "", false, err
	}
	if !locked {
		return "", true, nil
	}
	lockPath := filepath.Join(cacheDir, ".sync.lock")
	defer func() { _ = os.Remove(lockPath) }() // Stale-lock recovery retries cleanup after five minutes.
	if _, err := os.Stat(WiringPath(outDir)); err == nil {
		return "", false, nil
	} else if !errors.Is(err, fs.ErrNotExist) {
		return "", false, fmt.Errorf("check worktree graph: %w", err)
	}
	main, ok := mainWorktreeRoot(root)
	if !ok {
		return "", false, nil
	}
	sourceDir := filepath.Join(main, "graft")
	sourceGraph := WiringPath(sourceDir)
	info, err := os.Lstat(sourceGraph)
	if errors.Is(err, fs.ErrNotExist) {
		return "", false, nil
	}
	if err != nil {
		return "", false, fmt.Errorf("stat main worktree graph: %w", err)
	}
	if !info.Mode().IsRegular() {
		return "", false, nil
	}
	sourceCache := filepath.Join(sourceDir, ".cache")
	entries, err := os.ReadDir(sourceCache)
	if errors.Is(err, fs.ErrNotExist) {
		entries = nil
	} else if err != nil {
		return "", false, fmt.Errorf("read main worktree graph cache: %w", err)
	}
	for _, entry := range entries {
		name := entry.Name()
		if name != "ask-index.json" && !strings.HasPrefix(name, "extract.") && !strings.HasPrefix(name, "fingerprint.") {
			continue
		}
		if !entry.Type().IsRegular() {
			continue
		}
		data, err := os.ReadFile(filepath.Join(sourceCache, name))
		if err != nil {
			return "", false, fmt.Errorf("read main worktree graph cache %q: %w", name, err)
		}
		if err := writeAtomicSidecar(filepath.Join(cacheDir, name), data); err != nil {
			return "", false, fmt.Errorf("copy main worktree graph cache %q: %w", name, err)
		}
	}
	data, err := os.ReadFile(sourceGraph)
	if err != nil {
		return "", false, fmt.Errorf("read main worktree graph: %w", err)
	}
	if err := writeAtomicSidecar(WiringPath(outDir), data); err != nil {
		return "", false, fmt.Errorf("install main worktree graph: %w", err)
	}
	return main, false, nil
}
