package upkeep

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/h0rn3t/Graft/internal/hosts"
)

// WiredHostIDs lists the hosts a previous init wired here, read off disk: the
// Claude Code hook shim, each owned instruction file, and each shared file that
// carries graft's fenced section.
func WiredHostIDs(repo string) []string {
	var ids []string
	if _, err := os.Stat(filepath.Join(repo, ".claude", "helpers", "graft-hooks.cjs")); err == nil {
		ids = append(ids, "claude")
	}
	for _, host := range hosts.Hosts() {
		path := filepath.Join(repo, host.RelPath)
		if host.Kind == hosts.KindSection {
			data, err := os.ReadFile(path)
			if err != nil || !strings.Contains(string(data), hosts.GraftMarkers.Start) {
				continue
			}
		} else if _, err := os.Stat(path); err != nil {
			continue
		}
		ids = append(ids, host.ID)
	}
	return ids
}

// RewriteWiring re-runs init's writes for exactly the given hosts with the
// stamped choices, never building the graph.
func RewriteWiring(ctx context.Context, repo string, ids []string, options WiringOptions, env hosts.Env) error {
	if ctx == nil {
		return errors.New("rewrite wiring requires a context")
	}
	if slices.Contains(ids, "claude") {
		if _, err := hosts.RunClaudeInit(repo, env, options.Statusline, options.Global); err != nil {
			return err
		}
	}
	others := slices.DeleteFunc(slices.Clone(ids), func(id string) bool { return id == "claude" })
	if len(others) == 0 {
		return nil
	}
	_, err := hosts.RunHostsInit(repo, env, hosts.InitOptions{Agents: others, MCP: options.MCP, Hooks: options.Hooks, Global: options.Global})
	return err
}
