package upkeep

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestRefreshBrainRulesWritesSelectedHostSectionsContract(t *testing.T) {
	const response = `{"anchors":[{"rule_id":"r1","symbol":"src/api.ts#ready","fingerprint":"f1","rule":"Keep UTC","source_url":"https://example.test/rules/1"}]}`
	server := brainRulesTestServer(t, response)

	const body = "## House rules for this codebase\n\n" +
		"Mined from repo's own history — commit messages and pull-request\n" +
		"discussion — by Trail. These are decisions the team has already made, so\n" +
		"follow them and do not re-litigate them in passing. Each is attributed to\n" +
		"what established it.\n\n" +
		"### src/api.ts\n- Keep UTC (https://example.test/rules/1)"
	const block = "<!-- graft:brain:start -->\n" + body + "\n<!-- graft:brain:end -->"
	for _, tt := range []struct {
		name        string
		oldAgent    string
		blockAgent  bool
		wantAgent   string
		wantCopilot bool
	}{
		{
			name:        "replace only the managed block and retain CRLF user text",
			oldAgent:    "# Notes\r\n\r\n<!-- graft:brain:start -->\r\nold\r\n<!-- graft:brain:end -->\r\nEnd\r\n",
			wantAgent:   "# Notes\r\n\r\n" + strings.ReplaceAll(block, "\n", "\r\n") + "\r\nEnd\r\n",
			wantCopilot: true,
		},
		{
			name:        "skip an unwritable target and continue",
			blockAgent:  true,
			wantCopilot: true,
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			root := prepareBrainRulesRefresh(t, server.URL, "agents", "copilot", "cursor")
			agentPath := filepath.Join(root, "AGENTS.md")
			if tt.blockAgent {
				if err := os.Mkdir(agentPath, 0o755); err != nil {
					t.Fatalf("Mkdir(%q) error = %v, want nil", agentPath, err)
				}
			} else if tt.oldAgent != "" {
				if err := os.WriteFile(agentPath, []byte(tt.oldAgent), 0o644); err != nil {
					t.Fatalf("WriteFile(%q) error = %v, want nil", agentPath, err)
				}
			}

			if err := RefreshBrainRules(t.Context(), root, filepath.Join(root, "graft"), time.Unix(1_000, 0)); err != nil {
				t.Fatalf("RefreshBrainRules(%q) error = %v, want nil", root, err)
			}
			if tt.blockAgent {
				info, err := os.Stat(agentPath)
				if err != nil || !info.IsDir() {
					t.Errorf("RefreshBrainRules(%q) AGENTS.md Stat = %v, %v, want blocked directory", root, info, err)
				}
			} else if tt.wantAgent != "" {
				got, err := os.ReadFile(agentPath)
				if err != nil {
					t.Fatalf("ReadFile(%q) error = %v, want nil", agentPath, err)
				}
				if string(got) != tt.wantAgent {
					t.Errorf("RefreshBrainRules(%q) AGENTS.md = %q, want %q", root, got, tt.wantAgent)
				}
			}

			copilotPath := filepath.Join(root, ".github", "copilot-instructions.md")
			if tt.wantCopilot {
				got, err := os.ReadFile(copilotPath)
				if err != nil {
					t.Fatalf("ReadFile(%q) error = %v, want nil", copilotPath, err)
				}
				want := block + "\n"
				if string(got) != want {
					t.Errorf("RefreshBrainRules(%q) Copilot instructions = %q, want %q", root, got, want)
				}
			}
			if _, err := os.Stat(filepath.Join(root, ".cursor", "rules", "graft.mdc")); err == nil {
				t.Errorf("RefreshBrainRules(%q) rewrote an owned Cursor file, want section hosts only", root)
			}
		})
	}
}

func TestRefreshBrainRulesRendersGroupedCappedRulesContract(t *testing.T) {
	anchors := []string{
		`{"symbol":"repository","rule":"Repository rule"}`,
		`{"symbol":"src/a.ts#first","rule":"A rule","source_url":"https://example.test/a"}`,
		`{"symbol":"src/a.ts#second","rule":"Another A rule"}`,
		`{"symbol":"src/z.ts#first","rule":"Z rule"}`,
	}
	for index := range 37 {
		anchors = append(anchors, fmt.Sprintf(`{"symbol":"src/z.ts#extra%d","rule":"Z extra %d"}`, index, index))
	}
	server := brainRulesTestServer(t, `{"anchors":[`+strings.Join(anchors, ",")+"]}")
	root := prepareBrainRulesRefresh(t, server.URL, "agents")
	if err := RefreshBrainRules(t.Context(), root, filepath.Join(root, "graft"), time.Unix(1_000, 0)); err != nil {
		t.Fatalf("RefreshBrainRules(%q) error = %v, want nil", root, err)
	}

	wantLines := []string{
		"<!-- graft:brain:start -->",
		"## House rules for this codebase",
		"",
		"Mined from repo's own history — commit messages and pull-request",
		"discussion — by Trail. These are decisions the team has already made, so",
		"follow them and do not re-litigate them in passing. Each is attributed to",
		"what established it.",
		"",
		"- Repository rule",
		"",
		"### src/a.ts",
		"- A rule (https://example.test/a)",
		"- Another A rule",
		"",
		"### src/z.ts",
		"- Z rule",
	}
	for index := range 36 {
		wantLines = append(wantLines, fmt.Sprintf("- Z extra %d", index))
	}
	wantLines = append(wantLines,
		"",
		"_1 more rules apply to specific symbols; graft attaches those to each `graft ask` answer._",
		"<!-- graft:brain:end -->",
	)
	want := strings.Join(wantLines, "\n") + "\n"
	got, err := os.ReadFile(filepath.Join(root, "AGENTS.md"))
	if err != nil {
		t.Fatalf("ReadFile(%q) error = %v, want nil", filepath.Join(root, "AGENTS.md"), err)
	}
	if string(got) != want {
		t.Errorf("RefreshBrainRules(%q) rendered AGENTS.md = %q, want %q", root, got, want)
	}
}

func TestRefreshBrainRulesFallsBackToManagedSectionHostsWithoutStampContract(t *testing.T) {
	const response = `{"anchors":[{"symbol":"src/api.ts#ready","rule":"Keep UTC"}]}`
	server := brainRulesTestServer(t, response)
	root := prepareBrainRulesRefresh(t, server.URL, "agents")
	stampPath := filepath.Join(root, "graft", ".cache", "wiring-stamp.json")
	if err := os.Remove(stampPath); err != nil {
		t.Fatalf("Remove(%q) error = %v, want nil", stampPath, err)
	}
	const userText = "# Notes\n\n<!-- graft:start -->\nGraft instructions\n<!-- graft:end -->\n"
	if err := os.WriteFile(filepath.Join(root, "AGENTS.md"), []byte(userText), 0o644); err != nil {
		t.Fatalf("WriteFile(AGENTS.md) error = %v, want nil", err)
	}
	if err := RefreshBrainRules(t.Context(), root, filepath.Join(root, "graft"), time.Unix(1_000, 0)); err != nil {
		t.Fatalf("RefreshBrainRules(%q) error = %v, want nil", root, err)
	}
	got, err := os.ReadFile(filepath.Join(root, "AGENTS.md"))
	if err != nil {
		t.Fatalf("ReadFile(AGENTS.md) error = %v, want nil", err)
	}
	if !strings.HasPrefix(string(got), userText+"\n<!-- graft:brain:start -->") {
		t.Errorf("RefreshBrainRules(%q) AGENTS.md = %q, want preserved managed host text followed by brain rules", root, got)
	}
	if _, err := os.Stat(filepath.Join(root, ".github", "copilot-instructions.md")); err == nil {
		t.Errorf("RefreshBrainRules(%q) created an unmarked Copilot file without a stamp, want existing managed targets only", root)
	}
}

func TestRefreshBrainRulesHonorsSelectedHostsFromStampContract(t *testing.T) {
	const response = `{"anchors":[{"symbol":"src/api.ts#ready","rule":"Keep UTC"}]}`
	server := brainRulesTestServer(t, response)
	root := prepareBrainRulesRefresh(t, server.URL, "agents")
	if err := RefreshBrainRules(t.Context(), root, filepath.Join(root, "graft"), time.Unix(1_000, 0)); err != nil {
		t.Fatalf("RefreshBrainRules(%q) error = %v, want nil", root, err)
	}
	agentPath := filepath.Join(root, "AGENTS.md")
	got, err := os.ReadFile(agentPath)
	if err != nil {
		t.Fatalf("ReadFile(%q) error = %v, want nil", agentPath, err)
	}
	if !strings.Contains(string(got), "<!-- graft:brain:start -->") {
		t.Errorf("RefreshBrainRules(%q) AGENTS.md = %q, want brain section", root, got)
	}
	copilotPath := filepath.Join(root, ".github", "copilot-instructions.md")
	if _, err := os.Stat(copilotPath); err == nil {
		t.Errorf("RefreshBrainRules(%q) created unselected host file %q, want absent", root, copilotPath)
	}
}

func brainRulesTestServer(t *testing.T, response string) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/api/public/brains/brain/rules/anchors" {
			t.Errorf("RefreshBrainRules request = %s %s, want GET /api/public/brains/brain/rules/anchors", r.Method, r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer secret" {
			t.Errorf("RefreshBrainRules Authorization = %q, want %q", got, "Bearer secret")
		}
		_, _ = w.Write([]byte(response))
	}))
	t.Cleanup(server.Close)
	return server
}

func prepareBrainRulesRefresh(t *testing.T, brainURL string, hosts ...string) string {
	t.Helper()
	root := filepath.Join(t.TempDir(), "repo")
	home := t.TempDir()
	for _, dir := range []string{
		filepath.Join(root, ".graft"),
		filepath.Join(root, "graft", ".cache"),
		filepath.Join(root, ".github"),
		filepath.Join(root, ".cursor"),
		filepath.Join(home, ".codex"),
	} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatalf("MkdirAll(%q) error = %v, want nil", dir, err)
		}
	}
	t.Setenv("HOME", home)
	t.Setenv("GRAFT_BRAIN_ID", "")
	t.Setenv("GRAFT_BRAIN_TOKEN", "")
	t.Setenv("GRAFT_BRAIN_URL", brainURL)
	t.Setenv("GRAFT_DIR", "")
	if err := os.WriteFile(filepath.Join(root, ".graft", "config.json"), []byte(`{"brain":{"brainId":"brain","token":"secret"}}`), 0o644); err != nil {
		t.Fatalf("WriteFile(brain link) error = %v, want nil", err)
	}
	stamp, err := json.Marshal(struct {
		Version string   `json:"version"`
		Hosts   []string `json:"hosts"`
	}{Version: "1.0.0", Hosts: hosts})
	if err != nil {
		t.Fatalf("json.Marshal(wiring stamp) error = %v, want nil", err)
	}
	if err := os.WriteFile(filepath.Join(root, "graft", ".cache", "wiring-stamp.json"), stamp, 0o644); err != nil {
		t.Fatalf("WriteFile(wiring stamp) error = %v, want nil", err)
	}
	return root
}
