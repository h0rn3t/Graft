package main

import (
	"os"
	"path/filepath"
	"strings"
)

func ensureBuildIgnoreFiles(root, outDir string, noGitignore, noIgnore bool) {
	rel, err := filepath.Rel(root, outDir)
	if err != nil || rel == "." || !filepath.IsLocal(rel) {
		return
	}
	rel = filepath.ToSlash(rel)
	if value := os.Getenv("GRAFT_NO_GITIGNORE"); !noGitignore && (value == "" || value == "0" || value == "false") {
		path := filepath.Join(root, ".gitignore")
		data, _ := os.ReadFile(path) // A missing ignore file starts empty.
		current := string(data)
		present := false
		for line := range strings.SplitSeq(current, "\n") {
			entry := strings.TrimSpace(line)
			if entry == "/"+rel+"/" || entry == rel+"/" || entry == rel {
				present = true
				break
			}
		}
		if !present {
			gap := ""
			if current != "" {
				gap = "\n"
				if !strings.HasSuffix(current, "\n") {
					gap = "\n\n"
				}
			}
			_ = os.WriteFile(path, []byte(current+gap+"# graft's local graph cache — regenerable, not committed (run `graft build`).\n/"+rel+"/\n"), 0o644) // Best-effort convenience file.
		}
	}
	if value := os.Getenv("GRAFT_NO_IGNORE"); !noIgnore && (value == "" || value == "0" || value == "false") {
		path := filepath.Join(root, ".ignore")
		data, _ := os.ReadFile(path) // A missing ignore file starts empty.
		current := string(data)
		present := false
		for line := range strings.SplitSeq(current, "\n") {
			if strings.TrimSpace(line) == "!"+rel+"/" {
				present = true
				break
			}
		}
		if !present {
			gap := ""
			if current != "" {
				gap = "\n"
				if !strings.HasSuffix(current, "\n") {
					gap = "\n\n"
				}
			}
			block := "# graft's cards are gitignored but should stay greppable: ripgrep reads\n" +
				"# .ignore before .gitignore, so this re-admits the tree to search only.\n" +
				"!" + rel + "/\n" + rel + "/.cache/\n" + rel + "/.graph/\n"
			_ = os.WriteFile(path, []byte(current+gap+block), 0o644) // Best-effort convenience file.
		}
	}
}
