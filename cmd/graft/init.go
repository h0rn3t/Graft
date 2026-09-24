package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/h0rn3t/Graft/internal/graph"
	"github.com/h0rn3t/Graft/internal/hosts"
	"github.com/h0rn3t/Graft/internal/upkeep"
)

type initOptions struct {
	build, mcp, hooks, statusline, global bool
	allAgents, noAgents, dryRun, yes      bool
	agents                                []string
	contextDir                            string
}

func runInit(parsed parsedFlags, stdout, stderr io.Writer) int {
	dir := parsed.dir()
	if parsed.bools["--list-agents"] {
		for _, id := range append(hosts.HostIDs(), "claude") {
			if _, err := fmt.Fprintln(stdout, id); err != nil {
				return 1
			}
		}
		return 0
	}
	opts := initOptions{
		build: !parsed.bools["--no-build"], mcp: !parsed.bools["--no-mcp"], hooks: !parsed.bools["--no-hooks"],
		statusline: !parsed.bools["--no-statusline"], global: !parsed.bools["--no-global"],
		allAgents: parsed.bools["--all-agents"], noAgents: parsed.bools["--no-agents"],
		dryRun: parsed.bools["--dry-run"], yes: parsed.bools["--yes"],
		agents: parsed.values["--agents"],
	}
	opts.contextDir, _ = parsed.value("--dir")
	repo, err := filepath.Abs(dir)
	if err != nil {
		writeDiagnostic(stderr, "✗ %v\n", err)
		return 1
	}
	if opts.agents != nil {
		valid := append(hosts.HostIDs(), "claude")
		unknown := slices.DeleteFunc(slices.Clone(opts.agents), func(id string) bool { return slices.Contains(valid, id) })
		if len(unknown) > 0 {
			writeDiagnostic(stderr, "✗ unknown agent id(s): %s — valid: %s\n", strings.Join(unknown, ", "), strings.Join(valid, ", "))
			return 1
		}
	}
	home, err := os.UserHomeDir()
	if err != nil {
		writeDiagnostic(stderr, "✗ %v\n", err)
		return 1
	}
	env := hosts.Env{Home: home, Binary: executablePath(), Launch: hosts.ServerEntry()}
	return initRepo(repo, env, opts, stderr)
}

func initRepo(repo string, env hosts.Env, opts initOptions, stderr io.Writer) int {
	plan := hosts.PlanInit(repo, env.Home, env.Launch, nil)
	detected := make([]string, 0)
	for _, host := range plan {
		if host.Detected {
			detected = append(detected, host.ID)
		}
	}
	var ids []string
	switch {
	case opts.agents != nil:
		ids = opts.agents
	case opts.allAgents:
		for _, host := range plan {
			ids = append(ids, host.ID)
		}
	case opts.noAgents:
		ids = []string{"claude"}
	case opts.yes || opts.dryRun:
		ids = detected
	case isTerminal(os.Stdin) && isTerminal(os.Stderr):
		picked, ok := runPicker(plan, repo, env.Home, stderr)
		if !ok {
			writeDiagnostic(stderr, "· cancelled — nothing written\n")
			return 0
		}
		ids = picked
	default:
		writeDiagnostic(stderr, "%s\n", hosts.FormatNonInteractiveHelp(detected))
		return 0
	}

	children, _ := graph.WorkspaceBuildChildren(repo, opts.contextDir)
	targets := []string{repo}
	for _, child := range children {
		targets = append(targets, filepath.Join(repo, child))
	}
	tty := isTerminal(os.Stderr)
	if opts.dryRun {
		writeDiagnostic(stderr, "%s\n", hosts.FormatPlan(plan, ids, repo, env.Home, tty))
		for _, child := range children {
			childRepo := filepath.Join(repo, child)
			writeDiagnostic(stderr, "\n— %s/ (workspace child)\n%s\n", child, hosts.FormatPlan(hosts.PlanInit(childRepo, env.Home, env.Launch, nil), ids, childRepo, env.Home, tty))
		}
		return 0
	}
	if len(ids) == 0 {
		writeDiagnostic(stderr, "· no agents selected — nothing written\n")
		return 0
	}
	if len(children) > 0 {
		writeDiagnostic(stderr, "· workspace: wiring %s and %d child repo(s) — %s\n", repo, len(children), strings.Join(children, ", "))
	}
	for _, target := range targets {
		if target != repo {
			rel, _ := filepath.Rel(repo, target)
			writeDiagnostic(stderr, "\n— %s/\n", rel)
		}
		if err := wireTarget(target, ids, plan, env, opts, stderr); err != nil {
			writeDiagnostic(stderr, "%v\n", err)
			return 1
		}
	}
	nodes, edges, built := epilogueGraphs(repo, children, opts.contextDir)
	writeDiagnostic(stderr, "\n%s\n", hosts.FormatInitEpilogue(built, nodes, edges, currentVersion(), tty))
	return 0
}

// wireTarget converges one repo on the selected hosts: it retracts every
// unselected host, writes the selected ones, and stamps the wiring.
func wireTarget(repo string, ids []string, plan []hosts.HostPlan, env hosts.Env, opts initOptions, stderr io.Writer) error {
	wantStatusline := hosts.StatuslineWanted(opts.statusline)
	for _, retraction := range hosts.Changed(hosts.Retract(repo, env, hosts.RetractOptions{Apply: true, Global: opts.global, Exclude: ids})) {
		switch retraction.Action {
		case hosts.RetractUnparseable:
		case hosts.RetractFailed:
			writeDiagnostic(stderr, "⚠ could not remove %s (%s): %v\n", retraction.Path, retraction.What, retraction.Err)
		default:
			writeDiagnostic(stderr, "- removed %s (%s) — agent not selected\n", retraction.Path, retraction.What)
		}
	}
	wantClaude := slices.Contains(ids, "claude")
	if wantClaude {
		result, err := hosts.RunClaudeInit(repo, env, wantStatusline, opts.global)
		if err != nil {
			return err
		}
		built := buildGraphIfMissing(repo, opts.build)
		writeDiagnostic(stderr, "✓ wrote %s\n", result.SettingsPath)
		for _, shim := range result.Shims {
			writeDiagnostic(stderr, "✓ wrote %s\n", shim)
		}
		writeDiagnostic(stderr, "✓ wrote %s\n", result.Skill)
		switch result.MCP.Action {
		case hosts.ActionUnparseable:
			writeDiagnostic(stderr, "⚠ .mcp.json: %s left unchanged (not valid JSON) — add the graft server manually\n", result.MCP.Path)
		case hosts.ActionUnchanged:
			writeDiagnostic(stderr, "· mcp claude: %s (already registered)\n", result.MCP.Path)
		default:
			writeDiagnostic(stderr, "✓ mcp claude: %s (%s) — restart Claude Code to load the graft MCP server\n", result.MCP.Path, result.MCP.Action)
		}
		writeDiagnostic(stderr, "%s\n", builtNote(built))
		if !wantStatusline {
			writeDiagnostic(stderr, "· skipped Claude Code statusLine (--no-statusline)\n")
		}
		for _, warning := range result.Warnings {
			writeDiagnostic(stderr, "⚠ %s\n", warning)
		}
	}
	others := slices.DeleteFunc(slices.Clone(ids), func(id string) bool { return id == "claude" })
	if len(others) > 0 {
		result, err := hosts.RunHostsInit(repo, env, hosts.InitOptions{Agents: others, MCP: opts.mcp, Hooks: opts.hooks, Global: opts.global})
		if err != nil {
			return err
		}
		for _, write := range result.Written {
			writeDiagnostic(stderr, "✓ %s: %s (%s)\n", write.ID, write.Path, write.Action)
		}
		for _, write := range result.MCP {
			writeDiagnostic(stderr, "✓ mcp %s: %s (%s)\n", write.ID, write.Path, write.Action)
		}
		for _, write := range result.Hooks {
			writeDiagnostic(stderr, "✓ hook %s: %s (%s)\n", write.ID, write.Path, write.Action)
		}
		if !opts.global && slices.ContainsFunc(hosts.SelectedWrites(plan, ids), func(write hosts.PlannedWrite) bool { return write.Scope == hosts.ScopeGlobal }) {
			writeDiagnostic(stderr, "· skipped out-of-repo writes (--no-global)\n")
		}
	}
	options := upkeep.WiringOptions{Global: opts.global, MCP: opts.mcp, Hooks: opts.hooks, Statusline: wantStatusline}
	_ = upkeep.WriteWiringStamp(repo, "", currentVersion(), ids, options, time.Now()) // a refresh retries next session
	if !wantClaude {
		writeDiagnostic(stderr, "%s\n", builtNote(buildGraphIfMissing(repo, opts.build)))
	}
	return nil
}

func builtNote(built bool) string {
	if built {
		return "✓ built the graph (graft build)"
	}
	return "· skipped graph build"
}

// hasGraftIndex reports a built repo or a workspace parent.
func hasGraftIndex(dir string) bool {
	if _, err := os.Stat(graph.WiringPath(filepath.Join(dir, "graft"))); err == nil {
		return true
	}
	_, err := os.Stat(filepath.Join(dir, "graft", "workspace.json"))
	return err == nil
}

// buildGraphIfMissing runs `graft build .` in dir with inherited output when
// no graph exists yet. It reports whether a build ran and succeeded.
func buildGraphIfMissing(dir string, build bool) bool {
	if !build || hasGraftIndex(dir) {
		return false
	}
	executable, err := os.Executable()
	if err != nil {
		return false
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	command := exec.CommandContext(ctx, executable, "build", ".")
	command.Dir = dir
	command.Stdin, command.Stdout, command.Stderr = os.Stdin, os.Stdout, os.Stderr
	return command.Run() == nil
}

// epilogueGraphs totals the graphs init leaves behind: the children's for a
// workspace parent, which holds no nodes of its own, else the repo's.
func epilogueGraphs(repo string, children []string, contextDir string) (int, int, bool) {
	dirs := []string{graphContextDir(repo, contextDir)}
	if len(children) > 0 {
		dirs = dirs[:0]
		for _, child := range children {
			dirs = append(dirs, filepath.Join(repo, child, "graft"))
		}
	}
	nodes, edges, built := 0, 0, false
	for _, dir := range dirs {
		loaded, err := graph.Read(graph.WiringPath(dir))
		if err != nil {
			continue
		}
		built = true
		nodes += loaded.Meta.NodeCount
		edges += loaded.Meta.EdgeCount
	}
	return nodes, edges, built
}

func graphContextDir(repo, override string) string {
	if override == "" {
		return filepath.Join(repo, "graft")
	}
	absolute, err := filepath.Abs(override)
	if err != nil {
		return override
	}
	return absolute
}

// runPicker drives the interactive agent picker on stderr, reading keys from
// stdin in raw mode. It returns false when the user cancelled.
func runPicker(plan []hosts.HostPlan, repo, home string, stderr io.Writer) ([]string, bool) {
	state := hosts.InitialPickerState(plan, repo, home)
	restore, err := makeRaw(os.Stdin)
	if err != nil {
		return nil, false
	}
	defer restore()
	drawn := 0
	draw := func() {
		if drawn > 0 {
			writeDiagnostic(stderr, "\x1b[%dA\x1b[0J", drawn)
		}
		text := hosts.RenderPicker(state, true)
		writeDiagnostic(stderr, "%s\r\n", strings.ReplaceAll(text, "\n", "\r\n"))
		drawn = strings.Count(text, "\n") + 1
	}
	draw()
	buffer := make([]byte, 256)
	for !state.Done && !state.Aborted {
		count, err := os.Stdin.Read(buffer)
		if err != nil || count == 0 {
			state.Aborted = true
			break
		}
		keys := hosts.KeysOf(string(buffer[:count]))
		for _, key := range keys {
			state = hosts.ReducePicker(state, key)
			if state.Done || state.Aborted {
				break
			}
		}
		if len(keys) > 0 && !state.Done && !state.Aborted {
			draw()
		}
	}
	draw()
	if state.Aborted {
		return nil, false
	}
	return hosts.PickedHostIDs(state), true
}

func runUninstall(parsed parsedFlags, stderr io.Writer) int {
	dir := parsed.dir()
	repo, err := filepath.Abs(dir)
	if err != nil {
		writeDiagnostic(stderr, "✗ %v\n", err)
		return 1
	}
	home, err := os.UserHomeDir()
	if err != nil {
		writeDiagnostic(stderr, "✗ %v\n", err)
		return 1
	}
	env := hosts.Env{Home: home, Launch: hosts.ServerEntry()}
	global := !parsed.bools["--no-global"]
	opts := hosts.RetractOptions{Global: global, Cache: !parsed.bools["--keep-cache"]}
	if !parsed.bools["--yes"] {
		writeDiagnostic(stderr, "%s\n", hosts.FormatRetractions(hosts.Retract(repo, env, opts), false))
		writeDiagnostic(stderr, "\nDry run — nothing was touched. Re-run with -y to remove.\n")
		if global {
			writeDiagnostic(stderr, "Entries marked [machine-wide] affect every project; --no-global skips them.\n")
		}
		return 0
	}
	opts.Apply = true
	done := hosts.Retract(repo, env, opts)
	writeDiagnostic(stderr, "%s\n", hosts.FormatRetractions(done, true))
	bad := 0
	for _, retraction := range hosts.Changed(done) {
		if retraction.Action == hosts.RetractUnparseable || retraction.Action == hosts.RetractFailed {
			bad++
		}
	}
	if bad > 0 {
		writeDiagnostic(stderr, "\n⚠ %d file(s) could not be parsed or removed and were left as-is — see above.\n", bad)
	} else {
		writeDiagnostic(stderr, "\n✓ graft fully removed. `graft init` re-wires from scratch.\n")
	}
	return 0
}
