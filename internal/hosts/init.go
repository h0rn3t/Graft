package hosts

import (
	"errors"
	"os"
	"path/filepath"
	"slices"
)

// InitOptions selects what RunHostsInit writes.
type InitOptions struct {
	// Agents are the host ids to wire; unknown ids are reported, not written.
	Agents []string
	MCP    bool
	Hooks  bool
	Global bool
}

// InitResult reports every instruction, MCP, and hook write of a host init.
type InitResult struct {
	Written []ConfigWrite
	Unknown []string
	MCP     []ConfigWrite
	Hooks   []ConfigWrite
}

// RunHostsInit writes each selected host's instruction file, then its MCP
// registrations and hooks. Global targets are skipped when opts.Global is false;
// Cursor's hooks are repo-local and only opts.Hooks suppresses them.
func RunHostsInit(repo string, env Env, opts InitOptions) (InitResult, error) {
	result := InitResult{Written: []ConfigWrite{}, Unknown: []string{}, MCP: []ConfigWrite{}, Hooks: []ConfigWrite{}}
	ids := make([]string, 0, len(opts.Agents))
	for _, id := range opts.Agents {
		host, ok := HostByID(id)
		if !ok {
			result.Unknown = append(result.Unknown, id)
			continue
		}
		ids = append(ids, id)
		write, err := writeInstruction(repo, host)
		if err != nil {
			return result, err
		}
		result.Written = append(result.Written, write)
	}
	if opts.MCP {
		writes, err := RegisterMCPConfigs(repo, ids, env.Home, opts.Global, env.Launch)
		if err != nil {
			return result, err
		}
		result.MCP = writes
	}
	hooks, err := installHostHooks(repo, env, ids, opts)
	result.Hooks = hooks
	return result, err
}

func writeInstruction(repo string, host Host) (ConfigWrite, error) {
	path := filepath.Join(repo, host.RelPath)
	if host.Kind == KindOwned {
		action, err := writeOwnedInstruction(path, host.Content())
		return ConfigWrite{ID: host.ID, Path: path, Action: WriteAction(action)}, err
	}
	action, err := UpsertSection(path, host.Content(), GraftMarkers)
	return ConfigWrite{ID: host.ID, Path: path, Action: WriteAction(action)}, err
}

// installHostHooks writes the Codex hooks and Antigravity skill (global) and
// the Cursor hooks (repo-local) for the selected hosts.
func installHostHooks(repo string, env Env, ids []string, opts InitOptions) ([]ConfigWrite, error) {
	hooks := make([]ConfigWrite, 0)
	steps := []struct {
		enabled bool
		install func() ([]ConfigWrite, error)
	}{
		{opts.Hooks && opts.Global && slices.Contains(ids, "agents"), func() ([]ConfigWrite, error) { return InstallCodexHooks(env) }},
		{opts.Hooks && slices.Contains(ids, "cursor"), func() ([]ConfigWrite, error) { return InstallCursorHooks(repo, env) }},
		{opts.Global && slices.Contains(ids, "antigravity"), func() ([]ConfigWrite, error) { return InstallAntigravitySkill(env.Home) }},
	}
	for _, step := range steps {
		if !step.enabled {
			continue
		}
		writes, err := step.install()
		if err != nil {
			return hooks, err
		}
		hooks = append(hooks, writes...)
	}
	return hooks, nil
}

// writeOwnedInstruction writes a graft-owned instruction file and reports
// created, replaced, or unchanged.
func writeOwnedInstruction(path, content string) (string, error) {
	data, err := os.ReadFile(path)
	if err == nil && string(data) == content {
		return "unchanged", nil
	}
	existed := err == nil || !errors.Is(err, os.ErrNotExist)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return "", err
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		return "", err
	}
	if existed {
		return "replaced", nil
	}
	return "created", nil
}
