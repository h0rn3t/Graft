package upkeep

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"
)

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

// ReconcileWiring refreshes stale host wiring and records the choices it used.
// It merges stamped host intent with hosts found on disk, then calls rewrite
// with the saved options. Missing option values default to true. The returned
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
	cacheDir := filepath.Dir(brainRulesCachePath(repo, contextDir))
	stamp := readWiringStamp(cacheDir)
	if stamp != nil && stamp.Version != nil && *stamp.Version == current {
		return ""
	}
	diskHosts, err := wired(repo)
	if err != nil {
		return ""
	}
	hosts := slices.Clone(diskHosts)
	if stamp != nil {
		hosts = append(hosts, stamp.Hosts...)
	}
	slices.Sort(hosts)
	hosts = slices.Compact(hosts)
	if len(hosts) == 0 {
		return ""
	}
	options := WiringOptions{Global: true, MCP: true, Hooks: true, Statusline: true}
	if stamp != nil && len(stamp.Opts) > 0 {
		stored := struct {
			Global     *bool `json:"global"`
			MCP        *bool `json:"mcp"`
			Hooks      *bool `json:"hooks"`
			Statusline *bool `json:"statusline"`
		}{}
		if json.Unmarshal(stamp.Opts, &stored) == nil {
			if stored.Global != nil {
				options.Global = *stored.Global
			}
			if stored.MCP != nil {
				options.MCP = *stored.MCP
			}
			if stored.Hooks != nil {
				options.Hooks = *stored.Hooks
			}
			if stored.Statusline != nil {
				options.Statusline = *stored.Statusline
			}
		}
	}
	if err := rewrite(repo, slices.Clone(hosts), options); err != nil {
		return ""
	}
	data, _ := json.Marshal(struct {
		Version string        `json:"version"`
		Hosts   []string      `json:"hosts"`
		Opts    WiringOptions `json:"opts"`
		At      string        `json:"at"`
	}{
		Version: current,
		Hosts:   hosts,
		Opts:    options,
		At:      now.UTC().Truncate(time.Millisecond).Format("2006-01-02T15:04:05.000Z"),
	}) // every stamp field is JSON-safe
	_ = writeAtomicCache(filepath.Join(cacheDir, "wiring-stamp.json"), data, "wiring stamp") // retry on the next startup if persistence fails
	scope := ""
	if options.Global && slices.Contains(hosts, "agents") {
		scope = " (including this machine's ~/.codex config)"
	}
	from := "unwired"
	if stamp != nil && stamp.Version != nil {
		from = *stamp.Version
	}
	return fmt.Sprintf("· graft refreshed this repo's agent wiring%s (written by %s, now %s): %s.", scope, from, current, strings.Join(hosts, ", "))
}

func readWiringStamp(cacheDir string) *wiringStamp {
	data, err := os.ReadFile(filepath.Join(cacheDir, "wiring-stamp.json"))
	if err != nil {
		return nil
	}
	var stamp *wiringStamp
	if err := json.Unmarshal(data, &stamp); err != nil {
		return nil
	}
	return stamp
}
