package main

import (
	"os"
	"testing"
)

// TestMain points HOME at a scratch directory, so the startup upkeep that
// commands run never reads or writes the real home directory.
func TestMain(m *testing.M) {
	home, err := os.MkdirTemp("", "graft-cmd-home-")
	if err != nil {
		panic(err)
	}
	for _, name := range []string{"HOME", "USERPROFILE"} {
		if err := os.Setenv(name, home); err != nil {
			panic(err)
		}
	}
	status := m.Run()
	_ = os.RemoveAll(home) // best-effort scratch cleanup
	os.Exit(status)
}
