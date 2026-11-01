package log

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

// Two processes writing one directory corrupt it: each keeps its own sz and
// next, and recover in one cuts bytes the other is writing. The second Open
// must fail until the first Log is closed.
func TestOpenLocksDir(t *testing.T) {
	dir := t.TempDir()
	l := openLog(t, dir, Options{})
	if l2, err := Open(dir, Options{}); !errors.Is(err, ErrLocked) {
		if l2 != nil {
			l2.Close()
		}
		t.Fatalf("second Open error = %v, want ErrLocked", err)
	}
	l = restart(t, l, dir, Options{}) // Close releases the lock
	appendN(t, l, 1)
}

// A failed Open must release the lock too, or a fixed directory still cannot
// be opened until the process exits.
func TestFailedOpenReleasesLock(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "junk.log"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Open(dir, Options{}); err == nil {
		t.Fatal("Open with junk.log in the directory returned no error")
	}
	if err := os.Remove(filepath.Join(dir, "junk.log")); err != nil {
		t.Fatal(err)
	}
	openLog(t, dir, Options{})
}
