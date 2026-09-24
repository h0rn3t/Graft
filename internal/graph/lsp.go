package graph

import (
	"context"
	"encoding/json/jsontext"
	"net/url"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"unicode/utf16"

	"github.com/h0rn3t/Graft/internal/sourcefiles"
)

type lspServer struct {
	languages  []string
	command    string
	args       []string
	languageID string
}

var lspServers = []lspServer{
	{languages: []string{"rust"}, command: "rust-analyzer", languageID: "rust"},
	{languages: []string{"cpp", "c"}, command: "clangd", args: []string{"--background-index"}, languageID: "cpp"},
	{languages: []string{"go"}, command: "gopls", languageID: "go"},
	{languages: []string{"python"}, command: "pyright-langserver", args: []string{"--stdio"}, languageID: "python"},
	{languages: []string{"typescript", "javascript", "tsx"}, command: "typescript-language-server", args: []string{"--stdio"}, languageID: "typescript"},
}

type lspPosition struct {
	Line      int `json:"line"`
	Character int `json:"character"`
}

type lspRange struct {
	Start lspPosition `json:"start"`
	End   lspPosition `json:"end"`
}

type lspItem struct {
	Name           string         `json:"name"`
	Kind           int            `json:"kind"`
	Tags           []int          `json:"tags,omitzero"`
	Detail         string         `json:"detail,omitzero"`
	URI            string         `json:"uri"`
	Range          lspRange       `json:"range"`
	SelectionRange *lspRange      `json:"selectionRange,omitzero"`
	Data           jsontext.Value `json:"data,omitzero"`
}

type lspSpan struct {
	node       NodeV1
	start, end int
}

// LSPResult reports the best-effort compiler-grade enrichment outcome.
type LSPResult struct {
	Added   int    // Number of new compiler-resolved edges.
	Queried int    // Number of source definitions with call-hierarchy results.
	Server  string // Resolved executable path, or empty when no server applies.
}

// EnrichWithLSP adds compiler-resolved call edges when a supported language
// server is installed. Server startup, indexing, and request failures leave the
// existing graph usable and return the edges collected so far.
func EnrichWithLSP(ctx context.Context, graph *GraphV1, root string) LSPResult {
	result := LSPResult{}
	if graph == nil {
		return result
	}
	languages := make(map[string]struct{})
	for _, node := range graph.Nodes {
		if language := lspLanguage(node.Path); language != "" {
			languages[language] = struct{}{}
		}
	}
	server, ok := pickLSPServer(languages)
	if !ok {
		return result
	}
	result.Server = server.command

	realRoot, err := filepath.EvalSymlinks(root)
	if err == nil {
		root = realRoot
	}
	root, err = filepath.Abs(root)
	if err != nil {
		return result
	}
	client, err := startLSPClient(ctx, server, root)
	if err != nil {
		return result // Server startup failure leaves the AST graph intact.
	}
	defer client.close()
	if !client.initialize(ctx) {
		return result
	}

	spansByFile := make(map[string][]lspSpan)
	for _, node := range graph.Nodes {
		switch node.Kind {
		case "function", "method", "class", "struct", "interface", "type", "enum":
		default:
			continue
		}
		start, end, ok := parseLSPSpan(node.Span)
		if ok {
			spansByFile[node.Path] = append(spansByFile[node.Path], lspSpan{node: node, start: start, end: end})
		}
	}
	nodeAt := func(rel string, line int) *NodeV1 {
		var best *lspSpan
		for index := range spansByFile[rel] {
			candidate := &spansByFile[rel][index]
			if candidate.start > line || line > candidate.end {
				continue
			}
			if best == nil || candidate.end-candidate.start < best.end-best.start {
				best = candidate
			}
		}
		if best == nil {
			return nil
		}
		node := best.node
		return &node
	}

	serverLanguages := make(map[string]struct{}, len(server.languages))
	for _, language := range server.languages {
		serverLanguages[language] = struct{}{}
	}
	sources := make([]NodeV1, 0)
	for _, node := range graph.Nodes {
		if node.Kind != "function" && node.Kind != "method" {
			continue
		}
		if _, ok := serverLanguages[lspLanguage(node.Path)]; ok {
			sources = append(sources, node)
		}
	}

	fileLines := make(map[string][]string)
	linesOf := func(rel string) []string {
		if lines, ok := fileLines[rel]; ok {
			return lines
		}
		text, readable, err := sourcefiles.Read(filepath.Join(root, filepath.FromSlash(rel)))
		if err != nil || !readable {
			fileLines[rel] = nil // Unreadable sources cannot provide an LSP position.
			return nil
		}
		lines := strings.Split(text, "\n")
		fileLines[rel] = lines
		return lines
	}
	namePosition := func(node NodeV1) (lspPosition, bool) {
		start, _, ok := parseLSPSpan(node.Span)
		if !ok {
			return lspPosition{}, false
		}
		lines := linesOf(node.Path)
		for line := start - 1; line < min(start+2, len(lines)); line++ {
			if column := strings.Index(lines[line], node.Name); column >= 0 {
				character := len(utf16.Encode([]rune(lines[line][:column])))
				return lspPosition{Line: line, Character: character}, true
			}
		}
		return lspPosition{}, false
	}

	for _, source := range sources {
		if position, ok := namePosition(source); ok {
			if !client.waitUntilReady(ctx, filepath.Join(root, filepath.FromSlash(source.Path)), position) {
				return result
			}
			break
		}
	}

	existing := make(map[string]struct{}, len(graph.Edges))
	for _, edge := range graph.Edges {
		existing[edge.Source+"\x00"+string(edge.Relation)+"\x00"+edge.Target] = struct{}{}
	}
	for _, source := range sources {
		abs := filepath.Join(root, filepath.FromSlash(source.Path))
		client.didOpen(abs)
		position, ok := namePosition(source)
		if !ok {
			continue
		}
		items := client.prepareCallHierarchy(ctx, abs, position)
		if len(items) == 0 {
			continue
		}
		result.Queried++
		for _, callee := range client.outgoingCalls(ctx, items[0]) {
			calleePath, ok := lspFilePath(callee.URI)
			if !ok {
				continue
			}
			rel, err := filepath.Rel(root, calleePath)
			if err != nil || !filepath.IsLocal(rel) {
				continue
			}
			rel = filepath.ToSlash(rel)
			line := callee.Range.Start.Line
			if callee.SelectionRange != nil {
				line = callee.SelectionRange.Start.Line
			}
			target := nodeAt(rel, line+1)
			if target == nil || target.ID == source.ID {
				continue
			}
			key := source.ID + "\x00calls\x00" + target.ID
			if _, ok := existing[key]; ok {
				continue
			}
			existing[key] = struct{}{}
			graph.Edges = append(graph.Edges, EdgeV1{
				Source: source.ID, Target: target.ID, Relation: "calls", Confidence: "lsp_resolved",
			})
			result.Added++
		}
	}
	graph.Meta.EdgeCount = len(graph.Edges)
	return result
}

func pickLSPServer(languages map[string]struct{}) (lspServer, bool) {
	for _, server := range lspServers {
		matches := false
		for _, language := range server.languages {
			if _, ok := languages[language]; ok {
				matches = true
				break
			}
		}
		if !matches {
			continue
		}
		found, err := exec.LookPath(server.command)
		if err != nil {
			continue // Missing language servers are a supported no-op.
		}
		command, err := filepath.Abs(found)
		if err == nil {
			server.command = command
			return server, true
		}
	}
	return lspServer{}, false
}

func lspLanguage(file string) string {
	if language, ok := genericLanguageOf(file); ok {
		return language
	}
	_, language, _ := languageOf(file)
	return language
}

func parseLSPSpan(span string) (int, int, bool) {
	startText, endText, ok := strings.Cut(span, "-L")
	if !ok || !strings.HasPrefix(startText, "L") {
		return 0, 0, false
	}
	start, startErr := strconv.Atoi(strings.TrimPrefix(startText, "L"))
	end, endErr := strconv.Atoi(endText)
	return start, end, startErr == nil && endErr == nil && start > 0 && end >= start
}

func lspFileURI(abs string) string {
	path := filepath.ToSlash(abs)
	if filepath.Separator == '\\' && !strings.HasPrefix(path, "/") {
		path = "/" + path
	}
	return (&url.URL{Scheme: "file", Path: path}).String()
}

func lspFilePath(raw string) (string, bool) {
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Scheme != "file" || parsed.Host != "" || parsed.RawQuery != "" || parsed.Fragment != "" {
		return "", false
	}
	path := parsed.Path
	if filepath.Separator == '\\' && len(path) >= 3 && path[0] == '/' && path[2] == ':' {
		path = path[1:]
	}
	path = filepath.Clean(filepath.FromSlash(path))
	if !filepath.IsAbs(path) {
		return "", false
	}
	return path, true
}
