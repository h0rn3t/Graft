package graph

import (
	"context"
	jsonv2 "encoding/json/v2"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"

	"github.com/h0rn3t/Graft/internal/fsutil"
)

const lspPassVersion = 1

// lspPassRecord is what a build's language-server pass leaves in the context
// directory: the server and the content hash of every file it was run on, so
// the next build asks it only about code that changed since.
type lspPassRecord struct {
	Version   int    `json:"version"`
	Extractor string `json:"extractor"`
	Server    string `json:"server"`
	// Unanswered records that the server gave no answer for these files.
	Unanswered bool              `json:"unanswered,omitzero"`
	Files      map[string]string `json:"files"`
}

// LSPPassOptions chooses what a build's language-server pass does.
type LSPPassOptions struct {
	// Ask starts the language server for the code it has no answer for yet.
	Ask bool
	// Reuse starts from the pass recorded in the context directory and keeps
	// its edges for code unchanged since; without it every function is asked.
	Reuse bool
}

// LSPPass is the outcome of a build's language-server pass, which
// WriteLSPPass records once the graph it enriched is written.
type LSPPass struct {
	LSPResult
	// Kept reports that no code the server reads changed since its last
	// complete pass, so its edges were kept and it was not started.
	Kept bool
	// Skipped reports that the server gave no answer for these same files
	// last time, so it was not started again.
	Skipped bool

	record     *lspPassRecord // the record to write; nil removes it
	keepRecord bool           // leaves the record on file as it is
}

// RunLSPPass adds a language server's call edges to graph, built from files
// under root, when options.Ask is set and a server for its languages is
// installed.
//
// With options.Reuse it starts from the pass recorded in outDir. The edges
// that pass found for code unchanged since are kept, and the server is asked
// only about functions in changed files and functions whose code or last
// answer names a changed file. A server that gave no answer for the same
// files is not started again. Without a record, or without Reuse, every
// function is asked. A pass that does not ask still keeps the recorded edges
// of unchanged code, so a build without the server loses none of them.
func RunLSPPass(ctx context.Context, graph *GraphV1, root, outDir string, files map[string]FingerprintFile, options LSPPassOptions) LSPPass {
	var record *lspPassRecord
	var prior *GraphV1
	if options.Reuse {
		if record = readLSPPassRecord(outDir); record != nil {
			prior, _ = Read(WiringPath(outDir))
		}
	}
	if prior == nil {
		record = nil // The record vouches for edges no longer on file.
	}
	var server lspServer
	found := false
	if options.Ask {
		languages := make(map[string]struct{})
		for _, node := range graph.Nodes {
			if language := lspLanguage(node.Path); language != "" {
				languages[language] = struct{}{}
			}
		}
		server, found = pickLSPServer(languages)
	}
	if !found {
		if record == nil {
			return LSPPass{}
		}
		carryOverLSPEdges(graph, *prior, nil)
		return LSPPass{keepRecord: true}
	}

	var ask func(NodeV1) bool
	if record != nil && record.Server == server.command && record.Extractor == ExtractorID {
		changed := lspChangedFiles(record.Files, files)
		switch {
		case record.Unanswered && len(changed) == 0:
			carryOverLSPEdges(graph, *prior, nil)
			return LSPPass{Server: server.command, Skipped: true, keepRecord: true}
		case !record.Unanswered:
			requery := lspRequery(*graph, *prior, changed, server.languages)
			carryOverLSPEdges(graph, *prior, requery)
			if len(requery) == 0 {
				return LSPPass{
					Server:   server.command,
					Complete: true,
					Kept:     true,
					record:   newLSPPassRecord(server, files, false),
				}
			}
			ask = func(node NodeV1) bool { return requery[node.ID] }
		}
	}
	pass := LSPPass{LSPResult: enrichWithLSP(ctx, graph, root, server, ask)}
	switch {
	case pass.Complete:
		pass.record = newLSPPassRecord(server, files, false)
	case !pass.Answered:
		pass.record = newLSPPassRecord(server, files, true)
	}
	return pass
}

// WriteLSPPass records pass in outDir, so the next build starts from it. It
// is called once the graph pass enriched is written, since the record
// vouches for that graph's edges.
func WriteLSPPass(outDir string, pass LSPPass) error {
	if pass.keepRecord {
		return nil
	}
	path := lspPassPath(outDir)
	if pass.record == nil {
		if err := os.Remove(path); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return fmt.Errorf("remove language-server pass record: %w", err)
		}
		return nil
	}
	data, err := jsonv2.Marshal(pass.record, jsonv2.Deterministic(true))
	if err != nil {
		return fmt.Errorf("encode language-server pass record: %w", err)
	}
	if err := fsutil.WriteFileAtomic(path, data, 0o644); err != nil {
		return fmt.Errorf("write language-server pass record: %w", err)
	}
	return nil
}

func lspPassPath(outDir string) string {
	return filepath.Join(outDir, ".cache", "lsp-pass.json")
}

func readLSPPassRecord(outDir string) *lspPassRecord {
	data, err := os.ReadFile(lspPassPath(outDir))
	if err != nil {
		return nil
	}
	var record lspPassRecord
	if jsonv2.Unmarshal(data, &record) != nil || record.Version != lspPassVersion || record.Files == nil {
		return nil
	}
	return &record
}

func newLSPPassRecord(server lspServer, files map[string]FingerprintFile, unanswered bool) *lspPassRecord {
	return &lspPassRecord{
		Version: lspPassVersion, Extractor: ExtractorID, Server: server.command,
		Unanswered: unanswered, Files: lspFileHashes(files),
	}
}

func lspFileHashes(files map[string]FingerprintFile) map[string]string {
	hashes := make(map[string]string, len(files))
	for path, file := range files {
		hashes[path] = file.Hash
	}
	return hashes
}

// lspChangedFiles lists the files whose content differs from the recorded
// hashes, files added or removed since included.
func lspChangedFiles(recorded map[string]string, files map[string]FingerprintFile) map[string]bool {
	changed := make(map[string]bool)
	for path, file := range files {
		if hash := recorded[path]; hash == "" || hash != file.Hash {
			changed[path] = true
		}
	}
	for path := range recorded {
		if _, ok := files[path]; !ok {
			changed[path] = true
		}
	}
	return changed
}

// lspRequery returns the IDs of the functions and methods in the server's
// languages whose outgoing calls a change to the changed files can alter:
// those in a changed file, and those with an edge into one, in graph or among
// prior's language-server edges. A call resolves through the declarations it
// names, so a caller in an unchanged file is asked again when a declaration it
// names may have changed.
func lspRequery(graph, prior GraphV1, changed map[string]bool, languages []string) map[string]bool {
	requery := make(map[string]bool)
	if len(changed) == 0 {
		return requery
	}
	asked := make(map[string]bool)
	pathOf := make(map[string]string, len(graph.Nodes))
	for _, node := range graph.Nodes {
		pathOf[node.ID] = node.Path
		if (node.Kind == "function" || node.Kind == "method") && slices.Contains(languages, lspLanguage(node.Path)) {
			asked[node.ID] = true
			if changed[node.Path] {
				requery[node.ID] = true
			}
		}
	}
	for _, edge := range graph.Edges {
		if asked[edge.Source] && changed[pathOf[edge.Target]] {
			requery[edge.Source] = true
		}
	}
	priorPath := make(map[string]string, len(prior.Nodes))
	for _, node := range prior.Nodes {
		priorPath[node.ID] = node.Path
	}
	for _, edge := range prior.Edges {
		if edge.Confidence == "lsp_resolved" && asked[edge.Source] && changed[priorPath[edge.Target]] {
			requery[edge.Source] = true
		}
	}
	return requery
}
