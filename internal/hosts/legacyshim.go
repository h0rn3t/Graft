package hosts

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
)

// Releases before direct hook entries started graft through Node shims at
// these paths. Init and uninstall delete them, and upkeep treats one still on
// disk as stale wiring.

func legacyClaudeShims(repo string) []string {
	helpers := filepath.Join(repo, ".claude", "helpers")
	return []string{filepath.Join(helpers, legacyHooksShim), filepath.Join(helpers, legacyStatuslineShim)}
}

func legacyClaudeGlobalShim(home string) string {
	return filepath.Join(globalHelpersDir(home), legacyHooksShim)
}

func legacyCodexShim(home string) string {
	return filepath.Join(home, ".codex", "hooks", "graft", legacyHooksShim)
}

func legacyCursorShim(repo string) string {
	return filepath.Join(repo, ".cursor", "hooks", legacyHooksShim)
}

// isLegacyShim reports whether data is a shim graft wrote: the native Node
// shim spawns `_hook` or `_statusline`, and the TypeScript-era one imported
// the CLI through pathToFileURL.
func isLegacyShim(data []byte) bool {
	text := string(data)
	return strings.Contains(text, "spawnSync(binary, ['_hook'") ||
		strings.Contains(text, "spawnSync(binary, ['_statusline'") ||
		strings.Contains(text, "pathToFileURL")
}

// HasLegacyShim reports whether a Node shim graft wrote is still on disk
// where rewiring the hosts ids with these options would remove it: the Claude
// Code shims under repo, which on their own mark Claude as wired, and the
// Cursor, user-level Claude Code, and Codex shims only when their host is in
// ids and hooks or global writes reach them. A shim no refresh removes would
// otherwise mark the wiring stale on every startup.
func HasLegacyShim(repo, home string, ids []string, hooks, global bool) bool {
	paths := legacyClaudeShims(repo)
	if hooks && slices.Contains(ids, "cursor") {
		paths = append(paths, legacyCursorShim(repo))
	}
	if global && slices.Contains(ids, "claude") {
		paths = append(paths, legacyClaudeGlobalShim(home))
	}
	if hooks && global && slices.Contains(ids, "agents") {
		paths = append(paths, legacyCodexShim(home))
	}
	for _, path := range paths {
		data, err := os.ReadFile(path)
		if err == nil && isLegacyShim(data) {
			return true
		}
	}
	return false
}

// removeLegacyShim deletes the Node shim an earlier release wrote at path and
// reports it as removed. A file graft did not write is kept and reported as
// such; an absent one reports ok=false.
func (f *files) removeLegacyShim(id, path string) (write ConfigWrite, ok bool, err error) {
	data, err := f.readFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return ConfigWrite{}, false, nil
	}
	if err != nil {
		return ConfigWrite{}, false, err
	}
	if !isLegacyShim(data) {
		return ConfigWrite{ID: id, Path: path, Action: ActionKeptForeign}, true, nil
	}
	if err := f.remove(path); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return ConfigWrite{}, false, err
	}
	f.pruneEmptyDirs(filepath.Dir(path))
	return ConfigWrite{ID: id, Path: path, Action: ActionRemoved}, true, nil
}

// retractLegacyShim is removeLegacyShim for uninstall: a file graft did not
// write counts as absent.
func (f *files) retractLegacyShim(path string, apply bool) (RetractAction, error) {
	data, err := f.readFile(path)
	if errors.Is(err, fs.ErrNotExist) || err == nil && !isLegacyShim(data) {
		return RetractAbsent, nil
	}
	if err != nil {
		return RetractFailed, err
	}
	return f.removeFile(path, apply)
}
