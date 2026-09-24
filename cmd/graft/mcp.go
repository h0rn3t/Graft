package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/h0rn3t/Graft/internal/graph"
	"github.com/h0rn3t/Graft/internal/hosts"
	"github.com/h0rn3t/Graft/internal/jsonjs"
	"github.com/h0rn3t/Graft/internal/savings"
	"github.com/h0rn3t/Graft/internal/upkeep"
	"github.com/modelcontextprotocol/go-sdk/jsonrpc"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type mcpToolDefinition struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	InputSchema map[string]any `json:"inputSchema"`
}

var mcpAliases = map[string]string{
	"graft_ask":      "graft_find_code",
	"graft_grep":     "graft_find_all",
	"graft_callers":  "graft_trace_calls",
	"graft_skeleton": "graft_file_api",
	"graft_map":      "graft_repo_map",
	"graft_check":    "graft_check_freshness",
}

var mcpTools = []mcpToolDefinition{
	{
		Name:        "graft_find_code",
		Description: "Query the repo context graph in plain words. Returns ranked nodes with exact file:line spans and the relevant source inlined — usually the full answer, no file reads needed.",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"query": map[string]any{"type": "string", "description": "what you want to understand, in plain words"},
				"limit": map[string]any{"type": "number", "description": "max results (default 5)"},
				"full":  map[string]any{"type": "boolean", "description": "inline whole definition spans instead of the default ≤8-line crux excerpts"},
				"in":    map[string]any{"type": "string", "description": "narrow to nodes under this path prefix, filtered before scoring (segment-aware, like scopeOf)"},
			},
			"required": []string{"query"},
		},
	},
	{
		Name:        "graft_file_api",
		Description: "Signatures-only view of one file — every definition's signature + line span, ~10× cheaper than reading the file ($0, no LLM).",
		InputSchema: map[string]any{
			"type":       "object",
			"properties": map[string]any{"file": map[string]any{"type": "string", "description": "repo-relative path (or unique basename) of the file"}},
			"required":   []string{"file"},
		},
	},
	{
		Name:        "graft_check_freshness",
		Description: "Report whether the committed graph is in sync with the code (drift check).",
		InputSchema: map[string]any{"type": "object", "properties": map[string]any{}},
	},
	{
		Name:        "graft_trace_calls",
		Description: "Structural edges for a symbol, over call/reference/import/implements/extends ($0, no LLM). Defaults to direct callers (who depends on it). Set direction:\"out\" for callees (what it calls); set depth>1 (or depth:\"all\" for the full closure) to walk transitively for the full blast radius — every source that breaks if it changes. Run before a multi-file refactor to find ALL affected files.",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"symbol":    map[string]any{"type": "string", "description": "bare name, qualified (Class.method), or package-qualified (pkg.Fn); a file path also works"},
				"direction": map[string]any{"type": "string", "enum": []string{"in", "out"}, "description": "\"in\" (default) = callers/dependents; \"out\" = callees/dependencies"},
				"depth":     map[string]any{"description": "transitive walk depth for blast radius (default 1 = direct edges only); pass \"all\" for the full connected closure — every source that would be affected"},
				"in":        map[string]any{"type": "string", "description": "narrow matches to nodes at or under this repo-relative path prefix, e.g. server/src"},
			},
			"required": []string{"symbol"},
		},
	},
	{
		Name:        "graft_find_all",
		Description: "Regex search over the graph's indexed files, hits grouped by innermost enclosing symbol and ranked by incoming-edge count (coupling) — which hit matters, not just where it is.",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"pattern":     map[string]any{"type": "string", "description": "regex pattern (or literal string with fixed: true)"},
				"in":          map[string]any{"type": "string", "description": "narrow to files at or under this repo-relative path prefix, e.g. server/src"},
				"ignore_case": map[string]any{"type": "boolean", "description": "case-insensitive match"},
				"fixed":       map[string]any{"type": "boolean", "description": "treat pattern as a literal string, not a regex"},
			},
			"required": []string{"pattern"},
		},
	},
	{
		Name:        "graft_repo_map",
		Description: "Token-budgeted repo orientation — directory clusters, per-directory hubs, and global hotspots computed purely from the wiring graph ($0, no LLM). Use this to get oriented in an unfamiliar repo before diving into files.",
		InputSchema: map[string]any{
			"type":       "object",
			"properties": map[string]any{"max_dirs": map[string]any{"type": "number", "description": "max directory entries shown, rest counted into dropped (default 16)"}},
		},
	},
}

const mcpInstructionsText = `This repo is indexed by graft: a prebuilt graph of every symbol, its file:line
span, and who calls what. Prefer these tools over grep/read — one call usually
replaces several file reads.

**If these tools are deferred (names shown, schemas withheld), load them all in ONE lookup:** ToolSearch "select:mcp__graft__graft_find_code,mcp__graft__graft_find_all,mcp__graft__graft_trace_calls,mcp__graft__graft_file_api,mcp__graft__graft_repo_map" — one round trip for the whole session. Never load them one at a time.

- graft_find_code — "how does X work" / "where is Y": ranked hits, code inlined.
- graft_find_all — when you need EVERY occurrence; find_code is top-N and misses some.
- graft_trace_calls — who calls it, what it calls, blast radius before a rename.
- graft_file_api — a file's whole API in ~200 tokens.
- graft_repo_map — orientation in an unfamiliar repo.

Results already reflect uncommitted edits — the graph refreshes before each query.`

// runMCP serves the retrieval tools over newline-delimited JSON-RPC 2.0 until
// stdin ends or ctx is done.
func runMCP(ctx context.Context, opts callersOptions, stdin io.Reader, stdout, stderr io.Writer) int {
	root, contextDir, err := resolvePaths(opts, queryPathRules, stderr)
	if err != nil {
		writeDiagnostic(stderr, "✗ %v\n", err)
		return 1
	}

	version := currentVersion()
	lines := mcpUpkeepLines(ctx, root, contextDir, version)
	for _, line := range lines {
		writeDiagnostic(stderr, "%s\n", line)
	}
	server := newMCPServerWith(opts, root, contextDir, version, mcpInstructionsFrom(lines))
	err = server.Run(ctx, &mcpTransport{reader: stdin, writer: stdout})
	if err != nil && !errors.Is(err, io.EOF) && ctx.Err() == nil {
		writeDiagnostic(stderr, "✗ %v\n", err)
		return 1
	}
	return 0
}

func newMCPServer(ctx context.Context, opts callersOptions, root, contextDir string) *mcp.Server {
	version := currentVersion()
	return newMCPServerWith(opts, root, contextDir, version, mcpStartupInstructions(ctx, root, contextDir, version))
}

func newMCPServerWith(opts callersOptions, root, contextDir, version, instructions string) *mcp.Server {
	server := mcp.NewServer(
		&mcp.Implementation{Name: "graft", Version: version},
		&mcp.ServerOptions{
			Instructions: instructions,
			Capabilities: &mcp.ServerCapabilities{Tools: &mcp.ToolCapabilities{}},
		},
	)
	for _, definition := range mcpTools {
		server.AddTool(
			&mcp.Tool{
				Name:        definition.Name,
				Description: definition.Description,
				InputSchema: definition.InputSchema,
			},
			func(ctx context.Context, request *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
				args := mcpToolArguments(request.Params.Arguments)
				result := mcpCall(ctx, root, contextDir, opts.contextDir, request.Params.Name, args)
				return mcpSDKResult(result), nil
			},
		)
	}
	server.AddReceivingMiddleware(func(next mcp.MethodHandler) mcp.MethodHandler {
		return func(ctx context.Context, method string, request mcp.Request) (mcp.Result, error) {
			switch method {
			case "initialize":
				result, err := next(ctx, method, request)
				if err != nil {
					return result, err
				}
				if initialized, ok := result.(*mcp.InitializeResult); ok {
					protocol := "2024-11-05"
					if params, ok := request.GetParams().(*mcp.InitializeParams); ok && params.ProtocolVersion != "" {
						protocol = params.ProtocolVersion
					}
					initialized.ProtocolVersion = protocol
					initialized.Capabilities = &mcp.ServerCapabilities{Tools: &mcp.ToolCapabilities{}}
					initialized.Instructions = instructions
					initialized.ServerInfo = &mcp.Implementation{Name: "graft", Version: version}
				}
				return result, nil
			case "tools/list":
				if opts.contextDir == "" && !mcpGraphAvailable(contextDir) {
					return &mcp.ListToolsResult{Tools: []*mcp.Tool{}}, nil
				}
				tools := make([]*mcp.Tool, 0, len(mcpTools))
				for _, definition := range mcpTools {
					tools = append(tools, &mcp.Tool{
						Name:        definition.Name,
						Description: definition.Description,
						InputSchema: definition.InputSchema,
					})
				}
				return &mcp.ListToolsResult{Tools: tools}, nil
			case "tools/call":
				call, ok := request.(*mcp.CallToolRequest)
				if !ok {
					return next(ctx, method, request)
				}
				args := mcpToolArguments(call.Params.Arguments)
				result := mcpCall(ctx, root, contextDir, opts.contextDir, call.Params.Name, args)
				return mcpSDKResult(result), nil
			default:
				return next(ctx, method, request)
			}
		}
	})
	return server
}

func mcpStartupInstructions(ctx context.Context, root, contextDir, current string) string {
	return mcpInstructionsFrom(mcpUpkeepLines(ctx, root, contextDir, current))
}

func mcpInstructionsFrom(lines []string) string {
	if len(lines) == 0 {
		return mcpInstructionsText
	}
	return strings.Join(lines, "\n") + "\n\n" + mcpInstructionsText
}

// mcpUpkeepLines runs the boot-time upkeep and returns the lines worth showing.
// The stamp lives under GRAFT_DIR or <root>/graft, as the TypeScript cacheDir
// resolves it, whatever --dir the server was given.
func mcpUpkeepLines(ctx context.Context, root, _, current string) []string {
	now := time.Now()
	home := homeDir()
	env := hosts.Env{Home: home, BakedDir: packageRoot(), Launch: hosts.ServerEntry()}
	lines := make([]string, 0, 2)
	if note := upkeep.ReconcileWiring(root, "", current, now,
		func(repo string) ([]string, error) { return upkeep.WiredHostIDs(repo), nil },
		func(repo string, ids []string, options upkeep.WiringOptions) error {
			return upkeep.RewriteWiring(ctx, repo, ids, options, env)
		},
	); note != "" {
		lines = append(lines, note)
	}
	return lines
}

func mcpToolArguments(raw json.RawMessage) map[string]any {
	args := make(map[string]any)
	if len(bytes.TrimSpace(raw)) == 0 {
		return args
	}
	if err := json.Unmarshal(raw, &args); err != nil || args == nil {
		return make(map[string]any)
	}
	return args
}

func mcpSDKResult(result mcpResult) *mcp.CallToolResult {
	return &mcp.CallToolResult{
		Content: []mcp.Content{&mcp.TextContent{Text: result.text}},
		IsError: result.isError,
	}
}

func normalizeMCPResponse(data []byte) []byte {
	original := data
	var response map[string]json.RawMessage
	if err := json.Unmarshal(data, &response); err != nil {
		return data
	}
	resultData, ok := response["result"]
	if !ok {
		return data
	}
	var result map[string]json.RawMessage
	if err := json.Unmarshal(resultData, &result); err != nil {
		return data
	}
	if _, ok := result["content"]; ok {
		if _, ok := result["isError"]; !ok {
			result["isError"] = json.RawMessage("false")
		}
	}
	delete(result, "ttlMs")
	delete(result, "cacheScope")
	resultData, err := jsonjs.Marshal(result, "")
	if err != nil {
		return data
	}
	response["result"] = resultData
	data, err = jsonjs.Marshal(response, "")
	if err != nil {
		return original
	}
	return data
}

// mcpMaxLine caps one NDJSON message. No client request comes near it, so a
// longer line is answered with a parse error and skipped instead of ending
// the session.
const mcpMaxLine = 64 << 20

type mcpTransport struct {
	reader io.Reader
	writer io.Writer
	// maxLine overrides mcpMaxLine when positive.
	maxLine int
}

// Connect opens the NDJSON connection used by the MCP server.
func (t *mcpTransport) Connect(context.Context) (mcp.Connection, error) {
	maxLine := t.maxLine
	if maxLine <= 0 {
		maxLine = mcpMaxLine
	}
	return &mcpConnection{lines: bufio.NewReader(t.reader), maxLine: maxLine, reader: t.reader, writer: t.writer}, nil
}

type mcpConnection struct {
	lines        *bufio.Reader
	maxLine      int
	reader       io.Reader
	writer       io.Writer
	mu           sync.Mutex
	outstanding  sync.WaitGroup
	pending      jsonrpc.Message
	bootstrapped bool
}

// SessionID is empty because this transport does not support resumable sessions.
func (c *mcpConnection) SessionID() string { return "" }

// Read decodes the next JSON-RPC message from the NDJSON stream.
//
// Malformed lines and unsupported requests are answered through the writer and skipped.
func (c *mcpConnection) Read(ctx context.Context) (jsonrpc.Message, error) {
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	default:
	}
	if c.pending != nil {
		message := c.pending
		c.pending = nil
		c.trackRequest(message)
		return message, nil
	}
	for {
		raw, oversize, err := c.readLine()
		if errors.Is(err, io.EOF) {
			c.outstanding.Wait()
			return nil, io.EOF
		}
		if err != nil {
			return nil, err
		}
		line := bytes.TrimSpace(raw)
		if len(line) == 0 && !oversize {
			continue
		}
		message, err := jsonrpc.DecodeMessage(line)
		if oversize || err != nil {
			_ = c.writeValue(map[string]any{
				"jsonrpc": "2.0",
				"id":      nil,
				"error":   map[string]any{"code": -32700, "message": "parse error"},
			})
			continue
		}
		if request, ok := message.(*jsonrpc.Request); ok {
			switch request.Method {
			case "initialize", "notifications/initialized", "notifications/cancelled", "ping", "tools/list", "tools/call":
			default:
				if request.ID.IsValid() {
					_ = c.writeValue(map[string]any{
						"jsonrpc": "2.0",
						"id":      request.ID.Raw(),
						"error":   map[string]any{"code": -32601, "message": "method not found: " + request.Method},
					})
				}
				continue
			}
			if !c.bootstrapped && (request.Method == "notifications/initialized" || request.Method == "notifications/cancelled") {
				continue
			}
			if !c.bootstrapped && request.Method != "initialize" {
				c.bootstrapped = true
				c.pending = message
				bootstrapID, err := jsonrpc.MakeID("graft-bootstrap")
				if err != nil {
					return nil, err
				}
				message = &jsonrpc.Request{
					ID:     bootstrapID,
					Method: "initialize",
					Params: json.RawMessage(`{"protocolVersion":"2024-11-05","capabilities":{},"clientInfo":{"name":"graft","version":"0"}}`),
				}
				c.trackRequest(message)
				return message, nil
			}
		}
		c.trackRequest(message)
		return message, nil
	}
}

// readLine returns the next line, newline included, reporting oversize for a
// line longer than maxLine, whose bytes are skipped rather than buffered. A
// last line without a newline is returned before io.EOF.
func (c *mcpConnection) readLine() (line []byte, oversize bool, err error) {
	for {
		chunk, err := c.lines.ReadSlice('\n')
		if !oversize && len(line)+len(chunk) > c.maxLine+1 {
			oversize, line = true, nil
		}
		if !oversize {
			line = append(line, chunk...)
		}
		switch {
		case errors.Is(err, bufio.ErrBufferFull):
			continue
		case errors.Is(err, io.EOF) && (len(line) > 0 || oversize):
			return line, oversize, nil
		default:
			return line, oversize, err
		}
	}
}

// Write encodes a normalized JSON-RPC message as one NDJSON line.
func (c *mcpConnection) Write(ctx context.Context, message jsonrpc.Message) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if response, ok := message.(*jsonrpc.Response); ok {
		if response.ID.IsValid() {
			defer c.outstanding.Done()
		}
		if id, ok := response.ID.Raw().(string); ok && id == "graft-bootstrap" {
			return nil
		}
	}
	data, err := jsonrpc.EncodeMessage(message)
	if err != nil {
		return fmt.Errorf("encode MCP message: %w", err)
	}
	data = normalizeMCPResponse(data)
	data = append(data, '\n')
	c.mu.Lock()
	defer c.mu.Unlock()
	_, err = c.writer.Write(data)
	return err
}

func (c *mcpConnection) trackRequest(message jsonrpc.Message) {
	request, ok := message.(*jsonrpc.Request)
	if ok && request.ID.IsValid() {
		c.outstanding.Add(1)
	}
}

// Close closes the input reader when it supports io.Closer.
func (c *mcpConnection) Close() error {
	if closer, ok := c.reader.(io.Closer); ok {
		return closer.Close()
	}
	return nil
}

func (c *mcpConnection) writeValue(value any) error {
	data, err := jsonjs.Marshal(value, "")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	c.mu.Lock()
	defer c.mu.Unlock()
	_, err = c.writer.Write(data)
	return err
}

type mcpResult struct {
	text    string
	isError bool
	// thrown marks a failure the TypeScript tool raises as an exception; its
	// catch answers with the message alone, dropping any refresh note.
	thrown bool
}

// mcpCall answers one tools/call. The SDK runs calls concurrently, so it
// shares no mutable state with other calls; ctx is cancelled when the client
// cancels the request, and is checked between the refresh and the query.
func mcpCall(ctx context.Context, root, contextDir, dirOverride, requestedName string, args map[string]any) (result mcpResult) {
	name := mcpAliases[requestedName]
	if name == "" {
		name = requestedName
	}
	known := name == "graft_find_code" || name == "graft_file_api" || name == "graft_check_freshness" ||
		name == "graft_trace_calls" || name == "graft_find_all" || name == "graft_repo_map"
	// Price this session's tokens once per call, so the savings line can carry
	// dollars. The rate is an atomic and every call prices the same root.
	savings.SetInputRate(savings.SessionInputRate(root))
	if !known {
		return mcpResult{text: "unknown tool: " + requestedName, isError: true}
	}
	if ctx.Err() != nil {
		return mcpResult{text: context.Cause(ctx).Error(), isError: true, thrown: true}
	}
	if name != "graft_check_freshness" {
		children, workspace := graph.ReadWorkspaceChildren(contextDir)
		var refresh graph.RefreshResult
		if workspace {
			eligible := make([]string, 0, len(children))
			for _, child := range children {
				if refreshableGraph(filepath.Join(root, child, "graft")) {
					eligible = append(eligible, child)
				}
			}
			refresh = graph.EnsureFreshChildren(root, eligible)
		} else if refreshableGraph(contextDir) {
			options := graph.RefreshOptions{}
			if dirOverride != "" {
				options.Source.OutDir = contextDir
			}
			refresh = graph.EnsureFreshGraph(root, options)
		}
		if note := graph.RefreshNote(refresh); note != "" {
			defer func() {
				if !result.thrown {
					result.text = note + "\n" + result.text
				}
			}()
		}
	}
	if ctx.Err() != nil {
		return mcpResult{text: context.Cause(ctx).Error(), isError: true, thrown: true}
	}
	if name != "graft_check_freshness" && !mcpGraphAvailable(contextDir) {
		return mcpResult{text: "no graph found — run `graft build` first", isError: true}
	}

	switch name {
	case "graft_find_code":
		query := mcpString(args["query"])
		if query == "" {
			return mcpResult{text: "graft_find_code requires a query", isError: true}
		}
		limit := 5
		if value, ok := mcpNumber(args["limit"]); ok {
			limit = int(value)
		}
		// Typed options, never argv: a query such as "--dir=/x" stays a query.
		return mcpRunCommand(runAsk, callersOptions{
			command: "ask", query: query, root: root, rootSet: true, contextDir: dirOverride,
			limit: strconv.Itoa(limit), source: true, full: args["full"] == true, in: mcpString(args["in"]), noRefresh: true,
		})
	case "graft_file_api":
		file := mcpString(args["file"])
		if file == "" {
			return mcpResult{text: "graft_file_api requires a file", isError: true}
		}
		// Skeleton never federates: at a workspace root it reports the missing
		// graph through its own note, as the TypeScript tool does.
		result := graph.SkeletonResult{File: file, Entries: make([]graph.SkeletonEntry, 0), Note: "no wiring graph — run `graft build` first"}
		if loaded, err := graph.Read(graph.WiringPath(contextDir)); err == nil {
			result = graph.Skeleton(*loaded, file)
		}
		var text bytes.Buffer
		writeSkeletonHuman(&text, result)
		return mcpResult{text: text.String(), isError: len(result.Entries) == 0 && result.Note != ""}
	case "graft_trace_calls":
		symbol := mcpString(args["symbol"])
		if symbol == "" {
			symbol = mcpString(args["file"])
		}
		if symbol == "" {
			return mcpResult{text: "graft_trace_calls requires a symbol", isError: true}
		}
		if _, workspace := graph.ReadWorkspaceChildren(contextDir); workspace {
			return mcpWorkspaceTraceCalls(root, contextDir, symbol, args)
		}
		return mcpTraceCalls(root, contextDir, symbol, args)
	case "graft_find_all":
		pattern := mcpString(args["pattern"])
		if pattern == "" {
			return mcpResult{text: "graft_find_all requires a pattern", isError: true}
		}
		if _, workspace := graph.ReadWorkspaceChildren(contextDir); workspace {
			return mcpWorkspaceGrep(root, contextDir, pattern, args)
		}
		// Typed options, never argv: a pattern such as "-i" stays a pattern,
		// and the graph is the one this server was started on.
		output := mcpRunCommand(runGrep, callersOptions{
			command: "grep", query: pattern, root: root, rootSet: true, contextDir: dirOverride, jsonOutput: true,
			ignoreCase: args["ignore_case"] == true, fixed: args["fixed"] == true, in: mcpString(args["in"]), noRefresh: true,
		})
		if output.isError {
			return output
		}
		var result graph.GrepResult
		if err := json.Unmarshal([]byte(output.text), &result); err != nil {
			return mcpResult{text: err.Error(), isError: true, thrown: true}
		}
		if result.TotalHits == 0 {
			return mcpResult{text: grepZeroHitNote(result), isError: false}
		}
		return mcpResult{text: formatGrepResult(result), isError: false}
	case "graft_repo_map":
		maxDirs := 0
		if value, ok := mcpNumber(args["max_dirs"]); ok && value > 0 {
			maxDirs = int(value)
		}
		if _, ok := graph.ReadWorkspaceChildren(contextDir); ok {
			return mcpResult{text: graph.FederateMap(root, contextDir, graph.RepoMapOptions{MaxDirs: maxDirs}), isError: false}
		}
		loaded, err := graph.Read(graph.WiringPath(contextDir))
		if err != nil {
			return mcpResult{text: "no graph found — run `graft build` first", isError: true}
		}
		return mcpResult{text: graph.FormatRepoMap(graph.BuildRepoMap(*loaded, graph.RepoMapOptions{MaxDirs: maxDirs})), isError: false}
	case "graft_check_freshness":
		if _, workspace := graph.ReadWorkspaceChildren(contextDir); workspace {
			return mcpWorkspaceCheckFreshness(root, contextDir)
		}
		return mcpResult{text: mcpCheckFreshness(root, contextDir), isError: false}
	default:
		return mcpResult{text: "unknown tool: " + requestedName, isError: true}
	}
}

// mcpRunCommand runs a CLI command with options built from tool arguments; a
// failure answers with the command's diagnostic, as a thrown error.
func mcpRunCommand(command func(callersOptions, io.Writer, io.Writer) int, opts callersOptions) mcpResult {
	var stdout, stderr bytes.Buffer
	if status := command(opts, &stdout, &stderr); status != 0 {
		return mcpResult{text: mcpErrorText(stderr.String()), isError: true, thrown: true}
	}
	return mcpResult{text: stdout.String(), isError: false}
}

func mcpTraceCalls(root, contextDir, symbol string, args map[string]any) mcpResult {
	loaded, err := graph.Read(graph.WiringPath(contextDir))
	if err != nil {
		return mcpResult{text: "no graph found — run `graft build` first", isError: true}
	}
	direction := graph.DirectionIn
	if mcpString(args["direction"]) == "out" {
		direction = graph.DirectionOut
	}
	matches, err := graph.ResolveSymbol(*loaded, symbol, graph.ResolveSymbolOptions{In: mcpString(args["in"])})
	if err != nil {
		return mcpResult{text: err.Error(), isError: true, thrown: true}
	}
	if len(matches) == 0 {
		return mcpResult{text: "no symbol \"" + symbol + "\" in the graph — check spelling or run `graft build`", isError: true}
	}
	depth := mcpDepthValue(args["depth"])
	results := make([]callersResult, len(matches))
	for index, match := range matches {
		results[index] = callersResult{symbol: match, hits: graph.EdgeWalk(*loaded, match, direction, depth)}
	}

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
			}
		}
		body.WriteByte('\n')
	}
	text := strings.TrimRight(body.String(), "\n")
	return mcpResult{text: mcpWithSavings(text, callersSavings(*loaded, results)), isError: false}
}

func mcpWorkspaceTraceCalls(root, contextDir, symbol string, args map[string]any) mcpResult {
	direction := graph.DirectionIn
	if mcpString(args["direction"]) == "out" {
		direction = graph.DirectionOut
	}
	text, found, err := federateCallers(root, contextDir, symbol, direction, mcpDepthValue(args["depth"]), mcpString(args["in"]))
	if err != nil {
		return mcpResult{text: err.Error(), isError: true, thrown: true}
	}
	return mcpResult{text: text, isError: !found}
}

func mcpWorkspaceGrep(root, contextDir, pattern string, args map[string]any) mcpResult {
	result, coverage, err := federateGrep(root, contextDir, pattern, args["ignore_case"] == true, args["fixed"] == true)
	if err != nil {
		// The TypeScript tool reports a thrown error by its bare message.
		text := err.Error()
		if message, ok := grepSyntaxMessage(err, pattern, args["ignore_case"] == true); ok {
			text = message
		}
		return mcpResult{text: text, isError: true, thrown: true}
	}
	text := grepZeroHitNote(result)
	if result.TotalHits > 0 {
		text = formatGrepResult(result)
	}
	if coverage != "" {
		text += "\n" + coverage
	}
	return mcpResult{text: text, isError: false}
}

// mcpDepthValue reads depth as the CLI reads --depth, so 3, "3" and "max"
// mean the same in both; a value the CLI rejects walks direct edges only.
func mcpDepthValue(value any) int {
	raw := ""
	switch typed := value.(type) {
	case string:
		raw = typed
	case float64:
		raw = jsonjs.FormatNumber(typed)
	}
	depth, err := resolveDepth(raw)
	if err != nil {
		return 1
	}
	return depth
}

func mcpWithSavings(body string, saved *savedOutput) string {
	if saved == nil {
		return body
	}
	return savings.With(body, saved.Files, saved.BaselineChars)
}

func mcpGraphAvailable(contextDir string) bool {
	if _, err := os.Stat(graph.WiringPath(contextDir)); err == nil {
		return true
	}
	_, err := os.Stat(filepath.Join(contextDir, "workspace.json"))
	return err == nil
}

// mcpCheckFreshness reports graph freshness without refreshing first.
func mcpCheckFreshness(root, contextDir string) string {
	graphResult, err := graph.CheckGraph(root, contextDir)
	if err != nil {
		return err.Error()
	}
	return graph.FormatGraphCheckReport(graphResult)
}

func mcpWorkspaceCheckFreshness(root, contextDir string) mcpResult {
	workspace := graph.LoadWorkspaceGraphs(root, contextDir)
	lines := []string{fmt.Sprintf("workspace check — %d repo(s)", len(workspace.Loaded)+len(workspace.Missing)), ""}
	for _, child := range workspace.Loaded {
		childRoot := filepath.Join(root, child.Name)
		report := mcpCheckFreshness(childRoot, filepath.Join(childRoot, "graft"))
		if strings.Contains(report, "graph check: OK") {
			lines = append(lines, child.Name+"/: OK")
			continue
		}
		bits := make([]string, 0, 4)
		for _, group := range []struct {
			heading string
			label   string
		}{
			{heading: "added", label: "added"},
			{heading: "removed", label: "removed"},
			{heading: "changed", label: "changed"},
		} {
			for line := range strings.SplitSeq(report, "\n") {
				prefix := group.heading + " ("
				if !strings.HasPrefix(line, prefix) {
					continue
				}
				countText, _, ok := strings.Cut(strings.TrimPrefix(line, prefix), ")")
				count, err := strconv.Atoi(countText)
				if ok && err == nil && count > 0 {
					bits = append(bits, fmt.Sprintf("%d %s", count, group.label))
				}
				break
			}
		}
		if len(bits) == 0 {
			if strings.Contains(report, "graph check: UNKNOWN") {
				bits = append(bits, "freshness unknown")
			} else {
				bits = append(bits, "stale")
			}
		}
		lines = append(lines, child.Name+"/: STALE ("+strings.Join(bits, ", ")+")")
	}
	for _, child := range workspace.Missing {
		lines = append(lines, child+"/: not built (run graft build)")
	}
	if coverage := mcpWorkspaceCoverage(workspace); coverage != "" {
		lines = append(lines, "", coverage)
	}
	return mcpResult{text: strings.Join(lines, "\n") + "\n", isError: false}
}

func mcpWorkspaceCoverage(workspace graph.WorkspaceGraphs) string {
	if len(workspace.Missing) == 0 {
		return ""
	}
	return fmt.Sprintf("%d of %d workspace repos have graphs; run graft build to cover %s", len(workspace.Loaded), len(workspace.Loaded)+len(workspace.Missing), strings.Join(workspace.Missing, ", "))
}

func mcpString(value any) string {
	if value == nil {
		return ""
	}
	if text, ok := value.(string); ok {
		return text
	}
	return fmt.Sprint(value)
}

func mcpNumber(value any) (float64, bool) {
	number, ok := value.(float64)
	return number, ok && !math.IsNaN(number) && !math.IsInf(number, 0)
}

func mcpErrorText(text string) string {
	text = strings.TrimSpace(text)
	return strings.TrimPrefix(text, "✗ ")
}
