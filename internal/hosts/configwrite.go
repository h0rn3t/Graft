package hosts

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
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

// writeOwnedFile writes a graft-owned file only when its content differs,
// applying mode on POSIX when one is given, and re-applying it when only the
// mode drifted.
func (f *files) writeOwnedFile(id, path, content string, mode os.FileMode) (ConfigWrite, error) {
	data, err := f.readFile(path)
	existed := !errors.Is(err, fs.ErrNotExist)
	if err == nil && string(data) == content {
		if mode != 0 {
			if info, err := f.stat(path); err == nil && info.Mode().Perm() != mode {
				if err := f.chmod(path, mode); err != nil {
					return ConfigWrite{}, err
				}
			}
		}
		return ConfigWrite{ID: id, Path: path, Action: ActionUnchanged}, nil
	}
	perm := mode
	if perm == 0 {
		perm = 0o644
	}
	if err := f.writeFile(path, []byte(content), perm); err != nil {
		return ConfigWrite{}, err
	}
	if mode != 0 {
		if err := f.chmod(path, mode); err != nil {
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
// object and a plain object merges. Anything else reports ok=false and is left
// alone; a read failure is returned as the error.
func (f *files) readJSONObject(path string) (object *jsonjs.Object, existed, ok bool, err error) {
	data, err := f.readFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return jsonjs.NewObject(), false, true, nil
	}
	if err != nil {
		return nil, true, false, fmt.Errorf("read %s: %w", path, err)
	}
	value, err := jsonjs.Parse(data)
	if err != nil {
		return nil, true, false, nil
	}
	object, ok = jsonjs.AsObject(value)
	return object, true, ok, nil
}

func (f *files) writeJSON(path string, value jsonjs.Value) error {
	return f.writeFile(path, []byte(jsonjs.Stringify(value, 2)+"\n"), 0o644)
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
