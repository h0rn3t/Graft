package graph

import (
	"encoding/json/jsontext"
	jsonv2 "encoding/json/v2"
	"fmt"
	"path/filepath"
)

// WriteManifest atomically stores the context-node roster and source hashes.
func WriteManifest(outDir string, manifest Manifest) error {
	data, err := jsonv2.Marshal(manifest, jsontext.WithIndent("  "))
	if err != nil {
		return fmt.Errorf("encode context manifest: %w", err)
	}
	if err := writeAtomicSidecar(filepath.Join(outDir, "manifest.json"), append(data, '\n')); err != nil {
		return fmt.Errorf("write context manifest: %w", err)
	}
	return nil
}
