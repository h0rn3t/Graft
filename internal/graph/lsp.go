package graph

import (
	"context"
	"encoding/json/jsontext"
	"net/url"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/h0rn3t/Graft/internal/savings"
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

const (
	// lspTotalBudget bounds a whole enrichment run, server startup and
	// indexing included; what was collected by then is kept.
	lspTotalBudget = 10 * time.Minute
	// lspWorkers is how many source files are queried at once. JSON-RPC lets
	// requests overlap, and a few in flight hide the server's per-request
	// latency without flooding it.
	lspWorkers = 4
	// lspReadyProbes is how many definitions the readiness wait rotates over.
	lspReadyProbes = 3
)

// EnrichWithLSP adds compiler-resolved call edges when a supported language
// server is installed. Server startup, indexing, and request failures leave the
// existing graph usable and return the edges collected so far. The run stops
// after lspTotalBudget even when ctx allows longer.
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
	ctx, cancel := context.WithTimeout(ctx, lspTotalBudget)
	defer cancel()
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
	// Sources are grouped by file, in first-seen order, so each file is opened
	// once, queried, and closed again.
	var files []lspSourceFile
	fileIndex := make(map[string]int)
	for _, node := range graph.Nodes {
		if node.Kind != "function" && node.Kind != "method" {
			continue
		}
		if _, ok := serverLanguages[lspLanguage(node.Path)]; !ok {
			continue
		}
		index, ok := fileIndex[node.Path]
		if !ok {
			index = len(files)
			fileIndex[node.Path] = index
			files = append(files, lspSourceFile{abs: filepath.Join(root, filepath.FromSlash(node.Path))})
		}
		files[index].sources = append(files[index].sources, node)
	}

	probes := make([]lspProbe, 0, lspReadyProbes)
	for _, file := range files {
		lines := readLSPLines(file.abs)
		for _, source := range file.sources {
			if position, ok := lspNamePosition(lines, source); ok {
				probes = append(probes, lspProbe{abs: file.abs, position: position})
				break
			}
		}
		if len(probes) == lspReadyProbes {
			break
		}
	}
	if !client.waitUntilReady(ctx, probes) {
		return result
	}

	// Workers fill calls per file; the merge below walks them in source order,
	// so the edges added do not depend on which worker answered first.
	calls := make([][][]lspItem, len(files))
	next := make(chan int)
	var workers sync.WaitGroup
	for range min(lspWorkers, len(files)) {
		workers.Go(func() {
			for index := range next {
				calls[index] = client.queryFile(ctx, files[index])
			}
		})
	}
	for index := range files {
		if ctx.Err() != nil {
			break
		}
		next <- index
	}
	close(next)
	workers.Wait()

	existing := make(map[string]struct{}, len(graph.Edges))
	for _, edge := range graph.Edges {
		existing[edge.Source+"\x00"+string(edge.Relation)+"\x00"+edge.Target] = struct{}{}
	}
	for index, file := range files {
		for position, callees := range calls[index] {
			if callees == nil {
				continue
			}
			source := file.sources[position]
			result.Queried++
			for _, callee := range callees {
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
	}
	graph.Meta.EdgeCount = len(graph.Edges)
	return result
}

// lspSourceFile is one source file and the definitions queried in it.
type lspSourceFile struct {
	abs     string
	sources []NodeV1
}

// queryFile opens file, asks for each source's outgoing calls, and closes it
// again. The result holds one entry per source: nil when the server returned
// no call hierarchy item for it, else the callees, possibly none.
func (c *lspClient) queryFile(ctx context.Context, file lspSourceFile) [][]lspItem {
	calls := make([][]lspItem, len(file.sources))
	c.didOpen(ctx, file.abs)
	defer c.didClose(ctx, file.abs)
	lines := readLSPLines(file.abs)
	for index, source := range file.sources {
		if ctx.Err() != nil {
			break
		}
		position, ok := lspNamePosition(lines, source)
		if !ok {
			continue
		}
		items := c.prepareCallHierarchy(ctx, file.abs, position)
		if len(items) == 0 {
			continue
		}
		calls[index] = append(make([]lspItem, 0), c.outgoingCalls(ctx, items[0])...)
	}
	return calls
}

// readLSPLines splits a source into lines; an unreadable source has none and
// so provides no LSP position.
func readLSPLines(abs string) []string {
	text, readable, err := sourcefiles.Read(abs)
	if err != nil || !readable {
		return nil
	}
	return strings.Split(text, "\n")
}

// lspNamePosition is where node's name is declared, in LSP coordinates: the
// first whole-identifier occurrence of the name on the first three lines of
// its span, past a Go method's receiver. The character counts UTF-16 units.
func lspNamePosition(lines []string, node NodeV1) (lspPosition, bool) {
	start, _, ok := parseLSPSpan(node.Span)
	if !ok || node.Name == "" {
		return lspPosition{}, false
	}
	for line := start - 1; line < min(start+2, len(lines)); line++ {
		if column := identifierColumn(lines[line], node.Name); column >= 0 {
			return lspPosition{Line: line, Character: savings.Length(lines[line][:column])}, true
		}
	}
	return lspPosition{}, false
}

// identifierColumn is the byte offset of name in line as a whole identifier,
// not inside a longer one such as `Read` in `Reader`, or -1. A Go method's
// receiver list is skipped, since its type may share the method's name.
func identifierColumn(line, name string) int {
	from := 0
	if trimmed := strings.TrimLeft(line, " \t"); strings.HasPrefix(trimmed, "func (") {
		depth := 0
	receiver:
		for index := len(line) - len(trimmed) + len("func "); index < len(line); index++ {
			switch line[index] {
			case '(':
				depth++
			case ')':
				depth--
				if depth == 0 {
					from = index + 1
					break receiver
				}
			}
		}
	}
	for from <= len(line) {
		offset := strings.Index(line[from:], name)
		if offset < 0 {
			return -1
		}
		column := from + offset
		before, _ := utf8.DecodeLastRuneInString(line[:column])
		after, _ := utf8.DecodeRuneInString(line[column+len(name):])
		if !isIdentifierRune(before) && !isIdentifierRune(after) {
			return column
		}
		from = column + 1
	}
	return -1
}

func isIdentifierRune(r rune) bool {
	return r == '_' || r == '$' || unicode.IsLetter(r) || unicode.IsDigit(r)
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
