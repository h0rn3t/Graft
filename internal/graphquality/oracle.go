package graphquality

import (
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"path"
	"slices"
	"strings"

	"github.com/h0rn3t/Graft/internal/graph"
)

// Manifest describes a complete, reviewed set of facts in bounded fixture scopes.
type Manifest struct {
	Version     int               `json:"version"`
	Sources     []OracleSource    `json:"sources"`
	Build       OracleBuild       `json:"build"`
	Partitions  []OraclePartition `json:"partitions"`
	Limitations []string          `json:"limitations,omitempty"`
}

// OracleSource pins a fixture file to its raw bytes.
type OracleSource struct {
	Path   string `json:"path"`
	SHA256 string `json:"sha256"`
}

// OracleBuild selects source files for a structural fixture build.
type OracleBuild struct {
	Extensions []string `json:"extensions"`
	OnlyDirs   []string `json:"onlyDirs,omitempty"`
}

// Fact identifies a graph relation without treating confidence as identity.
type Fact struct {
	Source   string `json:"source"`
	Relation string `json:"relation"`
	Target   string `json:"target"`
}

// OraclePartition is a complete fact set for one relation and a set of files.
type OraclePartition struct {
	Name     string   `json:"name"`
	Language string   `json:"language"`
	Relation string   `json:"relation"`
	Files    []string `json:"files"`
	Expected []Fact   `json:"expected"`
}

var oracleRelations = []string{"contains", "calls", "imports", "references", "implements", "extends"}

// DecodeManifest reads a bounded, strict JSON oracle manifest.
func DecodeManifest(reader io.Reader) (Manifest, error) {
	const maxBytes = 1 << 20
	data, err := io.ReadAll(io.LimitReader(reader, maxBytes+1))
	if err != nil {
		return Manifest{}, fmt.Errorf("read oracle: %w", err)
	}
	if len(data) > maxBytes {
		return Manifest{}, fmt.Errorf("oracle exceeds %d bytes", maxBytes)
	}
	var manifest Manifest
	decoder := json.NewDecoder(strings.NewReader(string(data)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&manifest); err != nil {
		return Manifest{}, fmt.Errorf("decode oracle: %w", err)
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		return Manifest{}, fmt.Errorf("oracle contains trailing data")
	}
	if err := manifest.Validate(); err != nil {
		return Manifest{}, err
	}
	return manifest, nil
}

// Validate checks manifest structure and closed-world scope declarations.
func (m Manifest) Validate() error {
	if m.Version != 1 {
		return fmt.Errorf("unsupported oracle version %d", m.Version)
	}
	if len(m.Sources) == 0 || len(m.Partitions) == 0 {
		return fmt.Errorf("oracle needs sources and partitions")
	}
	supported := graph.SourceExtensions()
	if len(m.Build.Extensions) == 0 {
		return fmt.Errorf("oracle build needs extensions")
	}
	seenExtensions := make(map[string]bool)
	for _, ext := range m.Build.Extensions {
		if !slices.Contains(supported, ext) || seenExtensions[ext] {
			return fmt.Errorf("unsupported or duplicate extension %q", ext)
		}
		seenExtensions[ext] = true
	}
	for _, dir := range m.Build.OnlyDirs {
		if !safeOraclePath(dir) {
			return fmt.Errorf("unsafe onlyDir %q", dir)
		}
	}
	sources := make(map[string]bool, len(m.Sources))
	for _, source := range m.Sources {
		if !safeOraclePath(source.Path) || sources[source.Path] {
			return fmt.Errorf("unsafe or duplicate source %q", source.Path)
		}
		if len(source.SHA256) != 64 || strings.ToLower(source.SHA256) != source.SHA256 {
			return fmt.Errorf("invalid SHA-256 for %q", source.Path)
		}
		if _, err := hex.DecodeString(source.SHA256); err != nil {
			return fmt.Errorf("invalid SHA-256 for %q: %w", source.Path, err)
		}
		sources[source.Path] = true
	}
	partitionNames := make(map[string]bool)
	assessed := make(map[Fact]bool)
	for _, partition := range m.Partitions {
		if partition.Name == "" || partitionNames[partition.Name] {
			return fmt.Errorf("empty or duplicate partition %q", partition.Name)
		}
		partitionNames[partition.Name] = true
		if partition.Language == "" || !slices.Contains(oracleRelations, partition.Relation) || len(partition.Files) == 0 {
			return fmt.Errorf("invalid scope in partition %q", partition.Name)
		}
		files := make(map[string]bool)
		for _, file := range partition.Files {
			if !sources[file] || files[file] || oracleLanguage(file) != partition.Language {
				return fmt.Errorf("unknown, duplicate, or wrong-language file %q in partition %q", file, partition.Name)
			}
			files[file] = true
			key := Fact{Source: file, Relation: partition.Relation}
			if assessed[key] {
				return fmt.Errorf("overlapping assessment for %s %s", file, partition.Relation)
			}
			assessed[key] = true
		}
		facts := make(map[Fact]bool)
		for _, fact := range partition.Expected {
			if fact.Relation != partition.Relation || fact.Target == "" || !factSourceInFiles(fact.Source, files) || facts[fact] {
				return fmt.Errorf("invalid or duplicate fact in partition %q: %+v", partition.Name, fact)
			}
			facts[fact] = true
		}
	}
	return nil
}

func oracleLanguage(file string) string {
	switch strings.ToLower(path.Ext(file)) {
	case ".go":
		return "go"
	case ".py", ".pyi":
		return "python"
	case ".ts", ".tsx", ".mts", ".cts":
		return "typescript"
	case ".js", ".jsx", ".mjs", ".cjs":
		return "javascript"
	case ".java":
		return "java"
	case ".rs":
		return "rust"
	case ".sql":
		return "sql"
	case ".c", ".h":
		return "c"
	case ".cpp", ".cc", ".cxx", ".hpp", ".hh":
		return "cpp"
	default:
		return ""
	}
}

func safeOraclePath(value string) bool {
	return value != "" && value != "." && !strings.Contains(value, "\\") &&
		!strings.HasPrefix(value, "/") && path.Clean(value) == value && !strings.HasPrefix(value, "../") && value != ".."
}

func factSourceInFiles(source string, files map[string]bool) bool {
	for file := range files {
		if source == file || strings.HasPrefix(source, file+"#") {
			return true
		}
	}
	return false
}
