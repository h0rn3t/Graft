package graph

import (
	"cmp"
	"encoding/binary"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"unicode/utf16"
	"unicode/utf8"

	"github.com/h0rn3t/Graft/internal/jsonjs"
)

const (
	defaultGrepMaxHits = 300
	maxGrepHitText     = 160
)

var grepSpanPattern = regexp.MustCompile(`^L(\d+)-L(\d+)$`)

// GrepPatternError reports a pattern that the Go regular-expression engine could not compile.
type GrepPatternError struct {
	Err error
}

// Error returns the underlying regular-expression error message.
func (e *GrepPatternError) Error() string {
	return e.Err.Error()
}

// Unwrap returns the underlying regular-expression error.
func (e *GrepPatternError) Unwrap() error {
	return e.Err
}

// GrepOptions controls lexical matching over indexed files.
type GrepOptions struct {
	IgnoreCase bool
	Fixed      bool
	In         string
	MaxHits    int
}

// GrepHit is one matching source line.
type GrepHit struct {
	Line int    `json:"line"`
	Text string `json:"text"`
}

// GrepSymbolRef identifies the symbol enclosing a group of matching lines.
type GrepSymbolRef struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	Kind Kind   `json:"kind"`
	Path string `json:"path"`
	Span string `json:"span"`
}

// GrepGroup groups matching lines by their innermost enclosing symbol.
type GrepGroup struct {
	Symbol   *GrepSymbolRef `json:"symbol"`
	Path     string         `json:"path"`
	InDegree int            `json:"inDegree"`
	Hits     []GrepHit      `json:"hits"`
}

// GrepTruncated records indexed files and matching lines that were not collected.
type GrepTruncated struct {
	Files int `json:"files"`
	Hits  int `json:"hits"`
}

// GrepSavings records the known whole-file baseline for matching groups.
type GrepSavings struct {
	Files         int `json:"files"`
	BaselineChars int `json:"baselineChars"`
}

// GrepResult is the JSON-compatible result of a lexical graph search.
type GrepResult struct {
	Pattern       string        `json:"pattern"`
	FilesSearched int           `json:"filesSearched"`
	TotalHits     int           `json:"totalHits"`
	Groups        []GrepGroup   `json:"groups"`
	Truncated     GrepTruncated `json:"truncated"`
	Saved         *GrepSavings  `json:"saved,omitempty"`
}

// Grep searches readable indexed files, grouping hits by their innermost symbol.
func Grep(wiring GraphV1, repoRoot, pattern string, opts GrepOptions) (GrepResult, error) {
	result := GrepResult{
		Pattern: pattern,
		Groups:  make([]GrepGroup, 0),
	}
	maxHits := opts.MaxHits
	if maxHits == 0 {
		maxHits = defaultGrepMaxHits
	}

	patternSource := pattern
	if opts.Fixed {
		patternSource = regexp.QuoteMeta(patternSource)
	}
	if opts.IgnoreCase {
		patternSource = "(?i)" + patternSource
	}
	matcher, err := regexp.Compile(patternSource)
	if err != nil {
		return GrepResult{}, &GrepPatternError{Err: err}
	}

	inPrefix := normalizePathPrefix(opts.In)
	if err := assertPrefixIndexed(wiring, inPrefix); err != nil {
		return GrepResult{}, err
	}

	inDegree := grepInDegree(wiring.Edges)
	groupIndexes := make(map[string]int)
	hitPaths := make(map[string]struct{})
	for _, file := range wiring.Nodes {
		if file.Kind != Kind("file") || (inPrefix != "" && !pathUnderPrefix(file.Path, inPrefix)) {
			continue
		}
		result.FilesSearched++
		text, readable := readGrepSource(filepath.Join(repoRoot, filepath.FromSlash(file.Path)))
		if !readable {
			result.Truncated.Files++
			continue
		}
		symbols := grepSymbols(wiring.Nodes, file.Path)
		for lineIndex, raw := range strings.Split(text, "\n") {
			if !matcher.MatchString(raw) {
				continue
			}
			if result.TotalHits >= maxHits {
				result.Truncated.Hits++
				continue
			}

			result.TotalHits++
			symbol := grepEnclosingSymbol(symbols, lineIndex+1)
			key := "file:" + file.Path
			group := GrepGroup{Path: file.Path, Hits: make([]GrepHit, 0)}
			if symbol != nil {
				key = symbol.ID
				ref := grepSymbolRef(*symbol)
				group.Symbol = &ref
				group.InDegree = inDegree[symbol.ID]
			}
			if groupIndex, ok := groupIndexes[key]; ok {
				group = result.Groups[groupIndex]
			} else {
				groupIndexes[key] = len(result.Groups)
				result.Groups = append(result.Groups, group)
			}
			trimmed := jsonjs.TrimSpace(raw)
			result.Groups[groupIndexes[key]].Hits = append(result.Groups[groupIndexes[key]].Hits, GrepHit{
				Line: lineIndex + 1,
				Text: truncateGrepText(trimmed),
			})
			hitPaths[file.Path] = struct{}{}
		}
	}

	compare := localeCompare()
	slices.SortStableFunc(result.Groups, func(left, right GrepGroup) int {
		return cmp.Or(cmp.Compare(right.InDegree, left.InDegree), compare(left.Path, right.Path))
	})
	result.Saved = grepSavings(wiring.Nodes, hitPaths)
	return result, nil
}

type grepSymbolSpan struct {
	node       *NodeV1
	start, end int
}

func grepSymbols(nodes []NodeV1, path string) []grepSymbolSpan {
	symbols := make([]grepSymbolSpan, 0)
	for index := range nodes {
		node := &nodes[index]
		if node.Kind == Kind("file") || node.Path != path {
			continue
		}
		start, end, ok := grepSpanBounds(node.Span)
		if ok {
			symbols = append(symbols, grepSymbolSpan{node: node, start: start, end: end})
		}
	}
	slices.SortStableFunc(symbols, func(left, right grepSymbolSpan) int {
		return cmp.Compare(left.start, right.start)
	})
	return symbols
}

func grepSpanBounds(span string) (int, int, bool) {
	match := grepSpanPattern.FindStringSubmatch(span)
	if match == nil {
		return 0, 0, false
	}
	start := 0
	end := 0
	if _, err := fmt.Sscanf(match[1], "%d", &start); err != nil {
		return 0, 0, false
	}
	if _, err := fmt.Sscanf(match[2], "%d", &end); err != nil {
		return 0, 0, false
	}
	return start, end, true
}

func grepEnclosingSymbol(symbols []grepSymbolSpan, line int) *NodeV1 {
	var found *NodeV1
	for _, symbol := range symbols {
		if symbol.start > line {
			break
		}
		if line <= symbol.end {
			found = symbol.node
		}
	}
	return found
}

func grepSymbolRef(node NodeV1) GrepSymbolRef {
	name := node.Name
	if hash := strings.IndexByte(node.ID, '#'); hash >= 0 {
		name = node.ID[hash+1:]
	}
	return GrepSymbolRef{ID: node.ID, Name: name, Kind: node.Kind, Path: node.Path, Span: node.Span}
}

func grepInDegree(edges []EdgeV1) map[string]int {
	degrees := make(map[string]int)
	for _, edge := range edges {
		if isWalkRelation(edge.Relation) {
			degrees[edge.Target]++
		}
	}
	return degrees
}

func grepSavings(nodes []NodeV1, paths map[string]struct{}) *GrepSavings {
	fileChars := make(map[string]int)
	for _, node := range nodes {
		if node.Kind == Kind("file") && node.Chars != nil {
			fileChars[node.Path] = *node.Chars
		}
	}
	saved := &GrepSavings{}
	for path := range paths {
		chars, ok := fileChars[path]
		if !ok {
			continue
		}
		saved.Files++
		saved.BaselineChars += chars
	}
	if saved.Files == 0 {
		return nil
	}
	return saved
}

func readGrepSource(path string) (string, bool) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", false
	}
	if len(data) >= 2 && data[0] == 0xfe && data[1] == 0xff {
		return "", false
	}
	if len(data) >= 2 && data[0] == 0xff && data[1] == 0xfe {
		words := make([]uint16, (len(data)-2)/2)
		for index := range words {
			words[index] = binary.LittleEndian.Uint16(data[2+index*2:])
		}
		return string(utf16.Decode(words)), true
	}
	return strings.ToValidUTF8(string(data), string(utf8.RuneError)), true
}

func truncateGrepText(text string) string {
	if len(utf16.Encode([]rune(text))) <= maxGrepHitText {
		return text
	}
	var output strings.Builder
	units := 0
	for _, r := range text {
		runeUnits := 1
		if r > 0xffff {
			runeUnits = 2
		}
		if units+runeUnits > maxGrepHitText {
			break
		}
		output.WriteRune(r)
		units += runeUnits
	}
	return output.String()
}
