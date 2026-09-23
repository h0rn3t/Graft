package hosts

import (
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
)

// Markers fence one managed region in a file the user owns.
type Markers struct {
	Start string
	End   string
}

var (
	// GraftMarkers fence the instruction block.
	GraftMarkers = Markers{Start: "<!-- graft:start -->", End: "<!-- graft:end -->"}
	// BrainMarkers fence a brain's rules, kept separate from the instructions.
	BrainMarkers = Markers{Start: "<!-- graft:brain:start -->", End: "<!-- graft:brain:end -->"}
	// AllMarkers lists every region graft may own in a user file.
	AllMarkers = []Markers{GraftMarkers, BrainMarkers}
)

// UpsertAction is what a section upsert did.
type UpsertAction string

// Section upsert outcomes.
const (
	UpsertCreated   UpsertAction = "created"
	UpsertAppended  UpsertAction = "appended"
	UpsertReplaced  UpsertAction = "replaced"
	UpsertUnchanged UpsertAction = "unchanged"
)

var trailingWhitespace = regexp.MustCompile(`[\s\x{00a0}\x{1680}\x{2000}-\x{200a}\x{2028}\x{2029}\x{202f}\x{205f}\x{3000}\x{feff}]+$`)

// FencedBlock renders body between markers with the given line ending.
func FencedBlock(body, eol string, markers Markers) string {
	normalized := strings.ReplaceAll(body, "\r", "")
	block := markers.Start + "\n" + trailingWhitespace.ReplaceAllString(normalized, "") + "\n" + markers.End
	if eol == "\n" {
		return block
	}
	return strings.ReplaceAll(block, "\n", "\r\n")
}

func detectEOL(text string) string {
	if strings.Contains(text, "\r\n") {
		return "\r\n"
	}
	return "\n"
}

// splitLines splits on CRLF or LF, like text.split(/\r\n|\n/).
func splitLines(text string) []string {
	return strings.Split(strings.ReplaceAll(text, "\r\n", "\n"), "\n")
}

func markerLine(lines []string, marker string, from int) int {
	for i := from; i < len(lines); i++ {
		if strings.TrimSpace(lines[i]) == marker {
			return i
		}
	}
	return -1
}

// UpsertSection writes body into filePath between markers: creating the file,
// replacing an existing block, or appending a new one after the user's content.
func UpsertSection(filePath, body string, markers Markers) (UpsertAction, error) {
	data, err := os.ReadFile(filePath)
	if errors.Is(err, os.ErrNotExist) {
		if err := os.MkdirAll(filepath.Dir(filePath), 0o755); err != nil {
			return "", err
		}
		return UpsertCreated, os.WriteFile(filePath, []byte(FencedBlock(body, "\n", markers)+"\n"), 0o644)
	}
	if err != nil {
		return "", err
	}
	text := string(data)
	eol := detectEOL(text)
	lines := splitLines(text)
	start := markerLine(lines, markers.Start, 0)
	end := -1
	if start != -1 {
		end = markerLine(lines, markers.End, start+1)
	}
	if start != -1 && end != -1 {
		if strings.Join(lines[start:end+1], "\n") == FencedBlock(body, "\n", markers) {
			return UpsertUnchanged, nil
		}
		block := strings.Split(FencedBlock(body, eol, markers), eol)
		next := slices.Concat(lines[:start], block, lines[end+1:])
		return UpsertReplaced, os.WriteFile(filePath, []byte(strings.Join(next, eol)), 0o644)
	}
	separator := eol + eol
	switch {
	case strings.HasSuffix(text, eol+eol):
		separator = ""
	case strings.HasSuffix(text, eol):
		separator = eol
	}
	return UpsertAppended, os.WriteFile(filePath, []byte(text+separator+FencedBlock(body, eol, markers)+eol), 0o644)
}
