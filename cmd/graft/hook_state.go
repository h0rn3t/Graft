package main

import (
	jsonv2 "encoding/json/v2"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/NanoNets/context-graph-engine/internal/jsonjs"
)

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
	Summarized        *bool             `json:"summarized,omitempty"`
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

func readHookSession(root, id string) sessionState {
	session := emptySessionState()
	data, err := os.ReadFile(filepath.Join(hookSessionDir(root), id+".json"))
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
	return writeHookJSONAtomic(filepath.Join(hookSessionDir(root), id+".json"), session)
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

func readHookStats(root string) *hookStats {
	data, err := os.ReadFile(filepath.Join(hookCacheDir(root), "stats.json"))
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
	return writeHookJSONAtomic(filepath.Join(hookCacheDir(root), "stats.json"), stats)
}

func patchHookStats(root string, patch hookStatsPatch) (hookStats, error) {
	stats := emptyHookStats()
	if current := readHookStats(root); current != nil {
		stats = *current
	}
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
	return stats, writeHookStats(root, stats)
}

func writeHookJSONAtomic(path string, value any) (err error) {
	data, err := jsonjs.Marshal(value, "  ")
	if err != nil {
		return fmt.Errorf("encode %s: %w", path, err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("create state directory: %w", err)
	}
	temporary, err := os.CreateTemp(filepath.Dir(path), filepath.Base(path)+".*.tmp")
	if err != nil {
		return fmt.Errorf("create temporary state file: %w", err)
	}
	defer func() {
		_ = temporary.Close()
		_ = os.Remove(temporary.Name())
	}()
	if _, err := temporary.Write(data); err != nil {
		return fmt.Errorf("write temporary state file: %w", err)
	}
	if err := temporary.Close(); err != nil {
		return fmt.Errorf("close temporary state file: %w", err)
	}
	if err := os.Chmod(temporary.Name(), 0o644); err != nil {
		return fmt.Errorf("set state file mode: %w", err)
	}
	if err := os.Rename(temporary.Name(), path); err != nil {
		return fmt.Errorf("replace state file: %w", err)
	}
	return nil
}
