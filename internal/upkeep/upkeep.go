// Package upkeep keeps a repository's agent wiring current at startup,
// failing soft so a hook or MCP server never breaks on derived state.
package upkeep

import (
	"fmt"
	"os"
	"path/filepath"
)

// writeAtomicFile replaces path with data through a temporary file and a rename.
func writeAtomicFile(path string, data []byte, name string) error {
	directory := filepath.Dir(path)
	if err := os.MkdirAll(directory, 0o755); err != nil {
		return fmt.Errorf("create %s cache directory: %w", name, err)
	}
	tmp, err := os.CreateTemp(directory, ".cache-*.tmp")
	if err != nil {
		return fmt.Errorf("create %s cache temporary file: %w", name, err)
	}
	temporary := tmp.Name()
	defer func() { _ = os.Remove(temporary) }()
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("write %s cache: %w", name, err)
	}
	if err := tmp.Chmod(0o644); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("set %s cache mode: %w", name, err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close %s cache: %w", name, err)
	}
	if err := os.Rename(temporary, path); err != nil {
		return fmt.Errorf("replace %s cache: %w", name, err)
	}
	return nil
}
