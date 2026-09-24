// Package climeta contains the package metadata lookups used by the CLI.
package climeta

import (
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
)

type packageMetadata struct {
	Version string `json:"version"`
}

func fileURLPath(moduleURL string) (string, error) {
	parsed, err := url.Parse(moduleURL)
	if err != nil {
		return "", err
	}
	if parsed.Scheme != "file" {
		return "", fmt.Errorf("module URL must use the file scheme")
	}
	if parsed.Host != "" && parsed.Host != "localhost" {
		return "", fmt.Errorf("unsupported file URL host %q", parsed.Host)
	}
	path, err := url.PathUnescape(parsed.EscapedPath())
	if err != nil {
		return "", err
	}
	return filepath.FromSlash(path), nil
}

// ResolvePackageJSONPath finds package.json next to the module or one level above it.
func ResolvePackageJSONPath(moduleURL string) string {
	modulePath, err := fileURLPath(moduleURL)
	if err != nil {
		modulePath = moduleURL
	}
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

// ReadCurrentVersion reads the version of the package containing moduleURL.
func ReadCurrentVersion(moduleURL string) (string, error) {
	data, err := os.ReadFile(ResolvePackageJSONPath(moduleURL))
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
