package main

import (
	"bytes"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// fakeNpm puts an npm on PATH whose answers come from FAKE_NPM_* variables.
func fakeNpm(t *testing.T) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("the fake npm is a POSIX shell script")
	}
	dir := t.TempDir()
	script := `#!/bin/sh
case "$1" in
view) [ -n "$FAKE_NPM_VIEW" ] || exit 1; echo "$FAKE_NPM_VIEW" ;;
install) echo "installing $*"; exit "${FAKE_NPM_INSTALL_EXIT:-0}" ;;
root) [ -n "$FAKE_NPM_ROOT" ] || exit 1; echo "$FAKE_NPM_ROOT" ;;
esac
`
	if err := os.WriteFile(filepath.Join(dir, "npm"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
}

// packagedBinary builds graft into <pkg>/bin next to a package.json at version.
func packagedBinary(t *testing.T, version string) string {
	t.Helper()
	pkg := t.TempDir()
	if err := os.WriteFile(filepath.Join(pkg, "package.json"), []byte(`{"name":"@nanonets/graft","version":"`+version+`"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	binary := filepath.Join(pkg, "bin", "graft")
	if output, err := exec.Command("go", "build", "-o", binary, ".").CombinedOutput(); err != nil {
		t.Fatalf("go build: %v\n%s", err, output)
	}
	return binary
}

func runBinary(t *testing.T, binary string, args ...string) (int, string, string) {
	t.Helper()
	var stdout, stderr bytes.Buffer
	command := exec.Command(binary, args...)
	command.Stdout, command.Stderr = &stdout, &stderr
	err := command.Run()
	if exit, ok := errors.AsType[*exec.ExitError](err); ok {
		return exit.ExitCode(), stdout.String(), stderr.String()
	}
	if err != nil {
		t.Fatalf("run %s: %v", binary, err)
	}
	return 0, stdout.String(), stderr.String()
}

func TestVersionAndUpgradeContract(t *testing.T) {
	fakeNpm(t)
	binary := packagedBinary(t, "1.2.3")
	globalRoot := t.TempDir()
	if err := os.MkdirAll(filepath.Join(globalRoot, "@nanonets", "graft"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(globalRoot, "@nanonets", "graft", "package.json"), []byte(`{"version":"2.0.0"}`), 0o644); err != nil {
		t.Fatal(err)
	}

	cases := []struct {
		name       string
		env        map[string]string
		args       []string
		wantStatus int
		wantStdout string
	}{
		{"--version", nil, []string{"--version"}, 0, "1.2.3\n"},
		{"-v", nil, []string{"-v"}, 0, "1.2.3\n"},
		{"version up to date", map[string]string{"FAKE_NPM_VIEW": "1.2.3"}, []string{"version"}, 0, "graft 1.2.3\nlatest on npm: 1.2.3 ✓ up to date\n"},
		{"version behind", map[string]string{"FAKE_NPM_VIEW": "1.3.0"}, []string{"version"}, 0, "graft 1.2.3\nlatest on npm: 1.3.0 — run graft upgrade\n"},
		{"version offline", map[string]string{"FAKE_NPM_VIEW": ""}, []string{"version"}, 0, "graft 1.2.3\nlatest: unreachable (offline?)\n"},
		{"upgrade", map[string]string{"FAKE_NPM_ROOT": globalRoot}, []string{"upgrade"}, 0, "installing install -g @nanonets/graft@latest\ngraft 1.2.3 → 2.0.0\n"},
		{"upgrade falls back to npm view", map[string]string{"FAKE_NPM_ROOT": "", "FAKE_NPM_VIEW": "2.1.0"}, []string{"upgrade"}, 0, "installing install -g @nanonets/graft@latest\ngraft 1.2.3 → 2.1.0\n"},
		{"upgrade offline keeps the old version", map[string]string{"FAKE_NPM_ROOT": "", "FAKE_NPM_VIEW": ""}, []string{"upgrade"}, 0, "installing install -g @nanonets/graft@latest\ngraft 1.2.3 → 1.2.3\n"},
		{"upgrade install fails", map[string]string{"FAKE_NPM_INSTALL_EXIT": "3"}, []string{"upgrade"}, 1, "installing install -g @nanonets/graft@latest\n✗ npm install -g @nanonets/graft@latest failed\n"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			for _, name := range []string{"FAKE_NPM_VIEW", "FAKE_NPM_ROOT", "FAKE_NPM_INSTALL_EXIT"} {
				t.Setenv(name, tc.env[name])
			}
			status, stdout, stderr := runBinary(t, binary, tc.args...)
			if status != tc.wantStatus || stdout != tc.wantStdout {
				t.Errorf("graft %s = (%d, %q, %q), want (%d, %q)", strings.Join(tc.args, " "), status, stdout, stderr, tc.wantStatus, tc.wantStdout)
			}
		})
	}
}

func TestUpgradeWithoutPackageFails(t *testing.T) {
	fakeNpm(t)
	dir := t.TempDir()
	binary := filepath.Join(dir, "graft")
	if output, err := exec.Command("go", "build", "-o", binary, ".").CombinedOutput(); err != nil {
		t.Fatalf("go build: %v\n%s", err, output)
	}
	status, stdout, stderr := runBinary(t, binary, "upgrade")
	if status != 1 || stdout != "" || !strings.Contains(stderr, "package.json") {
		t.Errorf("graft upgrade outside a package = (%d, %q, %q), want exit 1 naming package.json", status, stdout, stderr)
	}
}
