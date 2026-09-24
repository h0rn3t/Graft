package main

import (
	"bytes"
	"os"
	"strings"
	"testing"

	"github.com/h0rn3t/Graft/internal/jsonjs"
	"github.com/h0rn3t/Graft/internal/telemetry"
)

func TestRunHookInstallOncePerVersion(t *testing.T) {
	home := openHookTelemetry(t)
	originalHome := hookHomeDir
	hookHomeDir = func() string { return home }
	t.Cleanup(func() { hookHomeDir = originalHome })
	t.Setenv("npm_config_global", "true")

	var stdout, stderr bytes.Buffer
	if status := runWithInput([]string{"_install"}, strings.NewReader(""), &stdout, &stderr); status != 0 || stdout.Len() != 0 || stderr.Len() != 0 {
		t.Fatalf("runWithInput(_install) = (%d, %q, %q), want silent success", status, stdout.String(), stderr.String())
	}
	queued := telemetry.Peek(home)
	if len(queued) != 1 {
		t.Fatalf("telemetry.Peek() = %d events, want one install", len(queued))
	}
	line := jsonjs.Stringify(queued[0], 0)
	for _, want := range []string{`"event":"install"`, `"global":"true"`, `"node_major":`} {
		if !strings.Contains(line, want) {
			t.Errorf("install event = %s, want substring %s", line, want)
		}
	}
	state, err := os.ReadFile(telemetryStatePathForTest(home))
	if err != nil {
		t.Fatalf("os.ReadFile(telemetry state) error = %v, want nil", err)
	}
	if !strings.Contains(string(state), `"installedVersion": "0.19.0"`) {
		t.Errorf("telemetry state = %s, want installedVersion", state)
	}
	if status := runWithInput([]string{"_install"}, strings.NewReader(""), &stdout, &stderr); status != 0 {
		t.Fatalf("second runWithInput(_install) status = %d, want 0", status)
	}
	if queued := telemetry.Peek(home); len(queued) != 1 {
		t.Errorf("telemetry.Peek(after repeat) = %d events, want 1", len(queued))
	}
}

func TestRunHookInstallDisabled(t *testing.T) {
	home := openHookTelemetry(t)
	telemetry.SetEnabled(home, false)
	originalHome := hookHomeDir
	hookHomeDir = func() string { return home }
	t.Cleanup(func() { hookHomeDir = originalHome })

	var stdout, stderr bytes.Buffer
	if status := runWithInput([]string{"_install"}, strings.NewReader(""), &stdout, &stderr); status != 0 || stdout.Len() != 0 || stderr.Len() != 0 {
		t.Fatalf("runWithInput(disabled _install) = (%d, %q, %q), want silent success", status, stdout.String(), stderr.String())
	}
	if queued := telemetry.Peek(home); len(queued) != 0 {
		t.Errorf("telemetry.Peek(disabled) = %d events, want 0", len(queued))
	}
}

func telemetryStatePathForTest(home string) string {
	return home + "/.graft/telemetry.json"
}
