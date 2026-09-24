package graph

import (
	"path"
	"slices"
	"strings"
	"unicode/utf16"
)

// maxDocChars caps a symbol's documentation, in UTF-16 units like the body caps.
const maxDocChars = 2000

// attachLeadingDocs sets each symbol node's summary to the documentation
// comment directly above its span. File nodes keep no summary.
func attachLeadingDocs(rel, source string, nodes []NodeV1) {
	prefix := "//"
	switch strings.ToLower(path.Ext(rel)) {
	case ".py", ".pyi":
		prefix = "#"
	case ".sql":
		prefix = "--"
	}
	lines := strings.Split(source, "\n")
	for i := range nodes {
		first, _, ok := parseLineSpan(nodes[i].Span)
		if nodes[i].Kind == "file" || !ok {
			continue
		}
		if doc := leadingDoc(lines, first-1, prefix); doc != "" {
			nodes[i].Summary = &doc
		}
	}
}

// leadingDoc returns the comment block that ends directly above lines[row],
// without its markers, or "" when there is none. prefix is the language's
// line-comment marker; Python ("#") is the only one without block comments.
func leadingDoc(lines []string, row int, prefix string) string {
	i := row - 1
	// Rust attributes and decorators sit between the comment and the span.
	for i >= 0 {
		line := strings.TrimSpace(lines[i])
		if !strings.HasPrefix(line, "@") && (prefix != "//" || !strings.HasPrefix(line, "#[") && !strings.HasPrefix(line, "#![")) {
			break
		}
		i--
	}
	var doc []string // bottom-up
scan:
	for ; i >= 0; i-- {
		line := strings.TrimSpace(lines[i])
		switch {
		case prefix != "#" && strings.HasSuffix(line, "*/"):
			start := i
			for start >= 0 && !strings.Contains(lines[start], "/*") {
				start--
			}
			if start < 0 {
				return ""
			}
			if !strings.HasPrefix(strings.TrimSpace(lines[start]), "/*") {
				break scan // code before the comment on its opening line
			}
			for j := i; j >= start; j-- {
				if text := strings.TrimSpace(lines[j]); !isDocDirective(text) {
					doc = append(doc, stripDocMarkers(text, prefix, true))
				}
			}
			i = start
		case strings.HasPrefix(line, prefix):
			if !isDocDirective(line) {
				doc = append(doc, stripDocMarkers(line, prefix, false))
			}
		default:
			break scan
		}
	}
	slices.Reverse(doc)
	text := strings.Trim(strings.Join(doc, "\n"), "\n")
	lower := strings.ToLower(text)
	if strings.Contains(lower, "copyright") || strings.Contains(lower, "spdx-license-identifier") {
		return ""
	}
	units := 0
	for index, r := range text {
		units += utf16.RuneLen(r)
		if units > maxDocChars {
			return text[:index]
		}
	}
	return text
}

// isDocDirective reports whether a trimmed comment line addresses a compiler,
// linter or type checker rather than a reader.
func isDocDirective(line string) bool {
	for _, directive := range []string{"//go:", "//nolint", "//lint:", "// +build", "#!", "# noqa", "# type:", "# pylint:"} {
		if strings.HasPrefix(line, directive) {
			return true
		}
	}
	for _, marker := range []string{"eslint-", "@ts-", "prettier-ignore"} {
		if strings.Contains(line, marker) {
			return true
		}
	}
	return false
}

// stripDocMarkers removes a trimmed comment line's markers and one space after them.
func stripDocMarkers(line, prefix string, block bool) string {
	text := line
	if block {
		text = strings.TrimSuffix(text, "*/")
		if rest, ok := strings.CutPrefix(text, "/*"); ok {
			text = rest
		}
		text = strings.TrimPrefix(text, "*")
	} else {
		text = strings.TrimPrefix(text, prefix)
		if prefix == "//" {
			// Rust's /// and //! doc comments.
			text = strings.TrimPrefix(strings.TrimPrefix(text, "/"), "!")
		}
	}
	return strings.TrimRight(strings.TrimPrefix(text, " "), " \t")
}
