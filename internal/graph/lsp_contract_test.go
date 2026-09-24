package graph

import (
	"bufio"
	"fmt"
	"strings"
	"testing"
)

func TestLSPNamePosition(t *testing.T) {
	tests := []struct {
		name  string
		lines []string
		node  NodeV1
		want  lspPosition
		ok    bool
	}{
		{"method name inside its receiver type", []string{"package io", "func (r *Reader) Read(p []byte) (int, error) {"}, NodeV1{Name: "Read", Span: "L2-L4"}, lspPosition{Line: 1, Character: 17}, true},
		{"receiver type named like the method", []string{"func (s Get) Get() {}"}, NodeV1{Name: "Get", Span: "L1-L1"}, lspPosition{Line: 0, Character: 13}, true},
		{"generic receiver", []string{"func (s *Stack[T]) Push(v T) {}"}, NodeV1{Name: "Push", Span: "L1-L1"}, lspPosition{Line: 0, Character: 19}, true},
		{"prefix of a longer identifier first", []string{"def load_all(): return load()", ""}, NodeV1{Name: "load", Span: "L1-L1"}, lspPosition{Line: 0, Character: 23}, true},
		{"name on a later line", []string{"@decorator", "def run():"}, NodeV1{Name: "run", Span: "L1-L2"}, lspPosition{Line: 1, Character: 4}, true},
		{"UTF-16 columns", []string{"const ключ😀 = 1; function go() {}"}, NodeV1{Name: "go", Span: "L1-L1"}, lspPosition{Line: 0, Character: 27}, true},
		{"absent name", []string{"function other() {}"}, NodeV1{Name: "run", Span: "L1-L1"}, lspPosition{}, false},
		{"span past the file", []string{"x"}, NodeV1{Name: "x", Span: "L5-L6"}, lspPosition{}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := lspNamePosition(tt.lines, tt.node)
			if got != tt.want || ok != tt.ok {
				t.Errorf("lspNamePosition(%q, %q) = (%+v, %t), want (%+v, %t)", tt.lines, tt.node.Name, got, ok, tt.want, tt.ok)
			}
		})
	}
}

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
