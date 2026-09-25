package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"

	"github.com/h0rn3t/Graft/internal/graph"
	"github.com/h0rn3t/Graft/internal/savings"
	"github.com/h0rn3t/Graft/internal/sourcefiles"
)

type readResult struct {
	ID         string     `json:"id"`
	Name       string     `json:"name"`
	Kind       graph.Kind `json:"kind"`
	Pointer    string     `json:"pointer"`
	SourceHash string     `json:"sourceHash"`
	Code       string     `json:"code"`
	// Note explains how an inexact selector was resolved.
	Note string `json:"note,omitempty"`
	// Callees of a single read have empty Code when only their pointer fit.
	Callees []readResult `json:"callees,omitempty"`
}

func runRead(opts callersOptions, stdout, stderr io.Writer) int {
	budget, err := validateAskOptions(opts)
	if err != nil {
		writeDiagnostic(stderr, "%v\n", err)
		return 1
	}
	if opts.symbols == nil && strings.TrimSpace(opts.query) == "" {
		writeDiagnostic(stderr, "read requires a symbol\n")
		return 1
	}
	if opts.symbols != nil {
		if err := validateReadSymbols(opts.symbols); err != nil {
			writeDiagnostic(stderr, "%v\n", err)
			return 1
		}
	}
	var diagnostics bytes.Buffer
	if opts.queryNote != "" {
		diagnostics.WriteString(opts.queryNote + "\n")
	}
	root, contextDir, err := resolvePaths(opts, queryPathRules, &diagnostics)
	if err != nil {
		writeDiagnostic(stderr, "%v\n", err)
		return 1
	}
	noteQueryRoot(opts)
	refreshBeforeQuery(root, contextDir, opts, &diagnostics)
	workspace := graph.WorkspaceGraphs{}
	if _, ok := graph.ReadWorkspaceChildren(contextDir); ok {
		workspace = graph.LoadWorkspaceGraphs(root, contextDir)
	} else {
		wiring, err := opts.queryCache.loadGraph(contextDir)
		if err != nil {
			writeDiagnostic(stderr, "cannot read graph; run graft build: %v\n", err)
			return 1
		}
		workspace.Loaded = []graph.WorkspaceChild{{Graph: *wiring}}
	}
	if len(workspace.Missing) > 0 {
		diagnostics.WriteString("Some workspace graphs are unavailable; run graft build for complete coverage.\n")
	}
	sources := make(map[string]string)
	if opts.symbols != nil {
		return writeReadBatch(root, workspace, opts, budget, sources, diagnostics.String(), stdout, stderr)
	}
	match, note, err := resolveReadSelector(workspace, opts.query, budget, opts.mcp)
	if err != nil {
		writeDiagnostic(stderr, "%v\n", err)
		return 1
	}
	result, err := readMatchSource(root, workspace, match, note, sources)
	if err != nil {
		writeDiagnostic(stderr, "%v\n", err)
		return 1
	}
	text := renderReadResult(result, opts.jsonOutput)
	required := savings.Tokens(savings.Length(text + diagnostics.String()))
	if required > budget {
		flag := "--budget"
		if opts.mcp {
			flag = "budget"
		}
		writeDiagnostic(stderr, "complete definition needs %d estimated tokens; increase %s (maximum 64000), or read the source range directly\n", required, flag)
		return 1
	}
	addReadCallees(root, workspace, match, &result, budget, diagnostics.String(), opts.jsonOutput, sources)
	if _, err := io.WriteString(stderr, diagnostics.String()); err != nil {
		return 1
	}
	if _, err := io.WriteString(stdout, renderReadResult(result, opts.jsonOutput)); err != nil {
		return 1
	}
	return 0
}

type readMatch struct {
	node  graph.NodeV1
	child int
	id    string
}

func matchReadSelector(workspace graph.WorkspaceGraphs, selector string) []readMatch {
	var matches []readMatch
	for i, child := range workspace.Loaded {
		prefix := ""
		if child.Name != "" {
			prefix = child.Name + "/"
		}
		for _, node := range child.Graph.Nodes {
			if node.Kind == "file" || node.Span == "" {
				continue
			}
			_, qualified, _ := strings.Cut(node.ID, "#")
			if selector == prefix+node.ID || selector == node.Name || selector == qualified ||
				selector == prefix+node.Path+"::"+node.Name || selector == prefix+node.Path+"::"+qualified {
				matches = append(matches, readMatch{node, i, prefix + node.ID})
			}
		}
	}
	return matches
}

// resolveReadSelector finds the one definition a selector names. Each failure
// here costs the agent another round, so two recoveries are tried before
// giving up: a path::name whose path holds no such name is retried by name,
// and a name shared by one production definition and only copies (testdata,
// fixtures, tests) reads the production one. The note says which applied.
func resolveReadSelector(workspace graph.WorkspaceGraphs, selector string, budget int, mcp bool) (readMatch, string, error) {
	matches := matchReadSelector(workspace, selector)
	var notes []string
	if path, name, qualified := strings.CutLast(selector, "::"); len(matches) == 0 && qualified && name != "" {
		matches = matchReadSelector(workspace, name)
		if len(matches) > 0 {
			notes = append(notes, fmt.Sprintf("%s is not in %s; resolved by name", name, path))
		}
	}
	slices.SortFunc(matches, func(a, b readMatch) int { return strings.Compare(a.id, b.id) })
	if len(matches) > 1 {
		production := slices.DeleteFunc(slices.Clone(matches), func(m readMatch) bool { return graph.IsCopyPath(m.node.Path) })
		if len(production) == 1 {
			var copies []string
			for _, m := range matches {
				if m.id != production[0].id {
					copies = append(copies, m.id)
				}
			}
			if len(copies) > 3 {
				copies = append(copies[:3], fmt.Sprintf("%d more", len(copies)-3))
			}
			notes = append(notes, "also matches copies: "+strings.Join(copies, ", "))
			matches = production
		}
	}
	if len(matches) == 1 {
		return matches[0], strings.Join(notes, "; "), nil
	}
	message := "no exact symbol; use graft grep or graft skeleton to find its name"
	if mcp {
		message = "no exact symbol; use graft_find_all or graft_file_api to find its name"
	}
	if len(matches) > 1 {
		message = fmt.Sprintf("ambiguous symbol (%d matches); select an exact node ID:", len(matches))
		shown := 0
		for _, match := range matches {
			line := "\n  " + match.id
			if shown == 8 || savings.Tokens(savings.Length(message+line)) > budget-32 {
				break
			}
			message += line
			shown++
		}
		if shown < len(matches) {
			message += "\nMore matches omitted; qualify with path::name."
		}
	}
	return readMatch{}, "", errors.New(message)
}

func readExactSymbol(root string, workspace graph.WorkspaceGraphs, selector string, budget int, mcp bool, sources map[string]string) (readResult, error) {
	match, note, err := resolveReadSelector(workspace, selector, budget, mcp)
	if err != nil {
		return readResult{}, err
	}
	return readMatchSource(root, workspace, match, note, sources)
}

func readMatchSource(root string, workspace graph.WorkspaceGraphs, match readMatch, note string, sources map[string]string) (readResult, error) {
	child := workspace.Loaded[match.child]
	code, err := readSymbolSource(root, filepath.Join(child.Name, match.node.Path), child.Graph, match.node, sources)
	if err != nil {
		return readResult{}, err
	}
	pointer := match.node.Path + ":" + match.node.Span
	if child.Name != "" {
		pointer = child.Name + "/" + pointer
	}
	return readResult{ID: match.id, Name: match.node.Name, Kind: match.node.Kind,
		Pointer: pointer, SourceHash: sourcefiles.Hash(code), Code: code, Note: note}, nil
}

// readCalleeLimit bounds how many direct callees a single read reports.
const readCalleeLimit = 8

// addReadCallees appends the definition's direct callees that are production
// definitions in its directory, in edge order: a lone definition usually
// needs its helpers next, and each follow-up read costs a model round. A
// callee is inlined when it fits the remaining budget and otherwise listed as
// a pointer; one whose source fails the freshness check is a pointer too.
func addReadCallees(root string, workspace graph.WorkspaceGraphs, match readMatch, result *readResult, budget int, diagnostics string, asJSON bool, sources map[string]string) {
	child := workspace.Loaded[match.child]
	prefix := ""
	if child.Name != "" {
		prefix = child.Name + "/"
	}
	nodes := make(map[string]graph.NodeV1, len(child.Graph.Nodes))
	for _, node := range child.Graph.Nodes {
		nodes[node.ID] = node
	}
	from, to, _ := spanLines(match.node.Span)
	dir := path.Dir(match.node.Path)
	seen := map[string]bool{match.node.ID: true}
	fits := func() bool {
		return savings.Tokens(savings.Length(diagnostics+renderReadResult(*result, asJSON))) <= budget
	}
	for _, edge := range child.Graph.Edges {
		if len(result.Callees) == readCalleeLimit {
			return
		}
		if edge.Source != match.node.ID || edge.Relation != "calls" || seen[edge.Target] {
			continue
		}
		seen[edge.Target] = true
		callee, ok := nodes[edge.Target]
		if !ok || callee.Kind == "file" || callee.Span == "" || path.Dir(callee.Path) != dir || graph.IsCopyPath(callee.Path) {
			continue
		}
		if start, end, ok := spanLines(callee.Span); ok && callee.Path == match.node.Path && start >= from && end <= to {
			continue
		}
		pointerOnly := readResult{ID: prefix + callee.ID, Name: callee.Name, Kind: callee.Kind, Pointer: prefix + callee.Path + ":" + callee.Span}
		if code, err := readSymbolSource(root, filepath.Join(child.Name, callee.Path), child.Graph, callee, sources); err == nil {
			inlined := pointerOnly
			inlined.Code, inlined.SourceHash = code, sourcefiles.Hash(code)
			result.Callees = append(result.Callees, inlined)
			if fits() {
				continue
			}
			result.Callees = result.Callees[:len(result.Callees)-1]
		}
		result.Callees = append(result.Callees, pointerOnly)
		if !fits() {
			result.Callees = result.Callees[:len(result.Callees)-1]
			return
		}
	}
}

func renderReadResult(result readResult, asJSON bool) string {
	if asJSON {
		data, _ := json.Marshal(result) // readResult contains only JSON-safe values.
		return string(data) + "\n"
	}
	var out strings.Builder
	fmt.Fprintf(&out, "%s · %s · %s\nsourceHash: %s\n", result.Name, result.Kind, result.Pointer, result.SourceHash)
	if result.Note != "" {
		fmt.Fprintf(&out, "note: %s\n", result.Note)
	}
	fmt.Fprintf(&out, "\n```\n%s\n```\n", result.Code)
	if len(result.Callees) > 0 {
		out.WriteString("\nDirect callees in this directory:\n")
	}
	for _, callee := range result.Callees {
		if callee.Code == "" {
			fmt.Fprintf(&out, "%s · %s · %s — not inlined; read it by name\n", callee.Name, callee.Kind, callee.Pointer)
			continue
		}
		out.WriteString(renderReadResult(callee, false))
	}
	return out.String()
}

func readSymbolSource(root, sourcePath string, wiring graph.GraphV1, node graph.NodeV1, sources map[string]string) (string, error) {
	text, cached := sources[sourcePath]
	if !cached {
		rootFS, err := os.OpenRoot(root)
		if err != nil {
			return "", fmt.Errorf("open source root: %w", err)
		}
		defer func() { _ = rootFS.Close() }() // Read-only handles have no buffered state.
		file, err := rootFS.Open(filepath.FromSlash(sourcePath))
		if err != nil {
			return "", fmt.Errorf("read source %q: %w", node.Path, err)
		}
		defer func() { _ = file.Close() }()
		const maxSourceBytes = 1_000_000
		data, err := io.ReadAll(io.LimitReader(file, maxSourceBytes+1))
		if err != nil {
			return "", fmt.Errorf("read source %q: %w", node.Path, err)
		}
		if len(data) > maxSourceBytes {
			return "", fmt.Errorf("source exceeds %d bytes", maxSourceBytes)
		}
		var ok bool
		text, ok = sourcefiles.Decode(data)
		if !ok {
			return "", fmt.Errorf("unsupported source encoding")
		}
		sources[sourcePath] = text
	}
	fileHash := ""
	for _, candidate := range wiring.Nodes {
		if candidate.Kind == "file" && candidate.Path == node.Path {
			fileHash = candidate.BodyHash
			break
		}
	}
	if fileHash == "" || fileHash != sourcefiles.Hash(text) {
		return "", fmt.Errorf("source changed or its indexed hash is unavailable; run graft build and retry")
	}
	from, to, ok := spanLines(node.Span)
	lines := strings.Split(text, "\n")
	if !ok || from < 1 || to < from || to > len(lines) {
		return "", fmt.Errorf("invalid indexed span; run graft build and retry")
	}
	return strings.Join(lines[from-1:to], "\n"), nil
}
