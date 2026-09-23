package graph

import (
	"fmt"
	"path"
	"strings"

	"github.com/NanoNets/context-graph-engine/internal/sourcefiles"
)

type sqlToken struct {
	text       string
	start, end int
	kind       byte
}

func lexSQL(source string) ([]sqlToken, error) {
	tokens := make([]sqlToken, 0)
	for i := 0; i < len(source); {
		if source[i] <= ' ' {
			i++
			continue
		}
		if strings.HasPrefix(source[i:], "--") {
			if end := strings.IndexByte(source[i:], '\n'); end >= 0 {
				i += end + 1
			} else {
				i = len(source)
			}
			continue
		}
		if strings.HasPrefix(source[i:], "/*") {
			depth := 1
			start := i
			i += 2
			for i < len(source) && depth > 0 {
				switch {
				case strings.HasPrefix(source[i:], "/*"):
					depth++
					i += 2
				case strings.HasPrefix(source[i:], "*/"):
					depth--
					i += 2
				default:
					i++
				}
			}
			if depth != 0 {
				return nil, fmt.Errorf("unterminated SQL comment at byte %d", start)
			}
			continue
		}
		start := i
		if source[i] == '\'' || source[i] == '"' {
			quote := source[i]
			i++
			closed := false
			for i < len(source) {
				if source[i] == quote {
					i++
					if i < len(source) && source[i] == quote {
						i++
						continue
					}
					closed = true
					break
				}
				i++
			}
			if !closed {
				return nil, fmt.Errorf("unterminated SQL quote at byte %d", start)
			}
			kind := byte('s')
			if quote == '"' {
				kind = 'i'
			}
			tokens = append(tokens, sqlToken{source[start:i], start, i, kind})
			continue
		}
		if source[i] == '$' {
			end := i + 1
			for end < len(source) && (source[end] == '_' || source[end] >= 'a' && source[end] <= 'z' || source[end] >= 'A' && source[end] <= 'Z' || source[end] >= '0' && source[end] <= '9') {
				end++
			}
			if end < len(source) && source[end] == '$' {
				delimiter := source[i : end+1]
				closeAt := strings.Index(source[end+1:], delimiter)
				if closeAt < 0 {
					return nil, fmt.Errorf("unterminated SQL dollar quote at byte %d", start)
				}
				i = end + 1 + closeAt + len(delimiter)
				tokens = append(tokens, sqlToken{source[start:i], start, i, 'd'})
				continue
			}
		}
		if source[i] == '_' || source[i] >= 'a' && source[i] <= 'z' || source[i] >= 'A' && source[i] <= 'Z' {
			i++
			for i < len(source) && (source[i] == '_' || source[i] >= 'a' && source[i] <= 'z' || source[i] >= 'A' && source[i] <= 'Z' || source[i] >= '0' && source[i] <= '9') {
				i++
			}
			tokens = append(tokens, sqlToken{source[start:i], start, i, 'i'})
			continue
		}
		i++
		tokens = append(tokens, sqlToken{source[start:i], start, i, 'p'})
	}
	return tokens, nil
}

func sqlName(tokens []sqlToken, at int) (string, int) {
	if at >= len(tokens) || tokens[at].kind != 'i' {
		return "", at
	}
	part := tokens[at].text
	if strings.HasPrefix(part, "\"") {
		part = strings.ReplaceAll(part[1:len(part)-1], "\"\"", "\"")
	} else {
		part = strings.ToLower(part)
	}
	var name strings.Builder
	name.WriteString(part)
	at++
	for at+1 < len(tokens) && tokens[at].text == "." && tokens[at+1].kind == 'i' {
		part = tokens[at+1].text
		if strings.HasPrefix(part, "\"") {
			part = strings.ReplaceAll(part[1:len(part)-1], "\"\"", "\"")
		} else {
			part = strings.ToLower(part)
		}
		name.WriteString("." + part)
		at += 2
	}
	return name.String(), at
}

func extractSQL(rel, source string) (extractResult, error) {
	tokens, err := lexSQL(source)
	if err != nil {
		return extractResult{}, fmt.Errorf("parse %q: %w", rel, err)
	}
	chars := len(source)
	nodes := []NodeV1{{
		ID: rel, Name: path.Base(rel), Kind: "file", Path: rel,
		Span:     fmt.Sprintf("L1-L%d", strings.Count(source, "\n")+1),
		Exported: true, Origin: "sql", BodyHash: sourcefiles.Hash(source),
		Chars: &chars, BodyText: new(searchBody(source, maxFileBodyChars)), SummaryState: "pending",
	}}
	edges := make([]rawEdge, 0)
	minted := map[string]struct{}{rel: {}}
	for start := 0; start < len(tokens); {
		end, depth := start, 0
		for ; end < len(tokens); end++ {
			switch tokens[end].text {
			case "(":
				depth++
			case ")":
				depth--
				if depth < 0 {
					return extractResult{}, fmt.Errorf("parse %q: unmatched SQL parenthesis at byte %d", rel, tokens[end].start)
				}
			case ";":
				if depth == 0 {
					end++
					goto statement
				}
			}
		}
	statement:
		if depth != 0 {
			return extractResult{}, fmt.Errorf("parse %q: unclosed SQL parenthesis at byte %d", rel, tokens[start].start)
		}
		stmt := tokens[start:end]
		start = end
		if len(stmt) == 0 || stmt[0].text == ";" {
			continue
		}
		sourceID := rel
		if strings.EqualFold(stmt[0].text, "CREATE") {
			at := 1
			if at+1 < len(stmt) && strings.EqualFold(stmt[at].text, "OR") && strings.EqualFold(stmt[at+1].text, "REPLACE") {
				at += 2
			}
			if at < len(stmt) && (strings.EqualFold(stmt[at].text, "TEMP") || strings.EqualFold(stmt[at].text, "TEMPORARY") || strings.EqualFold(stmt[at].text, "UNLOGGED") || strings.EqualFold(stmt[at].text, "MATERIALIZED")) {
				at++
			}
			if at < len(stmt) {
				kind := Kind("")
				switch strings.ToUpper(stmt[at].text) {
				case "TABLE", "VIEW", "TYPE":
					kind = "type"
				case "FUNCTION", "PROCEDURE":
					kind = "function"
				}
				if kind != "" {
					at++
					if at+2 < len(stmt) && strings.EqualFold(stmt[at].text, "IF") && strings.EqualFold(stmt[at+1].text, "NOT") && strings.EqualFold(stmt[at+2].text, "EXISTS") {
						at += 3
					}
					name, _ := sqlName(stmt, at)
					if name == "" {
						return extractResult{}, fmt.Errorf("parse %q: CREATE %s has no name at byte %d", rel, stmt[at-1].text, stmt[0].start)
					}
					base := rel + "#" + name
					sourceID = base
					for suffix := 2; ; suffix++ {
						if _, taken := minted[sourceID]; !taken {
							break
						}
						sourceID = fmt.Sprintf("%s~%d", base, suffix)
					}
					minted[sourceID] = struct{}{}
					body := source[stmt[0].start:stmt[len(stmt)-1].end]
					header := body
					if cut := strings.IndexAny(header, "(\n"); cut >= 0 {
						header = header[:cut]
					}
					if cut := strings.Index(strings.ToUpper(header), " AS "); cut >= 0 {
						header = header[:cut]
					}
					header = strings.TrimSpace(header)
					firstLine := strings.Count(source[:stmt[0].start], "\n") + 1
					lastLine := strings.Count(source[:stmt[len(stmt)-1].end], "\n") + 1
					nodes = append(nodes, NodeV1{
						ID: sourceID, Name: name, Kind: kind, Path: rel,
						Span: fmt.Sprintf("L%d-L%d", firstLine, lastLine), Signature: &header,
						Exported: true, Origin: "sql", BodyHash: sourcefiles.Hash(body),
						BodyText: new(searchBody(body, maxBodyChars)), SummaryState: "pending",
					})
				}
			}
		}
		var addReferences func([]sqlToken)
		addReferences = func(items []sqlToken) {
			for i, token := range items {
				if token.kind == 'd' {
					open := strings.IndexByte(token.text[1:], '$') + 2
					if inner, err := lexSQL(token.text[open : len(token.text)-open]); err == nil {
						addReferences(inner)
					}
					continue
				}
				if strings.EqualFold(token.text, "REFERENCES") || strings.EqualFold(token.text, "FROM") || strings.EqualFold(token.text, "JOIN") {
					if name, _ := sqlName(items, i+1); name != "" {
						edges = append(edges, rawEdge{source: sourceID, relation: "references", name: name, file: rel})
					}
				}
			}
		}
		addReferences(stmt)
	}
	return extractResult{language: "sql", nodes: nodes, rawEdges: edges}, nil
}
