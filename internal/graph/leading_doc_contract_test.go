package graph

import (
	"maps"
	"strings"
	"testing"
)

func TestLeadingDocContract(t *testing.T) {
	tests := []struct {
		name   string
		source string
		line   int // 1-based first line of the definition's span
		prefix string
		want   string
	}{
		{name: "Go doc comment", source: "// Parse reads a config file.\n// It returns an error for unknown keys.\nfunc Parse() {}", line: 3, prefix: "//", want: "Parse reads a config file.\nIt returns an error for unknown keys."},
		{name: "JSDoc block", source: "/**\n * Greets a user.\n *\n * @param name who\n */\nfunction greet(name) {}", line: 6, prefix: "//", want: "Greets a user.\n\n@param name who"},
		{name: "one-line block", source: "/* Adds numbers. */\nint add(void);", line: 2, prefix: "//", want: "Adds numbers."},
		{name: "Rust doc lines", source: "/// Line one.\n/// Line two.\nfn f() {}", line: 3, prefix: "//", want: "Line one.\nLine two."},
		{name: "indentation after the marker space", source: "// Example:\n//\tcode()\nfunc F() {}", line: 3, prefix: "//", want: "Example:\n\tcode()"},
		{name: "empty edge lines trimmed", source: "//\n// Body.\n//\nfunc F() {}", line: 4, prefix: "//", want: "Body."},
		{name: "indented method comment", source: "class K {\n  /** Runs. */\n  run() {}\n}", line: 3, prefix: "//", want: "Runs."},
		{name: "blank line separates", source: "// Orphan.\n\nfunc F() {}", line: 3, prefix: "//", want: ""},
		{name: "first line of file", source: "func F() {}", line: 1, prefix: "//", want: ""},
		{name: "Go directive dropped", source: "// Hot path.\n//go:noinline\nfunc F() {}", line: 3, prefix: "//", want: "Hot path."},
		{name: "nolint directive only", source: "//nolint:gocyclo\nfunc F() {}", line: 2, prefix: "//", want: ""},
		{name: "eslint block directive", source: "/* eslint-disable no-console */\nfunction f() {}", line: 2, prefix: "//", want: ""},
		{name: "Rust attribute skipped", source: "/// Doc for f.\n#[inline]\npub fn f() {}", line: 3, prefix: "//", want: "Doc for f."},
		{name: "TypeScript decorator skipped", source: "/** Doc for run. */\n@log\nrun() {}", line: 3, prefix: "//", want: "Doc for run."},
		{name: "Python decorator skipped", source: "# Loads.\n@cache\ndef load():\n    pass", line: 3, prefix: "#", want: "Loads."},
		{name: "C preprocessor line", source: "#define LIMIT 4\nint f(void);", line: 2, prefix: "//", want: ""},
		{name: "SPDX header", source: "// SPDX-License-Identifier: MIT\nfunc F() {}", line: 2, prefix: "//", want: ""},
		{name: "copyright block", source: "/* Copyright 2026 Acme */\nint f(void);", line: 2, prefix: "//", want: ""},
		{name: "Python comment", source: "# Loads the cache.\ndef load():\n    pass", line: 2, prefix: "#", want: "Loads the cache."},
		{name: "Python bracket comment is not an attribute", source: "#[note] odd\ndef f():\n    pass", line: 2, prefix: "#", want: "[note] odd"},
		{name: "Python shebang", source: "#!/usr/bin/env python\ndef main():\n    pass", line: 2, prefix: "#", want: ""},
		{name: "Python has no block comments", source: "/* not a comment */\ndef f():\n    pass", line: 2, prefix: "#", want: ""},
		{name: "SQL line comment", source: "-- Active users.\nCREATE VIEW v AS SELECT 1;", line: 2, prefix: "--", want: "Active users."},
		{name: "SQL block comment", source: "/* Orders. */\nCREATE TABLE o (id int);", line: 2, prefix: "--", want: "Orders."},
		{name: "unterminated block", source: "  more text */\nfunc F() {}", line: 2, prefix: "//", want: ""},
		{name: "code with trailing line comment", source: "x := 1 // note\nfunc F() {}", line: 2, prefix: "//", want: ""},
		{name: "code with trailing block comment", source: "x := 1 /* note */\nfunc F() {}", line: 2, prefix: "//", want: ""},
		{name: "capped length", source: "// " + strings.Repeat("x", 3000) + "\nfunc F() {}", line: 2, prefix: "//", want: strings.Repeat("x", maxDocChars)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := leadingDoc(strings.Split(tt.source, "\n"), tt.line-1, tt.prefix); got != tt.want {
				t.Errorf("leadingDoc(%q, %d, %q) = %q, want %q", tt.source, tt.line-1, tt.prefix, got, tt.want)
			}
		})
	}
}

func TestExtractFileLeadingDocsContract(t *testing.T) {
	tests := []struct {
		path   string
		source string
		want   map[string]string // symbol name → summary; undocumented symbols are absent
	}{
		{path: "pkg/a.go", source: "// Package a is documented on the file only.\npackage a\n\n// Parse reads a config file.\nfunc Parse() {}\n\nfunc bare() {}\n\n// Config holds settings.\ntype Config struct{}\n", want: map[string]string{"Parse": "Parse reads a config file.", "Config": "Config holds settings."}},
		{path: "src/a.ts", source: "/** Greets a user. */\nexport function greet(name: string) {}\nclass K {\n  /** Runs the job. */\n  @log\n  run() {}\n}\n", want: map[string]string{"greet": "Greets a user.", "run": "Runs the job."}},
		{path: "src/C.java", source: "/** A counter. */\nclass C {\n  /** Adds one. */\n  @Override\n  public void inc() {}\n}\n", want: map[string]string{"C": "A counter.", "inc": "Adds one."}},
		{path: "src/a.rs", source: "/// Doc for f.\n#[inline]\npub fn f() {}\n\n/// Doc for S.\n#[derive(Debug)]\npub struct S {}\n", want: map[string]string{"f": "Doc for f.", "S": "Doc for S."}},
		{path: "src/e.cpp", source: "#include <vector>\nint top(void);\n// Doc for tpl.\ntemplate <typename T>\nT tpl(T x) { return x; }\n", want: map[string]string{"tpl": "Doc for tpl."}},
		{path: "src/b.py", source: "# Loads the cache.\n@cache\ndef load():\n    \"\"\"Docstring stays in the body.\"\"\"\n    pass\n\ndef bare():\n    \"\"\"Only a docstring.\"\"\"\n", want: map[string]string{"load": "Loads the cache."}},
		{path: "db/schema.sql", source: "-- Registered accounts.\nCREATE TABLE users (id int);\n\n/* Active accounts. */\nCREATE VIEW active AS SELECT id FROM users;\n", want: map[string]string{"users": "Registered accounts.", "active": "Active accounts."}},
	}
	for _, tt := range tests {
		t.Run(tt.path, func(t *testing.T) {
			t.Parallel()
			result, err := extractFile(tt.path, tt.source)
			if err != nil {
				t.Fatalf("extractFile(%q, source) error = %v, want nil", tt.path, err)
			}
			got := map[string]string{}
			for _, node := range result.nodes {
				if node.Summary == nil {
					continue
				}
				if node.Kind == "file" {
					t.Errorf("extractFile(%q, source) file node summary = %q, want none", tt.path, *node.Summary)
					continue
				}
				got[node.Name] = *node.Summary
			}
			if !maps.Equal(got, tt.want) {
				t.Errorf("extractFile(%q, source) summaries = %v, want %v", tt.path, got, tt.want)
			}
		})
	}
}
