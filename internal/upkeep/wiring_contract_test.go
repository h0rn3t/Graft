package upkeep

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"
)

func TestReconcileWiringRewritesLegacyShimAtCurrentVersion(t *testing.T) {
	root := t.TempDir()
	shim := filepath.Join(root, ".claude", "helpers", "graft-hooks.cjs")
	if err := os.MkdirAll(filepath.Dir(shim), 0o755); err != nil {
		t.Fatalf("os.MkdirAll(%q) error = %v, want nil", filepath.Dir(shim), err)
	}
	if err := os.WriteFile(shim, []byte("const { pathToFileURL } = require('url');\n"), 0o755); err != nil {
		t.Fatalf("os.WriteFile(%q) error = %v, want nil", shim, err)
	}
	stamp := filepath.Join(root, "graft", ".cache", "wiring-stamp.json")
	if err := os.MkdirAll(filepath.Dir(stamp), 0o755); err != nil {
		t.Fatalf("os.MkdirAll(%q) error = %v, want nil", filepath.Dir(stamp), err)
	}
	if err := os.WriteFile(stamp, []byte(`{"version":"2.0.0","hosts":["claude"],"opts":{"global":false},"at":"old"}`), 0o644); err != nil {
		t.Fatalf("os.WriteFile(%q) error = %v, want nil", stamp, err)
	}
	writes := 0
	got := ReconcileWiring(root, "", "2.0.0", time.Now(),
		func(string) ([]string, error) { return []string{"claude"}, nil },
		func(_ string, ids []string, options WiringOptions) error {
			writes++
			if !reflect.DeepEqual(ids, []string{"claude"}) || options.Global {
				t.Errorf("rewrite args = (%v, %+v), want claude/no-global", ids, options)
			}
			return nil
		})
	if writes != 1 || got == "" {
		t.Errorf("ReconcileWiring(legacy shim) = (%q, %d writes), want one refresh", got, writes)
	}
}

func TestReconcileWiringContract(t *testing.T) {
	now := time.Date(2026, 9, 22, 10, 20, 30, 456_000_000, time.FixedZone("UTC+3", 3*60*60))
	defaultOptions := WiringOptions{Global: true, MCP: true, Hooks: true, Statusline: true}
	// Without a stamp nobody chose machine-wide writes.
	noStampOptions := WiringOptions{MCP: true, Hooks: true, Statusline: true}
	tests := []struct {
		name                  string
		contextDir            string
		stamp                 []byte
		stampDirectory        bool
		nilWired              bool
		nilRewrite            bool
		wired                 []string
		wiredError            bool
		rewriteError          bool
		mutateHosts           bool
		wantWiredCalls        int
		wantRewrite           bool
		wantHosts             []string
		wantOptions           WiringOptions
		wantNote              string
		wantStoredStamp       bool
		wantOriginalUnchanged bool
	}{
		{
			name:                  "current version is a no-op",
			stamp:                 []byte(`{"version":"2.0.0","hosts":["agents"],"opts":{"global":true},"at":"old"}`),
			wantOriginalUnchanged: true,
		},
		{
			name:            "context override controls the stamp directory",
			contextDir:      ".graft-context",
			stamp:           []byte(`{"version":"1.0.0","hosts":["gemini"],"at":"old"}`),
			wired:           []string{"gemini"},
			wantWiredCalls:  1,
			wantRewrite:     true,
			wantHosts:       []string{"gemini"},
			wantOptions:     defaultOptions,
			wantNote:        "· graft refreshed this repo's agent wiring (written by 1.0.0, now 2.0.0): gemini.",
			wantStoredStamp: true,
		},
		{
			name:           "never wired repository is left alone",
			wantWiredCalls: 1,
		},
		{
			name:     "nil host scanner is fail-soft",
			nilWired: true,
		},
		{
			name:       "nil writer is fail-soft",
			nilRewrite: true,
		},
		{
			name:            "unstamped hosts are sorted and get no machine-wide writes",
			wired:           []string{"gemini", "agents", "gemini"},
			wantWiredCalls:  1,
			wantRewrite:     true,
			wantHosts:       []string{"agents", "gemini"},
			wantOptions:     noStampOptions,
			wantNote:        "· graft refreshed this repo's agent wiring (written by unwired, now 2.0.0): agents, gemini.",
			wantStoredStamp: true,
			mutateHosts:     true,
		},
		{
			name:            "stamped and on-disk hosts are unioned and options replayed",
			stamp:           []byte(`{"version":"1.0.0","hosts":["agents","copilot","agents"],"opts":{"global":true,"mcp":false,"statusline":false},"at":"old"}`),
			wired:           []string{"agents", "claude"},
			wantWiredCalls:  1,
			wantRewrite:     true,
			wantHosts:       []string{"agents", "claude", "copilot"},
			wantOptions:     WiringOptions{Global: true, MCP: false, Hooks: true, Statusline: false},
			wantNote:        "· graft refreshed this repo's agent wiring (including this machine's ~/.codex config) (written by 1.0.0, now 2.0.0): agents, claude, copilot.",
			wantStoredStamp: true,
		},
		{
			name:            "no-global choice is replayed without the machine-wide note",
			stamp:           []byte(`{"version":"1.0.0","hosts":["agents"],"opts":{"global":false},"at":"old"}`),
			wantWiredCalls:  1,
			wantRewrite:     true,
			wantHosts:       []string{"agents"},
			wantOptions:     WiringOptions{MCP: true, Hooks: true, Statusline: true},
			wantNote:        "· graft refreshed this repo's agent wiring (written by 1.0.0, now 2.0.0): agents.",
			wantStoredStamp: true,
		},
		{
			name:            "invalid stamp falls back to on-disk hosts",
			stamp:           []byte("{"),
			wired:           []string{"gemini"},
			wantWiredCalls:  1,
			wantRewrite:     true,
			wantHosts:       []string{"gemini"},
			wantOptions:     noStampOptions,
			wantNote:        "· graft refreshed this repo's agent wiring (written by unwired, now 2.0.0): gemini.",
			wantStoredStamp: true,
		},
		{
			name:            "null stamp falls back to on-disk hosts",
			stamp:           []byte("null"),
			wired:           []string{"gemini"},
			wantWiredCalls:  1,
			wantRewrite:     true,
			wantHosts:       []string{"gemini"},
			wantOptions:     noStampOptions,
			wantNote:        "· graft refreshed this repo's agent wiring (written by unwired, now 2.0.0): gemini.",
			wantStoredStamp: true,
		},
		{
			name:            "malformed options retain stamped host intent",
			stamp:           []byte(`{"version":"1.0.0","hosts":["agents"],"opts":"invalid","at":"old"}`),
			wantWiredCalls:  1,
			wantRewrite:     true,
			wantHosts:       []string{"agents"},
			wantOptions:     defaultOptions,
			wantNote:        "· graft refreshed this repo's agent wiring (including this machine's ~/.codex config) (written by 1.0.0, now 2.0.0): agents.",
			wantStoredStamp: true,
		},
		{
			name:                  "host discovery failure is fail-soft",
			stamp:                 []byte(`{"version":"1.0.0","hosts":["agents"],"at":"old"}`),
			wiredError:            true,
			wantWiredCalls:        1,
			wantOriginalUnchanged: true,
		},
		{
			name:                  "rewrite failure keeps the previous stamp",
			stamp:                 []byte(`{"version":"1.0.0","hosts":["agents"],"at":"old"}`),
			wired:                 []string{"agents"},
			wantWiredCalls:        1,
			wantRewrite:           true,
			rewriteError:          true,
			wantHosts:             []string{"agents"},
			wantOptions:           defaultOptions,
			wantOriginalUnchanged: true,
		},
		{
			name:           "stamp write failure is fail-soft",
			stampDirectory: true,
			wired:          []string{"agents"},
			wantWiredCalls: 1,
			wantRewrite:    true,
			wantHosts:      []string{"agents"},
			wantOptions:    noStampOptions,
			wantNote:       "· graft refreshed this repo's agent wiring (written by unwired, now 2.0.0): agents.",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			root := t.TempDir()
			contextDir := tt.contextDir
			if contextDir == "" {
				contextDir = filepath.Join(root, "graft")
			} else if !filepath.IsAbs(contextDir) {
				contextDir = filepath.Join(root, contextDir)
			}
			stampPath := filepath.Join(contextDir, ".cache", "wiring-stamp.json")
			if tt.stampDirectory {
				if err := os.MkdirAll(stampPath, 0o755); err != nil {
					t.Fatalf("MkdirAll(%q) error = %v, want nil", stampPath, err)
				}
			} else if tt.stamp != nil {
				if err := os.MkdirAll(filepath.Dir(stampPath), 0o755); err != nil {
					t.Fatalf("MkdirAll(%q) error = %v, want nil", filepath.Dir(stampPath), err)
				}
				if err := os.WriteFile(stampPath, tt.stamp, 0o644); err != nil {
					t.Fatalf("WriteFile(%q) error = %v, want nil", stampPath, err)
				}
			}

			wiredCalls := 0
			rewriteCalls := 0
			gotHosts := []string(nil)
			gotOptions := WiringOptions{}
			wired := func(gotRoot string) ([]string, error) {
				wiredCalls++
				if gotRoot != root {
					t.Errorf("ReconcileWiring wired(%q) root = %q, want %q", root, gotRoot, root)
				}
				if tt.wiredError {
					return nil, errors.New("host scan failed")
				}
				return tt.wired, nil
			}
			rewrite := func(gotRoot string, hosts []string, options WiringOptions) error {
				rewriteCalls++
				if gotRoot != root {
					t.Errorf("ReconcileWiring rewrite(%q) root = %q, want %q", root, gotRoot, root)
				}
				gotHosts = append([]string(nil), hosts...)
				gotOptions = options
				if tt.mutateHosts && len(hosts) > 0 {
					hosts[0] = "mutated"
				}
				if tt.rewriteError {
					return errors.New("rewrite failed")
				}
				return nil
			}
			if tt.nilWired {
				wired = nil
			}
			if tt.nilRewrite {
				rewrite = nil
			}
			got := ReconcileWiring(root, contextDir, "2.0.0", now, wired, rewrite)
			if wiredCalls != tt.wantWiredCalls {
				t.Errorf("ReconcileWiring(%q, %q) wired calls = %d, want %d", root, "2.0.0", wiredCalls, tt.wantWiredCalls)
			}
			wantRewriteCalls := 0
			if tt.wantRewrite {
				wantRewriteCalls = 1
			}
			if rewriteCalls != wantRewriteCalls {
				t.Errorf("ReconcileWiring(%q, %q) rewrite calls = %d, want %d", root, "2.0.0", rewriteCalls, wantRewriteCalls)
			}
			if !reflect.DeepEqual(gotHosts, tt.wantHosts) {
				t.Errorf("ReconcileWiring(%q, %q) hosts = %v, want %v", root, "2.0.0", gotHosts, tt.wantHosts)
			}
			if tt.wantRewrite && gotOptions != tt.wantOptions {
				t.Errorf("ReconcileWiring(%q, %q) options = %+v, want %+v", root, "2.0.0", gotOptions, tt.wantOptions)
			}
			if got != tt.wantNote {
				t.Errorf("ReconcileWiring(%q, %q) note = %q, want %q", root, "2.0.0", got, tt.wantNote)
			}

			if tt.wantStoredStamp {
				data, err := os.ReadFile(stampPath)
				if err != nil {
					t.Fatalf("ReadFile(%q) after reconcile error = %v, want nil", stampPath, err)
				}
				var stored struct {
					Version string        `json:"version"`
					Hosts   []string      `json:"hosts"`
					Options WiringOptions `json:"opts"`
					At      string        `json:"at"`
				}
				if err := json.Unmarshal(data, &stored); err != nil {
					t.Fatalf("Unmarshal(%q) error = %v, want nil", stampPath, err)
				}
				wantStored := struct {
					Version string
					Hosts   []string
					Options WiringOptions
					At      string
				}{
					Version: "2.0.0",
					Hosts:   tt.wantHosts,
					Options: tt.wantOptions,
					At:      "2026-09-22T07:20:30.456Z",
				}
				gotStored := struct {
					Version string
					Hosts   []string
					Options WiringOptions
					At      string
				}{stored.Version, stored.Hosts, stored.Options, stored.At}
				if !reflect.DeepEqual(gotStored, wantStored) {
					t.Errorf("ReconcileWiring(%q, %q) stored stamp = %+v, want %+v", root, "2.0.0", gotStored, wantStored)
				}
			}
			if tt.wantOriginalUnchanged {
				data, err := os.ReadFile(stampPath)
				if err != nil {
					t.Fatalf("ReadFile(%q) after no-op/error = %v, want nil", stampPath, err)
				}
				if !reflect.DeepEqual(data, tt.stamp) {
					t.Errorf("ReconcileWiring(%q, %q) stamp = %q, want unchanged %q", root, "2.0.0", data, tt.stamp)
				}
			}
			if tt.stampDirectory {
				info, err := os.Stat(stampPath)
				if err != nil || !info.IsDir() {
					t.Errorf("ReconcileWiring(%q, %q) stamp path = (%v, %v), want an unchanged directory", root, "2.0.0", info, err)
				}
			}
			if !tt.wantStoredStamp && !tt.wantOriginalUnchanged && !tt.stampDirectory {
				if _, err := os.Stat(stampPath); !errors.Is(err, os.ErrNotExist) {
					t.Errorf("ReconcileWiring(%q, %q) stamp stat error = %v, want os.ErrNotExist", root, "2.0.0", err)
				}
			}
		})
	}
}
