package blast

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"unicode"
	"unicode/utf16"

	"gopkg.in/yaml.v3"
)

// ModuleIndex maps repository-relative paths to the concept node that claims them.
type ModuleIndex struct {
	claims map[string]claim
}

type claim struct {
	label string
	size  int
}

// ConceptOf returns the claiming concept's name, or false when none claims path.
func (index ModuleIndex) ConceptOf(path string) (string, bool) {
	found, ok := index.claims[path]
	return found.label, ok
}

// LoadModuleIndex reads the concept nodes in contextDir. When several concepts
// claim a file, the one grounded in the fewest sources wins.
func LoadModuleIndex(contextDir string) ModuleIndex {
	index := ModuleIndex{claims: make(map[string]claim)}
	for _, node := range readConceptNodes(contextDir) {
		if node.name == "" {
			continue
		}
		for _, source := range node.sources {
			prev, ok := index.claims[source]
			if !ok || len(node.sources) < prev.size {
				index.claims[source] = claim{label: node.name, size: len(node.sources)}
			}
		}
	}
	return index
}

type conceptNode struct {
	name    string
	sources []string
}

var wiringCardHeading = regexp.MustCompile(`^# [^\s#]+\.[A-Za-z0-9]{1,12}(?:\s|$)`)

// readConceptNodes mirrors the TypeScript readNodes: every top-level Markdown
// node except INDEX.md and root-level per-file wiring cards.
func readConceptNodes(dir string) []conceptNode {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	stems := rootFileCardStems(dir)
	nodes := make([]conceptNode, 0)
	for _, entry := range entries {
		name := entry.Name()
		if !strings.HasSuffix(name, ".md") || name == "INDEX.md" {
			continue
		}
		data, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			continue
		}
		front, content := splitFrontmatter(string(data))
		if isRootFileCard(strings.TrimSuffix(name, ".md"), front, content, stems) {
			continue
		}
		node := conceptNode{}
		if value, ok := front["name"]; ok && value != nil {
			node.name = jsString(value)
		}
		if list, ok := front["sources"].([]any); ok {
			for _, item := range list {
				if ref, ok := item.(map[string]any); ok {
					if path, ok := ref["path"].(string); ok {
						node.sources = append(node.sources, path)
					}
				}
			}
		}
		nodes = append(nodes, node)
	}
	return nodes
}

func splitFrontmatter(text string) (map[string]any, string) {
	normalized := strings.ReplaceAll(text, "\r\n", "\n")
	if !strings.HasPrefix(normalized, "---\n") {
		return map[string]any{}, normalized
	}
	front, body, ok := strings.Cut(normalized[4:], "\n---")
	if !ok {
		return map[string]any{}, normalized
	}
	values := map[string]any{}
	if err := yaml.Unmarshal([]byte(front), &values); err != nil || values == nil {
		values = map[string]any{}
	}
	_, body, _ = strings.Cut(body, "\n")
	return values, body
}

func rootFileCardStems(dir string) map[string]bool {
	stems := make(map[string]bool)
	data, err := os.ReadFile(filepath.Join(dir, "manifest.json"))
	if err != nil {
		return stems
	}
	var manifest struct {
		Files []struct {
			Path string `json:"path"`
		} `json:"files"`
	}
	if json.Unmarshal(data, &manifest) != nil {
		return stems
	}
	for _, file := range manifest.Files {
		if strings.Contains(file.Path, "/") {
			continue
		}
		stems[stripExtension(file.Path)] = true
	}
	return stems
}

func isRootFileCard(stem string, front map[string]any, content string, stems map[string]bool) bool {
	if slug, ok := front["slug"]; ok && slug != nil && jsString(slug) != "" {
		return false
	}
	if stems[stem] {
		return true
	}
	if _, covers := front["covers"]; covers && front["name"] == nil && front["type"] == nil && front["sources"] == nil {
		return true
	}
	line, _, _ := strings.Cut(strings.TrimLeft(content, " \t\r\n"), "\n")
	return wiringCardHeading.MatchString(strings.TrimSuffix(line, "\r"))
}

func jsString(value any) string {
	switch typed := value.(type) {
	case string:
		return typed
	case nil:
		return ""
	default:
		data, err := json.Marshal(typed)
		if err != nil {
			return ""
		}
		return strings.Trim(string(data), `"`)
	}
}

// maxLabel is the longest label a diagram circle holds, in UTF-16 code units.
const maxLabel = 30

var (
	conceptPrefix = regexp.MustCompile(`(?i)^concepts?:\s*`)
	clauseBreak   = regexp.MustCompile(`(?i)\s(?:\(|—|-\s)|:\s|\s(?:via|and|for|with|using|in|across|over|from|of|by|to)\s`)
)

// ShortLabel trims a concept name to fit a diagram node, cutting at a clause
// break when one falls in range and at a word boundary with an ellipsis otherwise.
func ShortLabel(label string) string {
	bare := strings.TrimSpace(conceptPrefix.ReplaceAllString(label, ""))
	units := utf16.Encode([]rune(bare))
	if len(units) <= maxLabel {
		return bare
	}
	if at := clauseBreak.FindStringIndex(bare); at != nil {
		index := utf16Len(bare[:at[0]])
		if index >= 8 && index <= maxLabel {
			return trimEndJS(bare[:at[0]])
		}
	}
	cut := lastSpaceAtOrBefore(units, maxLabel)
	end := maxLabel
	if cut > maxLabel/2 {
		end = cut
	}
	return trimEndJS(string(utf16.Decode(units[:end]))) + "…"
}

// trimEndJS trims trailing whitespace like String.prototype.trimEnd, which also
// strips the byte order mark.
func trimEndJS(text string) string {
	return strings.TrimRightFunc(text, func(r rune) bool { return unicode.IsSpace(r) || r == '\uFEFF' })
}

func utf16Len(text string) int {
	return len(utf16.Encode([]rune(text)))
}

func lastSpaceAtOrBefore(units []uint16, from int) int {
	for i := min(from, len(units)-1); i >= 0; i-- {
		if units[i] == ' ' {
			return i
		}
	}
	return -1
}

// ParentDir returns the parent of a directory label, or "" at the top level.
func ParentDir(dir string) string {
	trimmed := strings.TrimSuffix(dir, "/")
	cut := strings.LastIndex(trimmed, "/")
	if cut == -1 {
		return ""
	}
	return trimmed[:cut] + "/"
}

// DirLabel returns the directory of path with a trailing slash, or "(root)".
func DirLabel(path string) string {
	cut := strings.LastIndex(path, "/")
	if cut == -1 {
		return "(root)"
	}
	return path[:cut] + "/"
}

func stripExtension(path string) string {
	slash := strings.LastIndex(path, "/")
	dot := strings.LastIndex(path, ".")
	if dot > slash && dot >= 0 {
		return path[:dot]
	}
	return path
}
