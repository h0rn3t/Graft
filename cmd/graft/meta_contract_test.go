package main

import (
	"bytes"
	"errors"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// stampedBinary builds graft with its release version stamped at link time.
func stampedBinary(t *testing.T, version string) string {
	t.Helper()
	binary := filepath.Join(t.TempDir(), "graft")
	if output, err := exec.Command("go", "build", "-ldflags=-X main.version=v"+version, "-o", binary, ".").CombinedOutput(); err != nil {
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

func TestVersionContract(t *testing.T) {
	binary := stampedBinary(t, "1.2.3")
	cases := []struct {
		name       string
		args       []string
		wantStdout string
	}{
		{"--version", []string{"--version"}, "1.2.3\n"},
		{"-v", []string{"-v"}, "1.2.3\n"},
		{"version", []string{"version"}, "graft 1.2.3\n"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			status, stdout, stderr := runBinary(t, binary, tc.args...)
			if status != 0 || stdout != tc.wantStdout {
				t.Errorf("graft %s = (%d, %q, %q), want (0, %q)", strings.Join(tc.args, " "), status, stdout, stderr, tc.wantStdout)
			}
		})
	}
}
