// The graft command provides the Go implementation of migrated CLI commands.
package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/NanoNets/context-graph-engine/internal/brain"
	"github.com/NanoNets/context-graph-engine/internal/graph"
	"github.com/NanoNets/context-graph-engine/internal/jsonjs"
	"github.com/NanoNets/context-graph-engine/internal/telemetry"
	"github.com/NanoNets/context-graph-engine/internal/upkeep"
)

type callersOptions struct {
	command            string
	query              string
	root               string
	rootSet            bool
	contextDir         string
	in                 string
	ignoreCase         bool
	fixed              bool
	maxDirs            string
	limit              string
	direction          string
	depth              string
	source             bool
	full               bool
	noGraphRank        bool
	jsonOutput         bool
	noRefresh          bool
	noReuse            bool
	lsp                bool
	noGitignore        bool
	noIgnore           bool
	onlyDirs           []string
	extensions         []string
	includeDirs        []string
	followSubmodules   *bool
	followNestedRepos  *bool
	workspaceChildName string
	base               *string
	format             string
	name               bool
	noOwners           bool
	prAuthors          []string
}

type callersResult struct {
	symbol graph.NodeV1
	hits   []graph.EdgeHit
}

type symbolOutput struct {
	ID   string     `json:"id"`
	Name string     `json:"name"`
	Kind graph.Kind `json:"kind"`
	Path string     `json:"path"`
	Span string     `json:"span"`
}

type hitOutput struct {
	ID       string         `json:"id"`
	Relation graph.Relation `json:"relation"`
	Depth    int            `json:"depth"`
	Name     string         `json:"name,omitempty"`
	Kind     graph.Kind     `json:"kind,omitempty"`
	Path     string         `json:"path,omitempty"`
	Span     string         `json:"span,omitempty"`
}

type matchOutput struct {
	Symbol symbolOutput `json:"symbol"`
	Hits   []hitOutput  `json:"hits"`
	Note   string       `json:"note,omitempty"`
}

type savedOutput struct {
	Files         int `json:"files"`
	BaselineChars int `json:"baselineChars"`
}

type callersOutput struct {
	Query   string        `json:"query"`
	Matches []matchOutput `json:"matches"`
	Saved   *savedOutput  `json:"saved,omitempty"`
}

func main() {
	if status := run(os.Args[1:], os.Stdout, os.Stderr); status != 0 {
		os.Exit(status)
	}
}

func run(args []string, stdout, stderr io.Writer) int {
	queryNote.repo, queryNote.hit = "", ""
	parsed, err := parseCommandLine(programSpec(), args)
	var help *helpRequest
	var failure *cliError
	switch {
	case errors.As(err, &help):
		if help.toErr {
			writeDiagnostic(stderr, "%s", helpText(help.command))
			return 1
		}
		if _, err := io.WriteString(stdout, helpText(help.command)); err != nil {
			return 1
		}
		return 0
	case errors.As(err, &versionRequest{}):
		if _, err := io.WriteString(stdout, currentVersion()+"\n"); err != nil {
			return 1
		}
		return 0
	case errors.As(err, &failure):
		writeDiagnostic(stderr, "%s\n", failure.message)
		return 1
	case err != nil:
		writeDiagnostic(stderr, "%v\n", err)
		return 1
	}
	command := parsed.command.name
	pendingAction = func() { preAction(command, stderr) }
	status := dispatch(parsed, stdout, stderr)
	postAction(command, status)
	return status
}

func dispatch(parsed invocation, stdout, stderr io.Writer) int {
	args := parsed.args
	switch parsed.command.path() {
	case "_brain-refresh":
		// Spawned detached by upkeep: nothing here is user-visible, and a failure
		// leaves the cached rules serving until the next session tries again.
		dir := "."
		if len(args) > 0 {
			dir = args[0]
		}
		if repo, err := filepath.Abs(dir); err == nil {
			_, _, _ = brain.Pull(context.Background(), repo, homeDir(), nil, time.Now())
		}
		return 0
	case "_update-check":
		if home := homeDir(); home != "" {
			upkeep.RefreshUpdateCache(home, time.Now())
		}
		return 0
	case "_telemetry-flush":
		actionStarted()
		telemetry.RunFlush(homeDir())
		return 0
	case "version":
		return runVersion(stdout)
	case "upgrade":
		return runUpgrade(stdout, stderr)
	case "telemetry":
		return runTelemetry(parsed.flags, stdout, stderr)
	case "init":
		return runInit(parsed.flags, stdout, stderr)
	case "uninstall":
		return runUninstall(parsed.flags, stderr)
	case "brain connect", "brain pull", "brain push", "brain status", "brain disconnect":
		return runBrain(parsed.command.name, parsed.flags, stdout, stderr)
	}
	opts := queryOptions(parsed)
	actionStarted()
	switch opts.command {
	case "build":
		if value, ok := parsed.flags.value("--concurrency"); ok {
			if _, err := strconv.ParseFloat(strings.TrimSpace(value), 64); err != nil || strings.TrimSpace(value) == "" {
				writeDiagnostic(stderr, "✗ --concurrency must be a number, got \"%s\"\n", value)
				return 1
			}
		}
		return runBuild(opts, stdout, stderr)
	case "check":
		return runCheck(opts, stdout, stderr)
	case "stats":
		return runStats(opts, stdout, stderr)
	case "blast":
		return runBlast(opts, stdout, stderr)
	case "callers":
		return runCallers(opts, stdout, stderr)
	case "skeleton":
		return runSkeleton(opts, stdout, stderr)
	case "grep":
		return runGrep(opts, stdout, stderr)
	case "map":
		return runMap(opts, stdout, stderr)
	case "mcp":
		return runMCP(opts, os.Stdin, stdout, stderr)
	case "ask":
		return runAsk(opts, stdout, stderr)
	default:
		writeDiagnostic(stderr, "error: unknown command '%s'\n", opts.command)
		return 1
	}
}

// queryOptions maps a parsed query or build command onto callersOptions.
func queryOptions(parsed invocation) callersOptions {
	flags := parsed.flags
	value := func(name string) string {
		text, _ := flags.value(name)
		return text
	}
	opts := callersOptions{
		command:     parsed.command.name,
		contextDir:  value("--dir"),
		in:          value("--in"),
		ignoreCase:  flags.bools["--ignore-case"],
		fixed:       flags.bools["--fixed"],
		maxDirs:     value("--max-dirs"),
		limit:       value("--limit"),
		direction:   value("--direction"),
		depth:       value("--depth"),
		source:      flags.bools["--source"],
		full:        flags.bools["--full"],
		noGraphRank: flags.bools["--no-graph-rank"],
		jsonOutput:  flags.bools["--json"],
		noRefresh:   flags.bools["--no-refresh"],
		noReuse:     flags.bools["--no-reuse"],
		lsp:         flags.bools["--lsp"],
		noGitignore: flags.bools["--no-gitignore"],
		noIgnore:    flags.bools["--no-ignore"],
		onlyDirs:    flags.values["--only-dir"],
		extensions:  flags.values["--extensions"],
		includeDirs: flags.values["--include-dir"],
		format:      value("--format"),
		name:        flags.bools["--name"],
		noOwners:    flags.bools["--no-owners"],
		prAuthors:   flags.values["--pr-author"],
	}
	if base, ok := flags.value("--base"); ok {
		opts.base = &base
	}
	for _, pair := range []struct {
		name   string
		target **bool
	}{{"follow-submodules", &opts.followSubmodules}, {"follow-nested-repos", &opts.followNestedRepos}} {
		switch {
		case flags.bools["--"+pair.name]:
			follow := true
			*pair.target = &follow
		case flags.bools["--no-"+pair.name]:
			follow := false
			*pair.target = &follow
		}
	}
	args := parsed.args
	switch opts.command {
	case "ask", "callers", "skeleton", "grep":
		opts.query = args[0]
		args = args[1:]
	}
	if len(args) > 0 {
		opts.root, opts.rootSet = args[0], true
	} else if opts.command == "build" {
		opts.root, opts.rootSet = ".", true
	}
	return opts
}

func runCallers(opts callersOptions, stdout, stderr io.Writer) int {
	root, contextDir, err := resolvePaths(opts, queryPathRules, stderr)
	if err != nil {
		writeDiagnostic(stderr, "✗ %v\n", err)
		return 1
	}
	noteQueryRoot(opts)
	refreshBeforeQuery(root, contextDir, opts, stderr)
	if _, workspace := graph.ReadWorkspaceChildren(contextDir); workspace && !opts.jsonOutput {
		direction := graph.DirectionIn
		if opts.direction == "out" {
			direction = graph.DirectionOut
		}
		text, found, err := federateCallers(root, contextDir, opts.query, direction, workspaceCallersDepth(opts.depth), opts.in)
		switch {
		case err != nil:
			writeDiagnostic(stderr, "%v\n", err)
			return 1
		case !found:
			writeDiagnostic(stderr, "✗ %s\n", text)
			return 1
		}
		if _, err := io.WriteString(stdout, text+"\n"); err != nil {
			return 1
		}
		return 0
	}

	loaded, err := graph.Read(graph.WiringPath(contextDir))
	if err != nil {
		writeDiagnostic(stderr, "✗ no graph found at %s — run `graft build` first\n", contextDir)
		return 1
	}
	matches, err := graph.ResolveSymbol(*loaded, opts.query, graph.ResolveSymbolOptions{In: opts.in})
	if err != nil {
		writeDiagnostic(stderr, "✗ %v\n", err)
		return 1
	}
	if len(matches) == 0 {
		writeDiagnostic(stderr, "✗ no symbol \"%s\" in the graph — check spelling or run graft build\n", opts.query)
		return 1
	}

	direction, err := resolveDirection(opts.direction)
	if err != nil {
		writeDiagnostic(stderr, "✗ %v\n", err)
		return 1
	}
	depth, err := resolveDepth(opts.depth)
	if err != nil {
		writeDiagnostic(stderr, "✗ %v\n", err)
		return 1
	}
	results := make([]callersResult, len(matches))
	for index, symbol := range matches {
		results[index] = callersResult{symbol: symbol, hits: graph.EdgeWalk(*loaded, symbol, direction, depth)}
	}

	if opts.jsonOutput {
		return writeJSON(stdout, stderr, opts.query, *loaded, results, direction)
	}
	return writeHuman(stdout, root, *loaded, results, direction, depth)
}

// pathRules says how a command finds its repository and graph, as the
// TypeScript command does: query commands walk up to the nearest indexed
// ancestor (queryRoot), and only the commands whose TypeScript engine reads
// GRAFT_DIR honor it — the others, and every pre-query refresh, read --dir only.
type pathRules struct {
	walkUp   bool
	honorEnv bool
}

var (
	// queryPathRules serves grep, callers, map, skeleton and mcp.
	queryPathRules = pathRules{walkUp: true}
	// enginePathRules serves ask and check, which read their graph through the engine.
	enginePathRules = pathRules{walkUp: true, honorEnv: true}
	// buildPathRules serves build, which never walks up.
	buildPathRules = pathRules{honorEnv: true}
)

// resolvePaths returns a command's absolute root and graph directory. A walk
// up to an ancestor's graph is announced on stderr, never silent.
func resolvePaths(opts callersOptions, rules pathRules, stderr io.Writer) (string, string, error) {
	root := opts.root
	if root == "" {
		cwd, err := os.Getwd()
		if err != nil {
			return "", "", fmt.Errorf("failed to resolve working directory: %w", err)
		}
		root = cwd
		if rules.walkUp {
			root = nearestGraftRoot(cwd, opts.contextDir)
			if root != cwd {
				writeDiagnostic(stderr, "[graft] no graft/ here — answering from %s/graft\n", root)
			}
		}
	}
	absoluteRoot, err := filepath.Abs(root)
	if err != nil {
		return "", "", fmt.Errorf("failed to resolve repository root %q: %w", root, err)
	}
	return absoluteRoot, graphDir(absoluteRoot, opts, rules.honorEnv), nil
}

// graphDir is --dir, else GRAFT_DIR where honored (relative to the working
// directory, as the TypeScript engine passes it through), else <root>/graft.
func graphDir(root string, opts callersOptions, honorEnv bool) string {
	dir := opts.contextDir
	if dir == "" && honorEnv && opts.workspaceChildName == "" {
		dir = os.Getenv("GRAFT_DIR")
	}
	if dir == "" {
		return filepath.Join(root, "graft")
	}
	if absolute, err := filepath.Abs(dir); err == nil {
		return absolute
	}
	return dir
}

func refreshBeforeQuery(root, contextDir string, opts callersOptions, stderr io.Writer) {
	if opts.noRefresh {
		return
	}
	var refreshed graph.RefreshResult
	if children, workspace := graph.ReadWorkspaceChildren(contextDir); workspace {
		eligible := make([]string, 0, len(children))
		for _, child := range children {
			if refreshableGraph(filepath.Join(root, child, "graft")) {
				eligible = append(eligible, child)
			}
		}
		refreshed = graph.EnsureFreshChildren(root, eligible)
	} else {
		if !refreshableGraph(contextDir) {
			return
		}
		options := graph.RefreshOptions{}
		if opts.contextDir != "" {
			options.Source.OutDir = contextDir
		}
		refreshed = graph.EnsureFreshGraph(root, options)
	}
	if note := graph.RefreshNote(refreshed); note != "" {
		writeDiagnostic(stderr, "%s\n", note)
	}
}

func refreshableGraph(contextDir string) bool {
	if _, err := os.Stat(graph.WiringPath(contextDir)); err != nil {
		return true
	}
	paths, err := filepath.Glob(filepath.Join(contextDir, ".cache", "fingerprint.*.json"))
	return err == nil && len(paths) > 0
}

func resolveDirection(raw string) (graph.Direction, error) {
	switch raw {
	case "":
		return graph.DirectionIn, nil
	case string(graph.DirectionIn):
		return graph.DirectionIn, nil
	case string(graph.DirectionOut):
		return graph.DirectionOut, nil
	default:
		return "", fmt.Errorf(`--direction must be "in" or "out", got "%s"`, raw)
	}
}

func resolveDepth(raw string) (int, error) {
	if raw == "" {
		return 1, nil
	}
	if strings.EqualFold(raw, "all") || strings.EqualFold(raw, "full") || strings.EqualFold(raw, "max") {
		return int(^uint(0) >> 1), nil
	}
	// Number(raw), as the TypeScript CLI reads it: hex, exponents and
	// surrounding white space are numbers too.
	value := jsonjs.ToNumber(raw)
	if math.IsNaN(value) || math.IsInf(value, 0) || value < 1 {
		return 0, fmt.Errorf(`--depth must be a positive number or "all", got "%s"`, raw)
	}
	if maxInt := float64(int(^uint(0) >> 1)); value >= maxInt {
		return int(^uint(0) >> 1), nil
	}
	return int(math.Floor(value)), nil
}

func writeJSON(w, stderr io.Writer, query string, wiring graph.GraphV1, results []callersResult, direction graph.Direction) int {
	payload := callersOutput{Query: query, Matches: make([]matchOutput, 0, len(results)), Saved: callersSavings(wiring, results)}
	for _, result := range results {
		match := matchOutput{Symbol: symbolJSON(result.symbol), Hits: make([]hitOutput, 0, len(result.hits))}
		for _, hit := range result.hits {
			match.Hits = append(match.Hits, hitJSON(hit))
		}
		if len(result.hits) == 0 {
			match.Note = looseNote(direction, result.symbol.Name, len(results))
		}
		payload.Matches = append(payload.Matches, match)
	}
	data, err := jsonjs.Marshal(payload, "  ")
	if err != nil {
		writeDiagnostic(stderr, "✗ failed to encode callers result: %v\n", err)
		return 1
	}
	if _, err := fmt.Fprintf(w, "%s\n", data); err != nil {
		return 1
	}
	return 0
}

func writeDiagnostic(w io.Writer, format string, args ...any) {
	if _, err := fmt.Fprintf(w, format, args...); err != nil {
		return
	}
}

func writeHuman(w io.Writer, root string, wiring graph.GraphV1, results []callersResult, direction graph.Direction, depth int) int {
	var body strings.Builder
	for _, result := range results {
		fmt.Fprintf(&body, "%s · %s · %s:%s\n", result.symbol.Name, result.symbol.Kind, result.symbol.Path, result.symbol.Span)
		if len(result.hits) == 0 {
			body.WriteString(looseNote(direction, result.symbol.Name, len(results)))
			body.WriteByte('\n')
		} else {
			for _, hit := range result.hits {
				arrow := "←"
				if direction == graph.DirectionOut {
					arrow = "→"
				}
				label := fmt.Sprintf("%s (unresolved import)", hit.ID)
				if hit.Node != nil {
					label = fmt.Sprintf("%s (%s:%s)", hit.Node.Name, hit.Node.Path, hit.Node.Span)
				}
				depthLabel := ""
				if depth > 1 {
					depthLabel = fmt.Sprintf(" [depth %d]", hit.Depth)
				}
				fmt.Fprintf(&body, "  %s %s %s%s\n", hit.Relation, arrow, label, depthLabel)
				if line, number, ok := quoteFor(root, result.symbol.Name, hit); ok {
					fmt.Fprintf(&body, "      %d: %s\n", number, strings.TrimSpace(line))
				}
			}
		}
		body.WriteByte('\n')
	}
	text := mcpWithSavings(strings.TrimRight(body.String(), "\n")+"\n", callersSavings(wiring, results))
	if _, err := io.WriteString(w, text); err != nil {
		return 1
	}
	return 0
}

func symbolJSON(node graph.NodeV1) symbolOutput {
	return symbolOutput{ID: node.ID, Name: node.Name, Kind: node.Kind, Path: node.Path, Span: node.Span}
}

func hitJSON(hit graph.EdgeHit) hitOutput {
	output := hitOutput{ID: hit.ID, Relation: hit.Relation, Depth: hit.Depth}
	if hit.Node != nil {
		output.Name = hit.Node.Name
		output.Kind = hit.Node.Kind
		output.Path = hit.Node.Path
		output.Span = hit.Node.Span
	}
	return output
}

func callersSavings(wiring graph.GraphV1, results []callersResult) *savedOutput {
	fileChars := make(map[string]int)
	for _, node := range wiring.Nodes {
		if node.Kind == graph.Kind("file") && node.Chars != nil {
			fileChars[node.Path] = *node.Chars
		}
	}
	paths := make(map[string]struct{})
	for _, result := range results {
		paths[result.symbol.Path] = struct{}{}
		for _, hit := range result.hits {
			if hit.Node != nil {
				paths[hit.Node.Path] = struct{}{}
			}
		}
	}
	saved := &savedOutput{}
	for path := range paths {
		chars, ok := fileChars[path]
		if !ok {
			continue
		}
		saved.Files++
		saved.BaselineChars += chars
	}
	if saved.Files == 0 {
		return nil
	}
	return saved
}

func looseNote(direction graph.Direction, name string, candidateCount int) string {
	label, movement := "callers", "incoming"
	if direction == graph.DirectionOut {
		label, movement = "callees", "outgoing"
	}
	ambiguity := ""
	if candidateCount > 1 {
		ambiguity = fmt.Sprintf(" %d definitions share the name %q; a cross-file caller of an ambiguous name is dropped rather than guessed, so this may undercount.", candidateCount, name)
	}
	return fmt.Sprintf("  no indexed %s — the graph has no %s call/reference edges for this symbol as written.%s Check the name (try the bare symbol, or \"Type.method\"), or find its uses with graft grep %q. Fall back to raw grep -rn only for unindexed files", label, movement, ambiguity, name)
}

func quoteFor(root, name string, hit graph.EdgeHit) (string, int, bool) {
	if hit.Node == nil || hit.Depth > 1 {
		return "", 0, false
	}
	start, end, ok := spanLines(hit.Node.Span)
	if !ok {
		return "", 0, false
	}
	data, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(hit.Node.Path)))
	if err != nil {
		return "", 0, false
	}
	lines := strings.Split(string(data), "\n")
	pattern := regexp.MustCompile(`(^|[^[:alnum:]_$])` + regexp.QuoteMeta(name) + `([^[:alnum:]_$]|$)`)
	for line := max(start, 1); line <= min(end, len(lines)); line++ {
		if pattern.MatchString(lines[line-1]) {
			return lines[line-1], line, true
		}
	}
	return "", 0, false
}

func spanLines(span string) (int, int, bool) {
	parts := strings.Split(span, "-")
	if len(parts) != 2 || !strings.HasPrefix(parts[0], "L") || !strings.HasPrefix(parts[1], "L") {
		return 0, 0, false
	}
	start, startErr := strconv.Atoi(strings.TrimPrefix(parts[0], "L"))
	end, endErr := strconv.Atoi(strings.TrimPrefix(parts[1], "L"))
	return start, end, startErr == nil && endErr == nil && start <= end
}
