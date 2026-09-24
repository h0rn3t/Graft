package graph

import (
	"cmp"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
)

// CardFileInfo identifies a Markdown card and the source file it describes.
type CardFileInfo struct {
	Card    string
	Path    string
	Symbols int
}

// CardStats counts generated and removed per-file Markdown cards.
type CardStats struct {
	Written int
	Pruned  int
	Files   []CardFileInfo
}

// WriteCards projects graph nodes into per-file Markdown cards and INDEX.md.
func WriteCards(graph GraphV1, outDir string) (CardStats, error) {
	if err := os.MkdirAll(outDir, 0o755); err != nil {
		return CardStats{}, fmt.Errorf("create card directory: %w", err)
	}
	byPath := make(map[string][]NodeV1)
	for _, node := range graph.Nodes {
		byPath[node.Path] = append(byPath[node.Path], node)
	}
	compare := localeCompare()
	paths := make([]string, 0, len(byPath))
	for sourcePath := range byPath {
		paths = append(paths, sourcePath)
	}
	slices.SortFunc(paths, compare)
	written := make(map[string]struct{}, len(paths))
	stats := CardStats{Files: make([]CardFileInfo, 0, len(paths))}
	for _, sourcePath := range paths {
		if !filepath.IsLocal(filepath.FromSlash(sourcePath)) || strings.Contains(sourcePath, "\\") {
			return CardStats{}, fmt.Errorf("invalid card source path %q", sourcePath)
		}
		cardRel := strings.TrimSuffix(sourcePath, path.Ext(sourcePath)) + ".md"
		cardPath := filepath.Join(outDir, filepath.FromSlash(cardRel))
		if err := os.MkdirAll(filepath.Dir(cardPath), 0o755); err != nil {
			return CardStats{}, fmt.Errorf("create card parent: %w", err)
		}
		group := byPath[sourcePath]
		var fileNode *NodeV1
		symbols := make([]NodeV1, 0, len(group))
		for index := range group {
			if group[index].Kind == "file" {
				fileNode = &group[index]
			} else {
				symbols = append(symbols, group[index])
			}
		}
		slices.SortFunc(symbols, func(a, b NodeV1) int {
			return cmp.Or(cmp.Compare(cardSpanStart(a.Span), cardSpanStart(b.Span)), compare(a.Name, b.Name))
		})
		var body strings.Builder
		body.WriteString("# " + sourcePath + "\n\n")
		if fileNode != nil {
			if summary := cardOneLiner(*fileNode); summary != "" {
				body.WriteString(summary + "\n\n")
			}
		}
		for _, node := range symbols {
			body.WriteString("- " + node.Name + " · " + string(node.Kind) + " · " + node.Span)
			if summary := cardOneLiner(node); summary != "" {
				body.WriteString(" — " + summary)
			}
			body.WriteByte('\n')
		}
		if len(symbols) == 0 {
			body.WriteString("_No extracted symbols in this file._\n")
		}
		if err := os.WriteFile(cardPath, []byte(body.String()), 0o644); err != nil {
			return CardStats{}, fmt.Errorf("write card %q: %w", cardRel, err)
		}
		written[cardPath] = struct{}{}
		stats.Files = append(stats.Files, CardFileInfo{Card: cardRel, Path: sourcePath, Symbols: len(symbols)})
	}
	stats.Written = len(written)
	if err := pruneCards(outDir, written, &stats); err != nil {
		return CardStats{}, err
	}
	slices.SortFunc(stats.Files, func(a, b CardFileInfo) int { return compare(a.Card, b.Card) })
	if err := writeCardIndex(outDir, stats.Files); err != nil {
		return CardStats{}, err
	}
	return stats, nil
}

func cardOneLiner(node NodeV1) string {
	if node.Summary != nil {
		if summary := strings.TrimSpace(*node.Summary); summary != "" {
			first, _, _ := strings.Cut(summary, "\n")
			return strings.TrimSpace(first)
		}
	}
	if node.Signature != nil {
		return strings.TrimSpace(*node.Signature)
	}
	return ""
}

func cardSpanStart(span string) int {
	first, _, _ := strings.Cut(span, "-")
	start, err := strconv.Atoi(strings.TrimPrefix(first, "L"))
	if err != nil {
		return 0
	}
	return start
}

func pruneCards(outDir string, written map[string]struct{}, stats *CardStats) error {
	directories := make([]string, 0)
	err := filepath.WalkDir(outDir, func(cardPath string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if cardPath == outDir {
			return nil
		}
		if entry.IsDir() {
			if filepath.Dir(cardPath) == outDir && (entry.Name() == ".cache" || entry.Name() == ".graph") {
				return filepath.SkipDir
			}
			directories = append(directories, cardPath)
			return nil
		}
		if filepath.Dir(cardPath) == outDir || !strings.HasSuffix(entry.Name(), ".md") {
			return nil
		}
		if _, ok := written[cardPath]; ok {
			return nil
		}
		if err := os.Remove(cardPath); err != nil {
			return err
		}
		stats.Pruned++
		return nil
	})
	if err != nil {
		return fmt.Errorf("prune stale cards: %w", err)
	}
	slices.SortFunc(directories, func(a, b string) int { return cmp.Compare(len(b), len(a)) })
	for _, directory := range directories {
		_ = os.Remove(directory) // Non-empty directories are retained.
	}
	return nil
}

func writeCardIndex(outDir string, files []CardFileInfo) error {
	lines := []string{
		"# graft — repo map", "",
		"Small markdown nodes summarising this repo. `grep` any term, symbol, or",
		"filename here, or run `graft ask \"<task>\"`. Each node carries prose plus exact",
		"`file:line`; open a source file only to edit the named span.", "",
		"The same graph is queryable as MCP tools (`graft_find_code`, `graft_find_all`,",
		"`graft_trace_calls`, `graft_file_api`, `graft_repo_map`) where a host exposes them, and",
		"as the `graft` CLI everywhere else. Edges — who calls what — live only in the",
		"graph, not in these files: `graft callers <symbol>` is the only way to read them.", "",
	}
	if len(files) > 0 {
		withSymbols := 0
		for _, file := range files {
			if file.Symbols > 0 {
				withSymbols++
			}
		}
		lines = append(lines, "## Files", "", fmt.Sprintf(
			"%d per-file wiring cards mirror the source tree under `graft/` (%d carry extracted symbols). They are deliberately not enumerated here —",
			len(files), withSymbols,
		), "`grep` a symbol or `find`/`ls` a filename under `graft/` to land on the card for that file.", "")
	}
	if err := os.WriteFile(filepath.Join(outDir, "INDEX.md"), []byte(strings.Join(lines, "\n")), 0o644); err != nil {
		return fmt.Errorf("write card index: %w", err)
	}
	return nil
}
