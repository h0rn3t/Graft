// Package upkeep contains the fail-soft startup cache used by the MCP server.
package upkeep

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/NanoNets/context-graph-engine/internal/climeta"
)

// UpdateTTL is the period for which a registry answer remains current.
const UpdateTTL = 24 * time.Hour

// UpdateCache stores the latest version observed on npm and the last attempt time.
type UpdateCache struct {
	Latest    *string `json:"latest"`
	CheckedAt int64   `json:"checkedAt"`
}

// ReadUpdateCache reads the machine-global update cache. Invalid or missing data
// is treated as a cache miss so startup never fails because of derived state.
func ReadUpdateCache(home string) (*UpdateCache, bool) {
	data, err := os.ReadFile(updateCachePath(home))
	if err != nil {
		return nil, false
	}
	var cache UpdateCache
	if err := json.Unmarshal(data, &cache); err != nil || cache.CheckedAt <= 0 {
		return nil, false
	}
	return &cache, true
}

// WriteUpdateCache atomically replaces the machine-global update cache.
func WriteUpdateCache(home string, cache UpdateCache) error {
	data, err := json.Marshal(cache)
	if err != nil {
		return fmt.Errorf("encode update cache: %w", err)
	}
	return writeAtomicCache(updateCachePath(home), data, "update")
}

// NeedsRefresh reports whether the registry answer is missing or expired.
func NeedsRefresh(cache *UpdateCache, now time.Time) bool {
	return cache == nil || now.UnixMilli()-cache.CheckedAt >= UpdateTTL.Milliseconds()
}

// FormatUpdateNudge returns the user-facing update line when latest is newer.
func FormatUpdateNudge(current, latest string) string {
	if latest == "" || compareVersions(latest, current) <= 0 {
		return ""
	}
	return fmt.Sprintf("⬆ graft %s → %s available: run `npm i -g @nanonets/graft@latest` (restart your agent after).", current, latest)
}

// StartupLines reads cached upkeep facts without waiting on the network.
func StartupLines(current, home string) []string {
	cache, ok := ReadUpdateCache(home)
	if !ok || cache == nil || cache.Latest == nil {
		return nil
	}
	if line := FormatUpdateNudge(current, *cache.Latest); line != "" {
		return []string{line}
	}
	return nil
}

// RefreshUpdateCache performs one bounded npm version lookup and stores its result.
func RefreshUpdateCache(home string, now time.Time) UpdateCache {
	result := climeta.GetNpmViewVersion("", 2*time.Second)
	cache := UpdateCache{CheckedAt: now.UnixMilli()}
	if result.OK && result.Version != "" {
		cache.Latest = &result.Version
	}
	_ = WriteUpdateCache(home, cache)
	return cache
}

// MaybeRefreshInBackground records an attempt and starts a detached update check.
// It returns false when the cache is fresh or the current process cannot be detached.
func MaybeRefreshInBackground(home string, now time.Time) bool {
	executable, err := os.Executable()
	if err != nil || strings.HasSuffix(strings.TrimSuffix(filepath.Base(executable), ".exe"), ".test") {
		return false
	}
	cache, _ := ReadUpdateCache(home)
	if !NeedsRefresh(cache, now) {
		return false
	}
	seed := UpdateCache{CheckedAt: now.UnixMilli()}
	if cache != nil {
		seed.Latest = cache.Latest
	}
	if err := WriteUpdateCache(home, seed); err != nil {
		return false
	}
	command := exec.Command(executable, "_update-check")
	command.Stdin = strings.NewReader("")
	command.Stdout = io.Discard
	command.Stderr = io.Discard
	if err := command.Start(); err != nil {
		return false
	}
	_ = command.Process.Release()
	return true
}

func updateCachePath(home string) string {
	return filepath.Join(home, ".graft", "update-check.json")
}

func writeAtomicCache(path string, data []byte, name string) error {
	directory := filepath.Dir(path)
	if err := os.MkdirAll(directory, 0o755); err != nil {
		return fmt.Errorf("create %s cache directory: %w", name, err)
	}
	tmp, err := os.CreateTemp(directory, ".cache-*.tmp")
	if err != nil {
		return fmt.Errorf("create %s cache temporary file: %w", name, err)
	}
	temporary := tmp.Name()
	defer func() { _ = os.Remove(temporary) }()
	if _, err := tmp.Write(append(data, '\n')); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("write %s cache: %w", name, err)
	}
	if err := tmp.Chmod(0o644); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("set %s cache mode: %w", name, err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close %s cache: %w", name, err)
	}
	if err := os.Rename(temporary, path); err != nil {
		return fmt.Errorf("replace %s cache: %w", name, err)
	}
	return nil
}

func compareVersions(left, right string) int {
	left = strings.SplitN(left, "-", 2)[0]
	right = strings.SplitN(right, "-", 2)[0]
	leftParts := strings.Split(left, ".")
	rightParts := strings.Split(right, ".")
	for index := 0; index < max(len(leftParts), len(rightParts)); index++ {
		leftValue := versionPart(leftParts, index)
		rightValue := versionPart(rightParts, index)
		if leftValue < rightValue {
			return -1
		}
		if leftValue > rightValue {
			return 1
		}
	}
	return 0
}

func versionPart(parts []string, index int) int {
	if index >= len(parts) {
		return 0
	}
	value, err := strconv.Atoi(parts[index])
	if err != nil {
		return 0
	}
	return value
}
