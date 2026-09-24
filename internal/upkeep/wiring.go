package upkeep

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/h0rn3t/Graft/internal/fsutil"
	"github.com/h0rn3t/Graft/internal/hosts"
	"github.com/h0rn3t/Graft/internal/jsonjs"
)

// wiringLockWait bounds how long a startup waits for another process that is
// refreshing the same repository's wiring before it leaves the work to it.
const wiringLockWait = 2 * time.Second

// WiringOptions holds the init choices replayed when agent wiring is refreshed.
type WiringOptions struct {
	Global     bool `json:"global"`
	MCP        bool `json:"mcp"`
	Hooks      bool `json:"hooks"`
	Statusline bool `json:"statusline"`
}

type wiringStamp struct {
	Version *string         `json:"version"`
	Hosts   []string        `json:"hosts"`
	Opts    json.RawMessage `json:"opts"`
	At      string          `json:"at"`
}

func hasLegacyHookShim(repo string) bool {
	for _, path := range []string{
		filepath.Join(repo, ".claude", "helpers", "graft-hooks.cjs"),
		filepath.Join(repo, ".claude", "helpers", "graft-statusline.cjs"),
		filepath.Join(repo, ".cursor", "hooks", "graft-hooks.cjs"),
	} {
		data, err := os.ReadFile(path) //nolint:gosec // G304: fixed graft shim paths under the repository
		if err == nil && strings.Contains(string(data), "pathToFileURL") {
			return true
		}
	}
	return false
}

func stampIsCurrent(stamp *wiringStamp, repo, current string) bool {
	return stamp != nil && stamp.Version != nil && *stamp.Version == current && !hasLegacyHookShim(repo)
}

// ReconcileWiring refreshes stale host wiring and records the choices it used.
// It merges stamped host intent with hosts found on disk, then calls rewrite
// with the saved options. Missing option values default to true, except that
// machine-wide writes need a stamp that asked for them. Concurrent startups in
// one repository serialize on a lock in the cache directory; one that cannot
// take it within a moment leaves the refresh to the holder. The returned
// startup note is empty when no refresh is needed or any step fails.
func ReconcileWiring(
	repo, contextDir, current string,
	now time.Time,
	wired func(string) ([]string, error),
	rewrite func(string, []string, WiringOptions) error,
) string {
	if wired == nil || rewrite == nil {
		return ""
	}
	cacheDir := hosts.ContextCacheDir(repo, contextDir)
	if stampIsCurrent(readWiringStamp(cacheDir), repo, current) {
		return ""
	}
	diskHosts, err := wired(repo)
	if err != nil {
		return ""
	}
	stamp := readWiringStamp(cacheDir)
	if len(diskHosts) == 0 && (stamp == nil || len(stamp.Hosts) == 0) {
		return ""
	}
	ctx, cancel := context.WithTimeout(context.Background(), wiringLockWait)
	defer cancel()
	release, err := fsutil.Lock(ctx, filepath.Join(cacheDir, "wiring.lock"))
	if err != nil {
		return ""
	}
	defer release()
	// Another process may have finished the refresh while this one waited.
	stamp = readWiringStamp(cacheDir)
	if stampIsCurrent(stamp, repo, current) {
		return ""
	}
	ids := slices.Clone(diskHosts)
	if stamp != nil {
		ids = append(ids, stamp.Hosts...)
	}
	slices.Sort(ids)
	ids = slices.Compact(ids)
	if len(ids) == 0 {
		return ""
	}
	options := stampedOptions(stamp)
	if err := rewrite(repo, slices.Clone(ids), options); err != nil {
		return ""
	}
	_ = WriteWiringStamp(repo, contextDir, current, ids, options, now) // retry on the next startup if persistence fails
	scope := ""
	if options.Global && slices.Contains(ids, "agents") {
		scope = " (including this machine's ~/.codex config)"
	}
	from := "unwired"
	if stamp != nil && stamp.Version != nil {
		from = *stamp.Version
	}
	return fmt.Sprintf("· graft refreshed this repo's agent wiring%s (written by %s, now %s): %s.", scope, from, current, strings.Join(ids, ", "))
}

// stampedOptions replays the stamp's init choices. Without a stamp nobody
// chose machine-wide writes, so Global is off; every other choice defaults on.
func stampedOptions(stamp *wiringStamp) WiringOptions {
	options := WiringOptions{Global: stamp != nil, MCP: true, Hooks: true, Statusline: true}
	if stamp == nil || len(stamp.Opts) == 0 {
		return options
	}
	stored := struct {
		Global     *bool `json:"global"`
		MCP        *bool `json:"mcp"`
		Hooks      *bool `json:"hooks"`
		Statusline *bool `json:"statusline"`
	}{}
	if json.Unmarshal(stamp.Opts, &stored) != nil {
		return options
	}
	for _, field := range []struct {
		stored *bool
		option *bool
	}{
		{stored.Global, &options.Global},
		{stored.MCP, &options.MCP},
		{stored.Hooks, &options.Hooks},
		{stored.Statusline, &options.Statusline},
	} {
		if field.stored != nil {
			*field.option = *field.stored
		}
	}
	return options
}

// WriteWiringStamp records which graft version wired which hosts, and with
// which init choices, in <context>/.cache/wiring-stamp.json. The layout is the
// TypeScript writeStamp's: pretty-printed, hosts sorted, no trailing newline.
func WriteWiringStamp(repo, contextDir, version string, ids []string, options WiringOptions, now time.Time) error {
	sorted := slices.Clone(ids)
	slices.Sort(sorted)
	stamp := jsonjs.NewObject()
	stamp.Set("version", version)
	hostValues := make([]jsonjs.Value, len(sorted))
	for i, host := range sorted {
		hostValues[i] = host
	}
	stamp.Set("hosts", hostValues)
	opts := jsonjs.NewObject()
	opts.Set("global", options.Global)
	opts.Set("mcp", options.MCP)
	opts.Set("hooks", options.Hooks)
	opts.Set("statusline", options.Statusline)
	stamp.Set("opts", opts)
	stamp.Set("at", now.UTC().Truncate(time.Millisecond).Format("2006-01-02T15:04:05.000Z"))
	path := filepath.Join(hosts.ContextCacheDir(repo, contextDir), "wiring-stamp.json")
	if err := fsutil.WriteFileAtomic(path, []byte(jsonjs.Stringify(stamp, 2)), 0o644); err != nil {
		return fmt.Errorf("write wiring stamp: %w", err)
	}
	return nil
}

func readWiringStamp(cacheDir string) *wiringStamp {
	data, err := os.ReadFile(filepath.Join(cacheDir, "wiring-stamp.json")) //nolint:gosec // G304: graft's own cache file
	if err != nil {
		return nil
	}
	var stamp *wiringStamp
	if err := json.Unmarshal(data, &stamp); err != nil {
		return nil
	}
	return stamp
}
