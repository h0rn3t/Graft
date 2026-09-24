package hosts

import (
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"testing"

	"github.com/NanoNets/context-graph-engine/internal/jsonjs"
)

func listFiles(t *testing.T, roots ...string) []string {
	t.Helper()
	files := make([]string, 0)
	for _, root := range roots {
		err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if !entry.IsDir() {
				files = append(files, path)
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	slices.Sort(files)
	return files
}

func machine(t *testing.T) (string, string) {
	t.Helper()
	repo, home := t.TempDir(), t.TempDir()
	for _, dir := range []string{".codex", ".cursor", ".gemini/config", ".config/opencode", ".kiro", ".codeium/windsurf", ".adal", ".grok", ".hermes"} {
		if err := os.MkdirAll(filepath.Join(home, dir), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	return repo, home
}

// TestPlanMatchesTheWritesAnApplyMakes checks the dry-run contract: the plan
// touches nothing, and applying every host writes exactly the planned files.
func TestMergeGraftSettingsRemovesLegacyAllowEntry(t *testing.T) {
	existing := jsonjs.NewObject()
	permissions := jsonjs.NewObject()
	permissions.Set("allow", []jsonjs.Value{
		"Bash(ls:*)", "Bash(node dist/cli.js:*)", "Bash(graft:*)",
	})
	existing.Set("permissions", permissions)
	merged, warnings := MergeGraftSettings(existing, true)
	if len(warnings) != 0 {
		t.Errorf("MergeGraftSettings() warnings = %v, want none", warnings)
	}
	value, _ := merged.Get("permissions")
	object, _ := jsonjs.AsObject(value)
	allowValue, _ := object.Get("allow")
	allow, _ := jsonjs.AsArray(allowValue)
	got := make([]string, len(allow))
	for i, value := range allow {
		got[i] = jsonjs.String(value)
	}
	if !reflect.DeepEqual(got, []string{"Bash(ls:*)", "Bash(graft:*)", "Bash(npx graft:*)", "Bash(graft-dev:*)"}) {
		t.Errorf("MergeGraftSettings() allow = %v, want legacy TS entry removed", got)
	}
}

func TestPlanMatchesTheWritesAnApplyMakes(t *testing.T) {
	repo, home := machine(t)
	env := Env{Home: home, BakedDir: "/pkg", Launch: npxLaunch}
	before := listFiles(t, repo, home)
	plans := PlanInit(repo, home, env.Launch, nil)
	if after := listFiles(t, repo, home); !slices.Equal(after, before) {
		t.Fatalf("PlanInit() wrote files: %q", after)
	}
	ids := make([]string, 0, len(plans))
	for _, plan := range plans {
		ids = append(ids, plan.ID)
	}
	planned := make([]string, 0)
	for _, write := range SelectedWrites(plans, ids) {
		if !slices.Contains(planned, write.Path) {
			planned = append(planned, write.Path)
		}
	}
	slices.Sort(planned)

	if _, err := RunClaudeInit(repo, env, true, true); err != nil {
		t.Fatal(err)
	}
	others := slices.DeleteFunc(slices.Clone(ids), func(id string) bool { return id == "claude" })
	if _, err := RunHostsInit(repo, env, InitOptions{Agents: others, MCP: true, Hooks: true, Global: true}); err != nil {
		t.Fatal(err)
	}
	if written := listFiles(t, repo, home); !slices.Equal(written, planned) {
		t.Errorf("apply wrote %q, plan listed %q", written, planned)
	}
}

// TestNoGlobalKeepsHomeUntouched checks that --no-global writes nothing under
// home while repo-level targets, Cursor's hooks included, are still written.
func TestNoGlobalKeepsHomeUntouched(t *testing.T) {
	repo, home := machine(t)
	env := Env{Home: home, BakedDir: "/pkg", Launch: npxLaunch}
	before := listFiles(t, home)
	if _, err := RunClaudeInit(repo, env, true, false); err != nil {
		t.Fatal(err)
	}
	result, err := RunHostsInit(repo, env, InitOptions{Agents: HostIDs(), MCP: true, Hooks: true, Global: false})
	if err != nil {
		t.Fatal(err)
	}
	if after := listFiles(t, home); !slices.Equal(after, before) {
		t.Errorf("--no-global wrote under home: %q", after)
	}
	if !slices.ContainsFunc(result.Hooks, func(write ConfigWrite) bool { return write.ID == "cursor-hooks" }) {
		t.Errorf("--no-global hooks = %+v, want Cursor's repo-local hooks", result.Hooks)
	}
}
