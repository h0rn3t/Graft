package hosts

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"

	"github.com/h0rn3t/Graft/internal/jsonjs"
)

// RetractAction is what retracting one target did.
type RetractAction string

// Retraction outcomes.
const (
	RetractRemoved     RetractAction = "removed"
	RetractDeleted     RetractAction = "deleted"
	RetractAbsent      RetractAction = "absent"
	RetractUnparseable RetractAction = "skipped-unparseable"
	// RetractFailed means the target could not be read, rewritten, or
	// removed; Retraction.Err carries the cause.
	RetractFailed RetractAction = "failed"
)

var (
	errNotJSON       = errors.New("not valid JSON")
	errNotJSONObject = errors.New("not a JSON object")
)

// Retraction reports one target graft could have written.
type Retraction struct {
	HostID string
	Path   string
	What   string
	Scope  Scope
	Action RetractAction
	// Err says why a target was skipped (RetractUnparseable) or failed
	// (RetractFailed); nil otherwise.
	Err error
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
	run func(apply bool) (RetractAction, error)
}

var (
	blankRuns      = regexp.MustCompile(`\n{3,}`)
	leadingBlanks  = regexp.MustCompile(`^\n+`)
	trailingBlanks = regexp.MustCompile(`\n+$`)
)

func isBlank(text string) bool {
	return strings.TrimSpace(text) == ""
}

// removeFile deletes a graft-owned file; only a file already gone counts as
// removed without a removal.
func (f *files) removeFile(path string, apply bool) (RetractAction, error) {
	if _, err := f.lstat(path); errors.Is(err, fs.ErrNotExist) {
		return RetractAbsent, nil
	} else if err != nil {
		return RetractFailed, err
	}
	if !apply {
		return RetractDeleted, nil
	}
	if err := f.remove(path); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return RetractFailed, err
	}
	f.pruneEmptyDirs(filepath.Dir(path))
	return RetractDeleted, nil
}

func collapseBlankLines(text string) string {
	collapsed := blankRuns.ReplaceAllString(text, "\n\n")
	collapsed = leadingBlanks.ReplaceAllString(collapsed, "")
	return trailingBlanks.ReplaceAllString(collapsed, "\n")
}

// readText reads a target for retraction; a missing file is absent.
func (f *files) readText(path string) (string, RetractAction, error) {
	data, err := f.readFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return "", RetractAbsent, nil
	}
	if err != nil {
		return "", RetractFailed, err
	}
	return string(data), "", nil
}

// writeRest writes what is left of a target, or deletes it when nothing is.
func (f *files) writeRest(path, rest string) (RetractAction, error) {
	if isBlank(rest) {
		return f.removeFile(path, true)
	}
	if err := f.writeFile(path, []byte(rest), 0o644); err != nil {
		return RetractFailed, err
	}
	return RetractRemoved, nil
}

// stripSection removes every graft-fenced region from a file the user owns.
// A start marker without its end marker leaves the whole file untouched.
func (f *files) stripSection(path string, apply bool) (RetractAction, error) {
	text, action, err := f.readText(path)
	if action != "" {
		return action, err
	}
	if !slices.ContainsFunc(AllMarkers, func(markers Markers) bool { return strings.Contains(text, markers.Start) }) {
		return RetractAbsent, nil
	}
	eol := detectEOL(text)
	lines := splitLines(text)
	out := make([]string, 0, len(lines))
	found := false
	for i := 0; i < len(lines); i++ {
		trimmed := strings.TrimSpace(lines[i])
		at := slices.IndexFunc(AllMarkers, func(markers Markers) bool { return markers.Start == trimmed })
		if at < 0 {
			out = append(out, lines[i])
			continue
		}
		end := markerLine(lines, AllMarkers[at].End, i+1)
		if end == -1 {
			return RetractUnparseable, errUnclosedMarker
		}
		found, i = true, end
	}
	if !found {
		return RetractAbsent, nil
	}
	collapsed := collapseBlankLines(strings.Join(out, "\n"))
	if !apply {
		if isBlank(collapsed) {
			return RetractDeleted, nil
		}
		return RetractRemoved, nil
	}
	if eol != "\n" {
		collapsed = strings.ReplaceAll(collapsed, "\n", "\r\n")
	}
	return f.writeRest(path, collapsed)
}

// readRetractObject loads a JSON config for retraction; a non-empty action
// already says what happened to the target.
func (f *files) readRetractObject(path string) (*jsonjs.Object, RetractAction, error) {
	text, action, err := f.readText(path)
	if action != "" {
		return nil, action, err
	}
	value, err := jsonjs.Parse([]byte(text))
	if err != nil {
		return nil, RetractUnparseable, errNotJSON
	}
	object, ok := jsonjs.AsObject(value)
	if !ok {
		return nil, RetractUnparseable, errNotJSONObject
	}
	return object, "", nil
}

// finishJSON writes root back, or deletes the file when nothing is left.
func (f *files) finishJSON(path string, root *jsonjs.Object, apply bool) (RetractAction, error) {
	if !apply {
		if root.Len() == 0 {
			return RetractDeleted, nil
		}
		return RetractRemoved, nil
	}
	if root.Len() == 0 {
		return f.removeFile(path, true)
	}
	return f.writeRest(path, jsonjs.Stringify(root, 2)+"\n")
}

func (f *files) removeJSONKey(path, topKey string, apply bool) (RetractAction, error) {
	root, action, err := f.readRetractObject(path)
	if action != "" {
		return action, err
	}
	value, _ := root.Get(topKey)
	bucket, ok := jsonjs.AsObject(value)
	if !ok || !bucket.Has("graft") {
		return RetractAbsent, nil
	}
	if !apply {
		if bucket.Len() == 1 && root.Len() == 1 {
			return RetractDeleted, nil
		}
		return RetractRemoved, nil
	}
	bucket.Delete("graft")
	if bucket.Len() == 0 {
		root.Delete(topKey)
	}
	return f.finishJSON(path, root, true)
}

func (f *files) removeTOMLSection(path string, apply bool) (RetractAction, error) {
	text, action, err := f.readText(path)
	if action != "" {
		return action, err
	}
	kept, found := StripTOMLSection(text)
	if !found {
		return RetractAbsent, nil
	}
	if !apply {
		if isBlank(kept) {
			return RetractDeleted, nil
		}
		return RetractRemoved, nil
	}
	if !strings.HasSuffix(kept, "\n") {
		kept += "\n"
	}
	return f.writeRest(path, kept)
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

func (f *files) stripClaudeSettings(path string, apply bool) (RetractAction, error) {
	root, action, err := f.readRetractObject(path)
	if action != "" {
		return action, err
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
		return RetractAbsent, nil
	}
	return f.finishJSON(path, root, apply)
}

func orEmpty(value jsonjs.Value) jsonjs.Value {
	if value == nil {
		return ""
	}
	return value
}

func (f *files) stripCodexHooks(path string, apply bool) (RetractAction, error) {
	root, action, err := f.readRetractObject(path)
	if action != "" {
		return action, err
	}
	if _, ok := jsonjs.AsObject(mustGet(root, "hooks")); !ok {
		return RetractAbsent, nil
	}
	before := jsonjs.Stringify(root, 0)
	dropGraftHooks(root)
	if jsonjs.Stringify(root, 0) == before {
		return RetractAbsent, nil
	}
	return f.finishJSON(path, root, apply)
}

// ignoreRule is what graft adds to an ignore file: the comment lines it
// writes above its entries, and the entries themselves.
type ignoreRule struct {
	comments []string
	entries  []*regexp.Regexp
}

func (rule ignoreRule) ours(line string) bool {
	trimmed := strings.TrimSpace(line)
	return slices.Contains(rule.comments, trimmed) ||
		slices.ContainsFunc(rule.entries, func(entry *regexp.Regexp) bool { return entry.MatchString(trimmed) })
}

func (f *files) stripIgnoreEntries(path string, rule ignoreRule, apply bool) (RetractAction, error) {
	text, action, err := f.readText(path)
	if action != "" {
		return action, err
	}
	lines := strings.Split(text, "\n")
	kept := slices.DeleteFunc(slices.Clone(lines), rule.ours)
	if len(kept) == len(lines) {
		return RetractAbsent, nil
	}
	result := collapseBlankLines(strings.Join(kept, "\n"))
	if !apply {
		if isBlank(result) {
			return RetractDeleted, nil
		}
		return RetractRemoved, nil
	}
	if !strings.HasSuffix(result, "\n") {
		result += "\n"
	}
	return f.writeRest(path, result)
}

func (f *files) removeDir(path string, apply bool) (RetractAction, error) {
	info, err := f.lstat(path)
	if errors.Is(err, fs.ErrNotExist) || (err == nil && !info.IsDir()) {
		return RetractAbsent, nil
	}
	if err != nil {
		return RetractFailed, err
	}
	if !apply {
		return RetractDeleted, nil
	}
	if err := f.removeAll(path); err != nil {
		return RetractFailed, err
	}
	f.pruneEmptyDirs(filepath.Dir(path))
	return RetractDeleted, nil
}

var (
	gitignoreRule = ignoreRule{
		comments: []string{"# graft's local graph cache — regenerable, not committed (run `graft build`)."},
		entries:  []*regexp.Regexp{regexp.MustCompile(`^/?graft/?$`)},
	}
	ignoreFileRule = ignoreRule{
		comments: []string{
			"# graft's cards are gitignored but should stay greppable: ripgrep reads",
			"# .ignore before .gitignore, so this re-admits the tree to search only.",
		},
		entries: []*regexp.Regexp{regexp.MustCompile(`^!?graft/?$`), regexp.MustCompile(`^graft/\.(cache|graph)/?$`)},
	}
)

// ContextCacheDir is <context>/.cache: contextDir when given, else GRAFT_DIR
// (relative to repo), else <repo>/graft. The wiring stamp lives there.
func ContextCacheDir(repo, contextDir string) string {
	if contextDir == "" {
		contextDir = os.Getenv("GRAFT_DIR")
	}
	if contextDir == "" {
		contextDir = filepath.Join(repo, "graft")
	} else if !filepath.IsAbs(contextDir) {
		contextDir = filepath.Join(repo, contextDir)
	}
	return filepath.Join(contextDir, ".cache")
}

type targetList struct {
	kept map[string]bool
	seen map[string]bool
	out  []retractTarget
}

// add queues a target unless a kept host owns its path or it is already queued.
func (list *targetList) add(hostID, path, what string, scope Scope, run func(bool) (RetractAction, error)) {
	if list.kept[path] || list.seen[path] {
		return
	}
	list.seen[path] = true
	list.out = append(list.out, retractTarget{HostID: hostID, Path: path, What: what, Scope: scope, run: run})
}

// retractTargets lists every target graft could have written, in removal order.
func (f *files) retractTargets(repo string, env Env, opts RetractOptions) []retractTarget {
	exclude := func(id string) bool { return slices.Contains(opts.Exclude, id) }
	list := &targetList{kept: keptPaths(repo, env, opts.Exclude), seen: make(map[string]bool)}
	for _, host := range Hosts() {
		if exclude(host.ID) {
			continue
		}
		path := filepath.Join(repo, host.RelPath)
		if host.Kind == KindOwned {
			list.add(host.ID, path, "graft-owned instruction file", ScopeRepo, func(apply bool) (RetractAction, error) { return f.removeFile(path, apply) })
		} else {
			list.add(host.ID, path, "fenced graft section", ScopeRepo, func(apply bool) (RetractAction, error) { return f.stripSection(path, apply) })
		}
	}
	for _, mcp := range mcpTargets(repo, slices.DeleteFunc(HostIDs(), exclude), env.Home, true) {
		if !opts.Global && mcp.Scope == ScopeGlobal {
			continue
		}
		list.add(mcp.HostID, mcp.Path, mcp.What, mcp.Scope, func(apply bool) (RetractAction, error) {
			if mcp.Format == FormatTOML {
				return f.removeTOMLSection(mcp.Path, apply)
			}
			return f.removeJSONKey(mcp.Path, mcp.TopKey, apply)
		})
	}
	if !exclude("claude") {
		f.addClaudeTargets(list, repo)
	}
	if opts.Global {
		f.addGlobalTargets(list, env.Home, exclude)
	}
	cache := filepath.Join(repo, "graft")
	if opts.Cache {
		gitignore, ignore := filepath.Join(repo, ".gitignore"), filepath.Join(repo, ".ignore")
		list.add("graph", cache, "local graph cache", ScopeRepo, func(apply bool) (RetractAction, error) { return f.removeDir(cache, apply) })
		list.add("graph", gitignore, "graft/ ignore entry", ScopeRepo, func(apply bool) (RetractAction, error) {
			return f.stripIgnoreEntries(gitignore, gitignoreRule, apply)
		})
		list.add("graph", ignore, "graft/ search re-admit entries", ScopeRepo, func(apply bool) (RetractAction, error) {
			return f.stripIgnoreEntries(ignore, ignoreFileRule, apply)
		})
	}
	// A full retraction also drops the wiring stamp, or the next session
	// would re-wire the hosts it names; removing the cache already covers it.
	stamp := filepath.Join(ContextCacheDir(repo, ""), "wiring-stamp.json")
	if rel, err := filepath.Rel(cache, stamp); len(opts.Exclude) == 0 && (!opts.Cache || err != nil || !filepath.IsLocal(rel)) {
		list.add("graph", stamp, "wiring stamp", ScopeRepo, func(apply bool) (RetractAction, error) { return f.removeFile(stamp, apply) })
	}
	return list.out
}

func (f *files) addClaudeTargets(list *targetList, repo string) {
	claude := ClaudeTargets(repo)
	settings, statusline, hooks, skill, mcp := claude[0].Path, claude[1].Path, claude[2].Path, claude[3].Path, claude[4].Path
	list.add("claude", settings, "statusline + hooks + allowlist + footer regex", ScopeRepo, func(apply bool) (RetractAction, error) { return f.stripClaudeSettings(settings, apply) })
	list.add("claude", statusline, "statusline shim", ScopeRepo, func(apply bool) (RetractAction, error) { return f.removeFile(statusline, apply) })
	list.add("claude", hooks, "hooks shim", ScopeRepo, func(apply bool) (RetractAction, error) { return f.removeFile(hooks, apply) })
	list.add("claude", skill, "graft skill", ScopeRepo, func(apply bool) (RetractAction, error) { return f.removeFile(skill, apply) })
	list.add("claude", mcp, "mcpServers.graft", ScopeRepo, func(apply bool) (RetractAction, error) { return f.removeJSONKey(mcp, "mcpServers", apply) })
}

func (f *files) addGlobalTargets(list *targetList, home string, exclude func(string) bool) {
	if !exclude("claude") {
		global := ClaudeGlobalTargets(home)
		shim, settings, mcp := global[0], global[1], global[2]
		list.add("claude", shim.Path, shim.What, ScopeGlobal, func(apply bool) (RetractAction, error) { return f.removeFile(shim.Path, apply) })
		list.add("claude", settings.Path, settings.What, ScopeGlobal, func(apply bool) (RetractAction, error) { return f.stripClaudeSettings(settings.Path, apply) })
		list.add("claude", mcp.Path, mcp.What, ScopeGlobal, func(apply bool) (RetractAction, error) { return f.removeJSONKey(mcp.Path, "mcpServers", apply) })
	}
	if !exclude("agents") {
		for _, hook := range CodexHookTargets(home) {
			list.add(hook.HostID, hook.Path, hook.What, ScopeGlobal, func(apply bool) (RetractAction, error) {
				if strings.HasSuffix(hook.Path, ".json") {
					return f.stripCodexHooks(hook.Path, apply)
				}
				return f.removeFile(hook.Path, apply)
			})
		}
	}
	if !exclude("antigravity") {
		for _, skill := range AntigravitySkillTargets(home) {
			list.add(skill.HostID, skill.Path, skill.What, ScopeGlobal, func(apply bool) (RetractAction, error) { return f.removeFile(skill.Path, apply) })
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
	for _, target := range mcpTargets(repo, exclude, env.Home, true) {
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
// target it could have written. A target that cannot be handled is reported
// with its cause and does not stop the others.
func Retract(repo string, env Env, opts RetractOptions) []Retraction {
	f := openFiles(repo, env.Home)
	defer f.close()
	targets := f.retractTargets(repo, env, opts)
	out := make([]Retraction, len(targets))
	for i, target := range targets {
		out[i] = target.Retraction
		out[i].Action, out[i].Err = target.run(opts.Apply)
	}
	return out
}

// Changed keeps the retractions that had something to remove.
func Changed(retractions []Retraction) []Retraction {
	return slices.DeleteFunc(slices.Clone(retractions), func(retraction Retraction) bool { return retraction.Action == RetractAbsent })
}
