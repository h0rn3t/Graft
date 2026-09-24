package main

import (
	"encoding/json/v2"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"testing"
	"time"

	"github.com/NanoNets/context-graph-engine/internal/graph"
)

func TestHookStatsStateContract(t *testing.T) {
	root := t.TempDir()
	if got := readHookStats(root); got != nil {
		t.Fatalf("readHookStats(%q) = %#v, want nil", root, got)
	}

	want := emptyHookStats()
	want.NodeCount = 319
	want.EdgeCount = 730
	want.Languages = []string{"Go"}
	if err := writeHookStats(root, want); err != nil {
		t.Fatalf("writeHookStats(%q, %#v) error = %v, want nil", root, want, err)
	}
	syncedAt := "2026-09-24T10:00:00.000Z"
	lastFile := "cmd/graft/main.go"
	got, err := patchHookStats(root, hookStatsPatch{
		NodeCount:   new(320),
		TotalCount:  new(12),
		ReadyCount:  new(11),
		Dirty:       new(true),
		StaleCount:  new(4),
		Syncing:     new(true),
		SyncedAtSet: true,
		SyncedAt:    &syncedAt,
		LastFileSet: true,
		LastFile:    &lastFile,
	})
	if err != nil {
		t.Fatalf("patchHookStats(%q, %#v) error = %v, want nil", root, got, err)
	}
	if got.NodeCount != 320 || got.EdgeCount != 730 || got.TotalCount != 12 || got.ReadyCount != 11 || !got.Dirty || got.StaleCount != 4 || !got.Syncing || got.SyncedAt == nil || *got.SyncedAt != syncedAt || got.LastFile == nil || *got.LastFile != lastFile {
		t.Errorf("patchHookStats(%q) = %#v, want full partial stats merge", root, got)
	}
	got, err = patchHookStats(root, hookStatsPatch{
		EdgeCount:   new(731),
		Languages:   &[]string{"Go", "TypeScript"},
		SyncedAtSet: true,
		LastFileSet: true,
	})
	if err != nil {
		t.Fatalf("patchHookStats(%q, %#v) error = %v, want nil", root, got, err)
	}
	if got.SyncedAt != nil || got.LastFile != nil || got.EdgeCount != 731 || !slices.Equal(got.Languages, []string{"Go", "TypeScript"}) {
		t.Errorf("patchHookStats(%q) = %#v, want nullable fields cleared and remaining values updated", root, got)
	}
	if reread := readHookStats(root); reread == nil || !reflect.DeepEqual(*reread, got) {
		t.Errorf("readHookStats(%q) after patch = %#v, want %#v", root, reread, got)
	}
}

func TestHookSessionStateContract(t *testing.T) {
	root := t.TempDir()
	want := readHookSession(root, "abc")
	wantDefault := sessionState{
		PerAgentQuery:    map[string]string{},
		InjectedPointers: []string{},
	}
	if !reflect.DeepEqual(want, wantDefault) {
		t.Fatalf("readHookSession(%q, %q) = %#v, want %#v", root, "abc", want, wantDefault)
	}

	want.LastQuery = new("pkce")
	want.GraftReads = 2
	if err := writeHookSession(root, "abc", want); err != nil {
		t.Fatalf("writeHookSession(%q, %q, %#v) error = %v, want nil", root, "abc", want, err)
	}
	got := readHookSession(root, "abc")
	if got.LastQuery == nil || *got.LastQuery != "pkce" || got.GraftReads != 2 {
		t.Errorf("readHookSession(%q, %q) = %#v, want saved query and graft reads", root, "abc", got)
	}
	if other := readHookSession(root, "xyz"); other.GraftReads != 0 {
		t.Errorf("readHookSession(%q, %q) = %#v, want isolated empty default", root, "xyz", other)
	}
	if got := listHookSessionIDs(root); !slices.Equal(got, []string{"abc"}) {
		t.Errorf("listHookSessionIDs(%q) = %v, want [abc]", root, got)
	}
	if err := os.WriteFile(filepath.Join(hookSessionDir(root), "ignored.txt"), nil, 0o644); err != nil {
		t.Fatalf("os.WriteFile(non-session file) error = %v, want nil", err)
	}
	if got := listHookSessionIDs(root); !slices.Equal(got, []string{"abc"}) {
		t.Errorf("listHookSessionIDs(%q) with non-JSON file = %v, want [abc]", root, got)
	}
}

func TestHookSessionStateWireFormat(t *testing.T) {
	root := t.TempDir()
	query := "pkce"
	want := sessionState{
		LastQuery:        &query,
		PerAgentQuery:    map[string]string{"Explore": "auth"},
		GraftReads:       2,
		SourceReads:      1,
		SavedTokens:      3000,
		InjectedPointers: []string{"src/a.ts:L1"},
		Nudges:           1,
	}
	if err := writeHookSession(root, "wire", want); err != nil {
		t.Fatalf("writeHookSession(%q, %q, %#v) error = %v, want nil", root, "wire", want, err)
	}
	data, err := os.ReadFile(filepath.Join(hookSessionDir(root), "wire.json"))
	if err != nil {
		t.Fatalf("os.ReadFile(session) error = %v, want nil", err)
	}
	wantJSON := `{
  "lastQuery": "pkce",
  "perAgentQuery": {
    "Explore": "auth"
  },
  "graftReads": 2,
  "sourceReads": 1,
  "savedTokens": 3000,
  "injectedPointers": [
    "src/a.ts:L1"
  ],
  "nudges": 1
}`
	if string(data) != wantJSON {
		t.Errorf("session JSON = %q, want %q", data, wantJSON)
	}
}

func TestHookContextDirContract(t *testing.T) {
	root := t.TempDir()
	absolute := filepath.Join(t.TempDir(), "context")
	for _, test := range []struct {
		name      string
		value     string
		want      string
		wantCache string
	}{
		{name: "default", want: filepath.Join(root, "graft"), wantCache: filepath.Join(root, "graft", ".cache")},
		{name: "relative", value: ".repo-docs/graft", want: filepath.Join(root, ".repo-docs", "graft"), wantCache: filepath.Join(root, ".repo-docs", "graft", ".cache")},
		{name: "absolute", value: absolute, want: absolute, wantCache: filepath.Join(absolute, ".cache")},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Setenv("GRAFT_DIR", test.value)
			if got := hookContextDir(root); got != test.want {
				t.Errorf("hookContextDir(%q) with GRAFT_DIR=%q = %q, want %q", root, test.value, got, test.want)
			}
			if got := hookCacheDir(root); got != test.wantCache {
				t.Errorf("hookCacheDir(%q) with GRAFT_DIR=%q = %q, want %q", root, test.value, got, test.wantCache)
			}
		})
	}
}

func TestHookSyncLockContract(t *testing.T) {
	cache := t.TempDir()
	acquired, err := graph.AcquireLock(cache)
	if err != nil {
		t.Fatalf("graph.AcquireLock(%q) error = %v, want nil", cache, err)
	}
	if !acquired {
		t.Fatalf("graph.AcquireLock(%q) = false, want true", cache)
	}
	lockPath := filepath.Join(cache, ".sync.lock")
	data, err := os.ReadFile(lockPath)
	if err != nil {
		t.Fatalf("os.ReadFile(%q) error = %v, want nil", lockPath, err)
	}
	var payload struct {
		PID int       `json:"pid"`
		At  time.Time `json:"at"`
	}
	if err := json.Unmarshal(data, &payload); err != nil {
		t.Fatalf("json.Unmarshal(%q, %q) error = %v, want nil", data, lockPath, err)
	}
	if payload.PID != os.Getpid() || payload.At.IsZero() {
		t.Errorf("sync lock = %q, want current pid and timestamp", data)
	}
	if acquired, err := graph.AcquireLock(cache); err != nil || acquired {
		t.Errorf("second graph.AcquireLock(%q) = (%t, %v), want (false, nil)", cache, acquired, err)
	}
	graph.ReleaseLock(cache)
	if acquired, err := graph.AcquireLock(cache); err != nil || !acquired {
		t.Errorf("graph.AcquireLock(%q) after release = (%t, %v), want (true, nil)", cache, acquired, err)
	}
	graph.ReleaseLock(cache)

	if err := os.WriteFile(lockPath, []byte(`{"pid":1,"at":"2000-01-01T00:00:00.000Z"}`), 0o644); err != nil {
		t.Fatalf("os.WriteFile(%q) error = %v, want nil", lockPath, err)
	}
	stale := time.Now().Add(-6 * time.Minute)
	if err := os.Chtimes(lockPath, stale, stale); err != nil {
		t.Fatalf("os.Chtimes(%q) error = %v, want nil", lockPath, err)
	}
	if acquired, err := graph.AcquireLock(cache); err != nil || !acquired {
		t.Errorf("graph.AcquireLock(%q) with stale lock = (%t, %v), want (true, nil)", cache, acquired, err)
	}
}

func TestWriteHookJSONAtomicMode(t *testing.T) {
	target := filepath.Join(t.TempDir(), "state.json")
	if err := writeHookJSONAtomic(target, map[string]string{"state": "ready"}); err != nil {
		t.Fatalf("writeHookJSONAtomic(%q) error = %v, want nil", target, err)
	}
	info, err := os.Stat(target)
	if err != nil {
		t.Fatalf("os.Stat(%q) error = %v, want nil", target, err)
	}
	if got := info.Mode().Perm(); got != 0o644 {
		t.Errorf("writeHookJSONAtomic(%q) mode = %o, want 644", target, got)
	}
}

func TestWriteHookJSONAtomicFailureLeavesNoScratch(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "out.json")
	if err := os.Mkdir(target, 0o755); err != nil {
		t.Fatalf("os.Mkdir(%q) error = %v, want nil", target, err)
	}
	if err := writeHookJSONAtomic(target, map[string]string{"pad": string(make([]byte, 1024))}); err == nil {
		t.Fatalf("writeHookJSONAtomic(%q) error = nil, want failure", target)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("os.ReadDir(%q) error = %v, want nil", dir, err)
	}
	for _, entry := range entries {
		if filepath.Ext(entry.Name()) == ".tmp" {
			t.Errorf("writeHookJSONAtomic(%q) left scratch file %q", target, entry.Name())
		}
	}
	names := slices.Collect(entryNames(entries))
	slices.Sort(names)
	if !reflect.DeepEqual(names, []string{"out.json"}) {
		t.Errorf("writeHookJSONAtomic(%q) entries = %v, want only target", target, names)
	}
}

func entryNames(entries []os.DirEntry) func(func(string) bool) {
	return func(yield func(string) bool) {
		for _, entry := range entries {
			if !yield(entry.Name()) {
				return
			}
		}
	}
}
