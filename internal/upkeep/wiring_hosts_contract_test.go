package upkeep

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/h0rn3t/Graft/internal/hosts"
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
			name:  "shared agents section names no host: the stamp decides",
			files: map[string]string{"AGENTS.md": "<!-- graft:start -->\nowned\n<!-- graft:end -->\n"},
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

func TestRewriteWiringContract(t *testing.T) {
	wantBlock := "<!-- graft:start -->\n" + hosts.InstructionBody() + "\n<!-- graft:end -->"
	tests := []struct {
		name         string
		hosts        []string
		options      WiringOptions
		settings     string
		wantSettings bool
		wantError    bool
		wantNoWrite  bool
		nilContext   bool
		wantCursor   bool
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
			name:         "every registered host is rewritten, not only Gemini",
			hosts:        []string{"gemini", "cursor"},
			options:      WiringOptions{Global: true, MCP: true, Hooks: true, Statusline: true},
			settings:     `{"theme":"dark","mcpServers":{"other":{"command":"other"}}}`,
			wantSettings: true,
			wantCursor:   true,
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
			t.Setenv("GRAFT_MCP_COMMAND", "graft")
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
			env := hosts.Env{Home: home, BakedDir: "/pkg", Launch: hosts.ServerEntry()}
			err := RewriteWiring(ctx, root, tt.hosts, tt.options, env)
			if _, statErr := os.Stat(filepath.Join(root, ".cursor", "rules", "graft.mdc")); (statErr == nil) != tt.wantCursor {
				t.Errorf("RewriteWiring(%q, %v) Cursor rule present = %t, want %t", root, tt.hosts, statErr == nil, tt.wantCursor)
			}
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
				} else if entry, ok := bucket["graft"].(map[string]any); !ok || entry["command"] != "graft" || !reflect.DeepEqual(entry["args"], []any{"mcp"}) {
					t.Errorf("RewriteWiring(%q, %q, %v, %+v) mcpServers.graft = %v, want the graft launch", root, home, tt.hosts, tt.options, bucket["graft"])
				}
			} else if tt.settings != "" && (err != nil || string(gotSettings) != tt.settings) {
				t.Errorf("RewriteWiring(%q, %q, %v, %+v) settings = %q, %v, want unchanged %q", root, home, tt.hosts, tt.options, gotSettings, err, tt.settings)
			}
		})
	}
}

func TestRewriteWiringUpgradesClaudeHooksKeepingUserHooks(t *testing.T) {
	root := t.TempDir()
	home := t.TempDir()
	t.Setenv("GRAFT_MCP_COMMAND", "graft")
	settingsPath := filepath.Join(root, ".claude", "settings.json")
	legacy := `{"hooks":{
		"PostToolUse":[
			{"matcher":"Write","hooks":[{"type":"command","command":"echo user"}]},
			{"matcher":"Bash|mcp__graft__|Read|Grep|Glob","hooks":[{"type":"command","command":"node \"${CLAUDE_PROJECT_DIR:-.}/.claude/helpers/graft-hooks.cjs\" tool-savings","timeout":8}]}
		],
		"Stop":[{"hooks":[{"type":"command","command":"node \"${CLAUDE_PROJECT_DIR:-.}/.claude/helpers/graft-hooks.cjs\" stop","timeout":8}]}]
	}}`
	if err := os.MkdirAll(filepath.Dir(settingsPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(settingsPath, []byte(legacy), 0o644); err != nil {
		t.Fatal(err)
	}
	env := hosts.Env{Home: home, BakedDir: "/pkg", Launch: hosts.ServerEntry()}
	options := WiringOptions{Hooks: true, Statusline: true}
	if err := RewriteWiring(t.Context(), root, []string{"claude"}, options, env); err != nil {
		t.Fatalf("RewriteWiring(%q, claude) error = %v, want nil", root, err)
	}
	data, err := os.ReadFile(settingsPath)
	if err != nil {
		t.Fatal(err)
	}
	got := string(data)
	for _, want := range []string{`"echo user"`, `"matcher": "Grep|Bash"`, `"SubagentStop"`} {
		if !strings.Contains(got, want) {
			t.Errorf("RewriteWiring(legacy claude settings) settings = %s, want %s", got, want)
		}
	}
	if strings.Contains(got, "Bash|mcp__graft__|Read|Grep|Glob") {
		t.Errorf("RewriteWiring(legacy claude settings) settings = %s, want the legacy matcher gone", got)
	}
}
