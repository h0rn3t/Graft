package upkeep

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/NanoNets/context-graph-engine/internal/brain"
)

func TestUpdateCacheContract(t *testing.T) {
	now := time.Unix(1_000, 0)
	home := t.TempDir()
	latest := "99.0.0"
	want := UpdateCache{Latest: &latest, CheckedAt: now.UnixMilli()}
	if err := WriteUpdateCache(home, want); err != nil {
		t.Fatalf("WriteUpdateCache(%q, %#v) error = %v, want nil", home, want, err)
	}
	got, ok := ReadUpdateCache(home)
	if !ok || got == nil || got.CheckedAt != want.CheckedAt || got.Latest == nil || *got.Latest != latest {
		t.Errorf("ReadUpdateCache(%q) = %#v, %t, want %#v, true", home, got, ok, want)
	}
	if _, err := os.Stat(filepath.Join(home, ".graft", "update-check.json")); err != nil {
		t.Errorf("ReadUpdateCache(%q) cache path Stat error = %v, want nil", home, err)
	}
}

func TestUpdateCacheMissingAndInvalidContract(t *testing.T) {
	home := t.TempDir()
	if got, ok := ReadUpdateCache(home); ok || got != nil {
		t.Errorf("ReadUpdateCache(%q) = %#v, %t, want nil, false for missing cache", home, got, ok)
	}
	path := filepath.Join(home, ".graft", "update-check.json")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("MkdirAll(%q) error = %v, want nil", filepath.Dir(path), err)
	}
	if err := os.WriteFile(path, []byte(`{"latest": "not-a-cache"}`), 0o644); err != nil {
		t.Fatalf("WriteFile(%q) error = %v, want nil", path, err)
	}
	if got, ok := ReadUpdateCache(home); ok || got != nil {
		t.Errorf("ReadUpdateCache(%q) = %#v, %t, want nil, false for invalid cache", home, got, ok)
	}
}

func TestUpdateNudgeContract(t *testing.T) {
	for _, tt := range []struct {
		name    string
		current string
		latest  string
		want    string
	}{
		{name: "newer", current: "1.2.3", latest: "1.3.0", want: "⬆ graft 1.2.3 → 1.3.0 available: run `npm i -g @nanonets/graft@latest` (restart your agent after)."},
		{name: "equal", current: "1.2.3", latest: "1.2.3"},
		{name: "older", current: "2.0.0", latest: "1.9.9"},
		{name: "prerelease compares release part", current: "1.2.3-beta.1", latest: "1.2.3"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			got := FormatUpdateNudge(tt.current, tt.latest)
			if got != tt.want {
				t.Errorf("FormatUpdateNudge(%q, %q) = %q, want %q", tt.current, tt.latest, got, tt.want)
			}
		})
	}
}

func TestNeedsRefreshContract(t *testing.T) {
	now := time.Now()
	latest := "1.0.0"
	for _, tt := range []struct {
		name  string
		cache *UpdateCache
		want  bool
	}{
		{name: "missing", want: true},
		{name: "fresh", cache: &UpdateCache{Latest: &latest, CheckedAt: now.Add(-UpdateTTL + time.Millisecond).UnixMilli()}, want: false},
		{name: "expired", cache: &UpdateCache{Latest: &latest, CheckedAt: now.Add(-UpdateTTL).UnixMilli()}, want: true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if got := NeedsRefresh(tt.cache, now); got != tt.want {
				t.Errorf("NeedsRefresh(%#v, %v) = %t, want %t", tt.cache, now, got, tt.want)
			}
		})
	}
}

func TestStartupLinesContract(t *testing.T) {
	home := t.TempDir()
	latest := "99.0.0"
	if err := WriteUpdateCache(home, UpdateCache{Latest: &latest, CheckedAt: time.Now().UnixMilli()}); err != nil {
		t.Fatalf("WriteUpdateCache(%q) error = %v, want nil", home, err)
	}
	lines := StartupLines("1.0.0", home)
	if len(lines) != 1 || !strings.Contains(lines[0], "99.0.0 available") {
		t.Errorf("StartupLines(%q, %q) = %#v, want one newer-version nudge", "1.0.0", home, lines)
	}
}

func TestStartupLinesUsesStaleCacheContract(t *testing.T) {
	now := time.Unix(100_000, 0)
	home := t.TempDir()
	latest := "99.0.0"
	if err := WriteUpdateCache(home, UpdateCache{Latest: &latest, CheckedAt: now.Add(-UpdateTTL).UnixMilli()}); err != nil {
		t.Fatalf("WriteUpdateCache(%q) error = %v, want nil", home, err)
	}
	lines := StartupLines("1.0.0", home)
	if len(lines) != 1 || !strings.Contains(lines[0], "99.0.0 available") {
		t.Errorf("StartupLines(%q, %q) = %#v, want stale cached version nudge", "1.0.0", home, lines)
	}
}

func TestMaybeRefreshInBackgroundDoesNotWriteFromTestBinary(t *testing.T) {
	home := t.TempDir()
	if MaybeRefreshInBackground(home, time.Now()) {
		t.Errorf("MaybeRefreshInBackground(%q) = true, want false from test binary", home)
	}
	if _, err := os.Stat(filepath.Join(home, ".graft", "update-check.json")); !os.IsNotExist(err) {
		t.Errorf("MaybeRefreshInBackground(%q) cache Stat error = %v, want not exist", home, err)
	}
}

func TestMaybeRefreshBrainRulesContract(t *testing.T) {
	now := time.UnixMilli(10_000_000)
	for _, tt := range []struct {
		name         string
		linked       bool
		initialCache []byte
		wantChecked  bool
	}{
		{name: "unlinked repository is a no-op"},
		{name: "fresh cache is not touched", linked: true, initialCache: []byte(`{"brainId":"brain","fetchedAt":6400000,"rules":[{"rule":"rule"}]}`)},
		{name: "stale cache records attempted check before test spawn is skipped", linked: true, initialCache: []byte(`{"brainId":"brain","fetchedAt":-15200000,"rules":[{"rule":"rule"}]}`), wantChecked: true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			root := t.TempDir()
			contextDir := filepath.Join(root, "graft")
			cachePath := filepath.Join(contextDir, ".cache", "brain-rules.json")
			t.Setenv("GRAFT_BRAIN_ID", "")
			t.Setenv("GRAFT_BRAIN_TOKEN", "")
			t.Setenv("GRAFT_BRAIN_URL", "")
			t.Setenv("GRAFT_DIR", "")
			if tt.linked {
				if err := os.MkdirAll(filepath.Join(root, ".graft"), 0o755); err != nil {
					t.Fatalf("MkdirAll(%q) error = %v, want nil", filepath.Join(root, ".graft"), err)
				}
				if err := os.WriteFile(filepath.Join(root, ".graft", "config.json"), []byte(`{"brain":{"brainId":"brain","token":"token"}}`), 0o644); err != nil {
					t.Fatalf("WriteFile(%q) error = %v, want nil", filepath.Join(root, ".graft", "config.json"), err)
				}
			}
			if tt.initialCache != nil {
				if err := os.MkdirAll(filepath.Dir(cachePath), 0o755); err != nil {
					t.Fatalf("MkdirAll(%q) error = %v, want nil", filepath.Dir(cachePath), err)
				}
				if err := os.WriteFile(cachePath, tt.initialCache, 0o644); err != nil {
					t.Fatalf("WriteFile(%q) error = %v, want nil", cachePath, err)
				}
			}

			if got := MaybeRefreshBrainRules(root, contextDir, now); got {
				t.Errorf("MaybeRefreshBrainRules(%q, %q, %v) = true, want false from test binary", root, contextDir, now)
			}
			data, err := os.ReadFile(cachePath)
			if tt.initialCache == nil {
				if !os.IsNotExist(err) {
					t.Errorf("ReadFile(%q) error = %v, want not exist", cachePath, err)
				}
				return
			}
			if err != nil {
				t.Fatalf("ReadFile(%q) error = %v, want nil", cachePath, err)
			}
			if tt.wantChecked {
				var cache brain.RulesCache
				if err := json.Unmarshal(data, &cache); err != nil {
					t.Fatalf("json.Unmarshal(brain cache) error = %v, want nil", err)
				}
				if cache.CheckedAt == nil || *cache.CheckedAt != now.UnixMilli() {
					t.Errorf("MaybeRefreshBrainRules(%q, %q, %v) checkedAt = %v, want %d", root, contextDir, now, cache.CheckedAt, now.UnixMilli())
				}
				return
			}
			if !bytes.Equal(data, tt.initialCache) {
				t.Errorf("MaybeRefreshBrainRules(%q, %q, %v) changed a fresh cache: got %s, want %s", root, contextDir, now, data, tt.initialCache)
			}
		})
	}
}
