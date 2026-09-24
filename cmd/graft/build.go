package main

import (
	"cmp"
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/h0rn3t/Graft/internal/graph"
	"github.com/h0rn3t/Graft/internal/sourcefiles"
	"github.com/h0rn3t/Graft/internal/telemetry"
)

func runBuild(opts callersOptions, stdout, stderr io.Writer) int {
	started := time.Now()
	workspacePrefix := workspaceBuildPrefix(opts)
	root, contextDir, err := resolvePaths(opts, buildPathRules, stderr)
	if err != nil {
		writeDiagnostic(stderr, "✗ %s%v\n", workspacePrefix, err)
		return 1
	}

	var onlyDirs []string
	for _, dir := range opts.onlyDirs {
		dir = strings.ReplaceAll(dir, "\\", "/")
		dir = strings.Trim(filepath.ToSlash(filepath.Clean(dir)), "/")
		if dir == "" || dir == "." || !filepath.IsLocal(filepath.FromSlash(dir)) {
			writeDiagnostic(stderr, "✗ --only-dir: expected a non-empty repo-relative path\n")
			return 1
		}
		onlyDirs = append(onlyDirs, dir)
	}
	for _, name := range opts.includeDirs {
		if strings.HasPrefix(name, ".") {
			writeDiagnostic(stderr, "✗ --include-dir %q: dot-directories are never overridable\n", name)
			return 1
		}
		if strings.ContainsAny(name, `/\\`) {
			writeDiagnostic(stderr, "✗ --include-dir %q: expected a bare directory name, not a path\n", name)
			return 1
		}
	}
	if err := patchBuildConfig(root, opts); err != nil {
		writeDiagnostic(stderr, "✗ %sbuild configuration write failed: %v\n", workspacePrefix, err)
		return 1
	}
	if opts.workspaceChildName == "" {
		workspaceDir := contextDir
		if opts.contextDir == "" {
			workspaceDir = filepath.Join(root, "graft")
		}
		if children, workspace := graph.WorkspaceBuildChildren(root, workspaceDir); workspace {
			return runWorkspaceBuild(opts, root, workspaceDir, children, stdout, stderr)
		}
	}

	var onProgress func(index, total int, file string)
	if opts.workspaceChildName == "" {
		onProgress = func(index, total int, file string) {
			runes := []rune(file)
			if len(runes) > 50 {
				runes = runes[:50]
			}
			writeDiagnostic(stderr, "\rparsing %d/%d: %-50s", index+1, total, string(runes))
		}
	}
	if rootErr, ok := buildRootError(root); ok && opts.workspaceChildName == "" {
		// The TypeScript CLI's top-level handler prints the thrown message alone.
		telemetry.Track("build_failed", []telemetry.Property{{Key: "stage", Value: "graph"}, {Key: "code", Value: telemetry.ErrorCode(rootErr)}},
			telemetry.Context{Repo: root, Home: homeDir(), Version: currentVersion()})
		writeDiagnostic(stderr, "%s\n", rootErr)
		return 1
	}
	built, err := graph.BuildGraph(root, sourcefiles.Options{
		OutDir: contextDir, OnlyDirs: onlyDirs, Extensions: opts.extensions,
		NoReuse: opts.noReuse, NoSeed: opts.contextDir != "" || (opts.workspaceChildName == "" && os.Getenv("GRAFT_DIR") != ""),
		OnProgress: onProgress,
	})
	if err != nil {
		if opts.workspaceChildName == "" {
			writeDiagnostic(stderr, "\n")
		}
		// Only the stage and a code enum; the message stays on this machine.
		telemetry.Track("build_failed", []telemetry.Property{{Key: "stage", Value: "graph"}, {Key: "code", Value: telemetry.ErrorCode(err)}},
			telemetry.Context{Repo: root, Home: homeDir(), Version: currentVersion()})
		writeDiagnostic(stderr, "✗ %sgraph build failed: %v\n", workspacePrefix, err)
		return 1
	}
	if len(built.Unsupported) > 0 || len(built.Errors) > 0 {
		if opts.workspaceChildName == "" {
			writeDiagnostic(stderr, "\n")
		}
		if len(built.Unsupported) > 0 {
			writeDiagnostic(stderr, "✗ %sGo build skipped: %d source file(s) use unsupported language adapters; the existing graph was not changed\n", workspacePrefix, len(built.Unsupported))
			writeDiagnostic(stderr, "  %sexample: %s\n", workspacePrefix, built.Unsupported[0])
		}
		if len(built.Errors) > 0 {
			writeDiagnostic(stderr, "✗ %sGo build skipped: %d source file(s) could not be parsed or read; the existing graph was not changed\n", workspacePrefix, len(built.Errors))
			writeDiagnostic(stderr, "  %sexample: %s\n", workspacePrefix, built.Errors[0])
		}
		return 1
	}
	if opts.lsp {
		lsp := graph.EnrichWithLSP(context.Background(), &built.Graph, root)
		server := lsp.Server
		if server == "" {
			server = "none"
		}
		label := []rune("lsp:" + server)
		if len(label) > 50 {
			label = label[:50]
		}
		writeDiagnostic(stderr, "\rsummarizing %d/%d: %-50s", lsp.Added+1, lsp.Queried, string(label))
	}
	if opts.workspaceChildName == "" {
		writeDiagnostic(stderr, "\n")
	}
	if _, err := graph.Write(built.Graph, contextDir); err != nil {
		writeDiagnostic(stderr, "✗ %sgraph write failed: %v\n", workspacePrefix, err)
		return 1
	}
	if err := graph.WriteAskIndex(contextDir, built.Graph); err != nil {
		writeDiagnostic(stderr, "✗ %sask index write failed: %v\n", workspacePrefix, err)
		return 1
	}
	if err := graph.WriteFingerprint(contextDir, graph.ExtractorID, built.Fingerprints, built.OnlyDirs); err != nil {
		writeDiagnostic(stderr, "✗ %sfingerprint write failed: %v\n", workspacePrefix, err)
		return 1
	}
	ensureBuildIgnoreFiles(root, contextDir, opts.noGitignore, opts.noIgnore)
	cards, err := graph.WriteCards(built.Graph, contextDir)
	if err != nil {
		writeDiagnostic(stderr, "✗ %scard write failed: %v\n", workspacePrefix, err)
		return 1
	}
	if opts.workspaceChildName != "" {
		if _, err := fmt.Fprintf(stdout, "✓ %s/: %d nodes, %d edges, %d cards [%s]\n", opts.workspaceChildName, len(built.Graph.Nodes), len(built.Graph.Edges), cards.Written, strings.Join(built.Graph.Meta.Languages, ", ")); err != nil {
			return 1
		}
		for _, limitation := range built.Limitations {
			writeDiagnostic(stderr, "⚠ %s/: native extractor limitation: %s\n", opts.workspaceChildName, limitation)
		}
		return 0
	}
	counts := make(map[graph.Kind]int)
	order := make([]graph.Kind, 0)
	for _, node := range built.Graph.Nodes {
		if counts[node.Kind] == 0 {
			order = append(order, node.Kind)
		}
		counts[node.Kind]++
	}
	slices.SortStableFunc(order, func(a, b graph.Kind) int { return cmp.Compare(counts[b], counts[a]) })
	labels := make([]string, 0, len(order))
	for _, kind := range order {
		labels = append(labels, fmt.Sprintf("%d %s", counts[kind], kind))
	}
	if _, err := fmt.Fprintf(
		stdout,
		"✓ wiring: %d nodes (%s), %d edges, %d cards [%s]\n  parsed: %d of %d files (%d replayed from cache)\n",
		len(built.Graph.Nodes), strings.Join(labels, ", "), len(built.Graph.Edges), cards.Written, strings.Join(built.Graph.Meta.Languages, ", "), built.Parsed,
		len(built.Fingerprints), built.Reused,
	); err != nil {
		return 1
	}
	if built.SeededFrom != "" {
		if _, err := fmt.Fprintf(stdout, "  seeded: copied a starting graph from %s (git worktree)\n", built.SeededFrom); err != nil {
			return 1
		}
	}
	if _, err := fmt.Fprintf(stdout, "  → %s\n", contextDir); err != nil {
		return 1
	}
	telemetry.Track("build_completed", []telemetry.Property{
		{Key: "files_bucket", Value: telemetry.FilesBucket(len(built.Fingerprints))},
		{Key: "langs", Value: telemetry.LangsValue(built.Graph.Meta.Languages)},
		{Key: "mode", Value: "fast"},
		{Key: "duration_bucket", Value: telemetry.DurationBucket(time.Since(started))},
		{Key: "incremental", Value: strconv.FormatBool(built.Reused > 0)},
	}, telemetry.Context{Repo: root, Home: homeDir(), Version: currentVersion()})
	if cwd, err := os.Getwd(); err == nil {
		rel, err := filepath.Rel(cwd, contextDir)
		if err == nil {
			if rel == "." {
				rel = "graft"
			}
			value := os.Getenv("GRAFT_NO_GITIGNORE")
			footer := "  %s/ is git-ignored (added automatically) — a local cache; teammates run `graft build` to get their own.\n"
			if opts.noGitignore || value != "" {
				footer = "  %s/ is a local cache — add it to your gitignore if you want it untracked.\n"
			}
			if _, err := fmt.Fprintf(stdout, footer, filepath.ToSlash(rel)); err != nil {
				return 1
			}
		}
	}
	for _, limitation := range built.Limitations {
		writeDiagnostic(stderr, "⚠ native extractor limitation: %s\n", limitation)
	}
	return 0
}

func workspaceBuildPrefix(opts callersOptions) string {
	if opts.workspaceChildName == "" {
		return ""
	}
	return opts.workspaceChildName + "/: "
}
