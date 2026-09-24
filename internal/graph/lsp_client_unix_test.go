//go:build unix

package graph

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestLSPClientCloseKillsWedgedServerGroup(t *testing.T) {
	if _, err := exec.LookPath("sleep"); err != nil {
		t.Skip("sleep is not installed")
	}
	pidFile := filepath.Join(t.TempDir(), "grandchild.pid")
	t.Setenv("GRAFT_LSP_HELPER_PID", pidFile)
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	client := startFakeLSP(t, ctx, "wedge")
	if !client.initialize(ctx) {
		t.Fatalf("initialize() = false, want true")
	}
	closed := make(chan struct{})
	go func() {
		client.close()
		close(closed)
	}()
	select {
	case <-closed:
	case <-time.After(15 * time.Second):
		t.Fatal("close() on a server that ignores shutdown and leaves a helper on stdout did not return")
	}
	data, err := os.ReadFile(pidFile)
	if err != nil {
		t.Fatalf("ReadFile(%q) error = %v, want the helper's pid", pidFile, err)
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(data)))
	if err != nil {
		t.Fatalf("Atoi(%q) error = %v", data, err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for syscall.Kill(pid, 0) == nil {
		if time.Now().After(deadline) {
			_ = syscall.Kill(pid, syscall.SIGKILL)
			t.Fatalf("helper process %d survived close(), want the server's process group killed", pid)
		}
		time.Sleep(20 * time.Millisecond)
	}
}
