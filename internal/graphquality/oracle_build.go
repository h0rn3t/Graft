package graphquality

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"go/parser"
	"go/token"
	"io"
	"os"
	"path"
	"slices"
	"strings"

	"github.com/h0rn3t/Graft/internal/graph"
	"github.com/h0rn3t/Graft/internal/sourcefiles"
)

// BuildOracleFixture verifies pinned files and builds an isolated structural graph.
func BuildOracleFixture(rootPath string, manifest Manifest) (Graph, error) {
	if err := manifest.Validate(); err != nil {
		return Graph{}, err
	}
	root, err := os.OpenRoot(rootPath)
	if err != nil {
		return Graph{}, fmt.Errorf("open fixture root: %w", err)
	}
	defer func() { _ = root.Close() }() // Read-only root; cleanup is best effort.
	if config, err := root.Open(".graft/config.json"); err == nil {
		if err := config.Close(); err != nil {
			return Graph{}, fmt.Errorf("close fixture configuration: %w", err)
		}
		return Graph{}, fmt.Errorf("fixture has persisted .graft/config.json; oracle build configuration would be ambiguous")
	} else if !os.IsNotExist(err) {
		return Graph{}, fmt.Errorf("check fixture configuration: %w", err)
	}
	want := make([]string, 0, len(manifest.Sources))
	decodedHashes := make(map[string]string, len(manifest.Sources))
	for _, source := range manifest.Sources {
		file, err := root.Open(source.Path)
		if err != nil {
			return Graph{}, fmt.Errorf("open fixture source %q: %w", source.Path, err)
		}
		const maxSourceBytes = 16 << 20
		data, readErr := io.ReadAll(io.LimitReader(file, maxSourceBytes+1))
		closeErr := file.Close()
		if readErr != nil {
			return Graph{}, fmt.Errorf("read fixture source %q: %w", source.Path, readErr)
		}
		if closeErr != nil {
			return Graph{}, fmt.Errorf("close fixture source %q: %w", source.Path, closeErr)
		}
		if len(data) > maxSourceBytes {
			return Graph{}, fmt.Errorf("fixture source %q exceeds %d bytes", source.Path, maxSourceBytes)
		}
		sum := sha256.Sum256(data)
		if got := hex.EncodeToString(sum[:]); got != source.SHA256 {
			return Graph{}, fmt.Errorf("fixture source hash mismatch: %s", source.Path)
		}
		decoded, readable := sourcefiles.Decode(data)
		if !readable {
			return Graph{}, fmt.Errorf("incomplete oracle fixture: undecodable source %s", source.Path)
		}
		decodedHashes[source.Path] = sourcefiles.Hash(decoded)
		if strings.EqualFold(path.Ext(source.Path), ".go") {
			if _, err := parser.ParseFile(token.NewFileSet(), source.Path, decoded, parser.AllErrors); err != nil {
				return Graph{}, fmt.Errorf("incomplete oracle fixture: invalid Go syntax in %s: %w", source.Path, err)
			}
		}
		want = append(want, source.Path)
	}
	options := sourcefiles.Options{Extensions: slices.Clone(manifest.Build.Extensions), OnlyDirs: slices.Clone(manifest.Build.OnlyDirs)}
	files, err := sourcefiles.Walk(rootPath, options)
	if err != nil {
		return Graph{}, fmt.Errorf("select fixture files: %w", err)
	}
	got := make([]string, 0, len(files))
	for _, file := range files {
		got = append(got, file.Rel)
	}
	slices.Sort(want)
	slices.Sort(got)
	if !slices.Equal(got, want) {
		return Graph{}, fmt.Errorf("fixture source inventory differs: selected %v, oracle %v", got, want)
	}
	outDir, err := os.MkdirTemp("", "graft-oracle-")
	if err != nil {
		return Graph{}, fmt.Errorf("create oracle scratch: %w", err)
	}
	defer func() { _ = os.RemoveAll(outDir) }() // Temporary scratch is not published.
	options.OutDir = outDir
	options.NoReuse = true
	options.NoSeed = true
	options.NoCacheWrite = true
	built, err := graph.BuildGraph(rootPath, options)
	if err != nil {
		return Graph{}, fmt.Errorf("build oracle fixture: %w", err)
	}
	if len(built.Errors) > 0 || len(built.Unsupported) > 0 || len(built.Limitations) > 0 {
		return Graph{}, fmt.Errorf("incomplete oracle fixture: errors=%v unsupported=%v limitations=%v", built.Errors, built.Unsupported, built.Limitations)
	}
	for file, hash := range decodedHashes {
		if built.Fingerprints[file].Hash != hash {
			return Graph{}, fmt.Errorf("fixture source changed during build: %s", file)
		}
	}
	return built.Graph, nil
}
