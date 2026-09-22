// The graft command provides the Go implementation of migrated CLI commands.
package main

import (
	"encoding/json"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"github.com/NanoNets/context-graph-engine/internal/graph"
)

type callersOptions struct {
	command     string
	query       string
	root        string
	rootSet     bool
	contextDir  string
	in          string
	ignoreCase  bool
	fixed       bool
	maxDirs     string
	limit       string
	direction   string
	depth       string
	source      bool
	full        bool
	noGraphRank bool
	jsonOutput  bool
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
	Name     string         `json:"name,omitempty"`
	Kind     graph.Kind     `json:"kind,omitempty"`
	Path     string         `json:"path,omitempty"`
	Span     string         `json:"span,omitempty"`
	Relation graph.Relation `json:"relation"`
	Depth    int            `json:"depth"`
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
	opts, err := parseArgs(args)
	if err != nil {
		writeDiagnostic(stderr, "%v\n", err)
		return 2
	}
	switch opts.command {
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
		writeDiagnostic(stderr, "unsupported command %q\n", opts.command)
		return 2
	}
}

func parseArgs(args []string) (callersOptions, error) {
	var opts callersOptions
	positionals := make([]string, 0, 3)
	for index := 0; index < len(args); index++ {
		arg := args[index]
		switch {
		case arg == "--json":
			opts.jsonOutput = true
		case arg == "-i" || arg == "--ignore-case":
			opts.ignoreCase = true
		case arg == "--fixed":
			opts.fixed = true
		case arg == "--max-dirs":
			value, err := nextOptionValue(args, &index, arg)
			if err != nil {
				return callersOptions{}, err
			}
			opts.maxDirs = value
		case strings.HasPrefix(arg, "--max-dirs="):
			opts.maxDirs = strings.TrimPrefix(arg, "--max-dirs=")
		case arg == "--limit" || arg == "-n":
			value, err := nextOptionValue(args, &index, arg)
			if err != nil {
				return callersOptions{}, err
			}
			opts.limit = value
		case strings.HasPrefix(arg, "--limit="):
			opts.limit = strings.TrimPrefix(arg, "--limit=")
		case strings.HasPrefix(arg, "-n="):
			opts.limit = strings.TrimPrefix(arg, "-n=")
		case arg == "--source":
			opts.source = true
		case arg == "--full":
			opts.full = true
		case arg == "--no-graph-rank":
			opts.noGraphRank = true
		case arg == "--no-refresh":
		case arg == "--dir":
			value, err := nextOptionValue(args, &index, arg)
			if err != nil {
				return callersOptions{}, err
			}
			opts.contextDir = value
		case strings.HasPrefix(arg, "--dir="):
			opts.contextDir = strings.TrimPrefix(arg, "--dir=")
		case arg == "--direction":
			value, err := nextOptionValue(args, &index, arg)
			if err != nil {
				return callersOptions{}, err
			}
			opts.direction = value
		case strings.HasPrefix(arg, "--direction="):
			opts.direction = strings.TrimPrefix(arg, "--direction=")
		case arg == "--depth" || arg == "-d":
			value, err := nextOptionValue(args, &index, arg)
			if err != nil {
				return callersOptions{}, err
			}
			opts.depth = value
		case strings.HasPrefix(arg, "--depth="):
			opts.depth = strings.TrimPrefix(arg, "--depth=")
		case strings.HasPrefix(arg, "-d="):
			opts.depth = strings.TrimPrefix(arg, "-d=")
		case arg == "--in":
			value, err := nextOptionValue(args, &index, arg)
			if err != nil {
				return callersOptions{}, err
			}
			opts.in = value
		case strings.HasPrefix(arg, "--in="):
			opts.in = strings.TrimPrefix(arg, "--in=")
		case arg == "--provider" || arg == "--model" || arg == "--api-key" || arg == "--base-url":
			if _, err := nextOptionValue(args, &index, arg); err != nil {
				return callersOptions{}, err
			}
		case strings.HasPrefix(arg, "-"):
			return callersOptions{}, fmt.Errorf("unknown option %q", arg)
		default:
			positionals = append(positionals, arg)
		}
	}
	if len(positionals) == 0 || (positionals[0] != "callers" && positionals[0] != "skeleton" && positionals[0] != "grep" && positionals[0] != "map" && positionals[0] != "ask" && positionals[0] != "mcp") {
		return callersOptions{}, fmt.Errorf("usage: graft <ask|callers|skeleton|grep|mcp> <query> [dir] [options] or graft map [dir] [options]")
	}
	if positionals[0] == "mcp" {
		if len(positionals) > 2 {
			return callersOptions{}, fmt.Errorf("unexpected argument %q", positionals[2])
		}
		opts.command = "mcp"
		if len(positionals) == 2 {
			opts.root = positionals[1]
			opts.rootSet = true
		}
		return opts, nil
	}
	if positionals[0] == "map" {
		if len(positionals) > 2 {
			return callersOptions{}, fmt.Errorf("unexpected argument %q", positionals[2])
		}
		opts.command = "map"
		if len(positionals) == 2 {
			opts.root = positionals[1]
			opts.rootSet = true
		}
		return opts, nil
	}
	if len(positionals) < 2 {
		return callersOptions{}, fmt.Errorf("usage: graft <ask|callers|skeleton|grep> <query> [dir] [options]")
	}
	if len(positionals) > 3 {
		return callersOptions{}, fmt.Errorf("unexpected argument %q", positionals[3])
	}
	opts.command = positionals[0]
	opts.query = positionals[1]
	if len(positionals) == 3 {
		opts.root = positionals[2]
		opts.rootSet = true
	}
	return opts, nil
}

func nextOptionValue(args []string, index *int, name string) (string, error) {
	if *index+1 >= len(args) {
		return "", fmt.Errorf("option %s requires a value", name)
	}
	*index++
	return args[*index], nil
}

func runCallers(opts callersOptions, stdout, stderr io.Writer) int {
	root, contextDir, err := resolvePaths(opts)
	if err != nil {
		writeDiagnostic(stderr, "✗ %v\n", err)
		return 1
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
		writeDiagnostic(stderr, "✗ no symbol %q in the graph — check spelling or run graft build\n", opts.query)
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
	return writeHuman(stdout, root, results, direction, depth)
}

func resolvePaths(opts callersOptions) (string, string, error) {
	root := opts.root
	if root == "" {
		var err error
		root, err = os.Getwd()
		if err != nil {
			return "", "", fmt.Errorf("failed to resolve working directory: %w", err)
		}
	}
	absoluteRoot, err := filepath.Abs(root)
	if err != nil {
		return "", "", fmt.Errorf("failed to resolve repository root %q: %w", root, err)
	}
	contextDir := opts.contextDir
	if contextDir == "" {
		if opts.rootSet {
			contextDir = filepath.Join(absoluteRoot, "graft")
		} else {
			contextDir = nearestContextDir(absoluteRoot)
		}
		return absoluteRoot, contextDir, nil
	}
	absoluteContext, err := filepath.Abs(contextDir)
	if err != nil {
		return "", "", fmt.Errorf("failed to resolve context directory %q: %w", opts.contextDir, err)
	}
	return absoluteRoot, absoluteContext, nil
}

func nearestContextDir(start string) string {
	for dir := start; ; dir = filepath.Dir(dir) {
		contextDir := filepath.Join(dir, "graft")
		if _, err := os.Stat(graph.WiringPath(contextDir)); err == nil {
			return contextDir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return filepath.Join(start, "graft")
		}
	}
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
		return "", fmt.Errorf(`--direction must be "in" or "out", got %q`, raw)
	}
}

func resolveDepth(raw string) (int, error) {
	if raw == "" {
		return 1, nil
	}
	if strings.EqualFold(raw, "all") || strings.EqualFold(raw, "full") || strings.EqualFold(raw, "max") {
		return int(^uint(0) >> 1), nil
	}
	value, err := strconv.ParseFloat(raw, 64)
	maxInt := float64(int(^uint(0) >> 1))
	if err != nil || math.IsNaN(value) || math.IsInf(value, 0) || value < 1 || value > maxInt {
		return 0, fmt.Errorf(`--depth must be a positive number or "all", got %q`, raw)
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
	data, err := json.MarshalIndent(payload, "", "  ")
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

func writeHuman(w io.Writer, root string, results []callersResult, direction graph.Direction, depth int) int {
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
	text := strings.TrimRight(body.String(), "\n") + "\n"
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
