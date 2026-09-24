package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRunAskIgnoresLegacyConceptCards(t *testing.T) {
	t.Setenv("GRAFT_DIR", "")
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "src"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "src", "app.ts"), []byte("export function run() {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	var stdout, stderr bytes.Buffer
	if status := run([]string{"build", root}, &stdout, &stderr); status != 0 {
		t.Fatalf("run(build %q) status = %d, want 0; stderr = %q", root, status, stderr.String())
	}
	concept := "---\nname: Legacy Concept\nslug: legacy\n---\nuniquelegacy prose only\n"
	if err := os.WriteFile(filepath.Join(root, "graft", "legacy.md"), []byte(concept), 0o644); err != nil {
		t.Fatal(err)
	}
	stdout.Reset()
	stderr.Reset()
	args := []string{"ask", "uniquelegacy", root, "--json"}
	if status := run(args, &stdout, &stderr); status != 0 {
		t.Fatalf("run(%v) status = %d, want 0; stdout = %q; stderr = %q", args, status, stdout.String(), stderr.String())
	}
	if strings.Contains(stdout.String(), `"kind": "concept"`) {
		t.Errorf("run(%v) stdout = %q, want legacy concept ignored", args, stdout.String())
	}
}
