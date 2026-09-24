package graph

import (
	"fmt"
	"path"
	"slices"
	"strings"

	"github.com/h0rn3t/Graft/internal/savings"
	"github.com/h0rn3t/Graft/internal/sourcefiles"
)

type sqlToken struct {
	text       string
	start, end int
	kind       byte
	// later marks a token of a template branch after the first one: it
	// counts for references but not for parentheses or statement bounds.
	later bool
}

// lexSQL tokenizes source. A backslash escapes the next character inside a
// single-quoted string, as MySQL dumps and PostgreSQL escape strings write it;
// when that reading leaves a quote open, the standard reading, where a
// backslash is literal, is tried before giving up.
//
// Scripts written for a preprocessor lex as the SQL they expand to: a psql
// meta-command runs to the end of its line and is dropped, a psql variable
// (:name, :'name', :"name") and a Jinja or Django expression ({{ ... }}) are
// one 'v' token, and every template {% if %} branch after the first is marked
// later, since each branch alone is what balances its parentheses.
func lexSQL(source string) ([]sqlToken, error) {
	tokens, err := lexSQLMode(source, true)
	if err == nil {
		return tokens, nil
	}
	if standard, standardErr := lexSQLMode(source, false); standardErr == nil {
		return standard, nil
	}
	return nil, err
}

func lexSQLMode(source string, backslashEscapes bool) ([]sqlToken, error) {
	tokens := make([]sqlToken, 0)
	// pastFirst holds one entry per open template {% if %} chain, true once
	// the chain has left its first branch.
	var pastFirst []bool
	emit := func(start, end int, kind byte) {
		tokens = append(tokens, sqlToken{text: source[start:end], start: start, end: end, kind: kind, later: slices.Contains(pastFirst, true)})
	}
	for i := 0; i < len(source); {
		if source[i] <= ' ' {
			i++
			continue
		}
		if source[i] == '\\' {
			if end := strings.IndexByte(source[i:], '\n'); end >= 0 {
				i += end + 1
			} else {
				i = len(source)
			}
			continue
		}
		if source[i] == '{' && i+1 < len(source) && strings.IndexByte("{%#", source[i+1]) >= 0 {
			closer := "}}"
			if source[i+1] != '{' {
				closer = source[i+1:i+2] + "}"
			}
			if closeAt := strings.Index(source[i+2:], closer); closeAt >= 0 {
				start, inner := i, source[i+2:i+2+closeAt]
				i += 2 + closeAt + len(closer)
				switch source[start+1] {
				case '{':
					emit(start, i, 'v')
				case '%':
					tag := ""
					if fields := strings.Fields(strings.Trim(inner, "-+")); len(fields) > 0 {
						tag = fields[0]
					}
					switch {
					case tag == "if":
						pastFirst = append(pastFirst, false)
					case (tag == "elif" || tag == "else") && len(pastFirst) > 0:
						pastFirst[len(pastFirst)-1] = true
					case tag == "endif" && len(pastFirst) > 0:
						pastFirst = pastFirst[:len(pastFirst)-1]
					}
				}
				continue
			}
		}
		// A :: cast never starts a variable, nor does := assignment.
		if source[i] == ':' && i+1 < len(source) && (i == 0 || source[i-1] != ':') {
			end := sqlIdentEnd(source, i+1)
			if quote := source[i+1]; end == i+1 && (quote == '\'' || quote == '"') {
				if closeAt := sqlIdentEnd(source, i+2); closeAt > i+2 && closeAt < len(source) && source[closeAt] == quote {
					end = closeAt + 1
				}
			}
			if end > i+1 {
				emit(i, end, 'v')
				i = end
				continue
			}
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
				if backslashEscapes && quote == '\'' && source[i] == '\\' {
					i += 2
					continue
				}
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
			emit(start, i, kind)
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
				emit(start, i, 'd')
				continue
			}
		}
		end := sqlIdentEnd(source, i)
		// @name@ is a build-time placeholder, such as TimescaleDB's
		// @extschema@, that stands where a schema name will be.
		if placeholder := sqlIdentEnd(source, i+1); source[i] == '@' && placeholder > i+1 && placeholder < len(source) && source[placeholder] == '@' {
			end = placeholder + 1
		}
		if end > i {
			i = end
			emit(start, i, 'i')
			continue
		}
		i++
		emit(start, i, 'p')
	}
	return tokens, nil
}

// sqlIdentEnd returns the end of the unquoted identifier starting at at, or
// at itself when none starts there.
func sqlIdentEnd(source string, at int) int {
	if at >= len(source) || source[at] != '_' && (source[at]|0x20 < 'a' || source[at]|0x20 > 'z') {
		return at
	}
	end := at + 1
	for end < len(source) && (source[end] == '_' || source[end]|0x20 >= 'a' && source[end]|0x20 <= 'z' || source[end] >= '0' && source[end] <= '9') {
		end++
	}
	return end
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

// extractSQL indexes one SQL file. Source the lexer cannot follow degrades to
// the file node alone, and a statement the splitter cannot follow is dropped on
// its own, so one unusual dialect file never blocks the rest of the graph.
func extractSQL(rel, source string) (extractResult, error) {
	chars := savings.Length(source)
	newlines := make([]int, 0, strings.Count(source, "\n"))
	for offset := range len(source) {
		if source[offset] == '\n' {
			newlines = append(newlines, offset)
		}
	}
	file := NodeV1{
		ID: rel, Name: path.Base(rel), Kind: "file", Path: rel,
		Span:     fmt.Sprintf("L1-L%d", len(newlines)+1),
		Exported: true, Origin: "sql", BodyHash: sourcefiles.Hash(source),
		Chars: &chars, BodyText: new(searchBody(source, maxFileBodyChars)), SummaryState: "pending",
	}
	symbols, edges, dropped, err := parseSQL(rel, source, newlines)
	if err != nil {
		return extractResult{
			language: "sql", nodes: []NodeV1{file}, rawEdges: make([]rawEdge, 0),
			limitation: fmt.Sprintf("%s: SQL statements not indexed (%v)", rel, err),
		}, nil
	}
	result := extractResult{language: "sql", nodes: append([]NodeV1{file}, symbols...), rawEdges: edges}
	if dropped != "" {
		result.limitation = rel + ": " + dropped
	}
	return result, nil
}

// parseSQL returns the definitions and references of source's statements, and
// a note counting the malformed statements it dropped; newlines holds the byte
// offset of every line break in source.
func parseSQL(rel, source string, newlines []int) ([]NodeV1, []rawEdge, string, error) {
	tokens, err := lexSQL(source)
	if err != nil {
		return nil, nil, "", err
	}
	lineOf := func(offset int) int {
		before, _ := slices.BinarySearch(newlines, offset)
		return before + 1
	}
	nodes := make([]NodeV1, 0)
	edges := make([]rawEdge, 0)
	minted := map[string]struct{}{rel: {}}
	statements, dropped := 0, 0
	var firstDropped error
	drop := func(reason error) {
		dropped++
		if firstDropped == nil {
			firstDropped = reason
		}
	}
	for start := 0; start < len(tokens); {
		end, depth := start, 0
		var malformed error
	scan:
		for ; end < len(tokens); end++ {
			if tokens[end].later {
				continue
			}
			switch tokens[end].text {
			case "(":
				depth++
			case ")":
				depth--
				if depth < 0 {
					malformed = fmt.Errorf("unmatched SQL parenthesis at byte %d", tokens[end].start)
					// Resynchronize at the next semicolon.
					for end < len(tokens) && (tokens[end].later || tokens[end].text != ";") {
						end++
					}
					end = min(end+1, len(tokens))
					break scan
				}
			case ";":
				// A semicolon inside parentheses ends a statement that never
				// closed them rather than joining it to the next one.
				if depth > 0 {
					malformed = fmt.Errorf("unclosed SQL parenthesis at byte %d", tokens[start].start)
				}
				end++
				break scan
			}
		}
		if malformed == nil && depth > 0 {
			malformed = fmt.Errorf("unclosed SQL parenthesis at byte %d", tokens[start].start)
		}
		stmt := tokens[start:end]
		start = end
		if len(stmt) == 0 || !stmt[0].later && stmt[0].text == ";" {
			continue
		}
		statements++
		if malformed != nil {
			drop(malformed)
			continue
		}
		head := stmt
		if slices.ContainsFunc(stmt, func(token sqlToken) bool { return token.later }) {
			head = slices.DeleteFunc(slices.Clone(stmt), func(token sqlToken) bool { return token.later })
		}
		sourceID := rel
		if len(head) > 0 && strings.EqualFold(head[0].text, "CREATE") {
			at := 1
			if at+1 < len(head) && strings.EqualFold(head[at].text, "OR") && strings.EqualFold(head[at+1].text, "REPLACE") {
				at += 2
			}
			if at < len(head) && (strings.EqualFold(head[at].text, "TEMP") || strings.EqualFold(head[at].text, "TEMPORARY") || strings.EqualFold(head[at].text, "UNLOGGED") || strings.EqualFold(head[at].text, "MATERIALIZED")) {
				at++
			}
			if at < len(head) {
				kind := Kind("")
				switch strings.ToUpper(head[at].text) {
				case "TABLE", "VIEW", "TYPE":
					kind = "type"
				case "FUNCTION", "PROCEDURE":
					kind = "function"
				}
				if kind != "" {
					at++
					if at+2 < len(head) && strings.EqualFold(head[at].text, "IF") && strings.EqualFold(head[at+1].text, "NOT") && strings.EqualFold(head[at+2].text, "EXISTS") {
						at += 3
					}
					name, _ := sqlName(head, at)
					// A name a preprocessor fills in (:name, {{ name }}) defines
					// nothing to point at, but the statement's references count.
					dynamic := at < len(head) && head[at].kind == 'v'
					if name == "" && !dynamic {
						drop(fmt.Errorf("CREATE %s has no name at byte %d", head[at-1].text, head[0].start))
						continue
					}
					if name != "" {
						base := rel + "#" + name
						sourceID = base
						for suffix := 2; ; suffix++ {
							if _, taken := minted[sourceID]; !taken {
								break
							}
							sourceID = fmt.Sprintf("%s~%d", base, suffix)
						}
						minted[sourceID] = struct{}{}
						body := source[head[0].start:head[len(head)-1].end]
						header := body
						if cut := strings.IndexAny(header, "(\n"); cut >= 0 {
							header = header[:cut]
						}
						if cut := strings.Index(strings.ToUpper(header), " AS "); cut >= 0 {
							header = header[:cut]
						}
						header = strings.TrimSpace(header)
						nodes = append(nodes, NodeV1{
							ID: sourceID, Name: name, Kind: kind, Path: rel,
							Span: fmt.Sprintf("L%d-L%d", lineOf(head[0].start), lineOf(head[len(head)-1].end)), Signature: &header,
							Exported: true, Origin: "sql", BodyHash: sourcefiles.Hash(body),
							BodyText: new(searchBody(body, maxBodyChars)), SummaryState: "pending",
						})
					}
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
	if dropped == 0 {
		return nodes, edges, "", nil
	}
	return nodes, edges, fmt.Sprintf("%d of %d SQL statements not indexed (%v)", dropped, statements, firstDropped), nil
}
