// Package blast computes the blast radius of a git diff over a wiring graph and
// renders it as text, markdown, Mermaid, or JSON.
package blast

import (
	"bytes"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
)

// ChangeStatus is how git reported a changed path.
type ChangeStatus string

// Change statuses reported by git name-status output.
const (
	StatusAdded    ChangeStatus = "added"
	StatusModified ChangeStatus = "modified"
	StatusDeleted  ChangeStatus = "deleted"
	StatusRenamed  ChangeStatus = "renamed"
)

// LineRange is a contiguous run of changed lines in post-image line numbers.
type LineRange struct {
	Start int `json:"start"`
	End   int `json:"end"`
}

// DiffLine is one edited line inside a hunk. N is the post-image line number,
// nil for a deleted line.
type DiffLine struct {
	N    *int   `json:"n"`
	Sign string `json:"sign"`
	Text string `json:"text"`
}

// Hunk is one hunk's post-image range and the lines kept from it.
type Hunk struct {
	Start int        `json:"start"`
	End   int        `json:"end"`
	Lines []DiffLine `json:"lines"`
	// Dropped counts lines left out by the per-hunk and per-file caps.
	Dropped int `json:"dropped"`
}

// ChangedFile is one path in the diff with its post-image ranges and hunks.
type ChangedFile struct {
	Path    string       `json:"path"`
	Status  ChangeStatus `json:"status"`
	OldPath string       `json:"oldPath,omitempty"`
	Ranges  []LineRange  `json:"ranges"`
	Hunks   []*Hunk      `json:"hunks"`
}

// DiffResult is the set of changed files and a label for what was compared.
type DiffResult struct {
	Basis string
	Files []*ChangedFile
}

var hunkHeader = regexp.MustCompile(`^@@ -\d+(?:,\d+)? \+(\d+)(?:,(\d+))? @@`)

const (
	maxHunkLines = 24
	maxFileLines = 200
)

// git runs git in root and returns stdout, or false when git fails.
func git(root string, args ...string) (string, bool) {
	cmd := exec.Command("git", append([]string{"-c", "core.quotePath=false"}, args...)...)
	cmd.Dir = root
	var stdout bytes.Buffer
	cmd.Stdout = &stdout
	if err := cmd.Run(); err != nil {
		return "", false
	}
	return stdout.String(), true
}

// RefExists reports whether git can resolve ref to a commit in root.
func RefExists(root, ref string) bool {
	_, ok := git(root, "rev-parse", "--verify", "--quiet", ref+"^{commit}")
	return ok
}

// ChangedFiles reads the changed files and post-image line ranges. With an empty
// base it compares the working tree with HEAD, falling back to the last commit
// when the tree is clean. It returns false when git cannot produce a diff.
func ChangedFiles(root string, base *string) (DiffResult, bool) {
	if base != nil {
		basis := *base + "...HEAD"
		files, ok := diffFiles(root, basis)
		if !ok {
			return DiffResult{}, false
		}
		return DiffResult{Basis: basis, Files: files}, true
	}
	working, ok := diffFiles(root, "HEAD")
	if !ok {
		return DiffResult{}, false
	}
	if len(working) > 0 {
		return DiffResult{Basis: "working tree vs HEAD", Files: working}, true
	}
	last, ok := diffFiles(root, "HEAD~1...HEAD")
	if !ok {
		return DiffResult{Basis: "working tree vs HEAD", Files: []*ChangedFile{}}, true
	}
	return DiffResult{Basis: "HEAD~1...HEAD", Files: last}, true
}

func diffFiles(root, rangeArg string) ([]*ChangedFile, bool) {
	status, ok := git(root, "diff", "--name-status", "--find-renames", "-z", rangeArg, "--")
	if !ok {
		return nil, false
	}
	files := parseNameStatus(status)
	if len(files) == 0 {
		return files, true
	}
	if patch, ok := git(root, "diff", "--unified=0", "--no-color", "--no-ext-diff", "--find-renames", rangeArg, "--"); ok {
		applyHunks(files, patch)
	}
	return files, true
}

// parseNameStatus reads NUL-separated name-status output, where renames and
// copies carry three fields and every other status carries two.
func parseNameStatus(out string) []*ChangedFile {
	fields := make([]string, 0)
	for field := range strings.SplitSeq(out, "\x00") {
		if field != "" {
			fields = append(fields, field)
		}
	}
	files := make([]*ChangedFile, 0)
	for i := 0; i < len(fields); {
		code := fields[i]
		i++
		if strings.HasPrefix(code, "R") || strings.HasPrefix(code, "C") {
			if i+1 >= len(fields) {
				break
			}
			oldPath, path := fields[i], fields[i+1]
			i += 2
			files = append(files, newChangedFile(path, StatusRenamed, oldPath))
			continue
		}
		if i >= len(fields) {
			break
		}
		path := fields[i]
		i++
		switch {
		case strings.HasPrefix(code, "A"):
			files = append(files, newChangedFile(path, StatusAdded, ""))
		case strings.HasPrefix(code, "D"):
			files = append(files, newChangedFile(path, StatusDeleted, ""))
		default:
			files = append(files, newChangedFile(path, StatusModified, ""))
		}
	}
	return files
}

func newChangedFile(path string, status ChangeStatus, oldPath string) *ChangedFile {
	return &ChangedFile{Path: path, Status: status, OldPath: oldPath, Ranges: []LineRange{}, Hunks: []*Hunk{}}
}

// applyHunks attaches each hunk's post-image range and text to its file.
func applyHunks(files []*ChangedFile, patch string) {
	byPath := make(map[string]*ChangedFile, len(files))
	for _, file := range files {
		byPath[file.Path] = file
	}
	var current *ChangedFile
	var hunk *Hunk
	next, kept := 0, 0
	for line := range strings.SplitSeq(patch, "\n") {
		switch {
		case strings.HasPrefix(line, "diff --git "):
			current, hunk = nil, nil
			continue
		case strings.HasPrefix(line, "+++ "):
			raw := line[4:]
			current = nil
			if raw != "/dev/null" {
				current = byPath[stripPrefix(raw)]
			}
			hunk, kept = nil, 0
			continue
		case strings.HasPrefix(line, "@@"):
			hunk = nil
			if current == nil {
				continue
			}
			match := hunkHeader.FindStringSubmatch(line)
			if match == nil {
				continue
			}
			start, _ := strconv.Atoi(match[1])
			count := 1
			if match[2] != "" {
				count, _ = strconv.Atoi(match[2])
			}
			span := LineRange{Start: start, End: start + count - 1}
			if count == 0 {
				span.End = start
			}
			hunk = &Hunk{Start: span.Start, End: span.End, Lines: []DiffLine{}}
			current.Ranges = append(current.Ranges, span)
			current.Hunks = append(current.Hunks, hunk)
			next = start
			continue
		}
		if hunk == nil {
			continue
		}
		switch {
		case strings.HasPrefix(line, "+"):
			n := next
			pushLine(hunk, DiffLine{N: &n, Sign: "+", Text: line[1:]}, kept < maxFileLines)
			kept++
			next++
		case strings.HasPrefix(line, "-"):
			pushLine(hunk, DiffLine{Sign: "-", Text: line[1:]}, kept < maxFileLines)
			kept++
		}
	}
}

func pushLine(hunk *Hunk, line DiffLine, withinFile bool) {
	if !withinFile || len(hunk.Lines) >= maxHunkLines {
		hunk.Dropped++
		return
	}
	hunk.Lines = append(hunk.Lines, line)
}

// stripPrefix turns "b/src/x.ts" into "src/x.ts", minus any tab-separated suffix.
func stripPrefix(raw string) string {
	untabbed, _, _ := strings.Cut(raw, "\t")
	return strings.TrimPrefix(untabbed, "b/")
}
