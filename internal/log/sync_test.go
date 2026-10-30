package log

import (
	"errors"
	"os"
	"testing"
)

func TestSync(t *testing.T) {
	l := openLog(t, t.TempDir(), Options{})
	if err := l.Sync(); err != nil {
		t.Errorf("Sync on an empty log: %v", err)
	}
	appendN(t, l, 3)
	if err := l.Sync(); err != nil {
		t.Errorf("Sync after appends: %v", err)
	}
}

// A Sync that cannot reach the disk must say so. Swallowing this error is how
// databases lost data for years (search "fsyncgate", PostgreSQL, 2018).
func TestSyncAfterCloseFails(t *testing.T) {
	l, err := Open(t.TempDir(), Options{})
	if err != nil {
		t.Fatal(err)
	}
	appendN(t, l, 1)
	if err := l.Close(); err != nil {
		t.Fatal(err)
	}
	if err := l.Sync(); !errors.Is(err, os.ErrClosed) {
		t.Errorf("Sync after Close: error = %v, want os.ErrClosed", err)
	}
}

// benchAppend measures Append with an fsync every syncEvery records (0 = never).
// Run all three and compare:
//
//	go test ./internal/log -run '^$' -bench Append -count 3
func benchAppend(b *testing.B, syncEvery int) {
	l, err := Open(b.TempDir(), Options{})
	if err != nil {
		b.Fatal(err)
	}
	defer l.Close()
	r := rec(0)
	b.SetBytes(int64(encodedLen(b, r)))

	n := 0
	for b.Loop() {
		if _, err := l.Append(r); err != nil {
			b.Fatal(err)
		}
		n++
		if syncEvery > 0 && n%syncEvery == 0 {
			if err := l.Sync(); err != nil {
				b.Fatal(err)
			}
		}
	}
}

func BenchmarkAppendNoSync(b *testing.B)   { benchAppend(b, 0) }
func BenchmarkAppendSyncEach(b *testing.B) { benchAppend(b, 1) }
func BenchmarkAppendSync64(b *testing.B)   { benchAppend(b, 64) }
