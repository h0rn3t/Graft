package main

import (
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"runtime/debug"
	"strings"
	"sync"

	"github.com/h0rn3t/Graft/internal/climeta"
)

// runVersion prints the installed version.
func runVersion(stdout io.Writer) int {
	if _, err := io.WriteString(stdout, "graft "+currentVersion()+"\n"); err != nil {
		return 1
	}
	return 0
}

// executablePath is the running binary with symlinks resolved, or "" when the
// platform cannot say.
func executablePath() string {
	executable, err := os.Executable()
	if err != nil {
		return ""
	}
	if resolved, err := filepath.EvalSymlinks(executable); err == nil {
		return resolved
	}
	return executable
}

// packageRoot is the directory of the graft package holding this binary, or
// the source checkout when the binary was built outside a package. The binary
// does not move while it runs, so the answer is computed once.
var packageRoot = sync.OnceValue(func() string {
	if executable := executablePath(); executable != "" {
		path := climeta.ResolvePackageJSONPath(executable)
		if _, err := os.Stat(path); err == nil {
			return filepath.Dir(path)
		}
	}
	if _, source, _, ok := runtime.Caller(0); ok {
		return filepath.Clean(filepath.Join(filepath.Dir(source), "..", ".."))
	}
	return "."
})

// currentVersion is the version of the graft package holding this binary,
// falling back to the source checkout and then the module build info. It is
// computed once per process.
var currentVersion = sync.OnceValue(func() string {
	if executable := executablePath(); executable != "" {
		if version, err := climeta.ReadCurrentVersion(executable); err == nil {
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
})
