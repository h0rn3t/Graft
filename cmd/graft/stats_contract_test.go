package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestStatsContract(t *testing.T) {
	t.Setenv("GRAFT_DIR", "")
	root := t.TempDir()
	cases := []struct {
		name string
		args []string
		want string
	}{
		{"empty text", []string{"stats", root}, "graft stats: no session recorded yet — use graft in an agent session, then look again.\n"},
		{"empty json", []string{"stats", root, "--json"}, "null\n"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			if got := run(tc.args, &stdout, &stderr); got != 0 || stdout.String() != tc.want || stderr.Len() != 0 {
				t.Errorf("run(%v) = (%d, %q, %q), want (0, %q, empty)", tc.args, got, stdout.String(), stderr.String(), tc.want)
			}
		})
	}
	sessions := filepath.Join(root, "graft", ".cache", "session")
	if err := os.MkdirAll(sessions, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sessions, "recent.json"), []byte(`{"lastQuery":"auth","perAgentQuery":{},"graftReads":3,"sourceReads":1,"savedTokens":1200,"inputCostMicros":25000000,"inputTokensBilled":10000}`), 0o644); err != nil {
		t.Fatal(err)
	}
	var stdout, stderr bytes.Buffer
	if got := run([]string{"stats", root}, &stdout, &stderr); got != 0 || stderr.Len() != 0 {
		t.Fatalf("run(stats %q) = (%d, %q, %q), want success", root, got, stdout.String(), stderr.String())
	}
	for _, want := range []string{"session recent", "graft reads:   3", "source reads:  1", "75% graft", "tokens saved:  ~1,200", "value saved:   ~$3.00", "last query:    auth"} {
		if !strings.Contains(stdout.String(), want) {
			t.Errorf("run(stats %q) stdout = %q, want %q", root, stdout.String(), want)
		}
	}
	stdout.Reset()
	if got := run([]string{"stats", root, "--json"}, &stdout, &stderr); got != 0 || !strings.Contains(stdout.String(), `"id": "recent"`) {
		t.Errorf("run(stats %q --json) = (%d, %q, %q), want session JSON", root, got, stdout.String(), stderr.String())
	}
}
