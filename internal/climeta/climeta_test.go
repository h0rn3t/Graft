package climeta

import (
	"net/url"
	"os"
	"path/filepath"
	"testing"
)

func TestFormatVersionReport(t *testing.T) {
	tests := []struct {
		name    string
		current string
		latest  NpmViewResult
		want    string
	}{
		{
			name:    "up to date",
			current: "0.4.4",
			latest:  NpmViewResult{OK: true, Version: "0.4.4"},
			want:    "graft 0.4.4\nlatest on npm: 0.4.4 \u2713 up to date",
		},
		{
			name:    "newer version",
			current: "0.4.4",
			latest:  NpmViewResult{OK: true, Version: "0.4.5"},
			want:    "graft 0.4.4\nlatest on npm: 0.4.5 \u2014 run graft upgrade",
		},
		{
			name:    "offline",
			current: "0.4.4",
			latest:  NpmViewResult{OK: false},
			want:    "graft 0.4.4\nlatest: unreachable (offline?)",
		},
	}
	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			got := FormatVersionReport(testCase.current, testCase.latest)
			if got != testCase.want {
				t.Errorf("FormatVersionReport(%q, %#v) = %q, want %q", testCase.current, testCase.latest, got, testCase.want)
			}
		})
	}
}

func TestFormatUpgradeReport(t *testing.T) {
	tests := []struct {
		name   string
		result UpgradeResult
		want   string
	}{
		{
			name:   "npx",
			result: UpgradeResult{OK: true, OldVersion: "0.4.4"},
			want:   "running via npx — npx already fetches the latest graft on every run.\nFor a permanent install: npm install -g @nanonets/graft",
		},
		{
			name:   "success",
			result: UpgradeResult{Ran: true, OK: true, OldVersion: "0.4.4", NewVersion: "0.4.5"},
			want:   "graft 0.4.4 → 0.4.5",
		},
		{
			name:   "failure",
			result: UpgradeResult{Ran: true, OK: false, OldVersion: "0.4.4", ErrorMessage: "ENOENT"},
			want:   "✗ npm install -g @nanonets/graft@latest failed: ENOENT",
		},
	}
	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			got := FormatUpgradeReport(testCase.result)
			if got != testCase.want {
				t.Errorf("FormatUpgradeReport(%#v) = %q, want %q", testCase.result, got, testCase.want)
			}
		})
	}
}

func TestPackageMetadata(t *testing.T) {
	repository := t.TempDir()
	sourceDir := filepath.Join(repository, "src")
	if err := os.Mkdir(sourceDir, 0o755); err != nil {
		t.Fatal(err)
	}
	packageJSON := filepath.Join(repository, "package.json")
	if err := os.WriteFile(packageJSON, []byte(`{"version":"0.19.0"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	moduleURL := (&url.URL{Scheme: "file", Path: filepath.ToSlash(filepath.Join(sourceDir, "cli.ts"))}).String()

	if got, want := ResolvePackageJSONPath(moduleURL), packageJSON; got != want {
		t.Errorf("ResolvePackageJSONPath(%q) = %q, want %q", moduleURL, got, want)
	}
	got, err := ReadCurrentVersion(moduleURL)
	if err != nil {
		t.Fatalf("ReadCurrentVersion(%q) error = %v", moduleURL, err)
	}
	if got != "0.19.0" {
		t.Errorf("ReadCurrentVersion(%q) = %q, want %q", moduleURL, got, "0.19.0")
	}
}

func TestIsRunningViaNpx(t *testing.T) {
	tests := []struct {
		name string
		path string
		want bool
	}{
		{
			name: "npx cache",
			path: filepath.Join(string(filepath.Separator), "Users", "x", ".npm", "_npx", "abc123", "node_modules", "@nanonets", "graft", "dist", "cli.js"),
			want: true,
		},
		{
			name: "global install",
			path: filepath.Join(string(filepath.Separator), "usr", "local", "lib", "node_modules", "@nanonets", "graft", "dist", "cli.js"),
			want: false,
		},
	}
	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			moduleURL := (&url.URL{Scheme: "file", Path: filepath.ToSlash(testCase.path)}).String()
			if got := IsRunningViaNpx(moduleURL); got != testCase.want {
				t.Errorf("IsRunningViaNpx(%q) = %t, want %t", moduleURL, got, testCase.want)
			}
		})
	}
}
