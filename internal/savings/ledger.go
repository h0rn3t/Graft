package savings

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// pendingName is the file under graft/.cache where query commands leave the
// tokens they saved, for the agent hook to credit to the session at Stop.
// Retrieval output no longer carries a savings footer, so this ledger is the
// only channel between a query and the session statusline.
const pendingName = "savings-pending"

// RecordPending appends saved tokens to the pending ledger in contextDir.
// Failures are ignored: savings are a display nicety, never a query error.
func RecordPending(contextDir string, tokens int) {
	if tokens <= 0 {
		return
	}
	dir := filepath.Join(contextDir, ".cache")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return
	}
	file, err := os.OpenFile(filepath.Join(dir, pendingName), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return
	}
	defer func() { _ = file.Close() }()
	_, _ = file.WriteString(strconv.Itoa(tokens) + "\n")
}

// DrainPending returns the tokens recorded in cacheDir's pending ledger and
// clears it. The ledger is renamed away before it is read, so a query that
// records meanwhile starts a fresh ledger instead of being lost.
func DrainPending(cacheDir string) int {
	path := filepath.Join(cacheDir, pendingName)
	draining := path + ".draining." + strconv.Itoa(os.Getpid())
	if err := os.Rename(path, draining); err != nil {
		return 0
	}
	data, err := os.ReadFile(draining)
	_ = os.Remove(draining)
	if err != nil {
		return 0
	}
	total := 0
	for line := range strings.SplitSeq(string(data), "\n") {
		if value, err := strconv.Atoi(strings.TrimSpace(line)); err == nil && value > 0 {
			total += value
		}
	}
	return total
}
