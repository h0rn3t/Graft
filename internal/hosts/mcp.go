package hosts

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/NanoNets/context-graph-engine/internal/jsonjs"
)

// Launch is how a host starts the graft MCP server.
type Launch struct {
	Command string
	Args    []string
}

var (
	npxLaunch = Launch{Command: "npx", Args: []string{"-y", "@nanonets/graft", "mcp"}}
	binLaunch = Launch{Command: "graft", Args: []string{"mcp"}}
)

// ServerEntry picks the MCP launch command once per init: the installed
// binary when `graft --version` succeeds on PATH, npx otherwise, and npx
// always when GRAFT_MCP_NPX is truthy.
func ServerEntry() Launch {
	if envTruthy("GRAFT_MCP_NPX") {
		return npxLaunch
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if exec.CommandContext(ctx, "graft", "--version").Run() == nil {
		return binLaunch
	}
	return npxLaunch
}

func envTruthy(name string) bool {
	value := os.Getenv(name)
	return value != "" && value != "0" && value != "false"
}

func (launch Launch) object() *jsonjs.Object {
	entry := jsonjs.NewObject()
	entry.Set("command", launch.Command)
	entry.Set("args", stringValues(launch.Args))
	return entry
}

func (launch Launch) opencode() *jsonjs.Object {
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
	Entry  *jsonjs.Object
}

func jsonTarget(hostID, id, path, topKey string, entry *jsonjs.Object, scope Scope) MCPTarget {
	return MCPTarget{
		HostID: hostID, ID: id, Path: path, Scope: scope, Kind: WriteMCP, What: topKey + ".graft",
		Format: FormatJSON, TopKey: topKey, Entry: entry,
	}
}

// MCPTargets lists the MCP config files selecting ids would touch. Codex's
// config and Antigravity's registry live under home and are scoped global.
func MCPTargets(repo string, ids []string, home string, launch Launch) []MCPTarget {
	entry := launch.object()
	out := make([]MCPTarget, 0)
	for _, id := range ids {
		switch id {
		case "cursor":
			out = append(out, jsonTarget(id, id, filepath.Join(repo, ".cursor", "mcp.json"), "mcpServers", entry, ScopeRepo))
		case "gemini":
			out = append(out, jsonTarget(id, id, filepath.Join(repo, ".gemini", "settings.json"), "mcpServers", entry, ScopeRepo))
		case "antigravity":
			out = append(out, jsonTarget(id, "antigravity", filepath.Join(home, ".gemini", "config", "mcp_config.json"), "mcpServers", entry, ScopeGlobal))
		case "kiro":
			out = append(out, jsonTarget(id, id, filepath.Join(repo, ".kiro", "settings", "mcp.json"), "mcpServers", entry, ScopeRepo))
		case "grok":
			out = append(out, MCPTarget{
				HostID: id, ID: "grok", Path: filepath.Join(repo, ".grok", "config.toml"), Scope: ScopeRepo, Kind: WriteMCP, What: tomlHeader,
				Format: FormatTOML,
			})
		case "agents":
			if dirExists(filepath.Join(home, ".codex")) {
				out = append(out, MCPTarget{
					HostID: id, ID: "codex", Path: filepath.Join(home, ".codex", "config.toml"), Scope: ScopeGlobal, Kind: WriteMCP, What: tomlHeader,
					Format: FormatTOML,
				})
			}
			if dirExists(filepath.Join(home, ".config", "opencode")) {
				out = append(out, jsonTarget(id, "opencode", filepath.Join(repo, "opencode.json"), "mcp", launch.opencode(), ScopeRepo))
			}
		}
	}
	return out
}

// MergeJSONKey sets <topKey>.graft = entry in a JSON config, preserving every
// other key and never rewriting an unparseable file.
func MergeJSONKey(id, path, topKey string, entry *jsonjs.Object) (ConfigWrite, error) {
	root, existed, ok := readJSONObject(path)
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
	if err := writeJSON(path, root); err != nil {
		return ConfigWrite{}, err
	}
	return ConfigWrite{ID: id, Path: path, Action: action}, nil
}

const tomlHeader = "[mcp_servers.graft]"

var (
	blankRuns      = regexp.MustCompile(`\n{3,}`)
	leadingBlanks  = regexp.MustCompile(`^\n+`)
	trailingBlanks = regexp.MustCompile(`\n+$`)
)

// StripTOMLSection removes the [mcp_servers.graft] table, which runs from its
// header to the next table header or the end of the file.
func StripTOMLSection(text string) (string, bool) {
	lines := strings.Split(text, "\n")
	start := -1
	for i, line := range lines {
		if strings.TrimSpace(line) == tomlHeader {
			start = i
			break
		}
	}
	if start == -1 {
		return text, false
	}
	end := start + 1
	for end < len(lines) && !strings.HasPrefix(strings.TrimLeft(lines[end], " \t\r\n\f\v"), "[") {
		end++
	}
	rest := strings.Join(append(append([]string{}, lines[:start]...), lines[end:]...), "\n")
	rest = blankRuns.ReplaceAllString(rest, "\n\n")
	return leadingBlanks.ReplaceAllString(rest, ""), true
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

// upsertTOML replaces graft's table in a TOML config with the current launch.
func upsertTOML(id, path string, launch Launch) (ConfigWrite, error) {
	data, err := os.ReadFile(path)
	existed := err == nil
	text := string(data)
	quoted := make([]string, len(launch.Args))
	for i, arg := range launch.Args {
		quoted[i] = jsonjs.Quote(arg)
	}
	section := tomlHeader + "\ncommand = \"" + launch.Command + "\"\nargs = [" + strings.Join(quoted, ", ") + "]\n"
	rest, found := StripTOMLSection(text)
	next := appendTOMLSection(rest, section)
	if found && text == next {
		return ConfigWrite{ID: id, Path: path, Action: ActionUnchanged}, nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return ConfigWrite{}, err
	}
	if err := os.WriteFile(path, []byte(next), 0o644); err != nil {
		return ConfigWrite{}, err
	}
	action := ActionCreated
	if existed {
		action = ActionUpdated
	}
	return ConfigWrite{ID: id, Path: path, Action: action}, nil
}

// RegisterMCPConfigs registers graft in every MCP config the hosts use,
// skipping the global ones when global is false.
func RegisterMCPConfigs(repo string, ids []string, home string, global bool, launch Launch) ([]ConfigWrite, error) {
	writes := make([]ConfigWrite, 0)
	for _, target := range MCPTargets(repo, ids, home, launch) {
		if !global && target.Scope == ScopeGlobal {
			continue
		}
		var write ConfigWrite
		var err error
		if target.Format == FormatTOML {
			write, err = upsertTOML(target.ID, target.Path, launch)
		} else {
			write, err = MergeJSONKey(target.ID, target.Path, target.TopKey, target.Entry)
		}
		if err != nil {
			return writes, err
		}
		writes = append(writes, write)
	}
	return writes, nil
}
