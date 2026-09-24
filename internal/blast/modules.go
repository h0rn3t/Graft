package blast

import (
	"regexp"
	"strings"
	"unicode"
	"unicode/utf16"
)

// maxLabel is the longest label a diagram circle holds, in UTF-16 code units.

const maxLabel = 30

var clauseBreak = regexp.MustCompile(`(?i)\s(?:\(|—|-\s)|:\s|\s(?:via|and|for|with|using|in|across|over|from|of|by|to)\s`)

// ShortLabel trims a label to fit a diagram node, cutting at a clause break
// when one falls in range and at a word boundary with an ellipsis otherwise.
func ShortLabel(label string) string {
	bare := strings.TrimSpace(label)
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
