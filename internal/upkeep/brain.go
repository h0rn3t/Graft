package upkeep

import (
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/NanoNets/context-graph-engine/internal/brain"
)

// MaybeRefreshBrainRules starts a detached `graft _brain-refresh <root>` when the
// repo has a brain link and its cached rules are stale. The attempt is
// recorded before the spawn, so a brain still being built costs one request
// per TTL window rather than one per session.
// The rule cache lives under GRAFT_DIR or <root>/graft, so contextDir is unused.
func MaybeRefreshBrainRules(root, _ string, now time.Time) bool {
	if _, linked := brain.ReadLink(root); !linked {
		return false
	}
	cache, present := brain.ReadRulesCache(root)
	if !brain.CacheIsStale(cache, present, now) {
		return false
	}
	if brain.MarkRulesChecked(root, now) != nil {
		return false
	}
	executable, err := os.Executable()
	if err != nil || strings.HasSuffix(strings.TrimSuffix(filepath.Base(executable), ".exe"), ".test") {
		return false
	}
	command := exec.Command(executable, "_brain-refresh", root)
	command.Stdin = strings.NewReader("")
	command.Stdout = io.Discard
	command.Stderr = io.Discard
	if err := command.Start(); err != nil {
		return false
	}
	_ = command.Process.Release()
	return true
}
