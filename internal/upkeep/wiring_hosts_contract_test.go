package upkeep

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestWiredHostIDsContract(t *testing.T) {
	tests := []struct {
		name  string
		files map[string]string
		want  []string
	}{
		{name: "no managed host targets"},
		{
			name:  "gemini section",
			files: map[string]string{"GEMINI.md": "<!-- graft:start -->\nowned\n<!-- graft:end -->\n"},
			want:  []string{"gemini"},
		},
		{
			name:  "section without marker is user-owned only",
			files: map[string]string{"GEMINI.md": "User instructions\n"},
		},
		{
			name:  "shared agents section reports every registered host",
			files: map[string]string{"AGENTS.md": "<!-- graft:start -->\nowned\n<!-- graft:end -->\n"},
			want:  []string{"agents", "hermes", "antigravity"},
		},
		{
			name: "owned cursor file and claude hook marker",
			files: map[string]string{
				".cursor/rules/graft.mdc":         "owned",
				".claude/helpers/graft-hooks.cjs": "owned",
			},
			want: []string{"claude", "cursor"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			root := t.TempDir()
			for name, content := range tt.files {
				path := filepath.Join(root, name)
				if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
					t.Fatalf("MkdirAll(%q) error = %v, want nil", filepath.Dir(path), err)
				}
				if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
					t.Fatalf("WriteFile(%q) error = %v, want nil", path, err)
				}
			}
			got := WiredHostIDs(root)
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("WiredHostIDs(%q) = %v, want %v", root, got, tt.want)
			}
		})
	}
}

func TestRewriteWiringGeminiContract(t *testing.T) {
	golden, err := os.ReadFile("templates/gemini-instructions.md")
	if err != nil {
		t.Fatalf("ReadFile(%q) error = %v", "templates/gemini-instructions.md", err)
	}
	wantBlock := "<!-- graft:start -->\n" + strings.TrimSuffix(string(golden), "\n") + "\n<!-- graft:end -->"
	tests := []struct {
		name         string
		hosts        []string
		options      WiringOptions
		settings     string
		wantSettings bool
		wantError    bool
		wantNoWrite  bool
		nilContext   bool
	}{
		{
			name:         "replace the section and preserve other JSON config",
			hosts:        []string{"gemini"},
			options:      WiringOptions{Global: true, MCP: true, Hooks: true, Statusline: true},
			settings:     `{"theme":"dark","mcpServers":{"other":{"command":"other"},"graft":{"command":"old"}}}`,
			wantSettings: true,
		},
		{
			name:         "no-mcp preserves the existing Gemini settings file",
			hosts:        []string{"gemini"},
			options:      WiringOptions{Global: true, MCP: false, Hooks: true, Statusline: true},
			settings:     `{"mcpServers":{"other":{"command":"other"}}}`,
			wantSettings: false,
		},
		{
			name:         "unparseable settings are skipped without failing the instruction write",
			hosts:        []string{"gemini"},
			options:      WiringOptions{Global: true, MCP: true, Hooks: true, Statusline: true},
			settings:     "{",
			wantSettings: false,
		},
		{
			name:        "unsupported host aborts before writing any target",
			hosts:       []string{"gemini", "cursor"},
			options:     WiringOptions{Global: true, MCP: true, Hooks: true, Statusline: true},
			settings:    `{"mcpServers":{"other":{"command":"other"}}}`,
			wantError:   true,
			wantNoWrite: true,
		},
		{
			name:        "nil context is rejected before writing any target",
			hosts:       []string{"gemini"},
			options:     WiringOptions{MCP: true},
			settings:    `{"theme":"dark"}`,
			wantError:   true,
			wantNoWrite: true,
			nilContext:  true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			root := t.TempDir()
			home := t.TempDir()
			t.Setenv("GRAFT_MCP_NPX", "1")
			geminiPath := filepath.Join(root, "GEMINI.md")
			oldGemini := "# Project notes\n\n<!-- graft:start -->\nold instructions\n<!-- graft:end -->\n\nkeep this line\n"
			if err := os.WriteFile(geminiPath, []byte(oldGemini), 0o644); err != nil {
				t.Fatalf("WriteFile(%q) error = %v, want nil", geminiPath, err)
			}
			settingsPath := filepath.Join(root, ".gemini", "settings.json")
			if tt.settings != "" {
				if err := os.MkdirAll(filepath.Dir(settingsPath), 0o755); err != nil {
					t.Fatalf("MkdirAll(%q) error = %v, want nil", filepath.Dir(settingsPath), err)
				}
				if err := os.WriteFile(settingsPath, []byte(tt.settings), 0o644); err != nil {
					t.Fatalf("WriteFile(%q) error = %v, want nil", settingsPath, err)
				}
			}

			ctx := t.Context()
			if tt.nilContext {
				ctx = nil
			}
			err := RewriteWiring(ctx, root, tt.hosts, tt.options)
			if (err != nil) != tt.wantError {
				t.Fatalf("RewriteWiring(%q, %q, %v, %+v) error = %v, want error presence %t", root, home, tt.hosts, tt.options, err, tt.wantError)
			}
			gotGemini, err := os.ReadFile(geminiPath)
			if err != nil {
				t.Fatalf("ReadFile(%q) error = %v, want nil", geminiPath, err)
			}
			if tt.wantNoWrite {
				if string(gotGemini) != oldGemini {
					t.Errorf("RewriteWiring(%q, %q, %v, %+v) GEMINI.md = %q, want unchanged %q", root, home, tt.hosts, tt.options, gotGemini, oldGemini)
				}
				if got, err := os.ReadFile(settingsPath); err != nil || string(got) != tt.settings {
					t.Errorf("RewriteWiring(%q, %q, %v, %+v) settings = %q, %v, want unchanged %q", root, home, tt.hosts, tt.options, got, err, tt.settings)
				}
				return
			}
			if !strings.Contains(string(gotGemini), wantBlock) || !strings.HasPrefix(string(gotGemini), "# Project notes\n\n") || !strings.HasSuffix(string(gotGemini), "\nkeep this line\n") {
				t.Errorf("RewriteWiring(%q, %q, %v, %+v) GEMINI.md = %q, want updated managed block with user text preserved", root, home, tt.hosts, tt.options, gotGemini)
			}
			gotSettings, err := os.ReadFile(settingsPath)
			if tt.wantSettings {
				if err != nil {
					t.Fatalf("ReadFile(%q) after RewriteWiring error = %v, want nil", settingsPath, err)
				}
				var settings map[string]any
				if err := json.Unmarshal(gotSettings, &settings); err != nil {
					t.Fatalf("Unmarshal(%q) error = %v, want nil", settingsPath, err)
				}
				if settings["theme"] != "dark" {
					t.Errorf("RewriteWiring(%q, %q, %v, %+v) settings theme = %v, want dark", root, home, tt.hosts, tt.options, settings["theme"])
				}
				bucket, ok := settings["mcpServers"].(map[string]any)
				if !ok || bucket["other"] == nil {
					t.Errorf("RewriteWiring(%q, %q, %v, %+v) mcpServers = %v, want preserved other server", root, home, tt.hosts, tt.options, settings["mcpServers"])
				} else if entry, ok := bucket["graft"].(map[string]any); !ok || entry["command"] != "npx" || !reflect.DeepEqual(entry["args"], []any{"-y", "@nanonets/graft", "mcp"}) {
					t.Errorf("RewriteWiring(%q, %q, %v, %+v) mcpServers.graft = %v, want NPX launch", root, home, tt.hosts, tt.options, bucket["graft"])
				}
			} else if tt.settings != "" && (err != nil || string(gotSettings) != tt.settings) {
				t.Errorf("RewriteWiring(%q, %q, %v, %+v) settings = %q, %v, want unchanged %q", root, home, tt.hosts, tt.options, gotSettings, err, tt.settings)
			}
		})
	}
}
