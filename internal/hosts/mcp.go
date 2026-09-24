package hosts

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/h0rn3t/Graft/internal/jsonjs"
)

// Launch is how a host starts the graft MCP server.
type Launch struct {
	Command string
	Args    []string
	// resolve, when set, decides Command and Args on first use.
	resolve func() Launch
}

var binLaunch = Launch{Command: "graft", Args: []string{"mcp"}}

// ServerEntry returns the MCP launch command for this run, decided only when
// a config is written: GRAFT_MCP_COMMAND when set, else the installed binary
// when `graft --version` succeeds on PATH, else the absolute path of the
// running executable, so a host never has to download graft to start it.
func ServerEntry() Launch {
	if command := os.Getenv("GRAFT_MCP_COMMAND"); command != "" {
		return Launch{Command: command, Args: []string{"mcp"}}
	}
	return Launch{resolve: sync.OnceValue(probeLaunch)}
}

func probeLaunch() Launch {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if exec.CommandContext(ctx, "graft", "--version").Run() == nil {
		return binLaunch
	}
	executable, err := os.Executable()
	if err != nil {
		return binLaunch
	}
	if resolved, err := filepath.EvalSymlinks(executable); err == nil {
		executable = resolved
	}
	return Launch{Command: executable, Args: []string{"mcp"}}
}

func (launch Launch) resolved() Launch {
	if launch.resolve != nil {
		return launch.resolve()
	}
	return launch
}

func envTruthy(name string) bool {
	value := os.Getenv(name)
	return value != "" && value != "0" && value != "false"
}

func (launch Launch) object() *jsonjs.Object {
	launch = launch.resolved()
	entry := jsonjs.NewObject()
	entry.Set("command", launch.Command)
	entry.Set("args", stringValues(launch.Args))
	return entry
}

func (launch Launch) opencode() *jsonjs.Object {
	launch = launch.resolved()
	entry := jsonjs.NewObject()
	entry.Set("type", "local")
	entry.Set("command", stringValues(append([]string{launch.Command}, launch.Args...)))
	entry.Set("enabled", true)
	return entry
}

func stringValues(values []string) []jsonjs.Value {
	out := make([]jsonjs.Value, len(values))
	for i, value := range values {
		out[i] = value
	}
	return out
}

// Format is an MCP config file format.
type Format string

// MCP config formats.
const (
	FormatJSON Format = "json"
	FormatTOML Format = "toml"
)

// MCPTarget is a planned MCP write plus what performing it needs.
type MCPTarget struct {
	PlannedWrite
	Format Format
	TopKey string
}

func jsonTarget(hostID, id, path, topKey string, scope Scope) MCPTarget {
	return MCPTarget{
		HostID: hostID, ID: id, Path: path, Scope: scope, Kind: WriteMCP, What: topKey + ".graft",
		Format: FormatJSON, TopKey: topKey,
	}
}

// MCPTargets lists the MCP config files selecting ids would touch. Codex's
// config and Antigravity's registry live under home and are scoped global.
// Codex and opencode configs are listed only when their tool is installed.
func MCPTargets(repo string, ids []string, home string) []MCPTarget {
	return mcpTargets(repo, ids, home, false)
}

// mcpTargets is MCPTargets; everything also lists the Codex and opencode
// configs of a machine without those tools, for a retraction to find.
func mcpTargets(repo string, ids []string, home string, everything bool) []MCPTarget {
	out := make([]MCPTarget, 0)
	for _, id := range ids {
		switch id {
		case "cursor":
			out = append(out, jsonTarget(id, id, filepath.Join(repo, ".cursor", "mcp.json"), "mcpServers", ScopeRepo))
		case "gemini":
			out = append(out, jsonTarget(id, id, filepath.Join(repo, ".gemini", "settings.json"), "mcpServers", ScopeRepo))
		case "antigravity":
			out = append(out, jsonTarget(id, "antigravity", filepath.Join(home, ".gemini", "config", "mcp_config.json"), "mcpServers", ScopeGlobal))
		case "kiro":
			out = append(out, jsonTarget(id, id, filepath.Join(repo, ".kiro", "settings", "mcp.json"), "mcpServers", ScopeRepo))
		case "grok":
			out = append(out, MCPTarget{
				HostID: id, ID: "grok", Path: filepath.Join(repo, ".grok", "config.toml"), Scope: ScopeRepo, Kind: WriteMCP, What: tomlHeader,
				Format: FormatTOML,
			})
		case "agents":
			if everything || dirExists(filepath.Join(home, ".codex")) {
				out = append(out, MCPTarget{
					HostID: id, ID: "codex", Path: filepath.Join(home, ".codex", "config.toml"), Scope: ScopeGlobal, Kind: WriteMCP, What: tomlHeader,
					Format: FormatTOML,
				})
			}
			if everything || dirExists(filepath.Join(home, ".config", "opencode")) {
				out = append(out, jsonTarget(id, "opencode", filepath.Join(repo, "opencode.json"), "mcp", ScopeRepo))
			}
		}
	}
	return out
}

// mergeJSONKey sets <topKey>.graft = entry in a JSON config, preserving every
// other key and never rewriting an unparseable file.
func (f *files) mergeJSONKey(id, path, topKey string, entry *jsonjs.Object) (ConfigWrite, error) {
	root, existed, ok, err := f.readJSONObject(path)
	if err != nil {
		return ConfigWrite{}, err
	}
	if !ok {
		return ConfigWrite{ID: id, Path: path, Action: ActionUnparseable}, nil
	}
	bucket, ok := ensureObject(root, topKey)
	if !ok {
		return ConfigWrite{ID: id, Path: path, Action: ActionUnparseable}, nil
	}
	if current, ok := bucket.Get("graft"); ok && jsonjs.Equal(current, entry) {
		return ConfigWrite{ID: id, Path: path, Action: ActionUnchanged}, nil
	}
	action := ActionCreated
	if existed {
		action = ActionUpdated
	}
	bucket.Set("graft", entry)
	if err := f.writeJSON(path, root); err != nil {
		return ConfigWrite{}, err
	}
	return ConfigWrite{ID: id, Path: path, Action: action}, nil
}

const tomlHeader = "[mcp_servers.graft]"

// tomlTableKey parses a TOML table header line, [a.b] or [[a.b]], into its
// key path, allowing spaces around the dots, quoted keys, and a trailing
// comment. It reports false for any other line.
func tomlTableKey(line string) ([]string, bool) {
	text := strings.TrimSpace(line)
	if !strings.HasPrefix(text, "[") {
		return nil, false
	}
	closing := "]"
	text = text[1:]
	if strings.HasPrefix(text, "[") {
		closing, text = "]]", text[1:]
	}
	var keys []string
	for {
		text = strings.TrimLeft(text, " \t")
		key, rest, ok := tomlKey(text)
		if !ok {
			return nil, false
		}
		keys = append(keys, key)
		rest = strings.TrimLeft(rest, " \t")
		if next, found := strings.CutPrefix(rest, "."); found {
			text = next
			continue
		}
		after, found := strings.CutPrefix(rest, closing)
		if !found {
			return nil, false
		}
		after = strings.TrimSpace(after)
		return keys, after == "" || strings.HasPrefix(after, "#")
	}
}

// tomlKey reads one bare, basic-quoted, or literal-quoted key off text.
func tomlKey(text string) (key, rest string, ok bool) {
	switch {
	case strings.HasPrefix(text, `"`):
		end := 1
		for end < len(text) && text[end] != '"' {
			if text[end] == '\\' {
				end++
			}
			end++
		}
		if end >= len(text) {
			return "", "", false
		}
		unquoted, err := strconv.Unquote(text[:end+1])
		return unquoted, text[end+1:], err == nil
	case strings.HasPrefix(text, "'"):
		literal, rest, found := strings.Cut(text[1:], "'")
		return literal, rest, found
	default:
		end := strings.IndexFunc(text, func(r rune) bool {
			return (r < 'a' || r > 'z') && (r < 'A' || r > 'Z') && (r < '0' || r > '9') && r != '_' && r != '-'
		})
		if end == -1 {
			end = len(text)
		}
		return text[:end], text[end:], end > 0
	}
}

func isTOMLComment(line string) bool {
	return strings.HasPrefix(strings.TrimSpace(line), "#")
}

// StripTOMLSection removes graft's [mcp_servers.graft] table and every
// [mcp_servers.graft.*] subtable. Each table runs from its header to the next
// header, less the comments directly above that header, which belong to it.
// Blank lines are tidied only where a table was cut out.
func StripTOMLSection(text string) (string, bool) {
	return stripTOMLTables(text, true)
}

// stripTOMLTables removes graft's table, and its subtables when subtables is
// set; a rewrite keeps the subtables, which hold the user's own settings.
func stripTOMLTables(text string, subtables bool) (string, bool) {
	graft := []string{"mcp_servers", "graft"}
	ours := func(keys []string) bool {
		if subtables {
			return len(keys) >= len(graft) && slices.Equal(keys[:len(graft)], graft)
		}
		return slices.Equal(keys, graft)
	}
	lines := strings.Split(text, "\n")
	kept := make([]string, 0, len(lines))
	found := false
	for i := 0; i < len(lines); {
		keys, header := tomlTableKey(lines[i])
		if !header || !ours(keys) {
			kept = append(kept, lines[i])
			i++
			continue
		}
		found = true
		end := i + 1
		for end < len(lines) {
			if _, next := tomlTableKey(lines[end]); next {
				break
			}
			end++
		}
		if end < len(lines) {
			for end > i+1 && isTOMLComment(lines[end-1]) {
				end--
			}
		}
		// One blank line is left at the seam, none at either end of the file.
		for len(kept) > 0 && isBlank(kept[len(kept)-1]) {
			kept = kept[:len(kept)-1]
		}
		i = end
		for i < len(lines) && isBlank(lines[i]) {
			i++
		}
		if len(kept) > 0 && i < len(lines) {
			kept = append(kept, "")
		}
	}
	if !found {
		return text, false
	}
	if len(kept) > 0 && strings.HasSuffix(text, "\n") && kept[len(kept)-1] != "" {
		kept = append(kept, "")
	}
	return strings.Join(kept, "\n"), true
}

func appendTOMLSection(text, section string) string {
	if strings.TrimSpace(text) == "" {
		return section
	}
	separator := "\n\n"
	switch {
	case strings.HasSuffix(text, "\n\n"):
		separator = ""
	case strings.HasSuffix(text, "\n"):
		separator = "\n"
	}
	return text + separator + section
}

// upsertTOML replaces graft's table in a TOML config with the current launch,
// keeping any [mcp_servers.graft.*] subtable the user added.
func (f *files) upsertTOML(id, path string, launch Launch) (ConfigWrite, error) {
	data, err := f.readFile(path)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return ConfigWrite{}, err
	}
	existed := err == nil
	text := string(data)
	launch = launch.resolved()
	quoted := make([]string, len(launch.Args))
	for i, arg := range launch.Args {
		quoted[i] = jsonjs.Quote(arg)
	}
	section := tomlHeader + "\ncommand = " + jsonjs.Quote(launch.Command) + "\nargs = [" + strings.Join(quoted, ", ") + "]\n"
	rest, found := stripTOMLTables(text, false)
	next := appendTOMLSection(rest, section)
	if found && text == next {
		return ConfigWrite{ID: id, Path: path, Action: ActionUnchanged}, nil
	}
	if err := f.writeFile(path, []byte(next), 0o644); err != nil {
		return ConfigWrite{}, err
	}
	action := ActionCreated
	if existed {
		action = ActionUpdated
	}
	return ConfigWrite{ID: id, Path: path, Action: action}, nil
}

// registerMCPConfigs registers graft in every MCP config the hosts use,
// skipping the global ones when global is false.
func (f *files) registerMCPConfigs(repo string, ids []string, home string, global bool, launch Launch) ([]ConfigWrite, error) {
	writes := make([]ConfigWrite, 0)
	for _, target := range MCPTargets(repo, ids, home) {
		if !global && target.Scope == ScopeGlobal {
			continue
		}
		var write ConfigWrite
		var err error
		switch {
		case target.Format == FormatTOML:
			write, err = f.upsertTOML(target.ID, target.Path, launch)
		case target.ID == "opencode":
			write, err = f.mergeJSONKey(target.ID, target.Path, target.TopKey, launch.opencode())
		default:
			write, err = f.mergeJSONKey(target.ID, target.Path, target.TopKey, launch.object())
		}
		if err != nil {
			return writes, err
		}
		writes = append(writes, write)
	}
	return writes, nil
}
