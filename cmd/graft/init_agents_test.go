package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func runInitDryRun(t *testing.T, agents string) (int, string) {
	t.Helper()
	repo, home := t.TempDir(), t.TempDir()
	if err := os.MkdirAll(filepath.Join(repo, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("DO_NOT_TRACK", "1")
	var stdout, stderr bytes.Buffer
	args := []string{"init", repo, "--agents", agents, "--dry-run"}
	return runWithInput(args, strings.NewReader(""), &stdout, &stderr), stderr.String()
}

// TestInitAcceptsCommaSeparatedAgents checks that --agents reads a comma as a
// separator too, next to the space-separated values commander.js collects.
func TestInitAcceptsCommaSeparatedAgents(t *testing.T) {
	for _, agents := range []string{"copilot,cursor", " copilot , cursor "} {
		t.Run(agents, func(t *testing.T) {
			status, stderr := runInitDryRun(t, agents)
			if status != 0 {
				t.Fatalf("init --agents %q status = %d, want 0; stderr = %q", agents, status, stderr)
			}
			for _, want := range []string{"would write — this repo:", ".github/copilot-instructions.md", ".cursor/rules/graft.mdc"} {
				if !strings.Contains(stderr, want) {
					t.Errorf("init --agents %q = %q, want both agents planned, with %q", agents, stderr, want)
				}
			}
		})
	}
}

// TestInitRejectsAnUnknownAgentAmongKnownOnes checks that comma splitting does
// not weaken the unknown-id guard.
func TestInitRejectsAnUnknownAgentAmongKnownOnes(t *testing.T) {
	status, stderr := runInitDryRun(t, "cursor,nope")
	if status != 1 {
		t.Fatalf("init --agents cursor,nope status = %d, want 1; stderr = %q", status, stderr)
	}
	if !strings.Contains(stderr, "unknown agent id(s): nope") {
		t.Errorf("init --agents cursor,nope stderr = %q, want the unknown id reported", stderr)
	}
}
