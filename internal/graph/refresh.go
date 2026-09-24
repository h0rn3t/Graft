package graph

import (
	"context"
	jsonv2 "encoding/json/v2"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/h0rn3t/Graft/internal/fsutil"
	"github.com/h0rn3t/Graft/internal/sourcefiles"
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
		options.Source.Extensions = SourceExtensions()
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
	fingerprint, err := ReadFingerprint(outDir, ExtractorID)
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
		if len(files) == 0 {
			return RefreshResult{Note: seedNote}
		}
		for _, file := range files {
			if !nativeSupported(file.Rel) {
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

	release, err := waitForGraphLock(outDir)
	if err != nil {
		return RefreshResult{Drift: drift, Note: fmt.Sprintf("graph refresh skipped: %v", err)}
	}
	if release == nil {
		note := "a graph rebuild is already in flight — answering from the current graph"
		if seedNote != "" {
			note = seedNote + "; " + note
		}
		return RefreshResult{Drift: drift, Note: note}
	}
	defer release()
	latest := drift
	if drift != nil {
		fingerprint, err = ReadFingerprint(outDir, ExtractorID)
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
		latest = now
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
	if prior, err := Read(WiringPath(outDir)); err == nil {
		carryOverLSPEdges(&built.Graph, prior.Edges, latest)
	}
	if _, err := Write(built.Graph, outDir); err != nil {
		return RefreshResult{Drift: drift, Note: fmt.Sprintf("graph refresh skipped: %v", err)}
	}
	if err := WriteAskIndex(outDir, built.Graph); err != nil {
		return RefreshResult{Refreshed: true, Drift: drift, Note: fmt.Sprintf("ask index write failed: %v", err)}
	}
	if err := WriteFingerprint(outDir, ExtractorID, built.Fingerprints, options.Source.OnlyDirs); err != nil {
		return RefreshResult{Refreshed: true, Drift: drift, Note: fmt.Sprintf("fingerprint write failed: %v", err)}
	}
	// Cards exist only where graft build wrote them; keep those in step.
	if _, err := os.Stat(filepath.Join(outDir, "INDEX.md")); err == nil {
		if _, err := WriteCards(built.Graph, outDir); err != nil {
			return RefreshResult{Refreshed: true, Drift: drift, Note: fmt.Sprintf("card write failed: %v", err)}
		}
	}
	var notes []string
	if seedNote != "" {
		notes = append(notes, seedNote)
	}
	if len(built.Limitations) > 0 {
		notes = append(notes, "native extractor limitations: "+strings.Join(built.Limitations, "; "))
	}
	note := strings.Join(notes, "; ")
	return RefreshResult{Refreshed: true, Drift: drift, Note: note}
}

// carryOverLSPEdges keeps the compiler-resolved call edges of a graph built
// with --lsp, which a refresh does not recompute. An edge survives when both
// ends still exist and the calling file is not in drift; without a drift
// record nothing is known to be unchanged, so nothing is kept.
func carryOverLSPEdges(graph *GraphV1, prior []EdgeV1, drift *Drift) {
	if drift == nil {
		return
	}
	changed := make(map[string]struct{}, len(drift.Changed)+len(drift.Added))
	for _, path := range slices.Concat(drift.Changed, drift.Added) {
		changed[path] = struct{}{}
	}
	pathOf := make(map[string]string, len(graph.Nodes))
	for _, node := range graph.Nodes {
		pathOf[node.ID] = node.Path
	}
	existing := make(map[string]struct{}, len(graph.Edges))
	for _, edge := range graph.Edges {
		existing[edge.Source+"\x00"+string(edge.Relation)+"\x00"+edge.Target] = struct{}{}
	}
	for _, edge := range prior {
		if edge.Confidence != "lsp_resolved" {
			continue
		}
		sourcePath, sourceOK := pathOf[edge.Source]
		_, targetOK := pathOf[edge.Target]
		if !sourceOK || !targetOK {
			continue
		}
		if _, dirty := changed[sourcePath]; dirty {
			continue
		}
		key := edge.Source + "\x00" + string(edge.Relation) + "\x00" + edge.Target
		if _, ok := existing[key]; ok {
			continue
		}
		existing[key] = struct{}{}
		graph.Edges = append(graph.Edges, edge)
	}
	graph.Meta.EdgeCount = len(graph.Edges)
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

// graphLockFile is the writer lock shared by graph builds, refreshes, worktree
// seeding, and the hook sync marker, relative to a context directory.
const graphLockFile = ".cache/.sync.lock"

// LockGraph takes the exclusive graph writer lock of the context directory
// outDir, waiting until it is free or ctx is done. It is an operating-system
// advisory lock, so it dies with a crashed holder and never goes stale; the
// lock file itself is left in place. Every writer of the wiring graph, its ask
// index, and its fingerprint holds it while writing so the three stay a
// consistent set. The lock is not reentrant: a holder that calls LockGraph
// again on the same directory waits for itself.
func LockGraph(ctx context.Context, outDir string) (release func(), err error) {
	return fsutil.Lock(ctx, filepath.Join(outDir, filepath.FromSlash(graphLockFile)))
}

// hookMarkerStaleAfter is the age after which an AcquireLock marker whose
// sync never cleared it is ignored.
const hookMarkerStaleAfter = 5 * time.Minute

// waitForGraphLock waits up to two seconds for the graph writer lock, the
// time a query may spend before it answers from the current graph. A pending
// hook sync, marked by AcquireLock, counts as a writer too, so a query does
// not rebuild the graph the sync is about to replace. A nil release with a
// nil error means another writer is still busy.
func waitForGraphLock(outDir string) (release func(), err error) {
	const (
		wait       = 2 * time.Second
		markerPoll = 50 * time.Millisecond
	)
	ctx, cancel := context.WithTimeout(context.Background(), wait)
	defer cancel()
	lockPath := filepath.Join(outDir, filepath.FromSlash(graphLockFile))
	for {
		release, err = LockGraph(ctx, outDir)
		if errors.Is(err, context.DeadlineExceeded) {
			return nil, nil
		}
		if err != nil {
			return nil, fmt.Errorf("take graph writer lock: %w", err)
		}
		info, err := os.Stat(lockPath)
		if err != nil || info.Size() == 0 || time.Since(info.ModTime()) >= hookMarkerStaleAfter {
			return release, nil
		}
		release()
		select {
		case <-ctx.Done():
			return nil, nil
		case <-time.After(markerPoll):
		}
	}
}

// AcquireLock sets the hook sync marker in the shared lock file of cache, the
// `.cache` directory of a context directory. The marker outlives this process
// so a detached sync can clear it with ReleaseLock; the sync's own graph write
// still takes LockGraph. The marker is refused while a LockGraph holder is
// active or another marker younger than five minutes is present.
func AcquireLock(cache string) (bool, error) {
	if err := os.MkdirAll(cache, 0o755); err != nil {
		return false, fmt.Errorf("create graph lock directory: %w", err)
	}
	payload, err := jsonv2.Marshal(struct {
		PID int    `json:"pid"`
		At  string `json:"at"`
	}{PID: os.Getpid(), At: time.Now().UTC().Format("2006-01-02T15:04:05.000Z")})
	if err != nil {
		return false, fmt.Errorf("encode graph sync lock: %w", err)
	}
	lockPath := filepath.Join(cache, ".sync.lock")
	for attempt := range 2 {
		lock, err := os.OpenFile(lockPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o644)
		if err == nil {
			if _, err := lock.Write(payload); err != nil {
				_ = lock.Close()
				_ = os.Remove(lockPath)
				return false, fmt.Errorf("write graph sync lock: %w", err)
			}
			if err := lock.Close(); err != nil {
				_ = os.Remove(lockPath)
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
		claimed, retry, err := claimExistingLockFile(lockPath, payload, hookMarkerStaleAfter)
		if err != nil || !retry {
			return claimed, err
		}
	}
	return false, nil
}

// claimExistingLockFile decides on a lock file that already exists. An empty
// file is LockGraph's idle lock file and takes the marker in place; a marker
// older than staleAfter is removed so the caller can retry; a LockGraph holder
// or a fresh marker refuses the claim.
func claimExistingLockFile(lockPath string, payload []byte, staleAfter time.Duration) (claimed, retry bool, err error) {
	release, locked := tryLockFile(lockPath)
	if !locked {
		return false, false, nil
	}
	defer release()
	info, err := os.Stat(lockPath)
	switch {
	case err == nil && info.Size() == 0:
		if err := os.WriteFile(lockPath, payload, 0o644); err != nil {
			return false, false, fmt.Errorf("write graph sync lock: %w", err)
		}
		return true, false, nil
	case err == nil && time.Since(info.ModTime()) < staleAfter:
		return false, false, nil
	}
	_ = os.Remove(lockPath) // the retry reports a file that cannot be replaced
	return false, true, nil
}

// tryLockFile takes the advisory lock on lockPath without waiting.
func tryLockFile(lockPath string) (release func(), locked bool) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	release, err := fsutil.Lock(ctx, lockPath)
	return release, err == nil
}

// ReleaseLock clears the hook sync marker. The file is removed only while no
// LockGraph holder has it open; otherwise the marker is truncated away, since
// unlinking a file another writer holds would let a third writer lock a fresh
// file beside it.
func ReleaseLock(cache string) {
	lockPath := filepath.Join(cache, ".sync.lock")
	release, locked := tryLockFile(lockPath)
	if !locked {
		_ = os.Truncate(lockPath, 0) // a missing file already means released
		return
	}
	defer release()
	_ = os.Remove(lockPath) // a missing file already means released
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
		if !nativeSupported(path) {
			unsupported = append(unsupported, path)
		}
	}
	if drift != nil {
		for _, path := range drift.Added {
			if !nativeSupported(path) {
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
