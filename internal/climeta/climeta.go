// Package climeta contains the version and upgrade behavior used by the CLI.
package climeta

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

const PackageName = "@nanonets/graft"

const defaultNpmViewTimeout = 2 * time.Second

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

// IsRunningViaNpx reports whether moduleURL is inside an npx cache directory.
func IsRunningViaNpx(moduleURL string) bool {
	modulePath, err := fileURLPath(moduleURL)
	if err != nil {
		return false
	}
	return strings.Contains(filepath.ToSlash(modulePath), "/_npx/")
}

type NpmViewResult struct {
	OK      bool
	Version string
}

// GetNpmViewVersion reads npm's published version without failing the CLI offline.
func GetNpmViewVersion(pkgName string, timeout time.Duration) NpmViewResult {
	if pkgName == "" {
		pkgName = PackageName
	}
	if timeout == 0 {
		timeout = defaultNpmViewTimeout
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	output, err := exec.CommandContext(ctx, "npm", "view", pkgName, "version").Output()
	if err != nil || ctx.Err() != nil {
		return NpmViewResult{}
	}
	version := strings.TrimSpace(string(output))
	if version == "" {
		return NpmViewResult{}
	}
	return NpmViewResult{OK: true, Version: version}
}

// FormatVersionReport formats the result of the version command.
func FormatVersionReport(current string, latest NpmViewResult) string {
	lines := []string{"graft " + current}
	switch {
	case !latest.OK || latest.Version == "":
		lines = append(lines, "latest: unreachable (offline?)")
	case latest.Version == current:
		lines = append(lines, "latest on npm: "+current+" \u2713 up to date")
	default:
		lines = append(lines, "latest on npm: "+latest.Version+" \u2014 run graft upgrade")
	}
	return strings.Join(lines, "\n")
}

func globalRoot() (string, bool) {
	output, err := exec.Command("npm", "root", "-g").Output()
	if err != nil {
		return "", false
	}
	root := strings.TrimSpace(string(output))
	return root, root != ""
}

// ReadGlobalInstalledVersion reads the version in npm's global package directory.
func ReadGlobalInstalledVersion(pkgName string) (string, bool) {
	if pkgName == "" {
		pkgName = PackageName
	}
	root, ok := globalRoot()
	if !ok {
		return "", false
	}
	pkgJSON := filepath.Join(append([]string{root}, strings.Split(pkgName, "/")...)...)
	pkgJSON = filepath.Join(pkgJSON, "package.json")
	data, err := os.ReadFile(pkgJSON)
	if err != nil {
		return "", false
	}
	var metadata packageMetadata
	if err := json.Unmarshal(data, &metadata); err != nil || metadata.Version == "" {
		return "", false
	}
	return metadata.Version, true
}

type UpgradeResult struct {
	Ran          bool
	OK           bool
	ErrorMessage string
	OldVersion   string
	NewVersion   string
}

// FormatUpgradeReport formats a completed upgrade operation.
func FormatUpgradeReport(result UpgradeResult) string {
	if !result.Ran {
		return "running via npx — npx already fetches the latest graft on every run.\n" +
			"For a permanent install: npm install -g @nanonets/graft"
	}
	if !result.OK {
		message := "✗ npm install -g " + PackageName + "@latest failed"
		if result.ErrorMessage != "" {
			message += ": " + result.ErrorMessage
		}
		return message
	}
	oldVersion := result.OldVersion
	if oldVersion == "" {
		oldVersion = "?"
	}
	newVersion := result.NewVersion
	if newVersion == "" {
		newVersion = result.OldVersion
		if newVersion == "" {
			newVersion = "?"
		}
	}
	return "graft " + oldVersion + " → " + newVersion
}

// RunUpgrade installs the latest package unless the CLI is running through npx.
func RunUpgrade(moduleURL string) (UpgradeResult, error) {
	oldVersion, err := ReadCurrentVersion(moduleURL)
	if err != nil {
		return UpgradeResult{}, err
	}
	result := UpgradeResult{Ran: true, OldVersion: oldVersion}
	if IsRunningViaNpx(moduleURL) {
		result.Ran = false
		result.OK = true
		return result, nil
	}

	command := exec.Command("npm", "install", "-g", PackageName+"@latest")
	command.Stdin = os.Stdin
	command.Stdout = os.Stdout
	command.Stderr = os.Stderr
	if err := command.Run(); err != nil {
		if _, ok := errors.AsType[*exec.ExitError](err); !ok {
			result.ErrorMessage = err.Error()
		}
		return result, nil
	}

	result.OK = true
	if version, ok := ReadGlobalInstalledVersion(PackageName); ok {
		result.NewVersion = version
		return result, nil
	}
	if version := GetNpmViewVersion(PackageName, defaultNpmViewTimeout).Version; version != "" {
		result.NewVersion = version
		return result, nil
	}
	result.NewVersion = oldVersion
	return result, nil
}
