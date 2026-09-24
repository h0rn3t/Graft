// Package gitx runs git under a deadline and reports git's own complaint when
// a run fails.
package gitx

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"
)

// waitDelay bounds how long Run waits for output pipes after git exits or ctx
// ends, so a grandchild that inherited them cannot hold the caller.
const waitDelay = time.Second

// Run runs git with args in dir and returns its standard output. ctx bounds
// the whole run. On failure the error carries git's standard error, and the
// output read so far is still returned. Git never prompts for credentials, so
// a run inside a hook or CI job cannot block on a terminal.
func Run(ctx context.Context, dir string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, "git", args...) //nolint:gosec // G204: argv, no shell; callers put --end-of-options before refs
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0")
	cmd.WaitDelay = waitDelay
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		name := "git"
		if len(args) > 0 {
			name += " " + args[0]
		}
		if detail := strings.TrimSpace(stderr.String()); detail != "" {
			return stdout.String(), fmt.Errorf("%s: %w: %s", name, err, detail)
		}
		return stdout.String(), fmt.Errorf("%s: %w", name, err)
	}
	return stdout.String(), nil
}
