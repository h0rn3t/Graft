package graph

import (
	"bufio"
	"context"
	"encoding/json/jsontext"
	jsonv2 "encoding/json/v2"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"testing"
	"time"
)

// lspHelperEnv selects the fake language server TestLSPHelperProcess plays.
const lspHelperEnv = "GRAFT_LSP_HELPER"

// TestLSPHelperProcess is not a test: re-executed by startFakeLSP, it plays a
// scripted language server on stdin and stdout.
func TestLSPHelperProcess(t *testing.T) {
	mode := os.Getenv(lspHelperEnv)
	if mode == "" {
		t.Skip("helper process for the LSP client tests")
	}
	reader := bufio.NewReader(os.Stdin)
	write := func(message lspMessage) {
		data, err := jsonv2.Marshal(message)
		if err != nil {
			os.Exit(3)
		}
		_, _ = fmt.Fprintf(os.Stdout, "Content-Length: %d\r\n\r\n%s", len(data), data) // a closed client ends the helper next read
	}
	respond := func(id jsontext.Value, result string) {
		write(lspMessage{JSONRPC: "2.0", ID: id, Result: jsontext.Value(result)})
	}
	for {
		message, err := readLSPMessage(reader)
		if err != nil {
			os.Exit(0)
		}
		switch message.Method {
		case "initialize":
			switch mode {
			case "flood":
				// Ask far more than a pipe buffer holds before reading any
				// answer, as a busy server indexing a workspace can.
				const requests = 5000
				for index := range requests {
					write(lspMessage{
						JSONRPC: "2.0", ID: jsontext.Value(strconv.Quote("s" + strconv.Itoa(index))),
						Method: "workspace/configuration", Params: jsontext.Value(`{"items":[{"section":"x"}]}`),
					})
				}
				for range requests {
					if _, err := readLSPMessage(reader); err != nil {
						os.Exit(4)
					}
				}
			case "wedge":
				// A helper that inherits stdout keeps the pipe open after the
				// server itself dies.
				grandchild := exec.Command("sleep", "60")
				grandchild.Stdout = os.Stdout
				if err := grandchild.Start(); err != nil {
					os.Exit(5)
				}
				if file := os.Getenv("GRAFT_LSP_HELPER_PID"); file != "" {
					_ = os.WriteFile(file, []byte(strconv.Itoa(grandchild.Process.Pid)), 0o600)
				}
			}
			respond(message.ID, `{"capabilities":{}}`)
		case "textDocument/prepareCallHierarchy":
			var params struct {
				Position lspPosition `json:"position"`
			}
			_ = jsonv2.Unmarshal(message.Params, &params)
			if params.Position.Line == 7 {
				respond(message.ID, `[{"name":"ready","kind":12,"uri":"file:///x","range":{"start":{"line":7,"character":0},"end":{"line":7,"character":1}}}]`)
			} else {
				respond(message.ID, `[]`)
			}
		case "shutdown":
			if mode == "wedge" {
				continue // Never answer, never exit.
			}
			respond(message.ID, `null`)
		case "exit":
			os.Exit(0)
		}
	}
}

// startFakeLSP starts TestLSPHelperProcess as a language server in mode.
func startFakeLSP(t *testing.T, ctx context.Context, mode string) *lspClient {
	t.Helper()
	t.Setenv(lspHelperEnv, mode)
	server := lspServer{command: os.Args[0], args: []string{"-test.run=^TestLSPHelperProcess$"}, languageID: "go"}
	client, err := startLSPClient(ctx, server, t.TempDir())
	if err != nil {
		t.Fatalf("startLSPClient(%q) error = %v, want nil", mode, err)
	}
	return client
}

func TestLSPClientAnswersServerRequestsWhileItWrites(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	client := startFakeLSP(t, ctx, "flood")
	defer client.close()
	if !client.initialize(ctx) {
		t.Fatalf("initialize() with a server flooding requests = false, want true")
	}
}

func TestLSPClientWaitUntilReadyRotatesProbes(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	client := startFakeLSP(t, ctx, "ready")
	defer client.close()
	if !client.initialize(ctx) {
		t.Fatalf("initialize() = false, want true")
	}
	probes := []lspProbe{
		{abs: filepath.Join(t.TempDir(), "unplaced.go"), position: lspPosition{Line: 1}},
		{abs: filepath.Join(t.TempDir(), "placed.go"), position: lspPosition{Line: 7}},
	}
	start := time.Now()
	if !client.waitUntilReady(ctx, probes) {
		t.Fatalf("waitUntilReady(%v) = false, want true from the second probe", probes)
	}
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Errorf("waitUntilReady(%v) took %v, want the second probe tried before any wait", probes, elapsed)
	}
}
