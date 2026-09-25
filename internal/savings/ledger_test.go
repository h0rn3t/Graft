package savings

import (
	"path/filepath"
	"testing"
)

func TestPendingLedgerDrainsOnce(t *testing.T) {
	contextDir := t.TempDir()
	RecordPending(contextDir, 1200)
	RecordPending(contextDir, 0)
	RecordPending(contextDir, 300)
	cacheDir := filepath.Join(contextDir, ".cache")
	if got := DrainPending(cacheDir); got != 1500 {
		t.Errorf("DrainPending() = %d, want 1500", got)
	}
	if got := DrainPending(cacheDir); got != 0 {
		t.Errorf("DrainPending(after drain) = %d, want 0", got)
	}
}
