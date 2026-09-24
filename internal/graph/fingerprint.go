package graph

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"maps"
	"math"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/h0rn3t/Graft/internal/sourcefiles"
)

const fingerprintVersion = 1

// Fingerprint records the source stats and hashes used to produce a graph.
type Fingerprint struct {
	Version   int                        `json:"version"`
	Extractor string                     `json:"extractor"`
	Files     map[string]FingerprintFile `json:"files"`
	OnlyDirs  []string                   `json:"onlyDirs,omitempty"`
}

// FingerprintFile stores a source file's stat and decoded-content hash.
// Its JSON form is the TypeScript sidecar tuple [size, mtimeMs, hash].
type FingerprintFile struct {
	Size    int64
	MTimeMS float64
	Hash    string
}

// Drift lists source files that differ from a fingerprint.
type Drift struct {
	Changed []string `json:"changed"`
	Added   []string `json:"added"`
	Removed []string `json:"removed"`
}

// FingerprintPath returns the extractor-specific fingerprint sidecar path.
func FingerprintPath(outDir, extractor string) (string, error) {
	if extractor == "" {
		extractor = "nostamp"
	}
	if extractor == "." || extractor == ".." || strings.ContainsAny(extractor, `/\\`) {
		return "", fmt.Errorf("invalid extractor identity %q", extractor)
	}
	return filepath.Join(outDir, ".cache", "fingerprint."+extractor+".json"), nil
}

// ReadFingerprint reads a valid fingerprint for extractor. Missing, malformed,
// and older-version sidecars are treated as unknown so callers can rebuild once.
func ReadFingerprint(outDir, extractor string) (*Fingerprint, error) {
	if extractor == "" {
		extractor = "nostamp"
	}
	path, err := FingerprintPath(outDir, extractor)
	if err != nil {
		return nil, err
	}
	data, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read graph fingerprint: %w", err)
	}
	var fingerprint Fingerprint
	if err := json.Unmarshal(data, &fingerprint); err != nil {
		return nil, nil
	}
	if fingerprint.Version != fingerprintVersion || fingerprint.Extractor != extractor || fingerprint.Files == nil {
		return nil, nil
	}
	return &fingerprint, nil
}

// ReadFingerprintScope returns the latest usable source-scope fingerprint in
// outDir, including sidecars written by the previous TypeScript CLI. It returns
// nil when no valid scope record is available.
func ReadFingerprintScope(outDir string) *Fingerprint {
	if fingerprint, err := ReadFingerprint(outDir, ExtractorID); err == nil && fingerprint != nil {
		return fingerprint
	}
	entries, err := os.ReadDir(filepath.Join(outDir, ".cache"))
	if err != nil {
		return nil
	}
	var latest *Fingerprint
	var latestTime time.Time
	for _, entry := range entries {
		name := entry.Name()
		if !strings.HasPrefix(name, "fingerprint.") || !strings.HasSuffix(name, ".json") || name == "fingerprint."+ExtractorID+".json" {
			continue
		}
		stamp := strings.TrimSuffix(strings.TrimPrefix(name, "fingerprint."), ".json")
		info, err := entry.Info()
		if err != nil {
			continue
		}
		data, err := os.ReadFile(filepath.Join(outDir, ".cache", name))
		if err != nil {
			continue
		}
		var candidate Fingerprint
		if json.Unmarshal(data, &candidate) != nil || candidate.Version != fingerprintVersion ||
			candidate.Extractor != stamp || candidate.Files == nil {
			continue
		}
		if latest == nil || info.ModTime().After(latestTime) {
			latest = &candidate
			latestTime = info.ModTime()
		}
	}
	return latest
}

// WriteFingerprint atomically writes a fingerprint sidecar. File paths are
// repository-relative POSIX paths, matching GraphV1 and TypeScript cache keys.
func WriteFingerprint(outDir, extractor string, files map[string]FingerprintFile, onlyDirs []string) error {
	path, err := FingerprintPath(outDir, extractor)
	if err != nil {
		return err
	}
	if extractor == "" {
		extractor = "nostamp"
	}
	record := Fingerprint{
		Version:   fingerprintVersion,
		Extractor: extractor,
		Files:     maps.Clone(files),
	}
	if record.Files == nil {
		record.Files = make(map[string]FingerprintFile)
	}
	if len(onlyDirs) > 0 {
		record.OnlyDirs = slices.Clone(onlyDirs)
	}
	data, err := json.Marshal(record)
	if err != nil {
		return fmt.Errorf("encode graph fingerprint: %w", err)
	}
	if err := writeAtomicSidecar(path, data); err != nil {
		return fmt.Errorf("write graph fingerprint: %w", err)
	}
	return nil
}

func writeAtomicSidecar(path string, data []byte) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("create directory: %w", err)
	}
	temporary, err := os.CreateTemp(dir, filepath.Base(path)+".*.tmp")
	if err != nil {
		return fmt.Errorf("create temporary file: %w", err)
	}
	temporaryPath := temporary.Name()
	defer func() {
		_ = temporary.Close()
		_ = os.Remove(temporaryPath)
	}()
	if err := temporary.Chmod(0o644); err != nil {
		return fmt.Errorf("set temporary file permissions: %w", err)
	}
	if _, err := temporary.Write(data); err != nil {
		return fmt.Errorf("write temporary file: %w", err)
	}
	if err := temporary.Close(); err != nil {
		return fmt.Errorf("close temporary file: %w", err)
	}
	if err := os.Rename(temporaryPath, path); err != nil {
		return fmt.Errorf("replace file: %w", err)
	}
	return nil
}

// ProbeDrift compares current source files with the last graph fingerprint.
// A nil result means the fingerprint is missing or belongs to another extractor.
func ProbeDrift(root, outDir, extractor string, opts sourcefiles.Options) (*Drift, error) {
	fingerprint, err := ReadFingerprint(outDir, extractor)
	if err != nil {
		return nil, err
	}
	return probeDrift(root, outDir, opts, fingerprint)
}

// probeDrift compares files with a fingerprint already read by the caller.
func probeDrift(root, outDir string, opts sourcefiles.Options, fingerprint *Fingerprint) (*Drift, error) {
	if fingerprint == nil {
		return nil, nil
	}
	opts.OutDir = outDir
	opts.OnlyDirs = slices.Clone(fingerprint.OnlyDirs)
	if len(opts.Extensions) == 0 {
		opts.Extensions = SourceExtensions()
	}
	files, err := sourcefiles.Walk(root, opts)
	if err != nil {
		return nil, err
	}
	drift := &Drift{
		Changed: make([]string, 0),
		Added:   make([]string, 0),
		Removed: make([]string, 0),
	}
	seen := make(map[string]struct{}, len(files))
	for _, file := range files {
		seen[file.Rel] = struct{}{}
		recorded, ok := fingerprint.Files[file.Rel]
		if !ok {
			drift.Added = append(drift.Added, file.Rel)
			continue
		}
		if os.Getenv("GRAFT_REFRESH") != "hash" && recorded.Hash != "" &&
			recorded.Size == file.Size && recorded.MTimeMS == file.MTimeMS {
			continue
		}
		text, readable, err := sourcefiles.Read(file.Abs)
		if err != nil {
			continue
		}
		if !readable {
			if recorded.Hash != "" {
				drift.Changed = append(drift.Changed, file.Rel)
			}
			continue
		}
		if sourcefiles.Hash(text) != recorded.Hash {
			drift.Changed = append(drift.Changed, file.Rel)
		}
	}
	for path := range fingerprint.Files {
		if _, ok := seen[path]; !ok {
			drift.Removed = append(drift.Removed, path)
		}
	}
	slices.Sort(drift.Changed)
	slices.Sort(drift.Added)
	slices.Sort(drift.Removed)
	return drift, nil
}

// MarshalJSON encodes a file fingerprint as the TypeScript-compatible tuple.
func (f FingerprintFile) MarshalJSON() ([]byte, error) {
	return json.Marshal([3]any{f.Size, f.MTimeMS, f.Hash})
}

// UnmarshalJSON decodes a TypeScript-compatible file fingerprint tuple.
func (f *FingerprintFile) UnmarshalJSON(data []byte) error {
	var values []json.RawMessage
	if err := json.Unmarshal(data, &values); err != nil {
		return err
	}
	if len(values) != 3 {
		return errors.New("fingerprint file must contain size, mtimeMs, and hash")
	}
	if err := json.Unmarshal(values[0], &f.Size); err != nil {
		return err
	}
	if err := json.Unmarshal(values[1], &f.MTimeMS); err != nil {
		return err
	}
	if err := json.Unmarshal(values[2], &f.Hash); err != nil {
		return err
	}
	if f.Size < 0 || math.IsNaN(f.MTimeMS) || math.IsInf(f.MTimeMS, 0) {
		return errors.New("invalid fingerprint file metadata")
	}
	return nil
}
