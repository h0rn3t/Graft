package main

import (
	"io"
	"os"
	"path/filepath"
	"runtime/debug"
	"strings"
	"sync"
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

// version is the release version, stamped at build time with
// -ldflags "-X main.version=<version>". Left empty, the module build info
// supplies it: `go install ...@vX.Y.Z` records the tag, and a build inside a
// git checkout records a pseudo-version.
var version string

// currentVersion is the running graft version, computed once per process.
var currentVersion = sync.OnceValue(func() string {
	if version != "" {
		return strings.TrimPrefix(version, "v")
	}
	if info, ok := debug.ReadBuildInfo(); ok {
		if built := strings.TrimPrefix(info.Main.Version, "v"); built != "" && built != "(devel)" {
			return built
		}
	}
	return "0.0.0"
})
