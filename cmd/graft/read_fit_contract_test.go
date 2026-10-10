package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/h0rn3t/Graft/internal/savings"
)

// writeOversizedFixture adds a class and a function too big for small budgets
// to the read fixture: Big holds 40 eight-line methods, and long calls alpha
// only on its last statement.
func writeOversizedFixture(t *testing.T, root string) {
	t.Helper()
	writeReadFixture(t, root)
	var class strings.Builder
	class.WriteString("export class Big {\n  private count = 0;\n")
	for i := range 40 {
		fmt.Fprintf(&class, "  method%d(x: number): number {\n", i)
		for j := range 5 {
			fmt.Fprintf(&class, "    this.count += x * %d;\n", i*10+j)
		}
		class.WriteString("    return this.count;\n  }\n")
	}
	class.WriteString("}\n")
	var long strings.Builder
	long.WriteString("import { alpha } from \"./one\";\nexport function long(): number {\n")
	for i := range 150 {
		fmt.Fprintf(&long, "  const v%d = %d;\n", i, i)
	}
	long.WriteString("  return alpha() + v149;\n}\n")
	for name, body := range map[string]string{"big.ts": class.String(), "long.ts": long.String()} {
		if err := os.WriteFile(filepath.Join(root, "src", name), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
}

func TestReadFitsOversizedDefinitions(t *testing.T) {
	root := t.TempDir()
	writeOversizedFixture(t, root)
	dir := filepath.Join(root, "graft")
	tests := []struct {
		name          string
		args          map[string]any
		want, notWant []string
	}{
		{
			name:    "class lists its members with signatures",
			args:    map[string]any{"symbol": "Big", "budget": float64(900)},
			want:    []string{"export class Big {", "⋮ L3-L10 Big.method0 · method0(x: number): number", "⋮ L315-L322 Big.method39 · ", "needs"},
			notWant: []string{"this.count += x * 394"},
		},
		{
			name:    "class falls back to member names",
			args:    map[string]any{"symbol": "Big", "budget": float64(400)},
			want:    []string{"export class Big {", "⋮ L3-L10 Big.method0", "Big.method39"},
			notWant: []string{"(x: number)"},
		},
		{
			name:    "class lists the members that fit and points at the file API",
			args:    map[string]any{"symbol": "Big", "budget": float64(200)},
			want:    []string{"Big.method0", "more members: graft_file_api src/big.ts"},
			notWant: []string{"Big.method39"},
		},
		{
			name:    "function shows its head and names what the rest calls",
			args:    map[string]any{"symbol": "long", "budget": float64(300)},
			want:    []string{"export function long(): number {", "  const v0 = 0;", "· calls alpha", "budget "},
			notWant: []string{"return alpha() + v149"},
		},
		{
			name:    "batch keeps whole definitions and fits the oversized one",
			args:    map[string]any{"symbol": "alpha", "also": []any{"Big"}, "budget": float64(900)},
			want:    []string{"return 1;", "⋮ L3-L10 Big.method0"},
			notWant: []string{"omitted"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := mcpCall(t.Context(), root, dir, "", "graft_read_symbol", tt.args)
			if got.isError {
				t.Fatalf("mcpCall(graft_read_symbol, %v) = error %q, want a fitted answer", tt.args, got.text)
			}
			for _, want := range tt.want {
				if !strings.Contains(got.text, want) {
					t.Errorf("mcpCall(graft_read_symbol, %v) = %q, want it to contain %q", tt.args, got.text, want)
				}
			}
			// Advice to read the file sends agents back to whole-file reads.
			for _, notWant := range append(tt.notWant, "range directly", "retry once") {
				if strings.Contains(got.text, notWant) {
					t.Errorf("mcpCall(graft_read_symbol, %v) = %q, want no %q", tt.args, got.text, notWant)
				}
			}
			budget := int(tt.args["budget"].(float64))
			if tokens := savings.Tokens(savings.Length(got.text)); tokens > budget {
				t.Errorf("mcpCall(graft_read_symbol, %v) = %d estimated tokens, want at most %d", tt.args, tokens, budget)
			}
		})
	}
}

func TestReadFitsOversizedDefinitionsCLI(t *testing.T) {
	root := t.TempDir()
	writeOversizedFixture(t, root)
	var out, diagnostic bytes.Buffer
	if status := run([]string{"read", "Big", root, "--budget", "200"}, &out, &diagnostic); status != 0 ||
		!strings.Contains(out.String(), "more members: graft skeleton src/big.ts") || !strings.Contains(out.String(), "--also") {
		t.Errorf("run(read Big --budget 200) = (%d, %q, %q), want exit 0, the CLI skeleton pointer and --also", status, out.String(), diagnostic.String())
	}
	out.Reset()
	diagnostic.Reset()
	if status := run([]string{"read", "long", root, "--budget", "300", "--json"}, &out, &diagnostic); status != 0 {
		t.Fatalf("run(read long --budget 300 --json) = (%d, %q), want exit 0", status, diagnostic.String())
	}
	var result struct{ Code, Note string }
	if err := json.Unmarshal(out.Bytes(), &result); err != nil {
		t.Fatalf("json.Unmarshal(read long --json) error = %v, want nil; output %q", err, out.String())
	}
	if !strings.Contains(result.Code, "· calls alpha") || !strings.Contains(result.Note, "--budget ") || strings.Contains(result.Code, "v149;") {
		t.Errorf("read(long --json) = %+v, want the head, a gap naming alpha and a note naming --budget", result)
	}
	needed := 0
	if _, after, ok := strings.Cut(result.Note, "--budget "); ok {
		needed, _ = strconv.Atoi(strings.TrimRight(after, ". "))
	}
	out.Reset()
	diagnostic.Reset()
	if status := run([]string{"read", "long", root, "--budget", strconv.Itoa(needed)}, &out, &diagnostic); status != 0 || !strings.Contains(out.String(), "return alpha() + v149;") {
		t.Errorf("run(read long --budget %d) = (%d, %q), want the whole definition at the budget the note names", needed, status, out.String())
	}
}
