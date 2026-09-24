package graph

import (
	"bufio"
	"context"
	"encoding/json/jsontext"
	jsonv2 "encoding/json/v2"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/h0rn3t/Graft/internal/sourcefiles"
)

const (
	lspCallTimeout       = 15 * time.Second
	lspInitializeTimeout = 2 * time.Minute
	lspReadyTimeout      = 90 * time.Second
	// lspShutdownTimeout bounds the polite shutdown request and exit notice.
	lspShutdownTimeout = 2 * time.Second
	// lspExitGrace is how long a server may take to exit after the handshake
	// before its process group is killed.
	lspExitGrace = time.Second
	// lspWaitDelay bounds how long reaping the server waits on its pipes once
	// the process has exited or been killed.
	lspWaitDelay       = 2 * time.Second
	maxLSPMessageBytes = 16 << 20
)

var errLSPClosed = errors.New("language server closed")

type lspMessage struct {
	JSONRPC string         `json:"jsonrpc"`
	ID      jsontext.Value `json:"id,omitzero"`
	Method  string         `json:"method,omitzero"`
	Params  jsontext.Value `json:"params,omitzero"`
	Result  jsontext.Value `json:"result,omitzero"`
	Error   *lspRPCError   `json:"error,omitzero"`
}

type lspRPCError struct {
	Code    int            `json:"code"`
	Message string         `json:"message"`
	Data    jsontext.Value `json:"data,omitzero"`
}

type lspReply struct {
	result jsontext.Value
	err    error
}

// lspClient speaks JSON-RPC to one language server process. Requests may be
// issued from several goroutines; the reader goroutine only routes messages and
// never writes, so a server blocked on its own output cannot deadlock it.
type lspClient struct {
	root   string
	langID string
	cmd    *exec.Cmd
	stdin  io.WriteCloser
	done   chan struct{} // closed when the reader stops
	exited chan struct{} // closed once the process is reaped

	mu       sync.Mutex
	writeMu  sync.Mutex
	pending  map[string]chan lspReply
	openMu   sync.Mutex
	opened   map[string]struct{}
	replies  sync.WaitGroup // answers to server requests still being written
	ready    atomic.Bool
	nextID   atomic.Uint64
	closeOne sync.Once
}

func startLSPClient(ctx context.Context, server lspServer, root string) (*lspClient, error) {
	cmd := exec.CommandContext(ctx, server.command, server.args...)
	cmd.Dir = root
	cmd.Stderr = io.Discard
	startInOwnProcessGroup(cmd)
	cmd.Cancel = func() error { return killProcessGroup(cmd) }
	cmd.WaitDelay = lspWaitDelay
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, fmt.Errorf("open language server input: %w", err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		_ = stdin.Close() // Release the input pipe when startup cannot continue.
		return nil, fmt.Errorf("open language server output: %w", err)
	}
	if err := cmd.Start(); err != nil {
		_ = stdin.Close() // Release the input pipe when startup fails.
		return nil, fmt.Errorf("start language server: %w", err)
	}
	client := &lspClient{
		root:    root,
		langID:  server.languageID,
		cmd:     cmd,
		stdin:   stdin,
		done:    make(chan struct{}),
		exited:  make(chan struct{}),
		pending: make(map[string]chan lspReply),
		opened:  make(map[string]struct{}),
	}
	// Reaping runs beside the reader: Wait closes the output pipe once the
	// server exits, which also ends a read that a surviving grandchild holding
	// the pipe would otherwise block forever.
	go func() {
		_ = cmd.Wait() // The exit status of a best-effort server carries no information.
		close(client.exited)
	}()
	go client.readMessages(stdout)
	return client, nil
}

func (c *lspClient) initialize(ctx context.Context) bool {
	initCtx, cancel := context.WithTimeout(ctx, lspInitializeTimeout)
	defer cancel()
	rootURI := lspFileURI(c.root)
	result, err := c.request(initCtx, "initialize", map[string]any{
		"processId": os.Getpid(),
		"rootUri":   rootURI,
		"workspaceFolders": []map[string]string{{
			"uri": rootURI, "name": "root",
		}},
		"capabilities": map[string]any{
			"textDocument": map[string]any{
				"callHierarchy":   map[string]any{"dynamicRegistration": false},
				"synchronization": map[string]any{"dynamicRegistration": false},
			},
			"workspace": map[string]any{"workspaceFolders": true, "configuration": true},
		},
	})
	if err != nil || len(result) == 0 || string(result) == "null" {
		return false
	}
	if err := c.notify(initCtx, "initialized", map[string]any{}); err != nil {
		return false
	}
	c.ready.Store(true)
	return true
}

func (c *lspClient) didOpen(ctx context.Context, abs string) {
	if !c.ready.Load() {
		return
	}
	c.openMu.Lock()
	_, open := c.opened[abs]
	c.openMu.Unlock()
	if open {
		return
	}
	text, readable, err := sourcefiles.Read(abs)
	if err != nil || !readable {
		return
	}
	c.openMu.Lock()
	c.opened[abs] = struct{}{}
	c.openMu.Unlock()
	_ = c.notify(ctx, "textDocument/didOpen", map[string]any{
		"textDocument": map[string]any{
			"uri": lspFileURI(abs), "languageId": c.langID, "version": 1, "text": text,
		},
	}) // A server that closed mid-write is already on the fail-soft path.
}

// didClose releases a document opened by didOpen, so a server does not keep
// every source of a large repository in memory.
func (c *lspClient) didClose(ctx context.Context, abs string) {
	c.openMu.Lock()
	_, open := c.opened[abs]
	delete(c.opened, abs)
	c.openMu.Unlock()
	if !open || !c.ready.Load() {
		return
	}
	_ = c.notify(ctx, "textDocument/didClose", map[string]any{
		"textDocument": map[string]string{"uri": lspFileURI(abs)},
	}) // A server that closed mid-write is already on the fail-soft path.
}

// lspProbe is a definition whose call hierarchy shows the server has indexed.
type lspProbe struct {
	abs      string
	position lspPosition
}

// waitUntilReady polls prepareCallHierarchy until the server answers for one
// of probes. Each poll asks the next probe in turn, so one definition the
// server cannot place never decides readiness alone.
func (c *lspClient) waitUntilReady(ctx context.Context, probes []lspProbe) bool {
	if len(probes) == 0 {
		return false
	}
	for _, probe := range probes {
		c.didOpen(ctx, probe.abs)
	}
	deadline := time.Now().Add(lspReadyTimeout)
	for attempt := 0; time.Until(deadline) > 0; attempt++ {
		probe := probes[attempt%len(probes)]
		pollCtx, cancel := context.WithDeadline(ctx, deadline)
		items := c.prepareCallHierarchy(pollCtx, probe.abs, probe.position)
		cancel()
		if len(items) > 0 {
			return true
		}
		if ctx.Err() != nil {
			return false
		}
		if (attempt+1)%len(probes) != 0 {
			continue // Try the remaining probes before waiting.
		}
		timer := time.NewTimer(min(2*time.Second, time.Until(deadline)))
		select {
		case <-timer.C:
		case <-ctx.Done():
			timer.Stop()
			return false
		case <-c.done:
			timer.Stop()
			return false
		}
	}
	return false
}

func (c *lspClient) prepareCallHierarchy(ctx context.Context, abs string, position lspPosition) []lspItem {
	if !c.ready.Load() {
		return nil
	}
	result, err := c.call(ctx, "textDocument/prepareCallHierarchy", map[string]any{
		"textDocument": map[string]string{"uri": lspFileURI(abs)},
		"position":     position,
	})
	if err != nil {
		return nil
	}
	var items []lspItem
	if jsonv2.Unmarshal(result, &items) != nil {
		return nil
	}
	return items
}

func (c *lspClient) outgoingCalls(ctx context.Context, item lspItem) []lspItem {
	if !c.ready.Load() {
		return nil
	}
	result, err := c.call(ctx, "callHierarchy/outgoingCalls", map[string]any{"item": item})
	if err != nil {
		return nil
	}
	var calls []struct {
		To lspItem `json:"to"`
	}
	if jsonv2.Unmarshal(result, &calls) != nil {
		return nil
	}
	items := make([]lspItem, 0, len(calls))
	for _, call := range calls {
		items = append(items, call.To)
	}
	return items
}

func (c *lspClient) call(ctx context.Context, method string, params any) (jsontext.Value, error) {
	callCtx, cancel := context.WithTimeout(ctx, lspCallTimeout)
	defer cancel()
	return c.request(callCtx, method, params)
}

// encodeLSPParams encodes request parameters; nil params are omitted, as a
// parameterless request such as shutdown requires.
func encodeLSPParams(params any) (jsontext.Value, error) {
	if params == nil {
		return nil, nil
	}
	encoded, err := jsonv2.Marshal(params)
	return jsontext.Value(encoded), err
}

func (c *lspClient) request(ctx context.Context, method string, params any) (jsontext.Value, error) {
	encoded, err := encodeLSPParams(params)
	if err != nil {
		return nil, err
	}
	id := jsontext.Value(strconv.AppendUint(nil, c.nextID.Add(1), 10))
	key := string(id)
	reply := make(chan lspReply, 1)
	c.mu.Lock()
	select {
	case <-c.done:
		c.mu.Unlock()
		return nil, errLSPClosed
	default:
	}
	c.pending[key] = reply
	c.mu.Unlock()
	if err := c.send(ctx, lspMessage{JSONRPC: "2.0", ID: id, Method: method, Params: encoded}); err != nil {
		c.removePending(key)
		return nil, err
	}
	select {
	case response := <-reply:
		if response.err != nil {
			return nil, response.err
		}
		return response.result, nil
	case <-ctx.Done():
		c.removePending(key)
		return nil, ctx.Err()
	case <-c.done:
		c.removePending(key)
		return nil, errLSPClosed
	}
}

func (c *lspClient) notify(ctx context.Context, method string, params any) error {
	encoded, err := encodeLSPParams(params)
	if err != nil {
		return err
	}
	return c.send(ctx, lspMessage{JSONRPC: "2.0", Method: method, Params: encoded})
}

// send writes one framed message. A write the server stops reading would
// block forever, so when ctx ends first the server is killed, which fails the
// write and every later one.
func (c *lspClient) send(ctx context.Context, message lspMessage) error {
	data, err := jsonv2.Marshal(message)
	if err != nil {
		return fmt.Errorf("encode language server message: %w", err)
	}
	frame := fmt.Appendf(nil, "Content-Length: %d\r\n\r\n", len(data))
	frame = append(frame, data...)
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	select {
	case <-c.done:
		return errLSPClosed
	default:
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	stop := context.AfterFunc(ctx, c.kill)
	defer stop()
	for len(frame) > 0 {
		written, err := c.stdin.Write(frame)
		if err != nil {
			return fmt.Errorf("write language server message: %w", err)
		}
		if written == 0 {
			return io.ErrShortWrite
		}
		frame = frame[written:]
	}
	return nil
}

func (c *lspClient) readMessages(stdout io.Reader) {
	reader := bufio.NewReader(stdout)
	for {
		message, err := readLSPMessage(reader)
		if err != nil {
			c.kill() // Stop a server that stopped speaking or sent a bad frame.
			c.shutdown(err)
			return
		}
		c.dispatch(message)
	}
}

func (c *lspClient) dispatch(message lspMessage) {
	if message.Method != "" {
		if len(message.ID) == 0 {
			return
		}
		// Answer off the reader goroutine: a reply write can block until the
		// server reads its input, and the server may first need us to read.
		c.replies.Go(func() { c.handleServerRequest(message) })
		return
	}
	if len(message.ID) == 0 {
		return
	}
	key := string(message.ID)
	c.mu.Lock()
	reply := c.pending[key]
	delete(c.pending, key)
	c.mu.Unlock()
	if reply == nil {
		return
	}
	if message.Error != nil {
		reply <- lspReply{err: fmt.Errorf("language server request %s failed: %s", key, message.Error.Message)}
		return
	}
	reply <- lspReply{result: message.Result}
}

func (c *lspClient) handleServerRequest(message lspMessage) {
	ctx, cancel := context.WithTimeout(context.Background(), lspCallTimeout)
	defer cancel()
	var result any
	switch message.Method {
	case "workspace/configuration":
		var params struct {
			Items []jsontext.Value `json:"items"`
		}
		_ = jsonv2.Unmarshal(message.Params, &params) // Malformed parameters get an empty configuration list.
		items := make([]map[string]any, len(params.Items))
		for index := range items {
			items[index] = map[string]any{}
		}
		result = items
	case "client/registerCapability", "window/workDoneProgress/create":
		result = nil
	case "workspace/applyEdit":
		result = map[string]bool{"applied": false}
	default:
		_ = c.send(ctx, lspMessage{
			JSONRPC: "2.0", ID: message.ID,
			Error: &lspRPCError{Code: -32601, Message: "method not found"},
		}) // The peer may have closed while this request was being handled.
		return
	}
	encoded, err := jsonv2.Marshal(result)
	if err != nil {
		return
	}
	_ = c.send(ctx, lspMessage{JSONRPC: "2.0", ID: message.ID, Result: jsontext.Value(encoded)}) // The peer may have closed.
}

// kill stops the server and every process it started.
func (c *lspClient) kill() {
	_ = killProcessGroup(c.cmd) // The group may already be gone.
}

func (c *lspClient) shutdown(err error) {
	c.ready.Store(false)
	if err == nil {
		err = errLSPClosed
	}
	c.mu.Lock()
	pending := c.pending
	c.pending = make(map[string]chan lspReply)
	c.mu.Unlock()
	for _, reply := range pending {
		reply <- lspReply{err: err}
	}
	close(c.done)
}

func (c *lspClient) removePending(key string) {
	c.mu.Lock()
	delete(c.pending, key)
	c.mu.Unlock()
}

// close asks the server to shut down and exit, then kills its process group
// whether or not it complied, and waits for the reader and reply goroutines.
// Every step is bounded, so close returns even for a wedged server.
func (c *lspClient) close() {
	c.closeOne.Do(func() {
		if c.ready.Load() {
			ctx, cancel := context.WithTimeout(context.Background(), lspShutdownTimeout)
			if _, err := c.request(ctx, "shutdown", nil); err == nil {
				_ = c.notify(ctx, "exit", nil) // The server may exit before reading it.
			}
			cancel()
		}
		c.ready.Store(false)
		_ = c.stdin.Close() // A server that ignored exit sees end of input.
		timer := time.NewTimer(lspExitGrace)
		select {
		case <-c.exited:
		case <-timer.C:
		}
		timer.Stop()
		c.kill()
		<-c.exited
		<-c.done
		c.replies.Wait()
	})
}

func readLSPMessage(reader *bufio.Reader) (lspMessage, error) {
	length := -1
	for {
		line, err := reader.ReadString('\n')
		if err != nil {
			return lspMessage{}, err
		}
		line = strings.TrimRight(line, "\r\n")
		if line == "" {
			break
		}
		name, value, ok := strings.Cut(line, ":")
		if !ok || !strings.EqualFold(name, "Content-Length") {
			continue
		}
		if length >= 0 {
			return lspMessage{}, errors.New("duplicate language server content length")
		}
		length, err = strconv.Atoi(strings.TrimSpace(value))
		if err != nil || length < 0 || length > maxLSPMessageBytes {
			return lspMessage{}, errors.New("invalid language server content length")
		}
	}
	if length < 0 {
		return lspMessage{}, errors.New("missing language server content length")
	}
	data := make([]byte, length)
	if _, err := io.ReadFull(reader, data); err != nil {
		return lspMessage{}, err
	}
	var message lspMessage
	if err := jsonv2.Unmarshal(data, &message); err != nil {
		return lspMessage{}, fmt.Errorf("decode language server message: %w", err)
	}
	if message.JSONRPC != "2.0" || (message.Method == "" && len(message.ID) == 0) {
		return lspMessage{}, errors.New("invalid language server message")
	}
	return message, nil
}
