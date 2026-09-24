// Package repoconfig reads and patches a repository's local, git-ignored
// .graft/config.json the way the TypeScript CLI does, so key order and
// formatting survive a round trip through either implementation.
package repoconfig

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/h0rn3t/Graft/internal/fsutil"
	"github.com/h0rn3t/Graft/internal/jsonjs"
)

// Dir is the local configuration directory under a repository root.
const Dir = ".graft"

// Path is the configuration file for repository root.
func Path(root string) string {
	return filepath.Join(root, Dir, "config.json")
}

// Read returns the parsed configuration, or nil when the file is missing. A
// file that cannot be read or is not JSON is an error, so a caller never
// mistakes a broken config for an empty one.
func Read(root string) (jsonjs.Value, error) {
	path := Path(root)
	data, err := os.ReadFile(path) //nolint:gosec // G304: the repository's own config path
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", path, err)
	}
	value, err := jsonjs.Parse(data)
	if err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	return value, nil
}

// Patch merges fields into the configuration like
// { ...(existing ?? {}), ...patch }; a nil field value removes the key, as
// JSON.stringify drops an undefined one. The .gitignore entry is ensured first.
// A configuration that cannot be read or parsed is left untouched and its
// error returned, so a patch never discards the settings it could not read.
func Patch(root string, fields []Field) error {
	existing, err := Read(root)
	if err != nil {
		return err
	}
	config := jsonjs.Spread(existing, existing != nil)
	for _, field := range fields {
		if field.Value == nil {
			config.Delete(field.Key)
			continue
		}
		config.Set(field.Key, field.Value)
	}
	if err := ensureIgnored(root); err != nil {
		return err
	}
	return fsutil.WriteFileAtomic(Path(root), []byte(jsonjs.Stringify(config, 2)), 0o644)
}

// Field is one key to set, or to remove when Value is nil.
type Field struct {
	Key   string
	Value jsonjs.Value
}

// ensureIgnored adds /.graft/ to the root .gitignore unless an entry exists,
// so the local settings are never committed by accident.
func ensureIgnored(root string) error {
	path := filepath.Join(root, ".gitignore")
	data, err := os.ReadFile(path) //nolint:gosec // G304: the repository's own .gitignore
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("read %s: %w", path, err)
	}
	current := string(data)
	for line := range strings.SplitSeq(current, "\n") {
		value := strings.TrimSpace(line)
		if value == Dir || value == Dir+"/" || value == "/"+Dir+"/" {
			return nil
		}
	}
	gap := ""
	if current != "" {
		gap = "\n\n"
		if strings.HasSuffix(current, "\n") {
			gap = "\n"
		}
	}
	if err := fsutil.WriteFileAtomic(path, []byte(current+gap+"# graft's local repository settings — not committed.\n/"+Dir+"/\n"), 0o644); err != nil {
		return fmt.Errorf("add /%s/ to %s: %w", Dir, path, err)
	}
	return nil
}
