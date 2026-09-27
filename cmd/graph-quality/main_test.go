package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/h0rn3t/Graft/internal/graphquality"
)

func writeOracleFixture(t *testing.T, source string) (string, string) {
	t.Helper()
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "a.go"), []byte(source), 0o600); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256([]byte(source))
	manifest := graphquality.Manifest{
		Version:    1,
		Sources:    []graphquality.OracleSource{{Path: "a.go", SHA256: hex.EncodeToString(sum[:])}},
		Build:      graphquality.OracleBuild{Extensions: []string{".go"}},
		Partitions: []graphquality.OraclePartition{{Name: "calls", Language: "go", Relation: "calls", Files: []string{"a.go"}}},
	}
	data, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "oracle.json")
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	return root, path
}

func TestRunOracleValidAndHashMismatch(t *testing.T) {
	root, manifest := writeOracleFixture(t, "package demo\nfunc Run() {}\n")
	var stdout, stderr bytes.Buffer
	if status := run([]string{root, "--oracle", manifest, "--json"}, &stdout, &stderr); status != 0 {
		t.Fatalf("run valid status = %d, stderr = %q", status, stderr.String())
	}
	var decoded map[string]any
	if err := json.Unmarshal(stdout.Bytes(), &decoded); err != nil || decoded["oracle"] == nil || decoded["structural"] == nil {
		t.Errorf("oracle JSON = %s; error = %v", stdout.String(), err)
	}
	if err := os.WriteFile(filepath.Join(root, "a.go"), []byte("package demo\nfunc Changed() {}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	stdout.Reset()
	stderr.Reset()
	if status := run([]string{root, "--oracle", manifest}, &stdout, &stderr); status != 2 || !strings.Contains(stderr.String(), "hash mismatch") || stdout.Len() != 0 {
		t.Errorf("run hash mismatch = status %d, stdout %q, stderr %q", status, stdout.String(), stderr.String())
	}
}

func TestRunOracleRejectsUnlistedSource(t *testing.T) {
	root, manifest := writeOracleFixture(t, "package demo\nfunc Run() {}\n")
	if err := os.WriteFile(filepath.Join(root, "extra.go"), []byte("package demo\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	var stdout, stderr bytes.Buffer
	if status := run([]string{root, "--oracle", manifest}, &stdout, &stderr); status != 2 || stdout.Len() != 0 || !strings.Contains(stderr.String(), "inventory differs") {
		t.Errorf("run unlisted source = status %d, stdout %q, stderr %q", status, stdout.String(), stderr.String())
	}
}

func TestRunOracleRejectsEscapeAndIncompleteBuild(t *testing.T) {
	root, manifestPath := writeOracleFixture(t, "package demo\nfunc Run() {}\n")
	data, err := os.ReadFile(manifestPath)
	if err != nil {
		t.Fatal(err)
	}
	var manifest graphquality.Manifest
	if err := json.Unmarshal(data, &manifest); err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(t.TempDir(), "outside.go")
	if err := os.WriteFile(outside, []byte("package demo"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(root, "escape.go")); err != nil {
		t.Fatal(err)
	}
	manifest.Sources[0].Path = "escape.go"
	manifest.Partitions[0].Files = []string{"escape.go"}
	data, err = json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(manifestPath, data, 0o600); err != nil {
		t.Fatal(err)
	}
	var stdout, stderr bytes.Buffer
	if status := run([]string{root, "--oracle", manifestPath}, &stdout, &stderr); status != 2 || stdout.Len() != 0 {
		t.Errorf("run escape status = %d, stdout %q, stderr %q", status, stdout.String(), stderr.String())
	}
	manifest.Sources[0].Path = "../outside.go"
	manifest.Partitions[0].Files = []string{"../outside.go"}
	data, _ = json.Marshal(manifest)
	if err := os.WriteFile(manifestPath, data, 0o600); err != nil {
		t.Fatal(err)
	}
	stderr.Reset()
	if status := run([]string{root, "--oracle", manifestPath}, &stdout, &stderr); status != 2 {
		t.Errorf("run traversal status = %d, want 2", status)
	}
	badRoot, badManifest := writeOracleFixture(t, "package demo\nfunc Broken( {\n")
	stderr.Reset()
	if status := run([]string{badRoot, "--oracle", badManifest}, &stdout, &stderr); status != 2 || !strings.Contains(stderr.String(), "incomplete") {
		t.Errorf("run broken source = status %d, stderr %q", status, stderr.String())
	}
}

func TestRunOracleStrictMismatchReportsFacts(t *testing.T) {
	root, manifest := writeOracleFixture(t, "package demo\nfunc Save() {}\nfunc Run() { Save() }\n")
	var stdout, stderr bytes.Buffer
	status := run([]string{root, "--oracle", manifest, "--json", "--strict"}, &stdout, &stderr)
	if status != 1 || stderr.Len() != 0 {
		t.Fatalf("run strict mismatch = status %d, stderr %q, want 1 and empty", status, stderr.String())
	}
	var decoded struct {
		Oracle struct {
			Partitions []struct {
				FP             int                 `json:"fp"`
				FalsePositives []graphquality.Fact `json:"falsePositives"`
			} `json:"partitions"`
		} `json:"oracle"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &decoded); err != nil {
		t.Fatal(err)
	}
	if len(decoded.Oracle.Partitions) != 1 || decoded.Oracle.Partitions[0].FP != 1 || len(decoded.Oracle.Partitions[0].FalsePositives) != 1 {
		t.Errorf("strict mismatch JSON = %s", stdout.String())
	}
}

func TestRunHelpNamesOracleMode(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if status := run([]string{"--help"}, &stdout, &stderr); status != 0 || !strings.Contains(stdout.String(), "--oracle <manifest.json>") || stderr.Len() != 0 {
		t.Errorf("run --help = status %d, stdout %q, stderr %q", status, stdout.String(), stderr.String())
	}
}
