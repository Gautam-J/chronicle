package lsm

import (
	"bytes"
	"errors"
	"fmt"
	"iter"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// kvs yields pairs in the order given, so tests can also feed bad orders.
func kvs(pairs ...string) iter.Seq2[[]byte, []byte] {
	return func(yield func([]byte, []byte) bool) {
		for i := 0; i+1 < len(pairs); i += 2 {
			if !yield([]byte(pairs[i]), []byte(pairs[i+1])) {
				return
			}
		}
	}
}

// numbered yields n sorted keys key000000.. with values of varying length,
// enough for many 4 KiB blocks.
func numbered(n int) iter.Seq2[[]byte, []byte] {
	return func(yield func([]byte, []byte) bool) {
		for i := range n {
			if !yield(numKey(i), numVal(i)) {
				return
			}
		}
	}
}

func numKey(i int) []byte { return fmt.Appendf(nil, "key%06d", i) }
func numVal(i int) []byte { return bytes.Repeat([]byte{byte('a' + i%26)}, i%50) }

func writeTable(t *testing.T, entries iter.Seq2[[]byte, []byte]) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "L0-000001.sst")
	if err := WriteTable(path, entries); err != nil {
		t.Fatalf("WriteTable: %v", err)
	}
	return path
}

func openTable(t *testing.T, path string) *Table {
	t.Helper()
	tb, err := OpenTable(path)
	if err != nil {
		t.Fatalf("OpenTable: %v", err)
	}
	t.Cleanup(func() { tb.Close() })
	return tb
}

func wantGet(t *testing.T, tb *Table, k, want []byte) {
	t.Helper()
	v, ok, err := tb.Get(k)
	if err != nil || !ok || !bytes.Equal(v, want) {
		t.Fatalf("Get(%q) = %q, %v, %v; want %q, true, nil", k, v, ok, err, want)
	}
}

func wantMiss(t *testing.T, tb *Table, k []byte) {
	t.Helper()
	if v, ok, err := tb.Get(k); err != nil || ok {
		t.Errorf("Get(%q) = %q, %v, %v; want a miss: nil, false, nil", k, v, ok, err)
	}
}

func TestTableRoundTrip(t *testing.T) {
	const n = 5000
	path := writeTable(t, numbered(n))
	if st, _ := os.Stat(path); st.Size() < 10*blockSize {
		t.Fatalf("table is %d bytes; the test needs many blocks", st.Size())
	}
	tb := openTable(t, path)
	for i := range n {
		wantGet(t, tb, numKey(i), numVal(i))
	}
	// Misses before the first key, between keys (also at block edges), after the last.
	wantMiss(t, tb, []byte(""))
	wantMiss(t, tb, []byte("key"))
	for i := range n {
		wantMiss(t, tb, append(numKey(i), 'x'))
	}
	wantMiss(t, tb, []byte("zzz"))
}

// The point of the block index: a lookup reads one 4 KiB block, never the
// whole file. blockReads counts block reads from disk.
func TestTableGetReadsOneBlock(t *testing.T) {
	tb := openTable(t, writeTable(t, numbered(5000)))
	for _, i := range []int{0, 1, 777, 2500, 4999} {
		before := tb.blockReads
		wantGet(t, tb, numKey(i), numVal(i))
		if got := tb.blockReads - before; got != 1 {
			t.Errorf("Get(%s) read %d blocks, want 1", numKey(i), got)
		}
	}
	before := tb.blockReads
	wantMiss(t, tb, []byte("key002500x"))
	if got := tb.blockReads - before; got > 1 {
		t.Errorf("a miss read %d blocks, want at most 1", got)
	}
}

// A memtable flush is WriteTable(path, m.All()). Empty values (the S index
// keys) and a value larger than a block must both come back.
func TestTableFromMemtable(t *testing.T) {
	m := openMem(t, t.TempDir())
	big := bytes.Repeat([]byte("B"), 3*blockSize)
	put(t, m, "S|a", "")
	put(t, m, "S|b", "")
	put(t, m, "big", string(big))
	put(t, m, "", "the empty key sorts first")

	tb := openTable(t, writeTable(t, m.All()))
	wantGet(t, tb, []byte(""), []byte("the empty key sorts first"))
	wantGet(t, tb, []byte("S|a"), nil)
	wantGet(t, tb, []byte("S|b"), nil)
	wantGet(t, tb, []byte("big"), big)
}

// Compaction's merge iterator will decode keys into one reused buffer. The
// writer must copy any key it keeps (the last key, a block's first key).
func TestTableSourceReusesBuffer(t *testing.T) {
	reused := func(yield func([]byte, []byte) bool) {
		buf := make([]byte, 0, 16)
		for i := range 5000 {
			buf = fmt.Appendf(buf[:0], "key%06d", i)
			if !yield(buf, numVal(i)) {
				return
			}
		}
	}
	tb := openTable(t, writeTable(t, reused))
	for _, i := range []int{0, 1234, 4999} {
		wantGet(t, tb, numKey(i), numVal(i))
	}
}

func TestTableEmpty(t *testing.T) {
	tb := openTable(t, writeTable(t, kvs()))
	wantMiss(t, tb, []byte("a"))
	wantMiss(t, tb, []byte(""))
}

// Callers keep values Get returned. A later Get must not overwrite them, as
// it would if the reader reused one block buffer.
func TestTableValuesStayValid(t *testing.T) {
	tb := openTable(t, writeTable(t, numbered(5000)))
	v0, _, _ := tb.Get(numKey(49)) // 49 a's in block 0
	if _, _, err := tb.Get(numKey(4999)); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(v0, numVal(49)) {
		t.Errorf("value from the first Get changed after a second Get: %q", v0)
	}
}

// SSTables are binary-searched, so keys must arrive sorted and unique. A bad
// order is a bug in the caller; refuse it and leave no half-written file.
func TestTableRejectsBadOrder(t *testing.T) {
	for name, entries := range map[string]iter.Seq2[[]byte, []byte]{
		"descending": kvs("b", "1", "a", "2"),
		"duplicate":  kvs("a", "1", "a", "2"),
		"late": func(yield func([]byte, []byte) bool) { // many blocks written first
			for k, v := range numbered(3000) {
				if !yield(k, v) {
					return
				}
			}
			yield([]byte("a"), nil)
		},
	} {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "bad.sst")
			if err := WriteTable(path, entries); !errors.Is(err, ErrOrder) {
				t.Errorf("WriteTable = %v, want ErrOrder", err)
			}
			if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
				t.Errorf("a failed WriteTable left %s behind (stat: %v)", path, err)
			}
		})
	}
}

// Every block and the index carry a CRC. A flipped bit is an error, never a
// wrong answer.
func TestTableDetectsCorruption(t *testing.T) {
	flip := func(t *testing.T, path string, at func(size int) int) {
		t.Helper()
		b, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		b[at(len(b))] ^= 0x01
		if err := os.WriteFile(path, b, 0o600); err != nil {
			t.Fatal(err)
		}
	}

	t.Run("data block", func(t *testing.T) {
		path := writeTable(t, numbered(5000))
		flip(t, path, func(int) int { return 30 }) // inside block 0
		tb := openTable(t, path)
		if _, _, err := tb.Get(numKey(1)); !errors.Is(err, ErrCorrupt) {
			t.Errorf("Get from a damaged block = %v, want ErrCorrupt", err)
		}
		wantGet(t, tb, numKey(4999), numVal(4999)) // other blocks still read
	})

	// The index is the last thing before the 16-byte footer, and the footer
	// ends with a magic number.
	for name, at := range map[string]func(int) int{
		"index":  func(size int) int { return size - footerSize - 5 },
		"footer": func(size int) int { return size - 1 },
	} {
		t.Run(name, func(t *testing.T) {
			path := writeTable(t, numbered(5000))
			flip(t, path, at)
			if _, err := OpenTable(path); !errors.Is(err, ErrCorrupt) {
				t.Errorf("OpenTable = %v, want ErrCorrupt", err)
			}
		})
	}

	t.Run("truncated", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "short.sst")
		if err := os.WriteFile(path, []byte("tiny"), 0o600); err != nil {
			t.Fatal(err)
		}
		_, err := OpenTable(path)
		if !errors.Is(err, ErrCorrupt) || !strings.Contains(err.Error(), "short.sst") {
			t.Errorf("OpenTable = %v, want ErrCorrupt naming the file", err)
		}
	})
}
