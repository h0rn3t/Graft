// Package repoconfig reads and patches a repository's local, git-ignored
// .graft/config.json the way the TypeScript CLI does, so key order and
// formatting survive a round trip through either implementation.
package repoconfig

import (
	"os"
	"path/filepath"
	"strings"

	"github.com/NanoNets/context-graph-engine/internal/jsonjs"
)

// Dir is the local configuration directory under a repository root.
const Dir = ".graft"

// Path is the configuration file for repository root.
func Path(root string) string {
	return filepath.Join(root, Dir, "config.json")
}

// Read returns the parsed configuration, or nil when it is missing or not JSON.
func Read(root string) jsonjs.Value {
	data, err := os.ReadFile(Path(root))
	if err != nil {
		return nil
	}
	value, err := jsonjs.Parse(data)
	if err != nil {
		return nil
	}
	return value
}

// Patch merges fields into the configuration like
// { ...(existing ?? {}), ...patch }; a nil field value removes the key, as
// JSON.stringify drops an undefined one. The .gitignore entry is ensured first.
func Patch(root string, fields []Field) error {
	existing := Read(root)
	config := jsonjs.Spread(existing, existing != nil)
	for _, field := range fields {
		if field.Value == nil {
			config.Delete(field.Key)
			continue
		}
		config.Set(field.Key, field.Value)
	}
	ensureIgnored(root)
	return writeAtomic(Path(root), []byte(jsonjs.Stringify(config, 2)))
}

// Field is one key to set, or to remove when Value is nil.
type Field struct {
	Key   string
	Value jsonjs.Value
}

// ensureIgnored adds /.graft/ to the root .gitignore unless an entry exists.
func ensureIgnored(root string) {
	path := filepath.Join(root, ".gitignore")
	data, _ := os.ReadFile(path) // a missing .gitignore starts empty
	current := string(data)
	for line := range strings.SplitSeq(current, "\n") {
		value := strings.TrimSpace(line)
		if value == Dir || value == Dir+"/" || value == "/"+Dir+"/" {
			return
		}
	}
	gap := ""
	if current != "" {
		gap = "\n\n"
		if strings.HasSuffix(current, "\n") {
			gap = "\n"
		}
	}
	_ = os.WriteFile(path, []byte(current+gap+"# graft's local repository settings — not committed.\n/"+Dir+"/\n"), 0o644) // best-effort
}

func writeAtomic(path string, data []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	temporary, err := os.CreateTemp(filepath.Dir(path), filepath.Base(path)+".*.tmp")
	if err != nil {
		return err
	}
	defer func() { _ = os.Remove(temporary.Name()) }()
	if _, err := temporary.Write(data); err != nil {
		_ = temporary.Close()
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	// The TypeScript writer creates the file with the default mode, not 0600.
	if err := os.Chmod(temporary.Name(), 0o644); err != nil {
		return err
	}
	return os.Rename(temporary.Name(), path)
}
