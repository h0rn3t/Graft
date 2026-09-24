package main

import (
	"bytes"
	"os"
	"strings"
	"testing"
)

func TestRemovedCLIInputs(t *testing.T) {
	tests := []struct {
		name string
		args []string
		want string
	}{
		{name: "deep build", args: []string{"build", "--deep"}, want: "error: unknown option '--deep'\n(Did you mean --help?)\n"},
		{name: "short concurrency", args: []string{"build", "-j", "2"}, want: "error: unknown option '-j'\n"},
		{name: "long concurrency", args: []string{"build", "--concurrency", "2"}, want: "error: unknown option '--concurrency'\n"},
		{name: "allow partial", args: []string{"build", "--allow-partial"}, want: "error: unknown option '--allow-partial'\n"},
		{name: "viz command", args: []string{"viz"}, want: "error: unknown command 'viz'\n"},
		{name: "export viz", args: []string{"blast", "--export-viz", "out"}, want: "error: unknown option '--export-viz'\n"},
		{name: "viz title", args: []string{"blast", "--title", "PR"}, want: "error: unknown option '--title'\n"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			if status := run(tt.args, &stdout, &stderr); status != 1 {
				t.Fatalf("run(%v) status = %d, want 1; stdout = %q; stderr = %q", tt.args, status, stdout.String(), stderr.String())
			}
			if got := stdout.String(); got != "" {
				t.Errorf("run(%v) stdout = %q, want empty", tt.args, got)
			}
			if got := stderr.String(); got != tt.want {
				t.Errorf("run(%v) stderr = %q, want %q", tt.args, got, tt.want)
			}
		})
	}
}

func TestRemovedCLIInputsAreAbsentFromHelp(t *testing.T) {
	tests := []struct {
		args   []string
		absent []string
	}{
		{args: []string{"build", "--help"}, absent: []string{"--deep", "--concurrency", "--allow-partial"}},
		{args: []string{"blast", "--help"}, absent: []string{"--export-viz", "--title"}},
		{args: []string{"--help"}, absent: []string{"viz [options]"}},
	}
	for _, tt := range tests {
		var stdout, stderr bytes.Buffer
		if status := run(tt.args, &stdout, &stderr); status != 0 {
			t.Fatalf("run(%v) status = %d, want 0; stderr = %q", tt.args, status, stderr.String())
		}
		for _, text := range tt.absent {
			if strings.Contains(stdout.String(), text) {
				t.Errorf("run(%v) stdout = %q, want %q absent", tt.args, stdout.String(), text)
			}
		}
	}
	if _, err := os.Stat("help/viz.txt"); !os.IsNotExist(err) {
		t.Errorf("os.Stat(help/viz.txt) error = %v, want absent", err)
	}
}
