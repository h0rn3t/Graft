package upkeep

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/h0rn3t/Graft/internal/fsutil"
)

// TestReconcileWiringSkipsWhileAnotherProcessHoldsTheLock checks that two
// startups never rewrite the same configs at once: while the wiring lock is
// held, a reconcile leaves the work to the holder and writes nothing.
func TestReconcileWiringSkipsWhileAnotherProcessHoldsTheLock(t *testing.T) {
	root := t.TempDir()
	cacheDir := filepath.Join(root, "graft", ".cache")
	stampPath := filepath.Join(cacheDir, "wiring-stamp.json")
	if err := os.MkdirAll(cacheDir, 0o755); err != nil {
		t.Fatalf("MkdirAll(%q) error = %v, want nil", cacheDir, err)
	}
	const stamp = `{"version":"1.0.0","hosts":["gemini"],"opts":{"global":false},"at":"old"}`
	if err := os.WriteFile(stampPath, []byte(stamp), 0o644); err != nil {
		t.Fatalf("WriteFile(%q) error = %v, want nil", stampPath, err)
	}
	release, err := fsutil.Lock(t.Context(), filepath.Join(cacheDir, "wiring.lock"))
	if err != nil {
		t.Fatalf("fsutil.Lock() error = %v, want nil", err)
	}
	defer release()

	rewrites := 0
	got := ReconcileWiring(root, "", "2.0.0", time.Now(),
		func(string) ([]string, error) { return []string{"gemini"}, nil },
		func(string, []string, WiringOptions) error {
			rewrites++
			return nil
		})
	if got != "" || rewrites != 0 {
		t.Errorf("ReconcileWiring(locked) = (%q, %d rewrites), want (\"\", 0)", got, rewrites)
	}
	data, err := os.ReadFile(stampPath)
	if err != nil {
		t.Fatalf("ReadFile(%q) error = %v, want nil", stampPath, err)
	}
	if string(data) != stamp {
		t.Errorf("ReconcileWiring(locked) stamp = %q, want unchanged %q", data, stamp)
	}
}
