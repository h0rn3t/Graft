package hosts

import (
	"path/filepath"
	"slices"
)

// Scope is where a write lands: in the repo, or on the machine for every project.
type Scope string

// Write scopes.
const (
	ScopeRepo   Scope = "repo"
	ScopeGlobal Scope = "global"
)

// WriteKind classifies a planned write.
type WriteKind string

// Planned write kinds.
const (
	WriteInstruction WriteKind = "instruction"
	WriteMCP         WriteKind = "mcp"
	WriteHook        WriteKind = "hook"
	WriteClaude      WriteKind = "claude"
	WriteSkill       WriteKind = "skill"
)

// PlannedWrite is one file init would touch.
type PlannedWrite struct {
	// HostID is the selectable host the write belongs to.
	HostID string
	// ID labels the write in reports; one host may write several configs.
	ID    string
	Path  string
	Scope Scope
	Kind  WriteKind
	What  string
}

// HostPlan is every write selecting one host would make.
type HostPlan struct {
	ID       string
	Name     string
	Detected bool
	Writes   []PlannedWrite
}

func instructionTarget(repo string, host Host) PlannedWrite {
	what := "fenced graft section"
	if host.Kind == KindOwned {
		what = "graft-owned file"
	}
	return PlannedWrite{HostID: host.ID, ID: host.ID, Path: filepath.Join(repo, host.RelPath), Scope: ScopeRepo, Kind: WriteInstruction, What: what}
}

// PlanInit lists every host graft can wire with the files selecting it would
// touch, Claude Code first. A non-nil ids restricts the plan to those hosts.
func PlanInit(repo, home string, _ Launch, ids []string) []HostPlan {
	detected := make(map[string]bool)
	for _, host := range DetectHosts(home, repo) {
		detected[host.ID] = true
	}
	plans := []HostPlan{{
		ID: "claude", Name: "Claude Code", Detected: true,
		Writes: slices.Concat(ClaudeTargets(repo), ClaudeGlobalTargets(home)),
	}}
	for _, host := range Hosts() {
		writes := []PlannedWrite{instructionTarget(repo, host)}
		for _, target := range MCPTargets(repo, []string{host.ID}, home) {
			writes = append(writes, target.PlannedWrite)
		}
		switch host.ID {
		case "agents":
			writes = append(writes, CodexHookTargets(home)...)
		case "cursor":
			writes = append(writes, CursorHookTargets(repo)...)
		case "antigravity":
			writes = append(writes, AntigravitySkillTargets(home)...)
		}
		plans = append(plans, HostPlan{ID: host.ID, Name: host.Name, Detected: detected[host.ID], Writes: writes})
	}
	if ids == nil {
		return plans
	}
	return slices.DeleteFunc(plans, func(plan HostPlan) bool { return !slices.Contains(ids, plan.ID) })
}

// SelectedWrites flattens the plan to the writes of the selected hosts.
func SelectedWrites(plans []HostPlan, ids []string) []PlannedWrite {
	writes := make([]PlannedWrite, 0)
	for _, plan := range plans {
		if slices.Contains(ids, plan.ID) {
			writes = append(writes, plan.Writes...)
		}
	}
	return writes
}
