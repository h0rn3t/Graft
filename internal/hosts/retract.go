package hosts

import (
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"

	"github.com/NanoNets/context-graph-engine/internal/jsonjs"
)

// RetractAction is what retracting one target did.
type RetractAction string

// Retraction outcomes.
const (
	RetractRemoved     RetractAction = "removed"
	RetractDeleted     RetractAction = "deleted"
	RetractAbsent      RetractAction = "absent"
	RetractUnparseable RetractAction = "skipped-unparseable"
)

// Retraction reports one target graft could have written.
type Retraction struct {
	HostID string
	Path   string
	What   string
	Scope  Scope
	Action RetractAction
}

// RetractOptions controls a retraction.
type RetractOptions struct {
	// Apply removes; without it the retraction only reports.
	Apply bool
	// Global includes targets outside the repo.
	Global bool
	// Cache includes the graph cache and its ignore entries.
	Cache bool
	// Exclude spares the targets of hosts about to be rewritten.
	Exclude []string
}

type retractTarget struct {
	Retraction
	run func(apply bool) RetractAction
}

func isBlank(text string) bool {
	return strings.TrimSpace(text) == ""
}

func exists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

func removeFile(path string, apply bool) RetractAction {
	if !exists(path) {
		return RetractAbsent
	}
	if apply {
		_ = os.Remove(path) // force: a vanished file is already retracted
		pruneEmptyDirs(filepath.Dir(path))
	}
	return RetractDeleted
}

// pruneEmptyDirs removes up to six empty ancestors of a just-emptied directory.
func pruneEmptyDirs(dir string) {
	for range 6 {
		entries, err := os.ReadDir(dir)
		if err != nil || len(entries) > 0 || os.Remove(dir) != nil {
			return
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return
		}
		dir = parent
	}
}

func collapseBlankLines(text string) string {
	collapsed := blankRuns.ReplaceAllString(text, "\n\n")
	collapsed = leadingBlanks.ReplaceAllString(collapsed, "")
	return trailingBlanks.ReplaceAllString(collapsed, "\n")
}

// stripSection removes every graft-fenced region from a file the user owns.
func stripSection(path string, apply bool) RetractAction {
	data, err := os.ReadFile(path)
	if err != nil {
		return RetractAbsent
	}
	text := string(data)
	if !slices.ContainsFunc(AllMarkers, func(markers Markers) bool { return strings.Contains(text, markers.Start) }) {
		return RetractAbsent
	}
	eol := detectEOL(text)
	out := make([]string, 0)
	closing, inside, found := "", false, false
	for _, line := range splitLines(text) {
		trimmed := strings.TrimSpace(line)
		if !inside {
			if at := slices.IndexFunc(AllMarkers, func(markers Markers) bool { return markers.Start == trimmed }); at >= 0 {
				closing, inside, found = AllMarkers[at].End, true, true
				continue
			}
			out = append(out, line)
			continue
		}
		if trimmed == closing {
			inside = false
		}
	}
	if !found {
		return RetractAbsent
	}
	collapsed := collapseBlankLines(strings.Join(out, "\n"))
	if !apply {
		if isBlank(collapsed) {
			return RetractDeleted
		}
		return RetractRemoved
	}
	if isBlank(collapsed) {
		return removeFile(path, true)
	}
	if eol != "\n" {
		collapsed = strings.ReplaceAll(collapsed, "\n", "\r\n")
	}
	if os.WriteFile(path, []byte(collapsed), 0o644) != nil {
		return RetractUnparseable
	}
	return RetractRemoved
}

// readRetractObject loads a JSON config for retraction.
func readRetractObject(path string) (*jsonjs.Object, RetractAction, bool) {
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, RetractAbsent, false
	}
	if err != nil {
		return nil, RetractUnparseable, false
	}
	value, err := jsonjs.Parse(data)
	if err != nil {
		return nil, RetractUnparseable, false
	}
	object, ok := jsonjs.AsObject(value)
	if !ok {
		return nil, RetractUnparseable, false
	}
	return object, "", true
}

// finishJSON writes root back, or deletes the file when nothing is left.
func finishJSON(path string, root *jsonjs.Object, apply bool) RetractAction {
	if !apply {
		if root.Len() == 0 {
			return RetractDeleted
		}
		return RetractRemoved
	}
	if root.Len() == 0 {
		return removeFile(path, true)
	}
	if os.WriteFile(path, []byte(jsonjs.Stringify(root, 2)+"\n"), 0o644) != nil {
		return RetractUnparseable
	}
	return RetractRemoved
}

func removeJSONKey(path, topKey string, apply bool) RetractAction {
	root, action, ok := readRetractObject(path)
	if !ok {
		return action
	}
	value, _ := root.Get(topKey)
	bucket, ok := jsonjs.AsObject(value)
	if !ok || !bucket.Has("graft") {
		return RetractAbsent
	}
	if !apply {
		if bucket.Len() == 1 && root.Len() == 1 {
			return RetractDeleted
		}
		return RetractRemoved
	}
	bucket.Delete("graft")
	if bucket.Len() == 0 {
		root.Delete(topKey)
	}
	return finishJSON(path, root, true)
}

func removeTOMLSection(path string, apply bool) RetractAction {
	data, err := os.ReadFile(path)
	if err != nil {
		return RetractAbsent
	}
	kept, found := StripTOMLSection(string(data))
	if !found {
		return RetractAbsent
	}
	if !apply {
		if isBlank(kept) {
			return RetractDeleted
		}
		return RetractRemoved
	}
	if isBlank(kept) {
		return removeFile(path, true)
	}
	if !strings.HasSuffix(kept, "\n") {
		kept += "\n"
	}
	if os.WriteFile(path, []byte(kept), 0o644) != nil {
		return RetractUnparseable
	}
	return RetractRemoved
}

// dropGraftHooks removes every graft hook entry from root.hooks.
func dropGraftHooks(root *jsonjs.Object) {
	hooks, ok := jsonjs.AsObject(mustGet(root, "hooks"))
	if !ok {
		return
	}
	for _, event := range hooks.Keys() {
		prior, ok := jsonjs.AsArray(mustGet(hooks, event))
		if !ok {
			continue
		}
		kept := slices.DeleteFunc(slices.Clone(prior), isGraftEntry)
		if len(kept) == 0 {
			hooks.Delete(event)
		} else {
			hooks.Set(event, kept)
		}
	}
	if hooks.Len() == 0 {
		root.Delete("hooks")
	}
}

func mustGet(object *jsonjs.Object, key string) jsonjs.Value {
	value, _ := object.Get(key)
	return value
}

func stripClaudeSettings(path string, apply bool) RetractAction {
	root, action, ok := readRetractObject(path)
	if !ok {
		return action
	}
	before := jsonjs.Stringify(root, 0)
	for _, key := range []string{"statusLine", "subagentStatusLine"} {
		if value, ok := root.Get(key); ok && strings.Contains(jsonjs.Stringify(orEmpty(value), 0), statuslineHelper) {
			root.Delete(key)
		}
	}
	dropGraftHooks(root)
	if footers, ok := jsonjs.AsArray(mustGet(root, "footerLinksRegexes")); ok {
		kept := slices.DeleteFunc(slices.Clone(footers), IsGraftFooterRegex)
		if len(kept) == 0 {
			root.Delete("footerLinksRegexes")
		} else {
			root.Set("footerLinksRegexes", kept)
		}
	}
	if permissions, ok := jsonjs.AsObject(mustGet(root, "permissions")); ok {
		if allow, ok := jsonjs.AsArray(mustGet(permissions, "allow")); ok {
			kept := slices.DeleteFunc(slices.Clone(allow), IsGraftAllowEntry)
			if len(kept) == 0 {
				permissions.Delete("allow")
			} else {
				permissions.Set("allow", kept)
			}
			if permissions.Len() == 0 {
				root.Delete("permissions")
			}
		}
	}
	if jsonjs.Stringify(root, 0) == before {
		return RetractAbsent
	}
	return finishJSON(path, root, apply)
}

func orEmpty(value jsonjs.Value) jsonjs.Value {
	if value == nil {
		return ""
	}
	return value
}

func stripCodexHooks(path string, apply bool) RetractAction {
	root, action, ok := readRetractObject(path)
	if !ok {
		return action
	}
	if _, ok := jsonjs.AsObject(mustGet(root, "hooks")); !ok {
		return RetractAbsent
	}
	before := jsonjs.Stringify(root, 0)
	dropGraftHooks(root)
	if jsonjs.Stringify(root, 0) == before {
		return RetractAbsent
	}
	return finishJSON(path, root, apply)
}

func stripIgnoreEntries(path string, entries []*regexp.Regexp, apply bool) RetractAction {
	data, err := os.ReadFile(path)
	if err != nil {
		return RetractAbsent
	}
	text := string(data)
	lines := strings.Split(text, "\n")
	kept := slices.DeleteFunc(slices.Clone(lines), func(line string) bool {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "#") && strings.Contains(trimmed, "graft") {
			return true
		}
		return slices.ContainsFunc(entries, func(entry *regexp.Regexp) bool { return entry.MatchString(trimmed) })
	})
	result := collapseBlankLines(strings.Join(kept, "\n"))
	if result == text {
		return RetractAbsent
	}
	if !apply {
		if isBlank(result) {
			return RetractDeleted
		}
		return RetractRemoved
	}
	if isBlank(result) {
		return removeFile(path, true)
	}
	if !strings.HasSuffix(result, "\n") {
		result += "\n"
	}
	if os.WriteFile(path, []byte(result), 0o644) != nil {
		return RetractUnparseable
	}
	return RetractRemoved
}

func removeDir(path string, apply bool) RetractAction {
	if !dirExists(path) {
		return RetractAbsent
	}
	if apply {
		_ = os.RemoveAll(path) // force: whatever survives is reported by the next run
		pruneEmptyDirs(filepath.Dir(path))
	}
	return RetractDeleted
}

var (
	gitignoreEntry = regexp.MustCompile(`^/?graft/?$`)
	ignoreEntries  = []*regexp.Regexp{regexp.MustCompile(`^!?graft/?$`), regexp.MustCompile(`^graft/\.(cache|graph)/?$`)}
)

type targetList struct {
	kept map[string]bool
	seen map[string]bool
	out  []retractTarget
}

// add queues a target unless a kept host owns its path or it is already queued.
func (list *targetList) add(hostID, path, what string, scope Scope, run func(bool) RetractAction) {
	if list.kept[path] || list.seen[path] {
		return
	}
	list.seen[path] = true
	list.out = append(list.out, retractTarget{HostID: hostID, Path: path, What: what, Scope: scope, run: run})
}

// retractTargets lists every target graft could have written, in removal order.
func retractTargets(repo string, env Env, opts RetractOptions) []retractTarget {
	exclude := func(id string) bool { return slices.Contains(opts.Exclude, id) }
	list := &targetList{kept: keptPaths(repo, env, opts.Exclude), seen: make(map[string]bool)}
	for _, host := range Hosts() {
		if exclude(host.ID) {
			continue
		}
		path := filepath.Join(repo, host.RelPath)
		if host.Kind == KindOwned {
			list.add(host.ID, path, "graft-owned instruction file", ScopeRepo, func(apply bool) RetractAction { return removeFile(path, apply) })
		} else {
			list.add(host.ID, path, "fenced graft section", ScopeRepo, func(apply bool) RetractAction { return stripSection(path, apply) })
		}
	}
	for _, mcp := range MCPTargets(repo, slices.DeleteFunc(HostIDs(), exclude), env.Home, env.Launch) {
		if !opts.Global && mcp.Scope == ScopeGlobal {
			continue
		}
		list.add(mcp.HostID, mcp.Path, mcp.What, mcp.Scope, func(apply bool) RetractAction {
			if mcp.Format == FormatTOML {
				return removeTOMLSection(mcp.Path, apply)
			}
			return removeJSONKey(mcp.Path, mcp.TopKey, apply)
		})
	}
	if !exclude("claude") {
		addClaudeTargets(list, repo)
	}
	if opts.Global {
		addGlobalTargets(list, env.Home, exclude)
	}
	if opts.Cache {
		cache, gitignore, ignore := filepath.Join(repo, "graft"), filepath.Join(repo, ".gitignore"), filepath.Join(repo, ".ignore")
		list.add("graph", cache, "local graph cache", ScopeRepo, func(apply bool) RetractAction { return removeDir(cache, apply) })
		list.add("graph", gitignore, "graft/ ignore entry", ScopeRepo, func(apply bool) RetractAction {
			return stripIgnoreEntries(gitignore, []*regexp.Regexp{gitignoreEntry}, apply)
		})
		list.add("graph", ignore, "graft/ search re-admit entries", ScopeRepo, func(apply bool) RetractAction {
			return stripIgnoreEntries(ignore, ignoreEntries, apply)
		})
	}
	return list.out
}

func addClaudeTargets(list *targetList, repo string) {
	claude := ClaudeTargets(repo)
	settings, statusline, hooks, skill, mcp := claude[0].Path, claude[1].Path, claude[2].Path, claude[3].Path, claude[4].Path
	list.add("claude", settings, "statusline + hooks + allowlist + footer regex", ScopeRepo, func(apply bool) RetractAction { return stripClaudeSettings(settings, apply) })
	list.add("claude", statusline, "statusline shim", ScopeRepo, func(apply bool) RetractAction { return removeFile(statusline, apply) })
	list.add("claude", hooks, "hooks shim", ScopeRepo, func(apply bool) RetractAction { return removeFile(hooks, apply) })
	list.add("claude", skill, "graft skill", ScopeRepo, func(apply bool) RetractAction { return removeFile(skill, apply) })
	list.add("claude", mcp, "mcpServers.graft", ScopeRepo, func(apply bool) RetractAction { return removeJSONKey(mcp, "mcpServers", apply) })
}

func addGlobalTargets(list *targetList, home string, exclude func(string) bool) {
	if !exclude("claude") {
		global := ClaudeGlobalTargets(home)
		shim, settings, mcp := global[0], global[1], global[2]
		list.add("claude", shim.Path, shim.What, ScopeGlobal, func(apply bool) RetractAction { return removeFile(shim.Path, apply) })
		list.add("claude", settings.Path, settings.What, ScopeGlobal, func(apply bool) RetractAction { return stripClaudeSettings(settings.Path, apply) })
		list.add("claude", mcp.Path, mcp.What, ScopeGlobal, func(apply bool) RetractAction { return removeJSONKey(mcp.Path, "mcpServers", apply) })
	}
	if !exclude("agents") {
		for _, hook := range CodexHookTargets(home) {
			list.add(hook.HostID, hook.Path, hook.What, ScopeGlobal, func(apply bool) RetractAction {
				if strings.HasSuffix(hook.Path, ".json") {
					return stripCodexHooks(hook.Path, apply)
				}
				return removeFile(hook.Path, apply)
			})
		}
	}
	if !exclude("antigravity") {
		for _, skill := range AntigravitySkillTargets(home) {
			list.add(skill.HostID, skill.Path, skill.What, ScopeGlobal, func(apply bool) RetractAction { return removeFile(skill.Path, apply) })
		}
	}
}

// keptPaths are the files a host about to be rewritten also writes; they
// survive even when an unselected host names them too, as AGENTS.md does.
func keptPaths(repo string, env Env, exclude []string) map[string]bool {
	kept := make(map[string]bool)
	for _, host := range Hosts() {
		if slices.Contains(exclude, host.ID) {
			kept[filepath.Join(repo, host.RelPath)] = true
		}
	}
	for _, target := range MCPTargets(repo, exclude, env.Home, env.Launch) {
		kept[target.Path] = true
	}
	if slices.Contains(exclude, "claude") {
		for _, target := range slices.Concat(ClaudeTargets(repo), ClaudeGlobalTargets(env.Home)) {
			kept[target.Path] = true
		}
	}
	if slices.Contains(exclude, "agents") {
		for _, target := range CodexHookTargets(env.Home) {
			kept[target.Path] = true
		}
	}
	if slices.Contains(exclude, "antigravity") {
		for _, target := range AntigravitySkillTargets(env.Home) {
			kept[target.Path] = true
		}
	}
	return kept
}

// Retract reports, and with opts.Apply removes, graft's contribution to every
// target it could have written.
func Retract(repo string, env Env, opts RetractOptions) []Retraction {
	targets := retractTargets(repo, env, opts)
	out := make([]Retraction, len(targets))
	for i, target := range targets {
		out[i] = target.Retraction
		out[i].Action = target.run(opts.Apply)
	}
	return out
}

// Changed keeps the retractions that had something to remove.
func Changed(retractions []Retraction) []Retraction {
	return slices.DeleteFunc(slices.Clone(retractions), func(retraction Retraction) bool { return retraction.Action == RetractAbsent })
}
