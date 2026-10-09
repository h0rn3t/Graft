package main

import (
	"context"
	"fmt"
	"io"
	"strconv"
	"strings"
	"time"

	"github.com/h0rn3t/Graft/internal/gitx"
	"github.com/h0rn3t/Graft/internal/graph"
	"github.com/h0rn3t/Graft/internal/jsonjs"
)

const (
	pathDefaultDepth     = 10
	complexityTopDefault = 20
	hotspotsTopDefault   = 15
	hotspotsCommits      = 500
	// hotspotsGitTimeout bounds the one git log hotspots reads.
	hotspotsGitTimeout = 2 * time.Minute
)

// loadAnalysisGraph resolves, refreshes and reads the graph of the one
// repository an analysis command answers for. A multi-repo workspace root has
// no single graph, so the command is pointed at a child instead.
func loadAnalysisGraph(opts callersOptions, stderr io.Writer) (string, *graph.GraphV1, bool) {
	root, contextDir, err := resolvePaths(opts, queryPathRules, stderr)
	if err != nil {
		writeDiagnostic(stderr, "✗ %v\n", err)
		return "", nil, false
	}
	noteQueryRoot(opts)
	refreshBeforeQuery(root, contextDir, opts, stderr)
	if _, workspace := graph.ReadWorkspaceChildren(contextDir); workspace {
		writeDiagnostic(stderr, "✗ %s holds several repositories — run graft %s inside one of them\n", root, opts.command)
		return "", nil, false
	}
	loaded, err := graph.Read(graph.WiringPath(contextDir))
	if err != nil {
		writeDiagnostic(stderr, "✗ no graph — run graft build first\n")
		return "", nil, false
	}
	return root, loaded, true
}

// writeJSONResult prints value as indented JSON; what names it in an error.
func writeJSONResult(stdout, stderr io.Writer, value any, what string) int {
	data, err := jsonjs.Marshal(value, "  ")
	if err != nil {
		writeDiagnostic(stderr, "✗ failed to encode %s: %v\n", what, err)
		return 1
	}
	if _, err := fmt.Fprintf(stdout, "%s\n", data); err != nil {
		return 1
	}
	return 0
}

// positiveOption reads a positive integer option, or def when it is unset.
func positiveOption(raw, name string, def int) (int, error) {
	if raw == "" {
		return def, nil
	}
	value, err := strconv.Atoi(raw)
	if err != nil || value <= 0 {
		return 0, fmt.Errorf("%s must be a positive integer, got %q", name, raw)
	}
	return value, nil
}

// nodeLabel is a node as the listings print it: `name · kind · path:span`.
func nodeLabel(node graph.NodeV1) string {
	return fmt.Sprintf("%s · %s · %s:%s", node.Name, node.Kind, node.Path, node.Span)
}

// runPath prints a shortest chain of calls, references and imports from one
// symbol to another.
func runPath(opts callersOptions, stdout, stderr io.Writer) int {
	depth := pathDefaultDepth
	if opts.depth != "" {
		var err error
		if depth, err = resolveDepth(opts.depth); err != nil {
			writeDiagnostic(stderr, "✗ %v\n", err)
			return 1
		}
	}
	_, loaded, ok := loadAnalysisGraph(opts, stderr)
	if !ok {
		return 1
	}
	result, err := findPath(*loaded, opts.query, opts.target, opts.in, depth)
	if err != nil {
		writeDiagnostic(stderr, "✗ %v\n", err)
		return 1
	}
	if opts.jsonOutput {
		return writeJSONResult(stdout, stderr, result.output(), "path")
	}
	if _, err := io.WriteString(stdout, result.text()); err != nil {
		return 1
	}
	return 0
}

// pathResult is a path query and its answer; steps is nil when no path
// exists within depth.
type pathResult struct {
	graph    graph.GraphV1
	from, to string
	depth    int
	steps    []graph.PathStep
}

// findPath resolves both symbols, under the in prefix, and walks between them.
func findPath(loaded graph.GraphV1, from, to, in string, depth int) (pathResult, error) {
	result := pathResult{graph: loaded, from: from, to: to, depth: depth}
	var ends [2][]graph.NodeV1
	for index, query := range []string{from, to} {
		matches, err := graph.ResolveSymbol(loaded, query, graph.ResolveSymbolOptions{In: in})
		if err != nil {
			return result, err
		}
		if len(matches) == 0 {
			return result, fmt.Errorf("no symbol \"%s\" in the graph — check spelling or run graft build", query)
		}
		ends[index] = matches
	}
	result.steps = graph.ShortestPath(loaded, ends[0], ends[1], depth)
	return result, nil
}

func (result pathResult) node(id string) (graph.NodeV1, bool) {
	for _, node := range result.graph.Nodes {
		if node.ID == id {
			return node, true
		}
	}
	return graph.NodeV1{}, false
}

// text renders the path one step per line, the start first. An inferred step
// is marked, since the path is only as sure as its weakest step.
func (result pathResult) text() string {
	if result.steps == nil {
		limit := fmt.Sprintf("within %d steps", result.depth)
		if result.depth == int(^uint(0)>>1) {
			limit = "at any depth"
		}
		return fmt.Sprintf("no path from %s to %s %s — calls, references and imports are walked forward; try the reverse, or graft callers for who depends on %s\n",
			result.from, result.to, limit, result.to)
	}
	label := func(id string) string {
		if node, ok := result.node(id); ok {
			return nodeLabel(node)
		}
		return id + " (outside the graph)"
	}
	var body strings.Builder
	fmt.Fprintf(&body, "%s → %s · %d steps\n  %s\n", result.from, result.to, len(result.steps), label(result.steps[0].From))
	for _, step := range result.steps {
		relation := string(step.Relation)
		if step.Dispatch {
			relation = "dispatches to"
		}
		if step.Confidence == "inferred" {
			relation += " (inferred)"
		}
		fmt.Fprintf(&body, "  → %s  %s\n", relation, label(step.To))
	}
	return body.String()
}

type pathStepOutput struct {
	symbolOutput
	Relation   graph.Relation   `json:"relation"`
	Confidence graph.Confidence `json:"confidence"`
	Dispatch   bool             `json:"dispatch,omitempty"`
}

func (result pathResult) output() any {
	steps := make([]pathStepOutput, 0, len(result.steps))
	for _, step := range result.steps {
		node, ok := result.node(step.To)
		if !ok {
			node = graph.NodeV1{ID: step.To}
		}
		steps = append(steps, pathStepOutput{symbolOutput: symbolJSON(node), Relation: step.Relation, Confidence: step.Confidence, Dispatch: step.Dispatch})
	}
	var start *symbolOutput
	if len(result.steps) > 0 {
		if node, ok := result.node(result.steps[0].From); ok {
			start = new(symbolJSON(node))
		}
	}
	return struct {
		From  string           `json:"from"`
		To    string           `json:"to"`
		Depth int              `json:"depth"`
		Start *symbolOutput    `json:"start"`
		Steps []pathStepOutput `json:"steps"`
	}{result.from, result.to, result.depth, start, steps}
}

// runDead lists functions and methods nothing calls, most certain first.
func runDead(opts callersOptions, stdout, stderr io.Writer) int {
	root, loaded, ok := loadAnalysisGraph(opts, stderr)
	if !ok {
		return 1
	}
	dead, err := graph.FindDeadCode(*loaded, root, opts.in)
	if err != nil {
		writeDiagnostic(stderr, "✗ %v\n", err)
		return 1
	}
	counts := map[graph.DeadConfidence]int{}
	var shown []graph.DeadSymbol
	for _, symbol := range dead {
		counts[symbol.Confidence]++
		if opts.all || symbol.Confidence != graph.DeadLow {
			shown = append(shown, symbol)
		}
	}
	if opts.jsonOutput {
		type candidate struct {
			symbolOutput
			Confidence graph.DeadConfidence `json:"confidence"`
			Reason     string               `json:"reason"`
		}
		candidates := make([]candidate, 0, len(shown))
		for _, symbol := range shown {
			candidates = append(candidates, candidate{symbolJSON(symbol.Node), symbol.Confidence, symbol.Reason})
		}
		return writeJSONResult(stdout, stderr, struct {
			High       int         `json:"high"`
			Medium     int         `json:"medium"`
			Low        int         `json:"low"`
			Candidates []candidate `json:"candidates"`
		}{counts[graph.DeadHigh], counts[graph.DeadMedium], counts[graph.DeadLow], candidates}, "dead code")
	}
	var body strings.Builder
	switch {
	case len(dead) == 0:
		body.WriteString("no dead code: every function and method has a caller, or is an entry point\n")
	case opts.all:
		fmt.Fprintf(&body, "dead code — %d high, %d medium, %d low confidence\n", counts[graph.DeadHigh], counts[graph.DeadMedium], counts[graph.DeadLow])
	default:
		fmt.Fprintf(&body, "dead code — %d high, %d medium confidence (%d low hidden; --all lists them)\n", counts[graph.DeadHigh], counts[graph.DeadMedium], counts[graph.DeadLow])
	}
	var group graph.DeadConfidence
	for _, symbol := range shown {
		if symbol.Confidence != group {
			group = symbol.Confidence
			heading := symbol.Reason
			if group == graph.DeadLow {
				heading = "no resolved caller, but a caller may exist"
			}
			fmt.Fprintf(&body, "\n%s · %s\n", group, heading)
		}
		fmt.Fprintf(&body, "  %s", nodeLabel(symbol.Node))
		if group == graph.DeadLow {
			fmt.Fprintf(&body, " — %s", symbol.Reason)
		}
		body.WriteByte('\n')
	}
	if _, err := io.WriteString(stdout, body.String()); err != nil {
		return 1
	}
	return 0
}

// runComplexity lists the most complex functions; with --threshold it lists
// those above it and fails when there are any, as a CI gate.
func runComplexity(opts callersOptions, stdout, stderr io.Writer) int {
	limit, err := positiveOption(opts.limit, "--limit", complexityTopDefault)
	threshold := 0
	if err == nil {
		threshold, err = positiveOption(opts.threshold, "--threshold", 0)
	}
	if err != nil {
		writeDiagnostic(stderr, "✗ %v\n", err)
		return 1
	}
	_, loaded, ok := loadAnalysisGraph(opts, stderr)
	if !ok {
		return 1
	}
	functions, err := graph.MostComplex(*loaded, opts.in)
	if err != nil {
		writeDiagnostic(stderr, "✗ %v\n", err)
		return 1
	}
	total := 0
	for _, function := range functions {
		total += *function.Complexity
	}
	listed := functions[:min(limit, len(functions))]
	if threshold > 0 {
		end := 0
		for end < len(functions) && *functions[end].Complexity > threshold {
			end++
		}
		listed = functions[:end]
	}
	status := 0
	if threshold > 0 && len(listed) > 0 {
		status = 1
	}
	if opts.jsonOutput {
		type entry struct {
			symbolOutput
			Complexity int `json:"complexity"`
		}
		entries := make([]entry, 0, len(listed))
		for _, function := range listed {
			entries = append(entries, entry{symbolJSON(function), *function.Complexity})
		}
		if code := writeJSONResult(stdout, stderr, struct {
			Functions int     `json:"functions"`
			Total     int     `json:"total"`
			Threshold int     `json:"threshold,omitempty"`
			Listed    []entry `json:"listed"`
		}{len(functions), total, threshold, entries}, "complexity"); code != 0 {
			return code
		}
		return status
	}
	var body strings.Builder
	switch {
	case len(functions) == 0:
		body.WriteString("no functions with a body in the graph\n")
	case threshold > 0 && len(listed) == 0:
		fmt.Fprintf(&body, "complexity — no function above %d (%d functions, highest %d)\n", threshold, len(functions), *functions[0].Complexity)
	case threshold > 0:
		fmt.Fprintf(&body, "complexity — %d of %d functions above %d\n", len(listed), len(functions), threshold)
	default:
		fmt.Fprintf(&body, "complexity — top %d of %d functions, average %.1f\n", len(listed), len(functions), float64(total)/float64(len(functions)))
	}
	for _, function := range listed {
		fmt.Fprintf(&body, "  %4d  %s\n", *function.Complexity, nodeLabel(function))
	}
	if _, err := io.WriteString(stdout, body.String()); err != nil {
		return 1
	}
	return status
}

// runHotspots ranks files by how often they change times how complex they
// are, from the repository's recent git history.
func runHotspots(opts callersOptions, stdout, stderr io.Writer) int {
	commits, err := positiveOption(opts.commits, "--commits", hotspotsCommits)
	limit := 0
	if err == nil {
		limit, err = positiveOption(opts.limit, "--limit", hotspotsTopDefault)
	}
	if err != nil {
		writeDiagnostic(stderr, "✗ %v\n", err)
		return 1
	}
	root, loaded, ok := loadAnalysisGraph(opts, stderr)
	if !ok {
		return 1
	}
	ctx, cancel := context.WithTimeout(context.Background(), hotspotsGitTimeout)
	defer cancel()
	// --relative names paths from root, as the graph does, when root is a
	// subdirectory of the checkout.
	log, err := gitx.Run(ctx, root, "log", "--format=", "--name-only", "--relative", "-n", strconv.Itoa(commits), "--", ".")
	if err != nil {
		writeDiagnostic(stderr, "✗ could not read git history in %s: %v\n", root, err)
		return 1
	}
	churn := make(map[string]int)
	for line := range strings.Lines(log) {
		if file := strings.TrimSpace(line); file != "" {
			churn[file]++
		}
	}
	spots, err := graph.Hotspots(*loaded, churn, opts.in)
	if err != nil {
		writeDiagnostic(stderr, "✗ %v\n", err)
		return 1
	}
	spots = spots[:min(limit, len(spots))]
	if opts.jsonOutput {
		type entry struct {
			Path       string       `json:"path"`
			Score      int          `json:"score"`
			Commits    int          `json:"commits"`
			Complexity int          `json:"complexity"`
			Top        symbolOutput `json:"top"`
		}
		entries := make([]entry, 0, len(spots))
		for _, spot := range spots {
			entries = append(entries, entry{spot.Path, spot.Score, spot.Commits, spot.Complexity, symbolJSON(spot.Top)})
		}
		return writeJSONResult(stdout, stderr, struct {
			Commits int     `json:"commits"`
			Files   []entry `json:"files"`
		}{commits, entries}, "hotspots")
	}
	var body strings.Builder
	if len(spots) == 0 {
		fmt.Fprintf(&body, "no hotspots: no file with functions changed in the last %d commits\n", commits)
	} else {
		fmt.Fprintf(&body, "hotspots — change frequency in the last %d commits × cyclomatic complexity\n", commits)
		body.WriteString("  score  commits  complexity  file — most complex\n")
	}
	for _, spot := range spots {
		fmt.Fprintf(&body, "  %5d  %7d  %10d  %s — %s %d\n", spot.Score, spot.Commits, spot.Complexity, spot.Path, spot.Top.Name, *spot.Top.Complexity)
	}
	if _, err := io.WriteString(stdout, body.String()); err != nil {
		return 1
	}
	return 0
}

// runCycles lists the dependency cycles between directories or files.
func runCycles(opts callersOptions, stdout, stderr io.Writer) int {
	level := opts.level
	switch level {
	case "":
		level = "dir"
	case "dir", "file":
	default:
		writeDiagnostic(stderr, "✗ --level must be \"dir\" or \"file\", got \"%s\"\n", level)
		return 1
	}
	_, loaded, ok := loadAnalysisGraph(opts, stderr)
	if !ok {
		return 1
	}
	cycles, err := graph.DependencyCycles(*loaded, level, opts.in)
	if err != nil {
		writeDiagnostic(stderr, "✗ %v\n", err)
		return 1
	}
	if opts.jsonOutput {
		type hop struct {
			From     string         `json:"from"`
			To       string         `json:"to"`
			Source   string         `json:"source"`
			Target   string         `json:"target"`
			Relation graph.Relation `json:"relation"`
		}
		type cycle struct {
			Members []string `json:"members"`
			Loop    []hop    `json:"loop"`
		}
		entries := make([]cycle, 0, len(cycles))
		for _, found := range cycles {
			loop := make([]hop, 0, len(found.Loop))
			for _, step := range found.Loop {
				loop = append(loop, hop{step.From, step.To, step.Evidence.Source, step.Evidence.Target, step.Evidence.Relation})
			}
			entries = append(entries, cycle{found.Members, loop})
		}
		return writeJSONResult(stdout, stderr, struct {
			Level  string  `json:"level"`
			Cycles []cycle `json:"cycles"`
		}{level, entries}, "cycles")
	}
	noun := map[string]string{"dir": "directories", "file": "files"}[level]
	var body strings.Builder
	if len(cycles) == 0 {
		fmt.Fprintf(&body, "no dependency cycles between %s\n", noun)
	} else {
		fmt.Fprintf(&body, "%d dependency cycles between %s\n", len(cycles), noun)
	}
	for _, found := range cycles {
		fmt.Fprintf(&body, "\n%s\n", strings.Join(found.Members, " ⇄ "))
		for _, step := range found.Loop {
			fmt.Fprintf(&body, "  %s → %s  (%s %s %s)\n", step.From, step.To, step.Evidence.Source, step.Evidence.Relation, step.Evidence.Target)
		}
	}
	if _, err := io.WriteString(stdout, body.String()); err != nil {
		return 1
	}
	return 0
}

// runRoutes lists the HTTP routes declared in the code and their handlers.
func runRoutes(opts callersOptions, stdout, stderr io.Writer) int {
	root, loaded, ok := loadAnalysisGraph(opts, stderr)
	if !ok {
		return 1
	}
	routes, err := graph.FindRoutes(*loaded, root, opts.in)
	if err != nil {
		writeDiagnostic(stderr, "✗ %v\n", err)
		return 1
	}
	if opts.jsonOutput {
		type entry struct {
			Method      string        `json:"method"`
			Path        string        `json:"path"`
			File        string        `json:"file"`
			Line        int           `json:"line"`
			Handler     *symbolOutput `json:"handler"`
			HandlerText string        `json:"handlerText,omitempty"`
		}
		entries := make([]entry, 0, len(routes))
		for _, route := range routes {
			found := entry{Method: route.Method, Path: route.Path, File: route.File, Line: route.Line, HandlerText: route.HandlerText}
			if route.Handler != nil {
				found.Handler = new(symbolJSON(*route.Handler))
			}
			entries = append(entries, found)
		}
		return writeJSONResult(stdout, stderr, struct {
			Routes []entry `json:"routes"`
		}{entries}, "routes")
	}
	var body strings.Builder
	if len(routes) == 0 {
		body.WriteString("no HTTP routes found (Go net/http, gin, echo, chi; Flask, FastAPI, Django; Express, NestJS; Spring)\n")
	} else {
		fmt.Fprintf(&body, "%d HTTP routes\n", len(routes))
	}
	methodWidth, pathWidth := 0, 0
	for _, route := range routes {
		methodWidth, pathWidth = max(methodWidth, len(route.Method)), max(pathWidth, len(route.Path))
	}
	for _, route := range routes {
		handler := route.HandlerText
		if route.Handler != nil {
			handler = nodeLabel(*route.Handler)
		}
		fmt.Fprintf(&body, "  %-*s  %-*s  %s:%d → %s\n", methodWidth, route.Method, pathWidth, route.Path, route.File, route.Line, handler)
	}
	if _, err := io.WriteString(stdout, body.String()); err != nil {
		return 1
	}
	return 0
}
