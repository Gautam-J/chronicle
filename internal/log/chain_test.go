package log

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Gautam-J/chronicle/internal/record"
)

// checkChain reads offsets 0..to and checks the hash chain: the first
// record's prev_hash is all zeros, each later one's is the hash before it,
// and each hash is the ChainHash of its record.
func checkChain(t *testing.T, l *Log, to uint64) {
	t.Helper()
	var prev [32]byte
	for off := uint64(0); off < to; off++ {
		r, err := l.Read(off)
		if err != nil {
			t.Fatalf("Read(%d): %v", off, err)
		}
		if r.PrevHash != prev {
			t.Fatalf("offset %d: prev_hash = %x, want the hash of offset %d = %x", off, r.PrevHash[:4], int64(off)-1, prev[:4])
		}
		want, err := record.ChainHash(r)
		if err != nil {
			t.Fatal(err)
		}
		if r.Hash != want {
			t.Fatalf("offset %d: hash = %x, want ChainHash = %x", off, r.Hash[:4], want[:4])
		}
		prev = r.Hash
	}
}

func TestAppendChains(t *testing.T) {
	l := openLog(t, t.TempDir(), Options{})
	appendN(t, l, 20)
	checkChain(t, l, 20)
}

// The log owns the chain. Whatever hashes a caller sends are replaced.
func TestAppendReplacesCallerHashes(t *testing.T) {
	l := openLog(t, t.TempDir(), Options{})
	for range 3 {
		r := rec(0)
		for i := range r.Hash {
			r.PrevHash[i], r.Hash[i] = 0xAA, 0xBB
		}
		if _, err := l.Append(r); err != nil {
			t.Fatal(err)
		}
	}
	checkChain(t, l, 3)
}

// A rejected record must not move the chain forward: the next record links
// to the last one that was written.
func TestFailedAppendKeepsChain(t *testing.T) {
	l := openLog(t, t.TempDir(), Options{})
	appendN(t, l, 2)
	bad := rec(0)
	bad.Type = strings.Repeat("t", 256)
	if _, err := l.Append(bad); !errors.Is(err, record.ErrTooLong) {
		t.Fatalf("Append of a 256-byte type: error = %v, want record.ErrTooLong", err)
	}
	appendN(t, l, 1)
	checkChain(t, l, 3)
}

func TestChainCrossesSegments(t *testing.T) {
	dir := t.TempDir()
	l := openLog(t, dir, small)
	appendN(t, l, 30)
	if n := len(segmentNames(t, dir)); n < 3 {
		t.Fatalf("only %d segments; the test needs several", n)
	}
	checkChain(t, l, 30)
}

// The doc's recovery step "rebuild the sequencer state: next offset, last
// hash". After a restart the next record must link to the last one on disk.
func TestChainSurvivesRestart(t *testing.T) {
	dir := t.TempDir()
	l := openLog(t, dir, small)
	appendN(t, l, 10)
	l = restart(t, l, dir, small)
	appendN(t, l, 1)
	checkChain(t, l, 11)
}

// After a torn tail is cut, the chain continues from the last whole record,
// not from the torn one.
func TestChainSurvivesTornTail(t *testing.T) {
	dir := t.TempDir()
	l := openLog(t, dir, small)
	appendN(t, l, 10)
	if err := l.Close(); err != nil {
		t.Fatal(err)
	}
	names := segmentNames(t, dir)
	last := filepath.Join(dir, names[len(names)-1])
	if err := os.Truncate(last, fileSize(t, last)-5); err != nil {
		t.Fatal(err)
	}

	l = openLog(t, dir, small)
	appendN(t, l, 1)
	checkChain(t, l, 10)
}

// A crash right after a roll leaves the newest segment empty. The last hash
// then comes from the segment before it.
func TestChainSurvivesEmptyActiveSegment(t *testing.T) {
	dir := t.TempDir()
	l := openLog(t, dir, small)
	appendN(t, l, 10)
	if err := l.Close(); err != nil {
		t.Fatal(err)
	}
	for _, ext := range []string{".log", ".idx"} {
		if err := os.WriteFile(filepath.Join(dir, "00000000000000000010"+ext), nil, 0o600); err != nil {
			t.Fatal(err)
		}
	}

	l = openLog(t, dir, small)
	appendN(t, l, 1)
	checkChain(t, l, 11)
}
