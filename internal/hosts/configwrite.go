package hosts

import (
	"errors"
	"os"
	"path/filepath"
	"strings"

	"github.com/h0rn3t/Graft/internal/jsonjs"
)

// WriteAction is what one config or shim write did.
type WriteAction string

// Config write outcomes.
const (
	ActionCreated     WriteAction = "created"
	ActionUpdated     WriteAction = "updated"
	ActionUnchanged   WriteAction = "unchanged"
	ActionUnparseable WriteAction = "skipped-unparseable"
)

// ConfigWrite reports one config or shim write.
type ConfigWrite struct {
	ID     string
	Path   string
	Action WriteAction
}

// writeOwnedFile writes a graft-owned file idempotently, applying mode on POSIX
// when one is given, and re-applying it when only the mode drifted.
func writeOwnedFile(id, path, content string, mode os.FileMode) (ConfigWrite, error) {
	data, err := os.ReadFile(path)
	existed := err == nil
	if existed && string(data) == content {
		if mode != 0 {
			if info, err := os.Stat(path); err == nil && info.Mode().Perm() != mode {
				if err := os.Chmod(path, mode); err != nil {
					return ConfigWrite{}, err
				}
			}
		}
		return ConfigWrite{ID: id, Path: path, Action: ActionUnchanged}, nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return ConfigWrite{}, err
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		return ConfigWrite{}, err
	}
	if mode != 0 {
		if err := os.Chmod(path, mode); err != nil {
			return ConfigWrite{}, err
		}
	}
	action := ActionCreated
	if existed {
		action = ActionUpdated
	}
	return ConfigWrite{ID: id, Path: path, Action: action}, nil
}

// isGraftEntry reports whether a hooks-config entry is one graft installed.
func isGraftEntry(entry jsonjs.Value) bool {
	if entry == nil {
		entry = ""
	}
	return strings.Contains(jsonjs.Stringify(entry, 0), "graft-hooks.cjs")
}

// readJSONObject loads a JSON config for merging: a missing file is a fresh
// object, a plain object merges, anything else is unparseable and left alone.
func readJSONObject(path string) (*jsonjs.Object, bool, bool) {
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return jsonjs.NewObject(), false, true
	}
	if err != nil {
		return nil, false, false
	}
	value, err := jsonjs.Parse(data)
	if err != nil {
		return nil, true, false
	}
	object, ok := jsonjs.AsObject(value)
	return object, true, ok
}

func writeJSON(path string, value jsonjs.Value) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, []byte(jsonjs.Stringify(value, 2)+"\n"), 0o644)
}

// ensureObject returns object[key] as an object, creating it when absent like
// `object[key] ??= {}`. It reports false when the key holds something else.
func ensureObject(object *jsonjs.Object, key string) (*jsonjs.Object, bool) {
	value, ok := object.Get(key)
	if !ok || value == nil {
		created := jsonjs.NewObject()
		object.Set(key, created)
		return created, true
	}
	child, isObject := jsonjs.AsObject(value)
	return child, isObject
}
