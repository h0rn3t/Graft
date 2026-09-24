// Package climeta contains the package metadata lookups used by the CLI.
package climeta

import (
	"encoding/json"
	"os"
	"path/filepath"
)

type packageMetadata struct {
	Version string `json:"version"`
}

// ResolvePackageJSONPath finds package.json one level above the directory of
// modulePath, a file system path such as the running executable, or next to
// it.
func ResolvePackageJSONPath(modulePath string) string {
	moduleDir := filepath.Dir(modulePath)
	candidates := []string{
		filepath.Clean(filepath.Join(moduleDir, "..", "package.json")),
		filepath.Join(moduleDir, "package.json"),
	}
	for _, candidate := range candidates {
		if _, err := os.Stat(candidate); err == nil {
			return candidate
		}
	}
	return candidates[0]
}

// ReadCurrentVersion reads the version of the package containing modulePath.
func ReadCurrentVersion(modulePath string) (string, error) {
	data, err := os.ReadFile(ResolvePackageJSONPath(modulePath))
	if err != nil {
		return "", err
	}
	var metadata packageMetadata
	if err := json.Unmarshal(data, &metadata); err != nil {
		return "", err
	}
	if metadata.Version == "" {
		return "0.0.0", nil
	}
	return metadata.Version, nil
}
