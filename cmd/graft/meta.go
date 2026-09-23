package main

import (
	"encoding/json"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"runtime/debug"
	"strings"

	"github.com/NanoNets/context-graph-engine/internal/climeta"
)

// runVersion prints the installed version and the latest one on npm. An
// unreachable registry is reported, never an error.
func runVersion(stdout io.Writer) int {
	report := climeta.FormatVersionReport(currentVersion(), climeta.GetNpmViewVersion(climeta.PackageName, 0))
	if _, err := io.WriteString(stdout, report+"\n"); err != nil {
		return 1
	}
	return 0
}

// runUpgrade installs the latest package globally unless graft runs under npx.
func runUpgrade(stdout, stderr io.Writer) int {
	result, err := climeta.RunUpgrade(executableURL())
	if err != nil {
		writeDiagnostic(stderr, "%v\n", err)
		return 1
	}
	if _, err := io.WriteString(stdout, climeta.FormatUpgradeReport(result)+"\n"); err != nil {
		return 1
	}
	if result.Ran && !result.OK {
		return 1
	}
	return 0
}

// executableURL is the running binary as a file URL, the form climeta expects
// for the module whose package it resolves.
func executableURL() string {
	executable, err := os.Executable()
	if err != nil {
		return ""
	}
	if resolved, err := filepath.EvalSymlinks(executable); err == nil {
		executable = resolved
	}
	path := filepath.ToSlash(executable)
	if !strings.HasPrefix(path, "/") {
		path = "/" + path
	}
	return (&url.URL{Scheme: "file", Path: path}).String()
}

// packageRoot is the directory of the graft package holding this binary, or
// the source checkout when the binary was built outside a package.
func packageRoot() string {
	if moduleURL := executableURL(); moduleURL != "" {
		path := climeta.ResolvePackageJSONPath(moduleURL)
		if _, err := os.Stat(path); err == nil {
			return filepath.Dir(path)
		}
	}
	if _, source, _, ok := runtime.Caller(0); ok {
		return filepath.Clean(filepath.Join(filepath.Dir(source), "..", ".."))
	}
	return "."
}

// currentVersion is the version of the graft package holding this binary,
// falling back to the source checkout and then the module build info.
func currentVersion() string {
	if moduleURL := executableURL(); moduleURL != "" {
		if version, err := climeta.ReadCurrentVersion(moduleURL); err == nil {
			return version
		}
	}
	if _, source, _, ok := runtime.Caller(0); ok {
		data, err := os.ReadFile(filepath.Join(filepath.Dir(source), "..", "..", "package.json"))
		if err == nil {
			var metadata struct {
				Version string `json:"version"`
			}
			if json.Unmarshal(data, &metadata) == nil && metadata.Version != "" {
				return metadata.Version
			}
		}
	}
	if info, ok := debug.ReadBuildInfo(); ok {
		version := strings.TrimPrefix(info.Main.Version, "v")
		if version != "" && version != "(devel)" {
			return version
		}
	}
	return "0.0.0"
}
