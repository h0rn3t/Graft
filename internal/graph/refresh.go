package graph

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/NanoNets/context-graph-engine/internal/sourcefiles"
)

// RefreshOptions configures a fail-soft graph refresh.
type RefreshOptions struct {
	// Source controls visibility and extensions; an empty Extensions slice uses
	// BuildGraph's known source set. Its OutDir defaults to <root>/graft, and
	// relative paths are resolved under root.
	Source sourcefiles.Options
	// Disabled skips the refresh probe and all filesystem writes.
	Disabled bool
}

// RefreshResult reports whether this call refreshed the graph and what drift it saw.
type RefreshResult struct {
	// Refreshed is true after the wiring graph was atomically replaced.
	Refreshed bool
	// Drift records the source changes that prompted the refresh, when known.
	Drift *Drift
	// Note carries a fail-soft explanation or native extractor limitations.
	Note string
}

// EnsureFreshGraph refreshes an existing graph when supported source files drift.
// It seeds linked worktrees from their main checkout, shares .sync.lock with graph
// writers, and leaves the current graph in place when the native extractor cannot
// represent the tree. Refresh failures are returned in Note.
func EnsureFreshGraph(root string, options RefreshOptions) RefreshResult {
	value := os.Getenv("GRAFT_NO_REFRESH")
	if options.Disabled || (value != "" && value != "0" && value != "false") {
		return RefreshResult{}
	}
	root, err := filepath.Abs(root)
	if err != nil {
		return RefreshResult{Note: fmt.Sprintf("graph refresh skipped: %v", err)}
	}
	root = filepath.Clean(root)
	defaultOutDir := options.Source.OutDir == ""
	outDir := options.Source.OutDir
	if outDir == "" {
		outDir = filepath.Join(root, "graft")
	} else if !filepath.IsAbs(outDir) {
		outDir = filepath.Join(root, outDir)
	}
	options.Source.OutDir = outDir
	if len(options.Source.Extensions) == 0 {
		options.Source.Extensions = strings.Fields(defaultSourceExtensions)
	}
	seedNote := ""
	if _, err := os.Stat(WiringPath(outDir)); errors.Is(err, fs.ErrNotExist) {
		if !defaultOutDir {
			return RefreshResult{}
		}
		main, busy, err := seedGraphFromWorktree(root, outDir)
		if err != nil {
			return RefreshResult{Note: fmt.Sprintf("graph refresh skipped: %v", err)}
		}
		if busy {
			return RefreshResult{Note: "another process is still copying the graph into this worktree — retry the query in a moment"}
		}
		if main != "" {
			seedNote = "copied the graph from the main checkout (" + main + ")"
		}
		if _, err := os.Stat(WiringPath(outDir)); errors.Is(err, fs.ErrNotExist) {
			return RefreshResult{}
		} else if err != nil {
			return RefreshResult{Note: fmt.Sprintf("graph refresh skipped: %v", err)}
		}
	} else if err != nil {
		return RefreshResult{Note: fmt.Sprintf("graph refresh skipped: %v", err)}
	}
	fingerprint, err := ReadFingerprint(outDir, graphExtractorID)
	if err != nil {
		return RefreshResult{Note: fmt.Sprintf("graph refresh skipped: %v", err)}
	}
	drift, err := probeDrift(root, outDir, options.Source, fingerprint)
	if err != nil {
		return RefreshResult{Note: fmt.Sprintf("graph refresh skipped: %v", err)}
	}
	unsupported := unsupportedFingerprintFiles(fingerprint, drift)
	if fingerprint == nil {
		files, err := sourcefiles.Walk(root, options.Source)
		if err != nil {
			return RefreshResult{Note: fmt.Sprintf("graph refresh skipped: %v", err)}
		}
		for _, file := range files {
			if _, _, supported := sourceGrammar(file.Rel); !supported {
				unsupported = append(unsupported, file.Rel)
			}
		}
		slices.Sort(unsupported)
	}
	if len(unsupported) > 0 {
		return RefreshResult{Drift: drift, Note: unsupportedRefreshNote(unsupported)}
	}
	if drift != nil && len(drift.Changed)+len(drift.Added)+len(drift.Removed) == 0 {
		return RefreshResult{Note: seedNote}
	}

	cacheDir := filepath.Join(outDir, ".cache")
	locked, err := waitForGraphLock(cacheDir)
	if err != nil {
		return RefreshResult{Drift: drift, Note: fmt.Sprintf("graph refresh skipped: %v", err)}
	}
	if !locked {
		note := "a graph rebuild is already in flight — answering from the current graph"
		if seedNote != "" {
			note = seedNote + "; " + note
		}
		return RefreshResult{Drift: drift, Note: note}
	}
	lockPath := filepath.Join(cacheDir, ".sync.lock")
	defer func() { _ = os.Remove(lockPath) }() // Stale-lock recovery retries cleanup after five minutes.
	if drift != nil {
		fingerprint, err = ReadFingerprint(outDir, graphExtractorID)
		if err != nil {
			return RefreshResult{Drift: drift, Note: fmt.Sprintf("graph refresh skipped: %v", err)}
		}
		now, err := probeDrift(root, outDir, options.Source, fingerprint)
		if err != nil {
			return RefreshResult{Drift: drift, Note: fmt.Sprintf("graph refresh skipped: %v", err)}
		}
		if now != nil && len(now.Changed)+len(now.Added)+len(now.Removed) == 0 {
			if unsupported = unsupportedFingerprintFiles(fingerprint, now); len(unsupported) > 0 {
				return RefreshResult{Drift: drift, Note: unsupportedRefreshNote(unsupported)}
			}
			return RefreshResult{Note: seedNote}
		}
	}
	if fingerprint != nil {
		options.Source.OnlyDirs = slices.Clone(fingerprint.OnlyDirs)
	}
	built, err := BuildGraph(root, options.Source)
	if err != nil {
		return RefreshResult{Drift: drift, Note: fmt.Sprintf("graph refresh skipped: %v", err)}
	}
	if len(built.Unsupported) > 0 {
		return RefreshResult{Drift: drift, Note: unsupportedRefreshNote(built.Unsupported)}
	}
	if len(built.Errors) > 0 {
		return RefreshResult{Drift: drift, Note: fmt.Sprintf(
			"graph refresh skipped: native extraction reported %d source error(s), including %q",
			len(built.Errors), built.Errors[0],
		)}
	}
	if _, err := Write(built.Graph, outDir); err != nil {
		return RefreshResult{Drift: drift, Note: fmt.Sprintf("graph refresh skipped: %v", err)}
	}
	if err := WriteFingerprint(outDir, graphExtractorID, built.Fingerprints, options.Source.OnlyDirs); err != nil {
		return RefreshResult{Refreshed: true, Drift: drift, Note: fmt.Sprintf("fingerprint write failed: %v", err)}
	}
	note := "native extractor limitations: " + strings.Join(built.Limitations, "; ")
	if seedNote != "" {
		note = seedNote + "; " + note
	}
	return RefreshResult{Refreshed: true, Drift: drift, Note: note}
}

// EnsureFreshChildren refreshes changed child graphs in workspace order.
// Each child uses its own <root>/<child>/graft directory; unsafe or missing
// child directories are skipped.
func EnsureFreshChildren(root string, children []string) RefreshResult {
	refreshedIn := make([]string, 0, len(children))
	notes := make([]string, 0)
	files := 0
	for _, child := range children {
		if !filepath.IsLocal(child) || child == "." || filepath.Base(child) != child {
			continue
		}
		if info, err := os.Lstat(filepath.Join(root, child)); err != nil || !info.IsDir() {
			continue
		}
		result := EnsureFreshGraph(filepath.Join(root, child), RefreshOptions{})
		if !result.Refreshed {
			if result.Note != "" {
				notes = append(notes, child+"/: "+result.Note)
			}
			continue
		}
		refreshedIn = append(refreshedIn, child)
		if result.Drift != nil {
			files += len(result.Drift.Changed) + len(result.Drift.Added) + len(result.Drift.Removed)
		}
	}
	if len(refreshedIn) == 0 {
		return RefreshResult{Note: strings.Join(notes, "; ")}
	}
	count, plural := "?", "s"
	if files > 0 {
		count = fmt.Sprint(files)
	}
	if files == 1 {
		plural = ""
	}
	note := fmt.Sprintf("refreshed %s (%s file%s changed) before answering", strings.Join(refreshedIn, ", "), count, plural)
	if len(notes) > 0 {
		note += "; " + strings.Join(notes, "; ")
	}
	return RefreshResult{Refreshed: true, Note: note}
}

// RefreshNote formats a refresh result for a CLI or MCP response.
func RefreshNote(result RefreshResult) string {
	if !result.Refreshed {
		if result.Note != "" {
			return "[graft] " + result.Note
		}
		return ""
	}
	count := "?"
	if result.Drift != nil {
		n := len(result.Drift.Changed) + len(result.Drift.Added) + len(result.Drift.Removed)
		if n > 0 {
			count = fmt.Sprint(n)
		}
	}
	plural := "s"
	if count == "1" {
		plural = ""
	}
	note := fmt.Sprintf("[graft] refreshed the graph (%s file%s changed) before answering", count, plural)
	if result.Note != "" {
		note += " — " + result.Note
	}
	return note
}

// waitForGraphLock waits up to two seconds for the shared graph writer lock.
func waitForGraphLock(cache string) (bool, error) {
	const (
		wait = 2 * time.Second
		poll = 50 * time.Millisecond
	)
	deadline := time.Now().Add(wait)
	for {
		acquired, err := acquireGraphLock(cache)
		if err != nil || acquired {
			return acquired, err
		}
		if !time.Now().Before(deadline) {
			return false, nil
		}
		time.Sleep(poll)
	}
}

// acquireGraphLock creates the shared lock or reclaims it when it is five minutes old.
func acquireGraphLock(cache string) (bool, error) {
	const staleAfter = 5 * time.Minute
	if err := os.MkdirAll(cache, 0o755); err != nil {
		return false, fmt.Errorf("create graph lock directory: %w", err)
	}
	lockPath := filepath.Join(cache, ".sync.lock")
	for attempt := range 2 {
		lock, err := os.OpenFile(lockPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o644)
		if err == nil {
			if err := lock.Close(); err != nil {
				_ = os.Remove(lockPath) // Do not leave a lock after failing to close it.
				return false, fmt.Errorf("close graph sync lock: %w", err)
			}
			return true, nil
		}
		if !errors.Is(err, fs.ErrExist) {
			return false, fmt.Errorf("create graph sync lock: %w", err)
		}
		if attempt == 1 {
			return false, nil
		}
		info, err := os.Stat(lockPath)
		if err == nil && time.Since(info.ModTime()) < staleAfter {
			return false, nil
		}
		_ = os.Remove(lockPath) // Reclaim a stale lock before retrying exclusive creation.
	}
	return false, nil
}

func unsupportedFingerprintFiles(fingerprint *Fingerprint, drift *Drift) []string {
	if fingerprint == nil {
		return nil
	}
	var removed map[string]struct{}
	if drift != nil {
		removed = make(map[string]struct{}, len(drift.Removed))
		for _, path := range drift.Removed {
			removed[path] = struct{}{}
		}
	}
	var changed map[string]struct{}
	if drift != nil {
		changed = make(map[string]struct{}, len(drift.Changed))
		for _, path := range drift.Changed {
			changed[path] = struct{}{}
		}
	}
	unsupported := make([]string, 0)
	for path := range fingerprint.Files {
		if _, gone := removed[path]; gone {
			continue
		}
		if fingerprint.Files[path].Hash == "" {
			if _, readable := changed[path]; !readable {
				unsupported = append(unsupported, path)
				continue
			}
		}
		if _, _, supported := sourceGrammar(path); !supported {
			unsupported = append(unsupported, path)
		}
	}
	if drift != nil {
		for _, path := range drift.Added {
			if _, _, supported := sourceGrammar(path); !supported {
				unsupported = append(unsupported, path)
			}
		}
	}
	slices.Sort(unsupported)
	return unsupported
}

func unsupportedRefreshNote(paths []string) string {
	return fmt.Sprintf("graph refresh skipped: native graph cannot index %d unsupported or unreadable source file(s), including %q", len(paths), paths[0])
}
