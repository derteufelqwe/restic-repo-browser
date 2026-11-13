// Package testutil helps integration tests that need a real restic binary.
package testutil

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// Binary returns the path of a restic executable, or skips the test when none
// can be found. Set RESTIC_BIN to point at a specific one.
func Binary(t testing.TB) string {
	t.Helper()
	for _, c := range []string{os.Getenv("RESTIC_BIN"), filepath.Join("..", "..", "restic.exe"), filepath.Join("..", "..", "restic")} {
		if c == "" {
			continue
		}
		if abs, err := filepath.Abs(c); err == nil {
			if _, err := os.Stat(abs); err == nil {
				return abs
			}
		}
	}
	if p, err := exec.LookPath("restic"); err == nil {
		return p
	}
	t.Skip("restic binary not found; set RESTIC_BIN to run integration tests")
	return ""
}

// Restic runs the binary against a repository and fails the test on error.
func Restic(t testing.TB, bin, repo, password string, args ...string) []byte {
	t.Helper()
	cmd := exec.Command(bin, append([]string{"-r", repo}, args...)...)
	cmd.Env = append(os.Environ(), "RESTIC_PASSWORD="+password)
	out, err := cmd.Output()
	if err != nil {
		var stderr []byte
		if ee, ok := err.(*exec.ExitError); ok {
			stderr = ee.Stderr
		}
		t.Fatalf("restic %v: %v\n%s", args, err, stderr)
	}
	return out
}
