// Package blast computes the blast radius of a git diff over a wiring graph and
// renders it as text, markdown, Mermaid, or JSON.
package blast

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"github.com/h0rn3t/Graft/internal/gitx"
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

var hunkHeader = regexp.MustCompile(`^@@ -\d+(?:,(\d+))? \+(\d+)(?:,(\d+))? @@`)

const (
	maxHunkLines = 24
	maxFileLines = 200
)

var (
	// ErrNotRepository reports that the root ChangedFiles was given is not
	// inside a git repository.
	ErrNotRepository = errors.New("not a git repository")
	// errOptionRef rejects a revision git would read as an option.
	errOptionRef = errors.New(`a revision must not start with "-"`)
)

func checkRef(ref string) error {
	if strings.HasPrefix(ref, "-") {
		return fmt.Errorf("ref %q: %w", ref, errOptionRef)
	}
	return nil
}

// RefExists reports whether git can resolve ref to a commit in root.
func RefExists(ctx context.Context, root, ref string) bool {
	if checkRef(ref) != nil {
		return false
	}
	_, err := gitx.Run(ctx, root, "rev-parse", "--verify", "--quiet", "--end-of-options", ref+"^{commit}")
	return err == nil
}

// ChangedFiles reads the changed files and post-image line ranges, with paths
// relative to root; files outside root are left out. With a nil base it
// compares the working tree with HEAD, falling back to the last commit when the
// tree is clean. The error carries git's own message when git cannot produce a
// diff.
func ChangedFiles(ctx context.Context, root string, base *string) (DiffResult, error) {
	// Outside a repository git diff falls back to comparing two paths and
	// complains about those instead, so ask about the repository first.
	if _, err := gitx.Run(ctx, root, "rev-parse", "--show-prefix"); err != nil {
		// Git has no exit code of its own for this; its message is the signal.
		if strings.Contains(err.Error(), "not a git repository") {
			return DiffResult{}, fmt.Errorf("%w: %w", ErrNotRepository, err)
		}
		return DiffResult{}, err
	}
	if base != nil {
		if err := checkRef(*base); err != nil {
			return DiffResult{}, err
		}
		basis := *base + "...HEAD"
		files, err := diffFiles(ctx, root, basis)
		if err != nil {
			return DiffResult{}, err
		}
		return DiffResult{Basis: basis, Files: files}, nil
	}
	working, err := diffFiles(ctx, root, "HEAD")
	if err != nil {
		return DiffResult{}, err
	}
	if len(working) > 0 {
		return DiffResult{Basis: "working tree vs HEAD", Files: working}, nil
	}
	last, err := diffFiles(ctx, root, "HEAD~1...HEAD")
	if err != nil {
		// A repository with a single commit has no HEAD~1, so nothing changed.
		if ctx.Err() != nil {
			return DiffResult{}, err
		}
		return DiffResult{Basis: "working tree vs HEAD", Files: []*ChangedFile{}}, nil
	}
	return DiffResult{Basis: "HEAD~1...HEAD", Files: last}, nil
}

func diffFiles(ctx context.Context, root, rangeArg string) ([]*ChangedFile, error) {
	// --relative keeps paths relative to root when root is a subdirectory of
	// the repository, which is how the graph names them.
	common := []string{"--no-color", "--no-ext-diff", "--find-renames", "--relative"}
	revisions := []string{"--end-of-options", rangeArg, "--"}
	status, err := gitx.Run(ctx, root, slices.Concat([]string{"diff", "--name-status", "-z"}, common, revisions)...)
	if err != nil {
		return nil, err
	}
	files := parseNameStatus(status)
	if len(files) == 0 {
		return files, nil
	}
	// Explicit prefixes override diff.noprefix and diff.mnemonicPrefix.
	patch, err := gitx.Run(ctx, root, slices.Concat([]string{"diff", "--unified=0", "--src-prefix=a/", "--dst-prefix=b/"}, common, revisions)...)
	if err != nil {
		return nil, err
	}
	applyHunks(files, patch)
	return files, nil
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

// applyHunks attaches each hunk's post-image range and text to its file. A hunk
// body is read by the line counts in its header, so an added line that reads
// like a "+++ " file header stays inside the hunk.
func applyHunks(files []*ChangedFile, patch string) {
	byPath := make(map[string]*ChangedFile, len(files))
	for _, file := range files {
		byPath[file.Path] = file
	}
	var current *ChangedFile
	var hunk *Hunk
	next, kept := 0, 0
	oldLeft, newLeft := 0, 0
	for line := range strings.SplitSeq(patch, "\n") {
		if oldLeft > 0 || newLeft > 0 {
			switch {
			case strings.HasPrefix(line, "+") && newLeft > 0:
				newLeft--
				if hunk != nil {
					n := next
					pushLine(hunk, DiffLine{N: &n, Sign: "+", Text: line[1:]}, kept < maxFileLines)
					kept++
				}
				next++
				continue
			case strings.HasPrefix(line, "-") && oldLeft > 0:
				oldLeft--
				if hunk != nil {
					pushLine(hunk, DiffLine{Sign: "-", Text: line[1:]}, kept < maxFileLines)
					kept++
				}
				continue
			case strings.HasPrefix(line, " ") && oldLeft > 0 && newLeft > 0:
				oldLeft--
				newLeft--
				next++
				continue
			case strings.HasPrefix(line, `\`):
				// "\ No newline at end of file" counts on neither side.
				continue
			}
			// A line that fits neither side ends a truncated hunk.
			oldLeft, newLeft = 0, 0
		}
		switch {
		case strings.HasPrefix(line, "diff --git "):
			current, hunk = nil, nil
		case strings.HasPrefix(line, "+++ "):
			current, hunk, kept = nil, nil, 0
			if path, ok := headerPath(line[4:]); ok {
				current = byPath[path]
			}
		case strings.HasPrefix(line, "@@"):
			hunk = nil
			match := hunkHeader.FindStringSubmatch(line)
			if match == nil {
				continue
			}
			start, _ := strconv.Atoi(match[2])
			oldLeft, newLeft = headerCount(match[1]), headerCount(match[3])
			next = start
			if current == nil {
				continue
			}
			span := LineRange{Start: start, End: start + newLeft - 1}
			if newLeft == 0 {
				span.End = start
			}
			hunk = &Hunk{Start: span.Start, End: span.End, Lines: []DiffLine{}}
			current.Ranges = append(current.Ranges, span)
			current.Hunks = append(current.Hunks, hunk)
		}
	}
}

// headerCount reads a hunk header's line count, which git omits when it is 1.
func headerCount(field string) int {
	if field == "" {
		return 1
	}
	count, _ := strconv.Atoi(field)
	return count
}

func pushLine(hunk *Hunk, line DiffLine, withinFile bool) {
	if !withinFile || len(hunk.Lines) >= maxHunkLines {
		hunk.Dropped++
		return
	}
	hunk.Lines = append(hunk.Lines, line)
}

// headerPath turns a "+++ " header value such as b/src/x.ts, or its C-quoted
// form "b/q\"x.ts", into the path it names, minus any tab-separated suffix. It
// returns false for /dev/null and for a quoted value it cannot read.
func headerPath(raw string) (string, bool) {
	raw, _, _ = strings.Cut(raw, "\t")
	if raw == "/dev/null" {
		return "", false
	}
	path, ok := unquoteC(raw)
	if !ok {
		return "", false
	}
	return strings.TrimPrefix(path, "b/"), true
}

// unquoteC reads a path git printed, which git C-quotes when it holds a
// double quote, a backslash, a control character or, under core.quotePath,
// a non-ASCII byte.
func unquoteC(raw string) (string, bool) {
	if !strings.HasPrefix(raw, `"`) {
		return raw, true
	}
	path, err := strconv.Unquote(raw)
	return path, err == nil
}
