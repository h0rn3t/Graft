package main

import (
	"bytes"
	"context"
	"encoding/json/v2"
	"io"
	"strings"
	"testing"

	"github.com/h0rn3t/Graft/internal/graph"
	"github.com/h0rn3t/Graft/internal/savings"
)

func TestHookPromptSkipsHostEnvelopes(t *testing.T) {
	root := t.TempDir()
	ask := hookAsk
	t.Cleanup(func() { hookAsk = ask })
	hookAsk = func(context.Context, string, string, string, string) (graph.AskResult, bool) {
		return graph.AskResult{Hits: []graph.AskHit{{
			Title: "verify", Pointer: "auth.go:L1-L1", Code: "func verify() bool { return true }",
		}}}, true
	}
	tests := []struct {
		name   string
		prompt string
		want   bool
	}{
		{
			name:   "subagent hand-back",
			prompt: "Another Claude session sent a message:\n<agent-message from=\"a61f\">\nThe tests of verify pass.\n</agent-message>\n\nThat \"other Claude session\" is an agent working inside this same session.",
		},
		{
			name:   "bare agent message",
			prompt: "<agent-message from=\"a61f\">\nThe tests of verify pass.\n</agent-message>",
		},
		{
			name:   "task notification",
			prompt: "<task-notification>\n<task-id>a61f</task-id>\n<status>completed</status>\n</task-notification>",
		},
		{name: "user prompt", prompt: "verify the authentication flow", want: true},
		{name: "prompt naming the tag", prompt: "why does verify run on <agent-message> prompts?", want: true},
		{
			name:   "envelope quoted below a question",
			prompt: "verify this report:\nit came from a subagent\n<agent-message from=\"a61f\">\nreport\n</agent-message>",
			want:   true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			input := hookInput{"session_id": tt.name, "prompt": tt.prompt}
			var out bytes.Buffer
			handleHookPrompt(t.Context(), input, root, &out, io.Discard)
			if got := out.Len() > 0; got != tt.want {
				t.Errorf("handleHookPrompt(%q) injected = %t, want %t; output=%q", tt.prompt, got, tt.want, out.String())
			}
		})
	}
}

func TestEmitHookContextStaysUnderTheInlineLimit(t *testing.T) {
	tests := []struct {
		name    string
		context string
		want    func(string) bool
	}{
		{
			name:    "short",
			context: "[graft] retrieved context",
			want:    func(got string) bool { return got == "[graft] retrieved context" },
		},
		{
			name:    "minified line",
			context: "[graft] retrieved context\n```\n" + strings.Repeat("var a=1;", 2500) + "\n```",
			want: func(got string) bool {
				return savings.Length(got) <= hookContextMaxChars && strings.HasPrefix(got, "[graft] retrieved context\n```\nvar a=1;") && strings.HasSuffix(got, "(cut at the hook output limit)")
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var out bytes.Buffer
			emitHookContext(&out, "UserPromptSubmit", tt.context)
			var payload struct {
				HookSpecificOutput struct {
					AdditionalContext string `json:"additionalContext"`
				} `json:"hookSpecificOutput"`
			}
			if err := json.Unmarshal(out.Bytes(), &payload); err != nil {
				t.Fatalf("emitHookContext(%d chars) wrote %q: %v", len(tt.context), out.String(), err)
			}
			got := payload.HookSpecificOutput.AdditionalContext
			if !tt.want(got) {
				t.Errorf("emitHookContext(%d chars) additionalContext = %d chars ending %q, want at most %d chars with the cut noted", len(tt.context), savings.Length(got), got[max(0, len(got)-60):], hookContextMaxChars)
			}
		})
	}
}
