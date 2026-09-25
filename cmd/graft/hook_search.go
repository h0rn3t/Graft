package main

import (
	"cmp"
	jsonv2 "encoding/json/v2"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// hookSearchNudgeLimit caps how often one session is told that a raw search
// had a graft equivalent: the first note carries the replacement call, later
// ones would only repeat it.
const hookSearchNudgeLimit = 1

// hookSearch is a raw code search restated as graft_find_all arguments.
type hookSearch struct {
	Pattern    string `json:"pattern"`
	IgnoreCase bool   `json:"ignore_case,omitzero"`
	Fixed      bool   `json:"fixed,omitzero"`
	In         string `json:"in,omitempty"`
}

// hookSearchNudge returns the note pointing a raw recursive search over this
// repo's indexed code at the graft_find_all call that answers it, or "" when
// the call was no such search or the session has had its share of notes.
func hookSearchNudge(input hookInput, root string) string {
	if !mcpGraphAvailable(hookContextDir(root)) {
		return ""
	}
	cwd := cmp.Or(input.string("cwd"), root)
	toolInput := input.object("tool_input")
	var search hookSearch
	ok := false
	switch input.string("tool_name") {
	case "Grep":
		pattern, _ := toolInput["pattern"].(string)
		path, _ := toolInput["path"].(string)
		var paths []string
		if path != "" {
			paths = []string{path}
		}
		search, ok = hookSearchScope(root, cwd, hookSearch{Pattern: pattern, IgnoreCase: toolInput["-i"] == true}, paths)
	case "Bash":
		command, _ := toolInput["command"].(string)
		search, ok = parseHookSearchCommand(root, cwd, command)
	}
	if !ok || search.Pattern == "" {
		return ""
	}
	allowed := false
	_ = updateHookSession(root, hookSessionID(input), func(session *sessionState) bool {
		if session.SearchNudges >= hookSearchNudgeLimit {
			return false
		}
		session.SearchNudges++
		allowed = true
		return true
	})
	args, err := jsonv2.Marshal(search)
	if !allowed || err != nil {
		return ""
	}
	return fmt.Sprintf("[graft] That search ran over code graft has indexed. Next time call graft_find_all %s (CLI: graft grep): "+
		"the same regex over every indexed file, each hit grouped by its enclosing symbol with file:line and ranked by coupling, "+
		"in fewer tokens than raw grep output. For how or where something works, use graft_find_code; for who calls a symbol, "+
		"graft_trace_calls. Raw grep stays right for files graft does not index.", args)
}

// hookSearchScope narrows search to the one directory paths name, resolved
// against cwd, and refuses a search that reaches outside root, into graft's
// own cards, or into a single file.
func hookSearchScope(root, cwd string, search hookSearch, paths []string) (hookSearch, bool) {
	if len(paths) == 0 {
		paths = []string{"."}
	}
	for _, path := range paths {
		if !filepath.IsAbs(path) {
			path = filepath.Join(cwd, path)
		}
		rel, err := filepath.Rel(root, path)
		if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			return hookSearch{}, false
		}
		if cards, err := filepath.Rel(hookContextDir(root), path); err == nil && cards != ".." && !strings.HasPrefix(cards, ".."+string(filepath.Separator)) {
			return hookSearch{}, false
		}
		if info, err := os.Stat(path); err == nil && !info.IsDir() {
			return hookSearch{}, false
		}
		if len(paths) == 1 && rel != "." {
			search.In = filepath.ToSlash(rel)
		}
	}
	return search, true
}

// parseHookSearchCommand finds the recursive grep, git grep, rg, ag or ack
// that starts one of command's pipelines and restates it, following cd
// between commands; a search fed by a pipe filters output rather than code.
func parseHookSearchCommand(root, cwd, command string) (hookSearch, bool) {
	dir := cwd
	var words []string
	piped := false
	// finish ends the words of one pipeline stage; only a first stage can
	// be a code search.
	finish := func() (hookSearch, bool) {
		stage, first := words, !piped
		words = nil
		if !first {
			return hookSearch{}, false
		}
		if len(stage) == 2 && stage[0] == "cd" {
			if filepath.IsAbs(stage[1]) {
				dir = stage[1]
			} else {
				dir = filepath.Join(dir, stage[1])
			}
			return hookSearch{}, false
		}
		return parseHookSearchWords(root, dir, stage)
	}
	for _, token := range hookShellTokens(command) {
		if !token.op {
			words = append(words, token.text)
			continue
		}
		if search, ok := finish(); ok {
			return search, true
		}
		piped = token.text == "|"
	}
	return finish()
}

func parseHookSearchWords(root, dir string, words []string) (hookSearch, bool) {
	for len(words) > 0 && strings.Contains(words[0], "=") && !strings.HasPrefix(words[0], "-") {
		words = words[1:]
	}
	if len(words) == 0 {
		return hookSearch{}, false
	}
	program, args := filepath.Base(words[0]), words[1:]
	if program == "git" {
		if len(args) == 0 || args[0] != "grep" {
			return hookSearch{}, false
		}
		program, args = "git grep", args[1:]
	}
	switch program {
	case "grep", "egrep", "fgrep", "git grep", "rg", "ag", "ack":
	default:
		return hookSearch{}, false
	}
	// grep searches its operands alone unless told to recurse; the others
	// walk the tree by default.
	recursive := program != "grep" && program != "egrep" && program != "fgrep"
	basic := program == "grep" || program == "git grep"
	search := hookSearch{Fixed: program == "fgrep"}
	var positional []string
	patternSet := false
	for index := 0; index < len(args); index++ {
		arg := args[index]
		next := func() string {
			if index+1 < len(args) {
				index++
				return args[index]
			}
			return ""
		}
		switch {
		case arg == "--":
			positional = append(positional, args[index+1:]...)
			index = len(args)
		case strings.HasPrefix(arg, "--"):
			name, value, hasValue := strings.Cut(arg[2:], "=")
			switch name {
			case "recursive", "dereference-recursive":
				recursive = true
			case "ignore-case":
				search.IgnoreCase = true
			case "fixed-strings", "literal":
				search.Fixed = true
			case "extended-regexp", "perl-regexp":
				basic = false
			case "regexp":
				if !hasValue {
					value = next()
				}
				search.Pattern, patternSet = value, true
			case "file":
				return hookSearch{}, false
			case "include", "exclude", "exclude-dir", "glob", "type", "type-not", "max-count", "context",
				"after-context", "before-context", "max-depth", "threads", "max-columns":
				if !hasValue {
					next()
				}
			}
		case strings.HasPrefix(arg, "-") && len(arg) > 1:
			for at := 1; at < len(arg); at++ {
				switch flag := arg[at]; {
				case flag == 'r' || flag == 'R':
					recursive = true
				case flag == 'i' || flag == 'y':
					search.IgnoreCase = true
				case flag == 'F' || flag == 'Q':
					search.Fixed = true
				case flag == 'E' || flag == 'P':
					basic = false
				case strings.IndexByte("ABCmefgtTjMdD", flag) >= 0:
					// The flag takes the rest of the cluster or the next word.
					value := arg[at+1:]
					if value == "" {
						value = next()
					}
					switch flag {
					case 'e':
						search.Pattern, patternSet = value, true
					case 'f':
						return hookSearch{}, false
					}
					at = len(arg)
				}
			}
		default:
			positional = append(positional, arg)
		}
	}
	if !patternSet {
		if len(positional) == 0 {
			return hookSearch{}, false
		}
		search.Pattern, positional = positional[0], positional[1:]
	}
	if !recursive {
		return hookSearch{}, false
	}
	if basic && !search.Fixed {
		search.Pattern = hookBasicRegexp(search.Pattern)
	}
	return hookSearchScope(root, dir, search, positional)
}

// hookBasicRegexp rewrites a POSIX basic regular expression, where \| \( \)
// \{ \} \+ \? are operators and the bare characters are literal, in the
// extended syntax graft_find_all reads.
func hookBasicRegexp(pattern string) string {
	const swapped = "|(){}+?"
	var out strings.Builder
	for index := 0; index < len(pattern); index++ {
		char := pattern[index]
		switch {
		case char == '\\' && index+1 < len(pattern):
			index++
			if strings.IndexByte(swapped, pattern[index]) < 0 {
				out.WriteByte(char)
			}
			out.WriteByte(pattern[index])
		case strings.IndexByte(swapped, char) >= 0:
			out.WriteByte('\\')
			out.WriteByte(char)
		default:
			out.WriteByte(char)
		}
	}
	return out.String()
}

type hookShellToken struct {
	text string
	op   bool
}

// hookShellTokens splits a shell command into words and the control
// operators between them (| || && ; & and newlines), honoring quotes and
// backslashes and dropping redirections.
func hookShellTokens(command string) []hookShellToken {
	var tokens []hookShellToken
	var word strings.Builder
	inWord, quoted, skipTarget := false, false, false
	end := func() {
		if !inWord {
			return
		}
		text, wasQuoted := word.String(), quoted
		word.Reset()
		inWord, quoted = false, false
		operator := strings.TrimLeft(text, "0123456789&")
		switch {
		case skipTarget:
			skipTarget = false
		case !wasQuoted && (operator == ">" || operator == ">>" || operator == "<" || operator == ">&"):
			skipTarget = true
		case !wasQuoted && (strings.HasPrefix(operator, ">") || strings.HasPrefix(operator, "<")):
			// A redirection with its target attached, such as 2>/dev/null.
		default:
			tokens = append(tokens, hookShellToken{text: text})
		}
	}
	for index := 0; index < len(command); index++ {
		char := command[index]
		switch {
		case char == '\'':
			inWord, quoted = true, true
			closeAt := strings.IndexByte(command[index+1:], '\'')
			if closeAt < 0 {
				closeAt = len(command) - index - 1
			}
			word.WriteString(command[index+1 : index+1+closeAt])
			index += closeAt + 1
		case char == '"':
			inWord, quoted = true, true
			for index++; index < len(command) && command[index] != '"'; index++ {
				if command[index] == '\\' && index+1 < len(command) && strings.IndexByte("\"\\$`", command[index+1]) >= 0 {
					index++
				}
				word.WriteByte(command[index])
			}
		case char == '\\' && index+1 < len(command):
			inWord = true
			index++
			if command[index] != '\n' {
				word.WriteByte(command[index])
			}
		case char == ' ' || char == '\t':
			end()
		case char == '&' && inWord && strings.HasSuffix(word.String(), ">"):
			word.WriteByte(char)
		case char == '|' || char == '&' || char == ';' || char == '\n':
			end()
			op := string(char)
			if (char == '|' || char == '&') && index+1 < len(command) && command[index+1] == char {
				op += string(char)
				index++
			}
			tokens = append(tokens, hookShellToken{text: op, op: true})
		default:
			inWord = true
			word.WriteByte(char)
		}
	}
	end()
	return tokens
}
