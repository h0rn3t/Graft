package gitx

import (
	"context"
	"errors"
	"os/exec"
	"strings"
	"testing"
)

func TestRun(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skipf("git not installed: %v", err)
	}
	dir := t.TempDir()

	t.Run("returns standard output", func(t *testing.T) {
		got, err := Run(t.Context(), dir, "--version")
		if err != nil || !strings.HasPrefix(got, "git version") {
			t.Errorf("Run(--version) = %q, %v, want \"git version …\", nil", got, err)
		}
	})
	t.Run("a failure carries git's stderr", func(t *testing.T) {
		_, err := Run(t.Context(), dir, "rev-parse", "--verify", "HEAD")
		if err == nil || !strings.Contains(err.Error(), "git rev-parse:") || !strings.Contains(err.Error(), "not a git repository") {
			t.Errorf("Run(rev-parse outside a repo) error = %v, want git's own complaint", err)
		}
		if _, ok := errors.AsType[*exec.ExitError](err); !ok {
			t.Errorf("Run(rev-parse outside a repo) error = %v, want an *exec.ExitError in the chain", err)
		}
	})
	t.Run("a done context stops the run", func(t *testing.T) {
		ctx, cancel := context.WithCancel(t.Context())
		cancel()
		if _, err := Run(ctx, dir, "--version"); err == nil {
			t.Errorf("Run(cancelled ctx) error = nil, want the cancellation")
		}
	})
}
