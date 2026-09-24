package main

import (
	"context"
	jsonv2 "encoding/json/v2"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/h0rn3t/Graft/internal/fsutil"
	"github.com/h0rn3t/Graft/internal/jsonjs"
)

// hookStateLockWait bounds how long a hook waits for another hook's update of
// the same state file. Each update takes milliseconds, so a longer wait means
// a stuck peer, and the agent should not wait on it.
const hookStateLockWait = 2 * time.Second

type sessionState struct {
	LastQuery         *string           `json:"lastQuery"`
	PerAgentQuery     map[string]string `json:"perAgentQuery"`
	GraftReads        int               `json:"graftReads"`
	SourceReads       int               `json:"sourceReads"`
	SavedTokens       int               `json:"savedTokens"`
	InjectedPointers  []string          `json:"injectedPointers"`
	Nudges            int               `json:"nudges"`
	TurnUsedGraft     *bool             `json:"turnUsedGraft,omitempty"`
	InputCostMicros   *int              `json:"inputCostMicros,omitempty"`
	InputTokensBilled *int              `json:"inputTokensBilled,omitempty"`
	LastBillingUUID   *string           `json:"lastBillingUuid,omitempty"`
	GraftTurns        *int              `json:"graftTurns,omitempty"`
	ReportedTurns     *int              `json:"reportedTurns,omitempty"`
	LastTallyUUID     *string           `json:"lastTallyUuid,omitempty"`
	Host              *string           `json:"host,omitempty"`
}

func hookContextDir(root string) string {
	override := os.Getenv("GRAFT_DIR")
	if override == "" {
		return filepath.Join(root, "graft")
	}
	if filepath.IsAbs(override) {
		return filepath.Clean(override)
	}
	return filepath.Join(root, override)
}

func hookCacheDir(root string) string {
	return filepath.Join(hookContextDir(root), ".cache")
}

func hookSessionDir(root string) string {
	return filepath.Join(hookCacheDir(root), "session")
}

func listHookSessionIDs(root string) []string {
	entries, err := os.ReadDir(hookSessionDir(root))
	if err != nil {
		return []string{}
	}
	ids := make([]string, 0, len(entries))
	for _, entry := range entries {
		if name, ok := strings.CutSuffix(entry.Name(), ".json"); ok {
			ids = append(ids, name)
		}
	}
	return ids
}

func emptySessionState() sessionState {
	return sessionState{
		PerAgentQuery:    make(map[string]string),
		InjectedPointers: make([]string, 0),
	}
}

// hookSessionPath is the state file of session id. The id comes from the
// host's hook input, so anything but a single plain path element falls back
// to the default session rather than naming a file outside the cache.
func hookSessionPath(root, id string) string {
	if !filepath.IsLocal(id) || strings.ContainsAny(id, `/\`) {
		id = "default"
	}
	return filepath.Join(hookSessionDir(root), id+".json")
}

// withHookStateLock runs update while holding the advisory lock beside path,
// so hooks running in parallel read-modify-write the file one at a time. A
// hook that cannot get the lock within hookStateLockWait skips its update
// rather than stall the agent.
func withHookStateLock(path string, update func() error) error {
	ctx, cancel := context.WithTimeout(context.Background(), hookStateLockWait)
	defer cancel()
	release, err := fsutil.Lock(ctx, path+".lock")
	if err != nil {
		return fmt.Errorf("lock %s: %w", filepath.Base(path), err)
	}
	defer release()
	return update()
}

// updateHookSession applies change to session id under its lock and writes
// the result when change reports that it modified the session.
func updateHookSession(root, id string, change func(*sessionState) bool) error {
	return withHookStateLock(hookSessionPath(root, id), func() error {
		session := readHookSession(root, id)
		if !change(&session) {
			return nil
		}
		return writeHookSession(root, id, session)
	})
}

func readHookSession(root, id string) sessionState {
	session := emptySessionState()
	data, err := os.ReadFile(hookSessionPath(root, id))
	if err != nil {
		return emptySessionState()
	}
	if err := jsonv2.Unmarshal(data, &session); err != nil {
		return emptySessionState()
	}
	if session.PerAgentQuery == nil {
		session.PerAgentQuery = make(map[string]string)
	}
	if session.InjectedPointers == nil {
		session.InjectedPointers = make([]string, 0)
	}
	return session
}

func writeHookSession(root, id string, session sessionState) error {
	if session.PerAgentQuery == nil {
		session.PerAgentQuery = make(map[string]string)
	}
	if session.InjectedPointers == nil {
		session.InjectedPointers = make([]string, 0)
	}
	return writeHookJSONAtomic(hookSessionPath(root, id), session)
}

type hookStats struct {
	NodeCount  int      `json:"nodeCount"`
	EdgeCount  int      `json:"edgeCount"`
	Languages  []string `json:"languages"`
	TotalCount int      `json:"totalCount"`
	ReadyCount int      `json:"readyCount"`
	StaleCount int      `json:"staleCount"`
	Dirty      bool     `json:"dirty"`
	Syncing    bool     `json:"syncing"`
	SyncedAt   *string  `json:"syncedAt"`
	LastFile   *string  `json:"lastFile"`
}

type hookStatsPatch struct {
	NodeCount   *int
	EdgeCount   *int
	Languages   *[]string
	TotalCount  *int
	ReadyCount  *int
	StaleCount  *int
	Dirty       *bool
	Syncing     *bool
	SyncedAtSet bool
	SyncedAt    *string
	LastFileSet bool
	LastFile    *string
}

func emptyHookStats() hookStats {
	return hookStats{Languages: make([]string, 0)}
}

func hookStatsPath(root string) string {
	return filepath.Join(hookCacheDir(root), "stats.json")
}

func readHookStats(root string) *hookStats {
	data, err := os.ReadFile(hookStatsPath(root))
	if err != nil {
		return nil
	}
	var stats hookStats
	if jsonv2.Unmarshal(data, &stats) != nil {
		return nil
	}
	if stats.Languages == nil {
		stats.Languages = make([]string, 0)
	}
	return &stats
}

func writeHookStats(root string, stats hookStats) error {
	if stats.Languages == nil {
		stats.Languages = make([]string, 0)
	}
	return writeHookJSONAtomic(hookStatsPath(root), stats)
}

// updateHookStats applies change to the stats file under its lock.
func updateHookStats(root string, change func(*hookStats)) (hookStats, error) {
	stats := emptyHookStats()
	err := withHookStateLock(hookStatsPath(root), func() error {
		if current := readHookStats(root); current != nil {
			stats = *current
		}
		change(&stats)
		return writeHookStats(root, stats)
	})
	return stats, err
}

func patchHookStats(root string, patch hookStatsPatch) (hookStats, error) {
	return updateHookStats(root, func(stats *hookStats) { patch.apply(stats) })
}

func (patch hookStatsPatch) apply(stats *hookStats) {
	if patch.NodeCount != nil {
		stats.NodeCount = *patch.NodeCount
	}
	if patch.EdgeCount != nil {
		stats.EdgeCount = *patch.EdgeCount
	}
	if patch.Languages != nil {
		stats.Languages = *patch.Languages
	}
	if patch.TotalCount != nil {
		stats.TotalCount = *patch.TotalCount
	}
	if patch.ReadyCount != nil {
		stats.ReadyCount = *patch.ReadyCount
	}
	if patch.StaleCount != nil {
		stats.StaleCount = *patch.StaleCount
	}
	if patch.Dirty != nil {
		stats.Dirty = *patch.Dirty
	}
	if patch.Syncing != nil {
		stats.Syncing = *patch.Syncing
	}
	if patch.SyncedAtSet {
		stats.SyncedAt = patch.SyncedAt
	}
	if patch.LastFileSet {
		stats.LastFile = patch.LastFile
	}
}

func writeHookJSONAtomic(path string, value any) error {
	data, err := jsonjs.Marshal(value, "  ")
	if err != nil {
		return fmt.Errorf("encode %s: %w", path, err)
	}
	if err := fsutil.WriteFileAtomic(path, data, 0o644); err != nil {
		return fmt.Errorf("replace state file: %w", err)
	}
	return nil
}
