package graph

import (
	"bufio"
	"fmt"
	"strings"
	"testing"
)

func TestReadLSPMessageFraming(t *testing.T) {
	body := `{"jsonrpc":"2.0","id":7,"method":"initialize","params":{}}`
	input := fmt.Sprintf("Content-Length: %d\r\nContent-Type: application/vscode-jsonrpc; charset=utf-8\r\n\r\n%s", len(body), body)
	message, err := readLSPMessage(bufio.NewReader(strings.NewReader(input)))
	if err != nil {
		t.Fatalf("readLSPMessage(valid frame) error = %v, want nil", err)
	}
	if message.JSONRPC != "2.0" || message.Method != "initialize" || string(message.ID) != "7" {
		t.Errorf("readLSPMessage(valid frame) = %#v, want JSON-RPC initialize request with id 7", message)
	}
}

func TestReadLSPMessageRejectsInvalidFraming(t *testing.T) {
	for _, input := range []string{
		"Content-Length: -1\r\n\r\n",
		"Content-Length: 1\r\nContent-Length: 1\r\n\r\nx",
		fmt.Sprintf("Content-Length: %d\r\n\r\n", maxLSPMessageBytes+1),
		"Content-Length: 1\r\n\r\nx",
	} {
		if _, err := readLSPMessage(bufio.NewReader(strings.NewReader(input))); err == nil {
			t.Errorf("readLSPMessage(%q) error = nil, want framing error", input)
		}
	}
}

func FuzzReadLSPMessage(f *testing.F) {
	body := `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{}}`
	f.Add(fmt.Sprintf("Content-Length: %d\r\n\r\n%s", len(body), body))
	f.Add("Content-Length: 3\r\n\r\n{}")
	f.Add("invalid frame")
	f.Fuzz(func(t *testing.T, input string) {
		if len(input) > maxLSPMessageBytes+1024 {
			t.Skip()
		}
		_, _ = readLSPMessage(bufio.NewReader(strings.NewReader(input)))
	})
}
