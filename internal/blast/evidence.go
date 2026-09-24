package blast

import (
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
)

// evidenceLine is one quoted source line; N is its 1-based line number.
type evidenceLine struct {
	N    int
	Text string
}

// reachTerms is what a line must mention to be the line that reaches the diff.
type reachTerms struct {
	names   []string
	modules []*regexp.Regexp
}

var (
	spanRangePattern = regexp.MustCompile(`^L(\d+)(?:-L(\d+))?$`)
	extensionSuffix  = regexp.MustCompile(`\.[^./]+$`)
)

func spanRange(span string) (int, int, bool) {
	match := spanRangePattern.FindStringSubmatch(span)
	if match == nil {
		return 0, 0, false
	}
	start, _ := strconv.Atoi(match[1])
	end := start
	if match[2] != "" {
		end, _ = strconv.Atoi(match[2])
	}
	return start, end, true
}

func newReachTerms(seeds []Seed, changed []*ChangedFile) reachTerms {
	names := make([]string, 0)
	for _, seed := range seeds {
		if !seed.WholeFile && !slices.Contains(names, seed.Name) {
			names = append(names, seed.Name)
		}
	}
	slices.SortStableFunc(names, func(a, b string) int { return utf16Len(b) - utf16Len(a) })
	stems := make([]string, 0)
	for _, file := range changed {
		base := extensionSuffix.ReplaceAllString(file.Path, "")
		stem := base[strings.LastIndex(base, "/")+1:]
		if stem != "" && !slices.Contains(stems, stem) {
			stems = append(stems, stem)
		}
	}
	terms := reachTerms{names: names}
	for _, stem := range stems {
		terms.modules = append(terms.modules, regexp.MustCompile("[\"'`][^\"'`]*"+jsWordBoundary+regexp.QuoteMeta(stem)+"(\\.[a-z]+)?[\"'`]"))
	}
	return terms
}

// jsWordBoundary is \b; RE2 and JavaScript agree on ASCII word characters.
const jsWordBoundary = `\b`

func wordPattern(name string) *regexp.Regexp {
	return regexp.MustCompile(jsWordBoundary + regexp.QuoteMeta(name) + jsWordBoundary)
}

func referenceLine(path, span string, needles []*regexp.Regexp, read func(string) []string) (evidenceLine, bool) {
	start, end, ok := spanRange(span)
	lines := read(path)
	if !ok || lines == nil {
		return evidenceLine{}, false
	}
	from := max(start-1, 0)
	for i := from; i < min(end, len(lines)); i++ {
		for _, needle := range needles {
			if needle.MatchString(lines[i]) {
				return evidenceLine{N: start + (i - from), Text: lines[i]}, true
			}
		}
	}
	return evidenceLine{}, false
}

// impactedLine returns the line in a dependent's span that names the diff.
func impactedLine(symbol Impacted, terms reachTerms, read func(string) []string) (evidenceLine, bool) {
	names := make([]*regexp.Regexp, len(terms.names))
	for i, name := range terms.names {
		names[i] = wordPattern(name)
	}
	if line, ok := referenceLine(symbol.Path, symbol.Span, names, read); ok {
		return line, true
	}
	return referenceLine(symbol.Path, symbol.Span, terms.modules, read)
}

// fileReader reads each file under root once and never fails. The paths come
// from the graph, so they are read through root and cannot name a file outside
// it; a nil root reads nothing.
func fileReader(root *os.Root) func(string) []string {
	cache := make(map[string][]string)
	return func(path string) []string {
		if root == nil {
			return nil
		}
		if lines, ok := cache[path]; ok {
			return lines
		}
		var lines []string
		if data, err := root.ReadFile(filepath.FromSlash(path)); err == nil {
			lines = strings.Split(decodeUTF8(data), "\n")
		}
		cache[path] = lines
		return lines
	}
}

// decodeUTF8 replaces each invalid byte with U+FFFD, as Node's utf8 decoder does.
func decodeUTF8(data []byte) string {
	return string([]rune(string(data)))
}
