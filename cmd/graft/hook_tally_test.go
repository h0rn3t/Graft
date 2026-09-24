package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestHasSavingsTally(t *testing.T) {
	for _, text := range []string{
		"🌱 graft saved ~12,400 tokens this turn (3 calls)",
		"graft saved ~12k tokens this turn",
		"Graft saved 8,100 tokens across 2 calls.",
		"graft saved ≈ 900 tok this turn",
	} {
		if !hasSavingsTally(text) {
			t.Errorf("hasSavingsTally(%q) = false, want true", text)
		}
	}
	for _, text := range []string{
		"I used graft to find the call sites.",
		"graft saved me a lot of time here",
		"The 3 calls to graft returned 8 hits each.",
		"",
	} {
		if hasSavingsTally(text) {
			t.Errorf("hasSavingsTally(%q) = true, want false", text)
		}
	}
}

func TestLastHookAssistantTurn(t *testing.T) {
	path := writeHookTranscript(t,
		`{"type":"user","message":{"role":"user","content":"older question"}}`,
		`{"type":"assistant","uuid":"a1","message":{"role":"assistant","content":[{"type":"text","text":"no tally in this earlier turn"}]}}`,
		`{"type":"user","message":{"role":"user","content":"how does auth work"}}`,
		`{"type":"assistant","uuid":"a2","message":{"role":"assistant","content":[{"type":"text","text":"🌱 graft saved ~5,000 tokens this turn"}]}}`,
		`{"type":"user","message":{"role":"user","content":[{"type":"tool_result","content":"[graft] tokens saved ≈ 1,000"}]}}`,
		`{"type":"assistant","uuid":"a3","message":{"role":"assistant","content":[{"type":"thinking","thinking":"hidden"},{"type":"text","text":"Done."}]}}`,
	)
	turn := lastHookAssistantTurn(path)
	if turn == nil || turn.UUID != "a3" || !hasSavingsTally(turn.Text) || !strings.Contains(turn.Text, "Done.") {
		t.Errorf("lastHookAssistantTurn() = %#v, want whole current turn", turn)
	}

	path = writeHookTranscript(t,
		`{"type":"user","message":{"role":"user","content":"first"}}`,
		`{"type":"assistant","uuid":"a1","message":{"role":"assistant","content":"graft saved ~9,000 tokens"}}`,
		`{"type":"user","message":{"role":"user","content":"second"}}`,
		`{"type":"assistant","uuid":"a2","message":{"role":"assistant","content":"Nothing to report."}}`,
	)
	if turn := lastHookAssistantTurn(path); turn == nil || hasSavingsTally(turn.Text) {
		t.Errorf("lastHookAssistantTurn(previous tally) = %#v, want no tally", turn)
	}

	path = writeHookTranscript(t,
		`{"type":"user","message":{"role":"user","content":"go"}}`,
		`{"type":"assistant","uuid":"s1","isSidechain":true,"message":{"role":"assistant","content":"graft saved ~4,000 tokens"}}`,
		`{"type":"assistant","uuid":"a1","message":{"role":"assistant","content":"The subagent finished."}}`,
	)
	if turn := lastHookAssistantTurn(path); turn == nil || turn.UUID != "a1" || hasSavingsTally(turn.Text) {
		t.Errorf("lastHookAssistantTurn(sidechain) = %#v, want main agent only", turn)
	}

	path = writeHookTranscript(t, `{"type":"user","message":{"role":"user","content":"go"}}`)
	if turn := lastHookAssistantTurn(path); turn != nil {
		t.Errorf("lastHookAssistantTurn(no assistant) = %#v, want nil", turn)
	}
	if turn := lastHookAssistantTurn(filepath.Join(t.TempDir(), "missing.jsonl")); turn != nil {
		t.Errorf("lastHookAssistantTurn(missing) = %#v, want nil", turn)
	}
}

func TestLastHookTurnBilling(t *testing.T) {
	usage := `"usage":{"input_tokens":10,"cache_creation_input_tokens":1000,"cache_read_input_tokens":100000,"output_tokens":20}`
	path := writeHookTranscript(t,
		`{"type":"user","message":{"role":"user","content":"a question"}}`,
		`{"type":"assistant","uuid":"a1","message":{"id":"msg_1","model":"claude-opus-5","content":[{"type":"thinking","thinking":"..."}],`+usage+`}}`,
		`{"type":"assistant","uuid":"a2","message":{"id":"msg_1","model":"claude-opus-5","content":[{"type":"tool_use","name":"Bash"}],`+usage+`}}`,
	)
	billing := lastHookTurnBilling(path)
	if billing == nil || billing.UUID != "a2" || billing.Tokens != 101_010 || billing.CostMicros != 56_300 {
		t.Errorf("lastHookTurnBilling(dedup) = %#v, want a2/101010/56300", billing)
	}

	path = writeHookTranscript(t,
		`{"type":"user","message":{"role":"user","content":"a question"}}`,
		`{"type":"assistant","uuid":"a1","message":{"id":"msg_1","model":"claude-opus-5","content":"first","usage":{"input_tokens":100}}}`,
		`{"type":"assistant","uuid":"a2","message":{"id":"msg_2","model":"claude-opus-5","content":"second","usage":{"input_tokens":200}}}`,
	)
	if billing := lastHookTurnBilling(path); billing == nil || billing.Tokens != 300 {
		t.Errorf("lastHookTurnBilling(distinct) = %#v, want 300 tokens", billing)
	}

	path = writeHookTranscript(t,
		`{"type":"user","message":{"role":"user","content":"older"}}`,
		`{"type":"assistant","uuid":"a1","message":{"id":"msg_1","model":"claude-opus-5","content":"old","usage":{"input_tokens":9999}}}`,
		`{"type":"user","message":{"role":"user","content":"current"}}`,
		`{"type":"assistant","uuid":"a2","message":{"id":"msg_2","model":"claude-opus-5","content":"new","usage":{"input_tokens":100}}}`,
	)
	if billing := lastHookTurnBilling(path); billing == nil || billing.Tokens != 100 {
		t.Errorf("lastHookTurnBilling(previous turn) = %#v, want 100 tokens", billing)
	}

	path = writeHookTranscript(t,
		`{"type":"user","message":{"role":"user","content":"a question"}}`,
		`{"type":"assistant","uuid":"a1","message":{"id":"future","model":"some-future-model","content":"x","usage":{"input_tokens":500}}}`,
		`{"type":"assistant","uuid":"a2","message":{"id":"priced","model":"claude-opus-5","content":"y","usage":{"input_tokens":100}}}`,
	)
	if billing := lastHookTurnBilling(path); billing == nil || billing.Tokens != 100 {
		t.Errorf("lastHookTurnBilling(unpriced) = %#v, want only priced tokens", billing)
	}

	path = writeHookTranscript(t,
		`{"type":"user","message":{"role":"user","content":"q"}}`,
		`{"type":"assistant","uuid":"a1","message":{"role":"assistant","content":"no usage"}}`,
	)
	if billing := lastHookTurnBilling(path); billing != nil {
		t.Errorf("lastHookTurnBilling(no usage) = %#v, want nil", billing)
	}
}

func TestHookTranscriptTailDropsPartialLine(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "large.jsonl")
	prefix := strings.Repeat("x", hookTranscriptTailBytes)
	content := prefix + "\n" +
		`{"type":"user","message":{"role":"user","content":"q"}}` + "\n" +
		`{"type":"assistant","uuid":"a1","message":{"role":"assistant","content":"done"}}` + "\n" +
		"not-json"
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("os.WriteFile(%q) error = %v, want nil", path, err)
	}
	turn := lastHookAssistantTurn(path)
	if turn == nil || turn.UUID != "a1" || turn.Text != "done" {
		t.Errorf("lastHookAssistantTurn(large tail) = %#v, want complete lines only", turn)
	}
}

func writeHookTranscript(t *testing.T, lines ...string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "session.jsonl")
	if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o644); err != nil {
		t.Fatalf("os.WriteFile(%q) error = %v, want nil", path, err)
	}
	return path
}
