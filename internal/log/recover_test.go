package log

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"testing"

	"github.com/Gautam-J/chronicle/internal/record"
)

func segPath(dir string, base uint64, ext string) string {
	return filepath.Join(dir, fmt.Sprintf("%020d", base)+ext)
}

// reopen opens the files of a segment that was written and closed earlier,
// the way a restart after a crash finds them, and runs recover.
func reopen(t *testing.T, dir string, base uint64, interval int) (*segment, int64) {
	t.Helper()
	s, err := newSegment(dir, base, interval)
	if err != nil {
		t.Fatalf("newSegment: %v", err)
	}
	t.Cleanup(func() { s.close() })
	torn, err := s.recover()
	if err != nil {
		t.Fatalf("recover: %v", err)
	}
	return s, torn
}

// writeClosed writes records from..to into a fresh segment in dir, then
// closes it.
func writeClosed(t *testing.T, dir string, base, to uint64, interval int) {
	t.Helper()
	s, err := newSegment(dir, base, interval)
	if err != nil {
		t.Fatalf("newSegment: %v", err)
	}
	appendRange(t, s, base, to)
	if err := s.close(); err != nil {
		t.Fatalf("close: %v", err)
	}
}

func fileSize(t *testing.T, path string) int64 {
	t.Helper()
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	return fi.Size()
}

// checkState checks what recover rebuilt: the next offset, the size, and an
// .idx file that matches the in-memory entries and points only at good bytes.
func checkState(t *testing.T, s *segment, dir string, wantNext uint64, wantSize int64) {
	t.Helper()
	if s.next != wantNext {
		t.Errorf("next = %d, want %d", s.next, wantNext)
	}
	if s.size() != wantSize {
		t.Errorf("size() = %d, want %d", s.size(), wantSize)
	}
	onDisk := indexEntries(t, segPath(dir, s.base, ".idx"))
	if len(onDisk) != len(s.entries) {
		t.Fatalf(".idx has %d entries, memory has %d", len(onDisk), len(s.entries))
	}
	for i, e := range s.entries {
		if onDisk[i] != [2]uint32{e.rel, e.pos} {
			t.Errorf("entry %d: .idx has %v, memory has %v", i, onDisk[i], e)
		}
		if int64(e.pos) >= wantSize {
			t.Errorf("entry %d points at byte %d, past the last good record", i, e.pos)
		}
	}
}

// A clean restart finds every record and carries on at the next offset.
func TestRecoverClean(t *testing.T) {
	dir := t.TempDir()
	writeClosed(t, dir, 500, 600, 1000)
	size := fileSize(t, segPath(dir, 500, ".log"))

	s, torn := reopen(t, dir, 500, 1000)
	if torn != 0 {
		t.Errorf("torn = %d bytes, want 0 for a clean segment", torn)
	}
	checkState(t, s, dir, 600, size)
	for off := uint64(500); off < 600; off++ {
		checkRead(t, s, off)
	}
	appendRange(t, s, 600, 601)
	checkRead(t, s, 600)
}

func TestRecoverEmpty(t *testing.T) {
	dir := t.TempDir()
	s, torn := reopen(t, dir, 7, 4096)
	if torn != 0 {
		t.Errorf("torn = %d, want 0", torn)
	}
	checkState(t, s, dir, 7, 0)
	appendRange(t, s, 7, 8)
	checkRead(t, s, 7)
}

// The crash this exists for: the process died part way through writing the
// last record. Every cut point, from 1 byte to all but 1 byte of the record,
// must leave the 9 whole records and nothing else.
func TestRecoverTornTail(t *testing.T) {
	recLen := encodedLen(t, rec(0)) // records 0..9 all encode to the same length
	for _, kept := range []int{1, 3, 4, 7, 8, recLen / 2, recLen - 1} {
		t.Run(fmt.Sprintf("kept %d bytes", kept), func(t *testing.T) {
			dir := t.TempDir()
			writeClosed(t, dir, 0, 10, 1000)
			good := int64(9 * recLen)
			if err := os.Truncate(segPath(dir, 0, ".log"), good+int64(kept)); err != nil {
				t.Fatal(err)
			}

			s, torn := reopen(t, dir, 0, 1000)
			if torn != int64(kept) {
				t.Errorf("torn = %d, want %d", torn, kept)
			}
			checkState(t, s, dir, 9, good)

			if err := s.truncate(); err != nil {
				t.Fatalf("truncate: %v", err)
			}
			if got := fileSize(t, segPath(dir, 0, ".log")); got != good {
				t.Errorf("after truncate the .log is %d bytes, want %d", got, good)
			}
			// O_APPEND writes at the end of the file, so without the truncate
			// record 9 would land after the torn bytes and read would fail.
			appendRange(t, s, 9, 10)
			checkRead(t, s, 9)
		})
	}
}

// A flipped bit fails the CRC. Recovery stops there: it cannot know whether
// the records after it are real or leftover garbage.
func TestRecoverStopsAtBadCRC(t *testing.T) {
	dir := t.TempDir()
	writeClosed(t, dir, 0, 10, 1000)
	recLen := int64(encodedLen(t, rec(0)))
	path := segPath(dir, 0, ".log")
	size := fileSize(t, path)

	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	b[5*recLen+recLen-40] ^= 0x01 // inside record 5's payload
	if err := os.WriteFile(path, b, 0o600); err != nil {
		t.Fatal(err)
	}

	s, torn := reopen(t, dir, 0, 1000)
	checkState(t, s, dir, 5, 5*recLen)
	if torn != size-5*recLen {
		t.Errorf("torn = %d, want %d", torn, size-5*recLen)
	}
}

// A record can pass its CRC and still not belong here: an offset that is
// not the next one means the bytes came from somewhere else.
func TestRecoverStopsAtWrongOffset(t *testing.T) {
	dir := t.TempDir()
	writeClosed(t, dir, 0, 3, 1000)
	path := segPath(dir, 0, ".log")
	good := fileSize(t, path)

	stray, err := record.Encode(rec(99))
	if err != nil {
		t.Fatal(err)
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_APPEND, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.Write(stray); err != nil {
		t.Fatal(err)
	}
	f.Close()

	s, torn := reopen(t, dir, 0, 1000)
	checkState(t, s, dir, 3, good)
	if torn != int64(len(stray)) {
		t.Errorf("torn = %d, want %d", torn, len(stray))
	}
}

// A torn len field can claim up to 4 GiB. Recovery must check it against
// the file size before allocating, as read does.
func TestRecoverHugeLen(t *testing.T) {
	dir := t.TempDir()
	writeClosed(t, dir, 0, 3, 1000)
	path := segPath(dir, 0, ".log")
	good := fileSize(t, path)

	tail := binary.LittleEndian.AppendUint32(nil, 0xFFFF_FFF0)
	tail = append(tail, "half a record"...)
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_APPEND, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.Write(tail); err != nil {
		t.Fatal(err)
	}
	f.Close()

	s, err := newSegment(dir, 0, 1000)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.close() })

	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	torn, err := s.recover()
	runtime.ReadMemStats(&after)

	if err != nil {
		t.Fatalf("recover: %v", err)
	}
	if grew := after.TotalAlloc - before.TotalAlloc; grew > 1<<20 {
		t.Errorf("recover allocated %d MiB for a torn len", grew>>20)
	}
	checkState(t, s, dir, 3, good)
	if torn != int64(len(tail)) {
		t.Errorf("torn = %d, want %d", torn, len(tail))
	}
}

// The .idx is written after the .log, so a crash can leave it short, long,
// or cut mid-entry. It is derived data: recover rebuilds it from the .log.
func TestRecoverRebuildsIndex(t *testing.T) {
	dir := t.TempDir()
	writeClosed(t, dir, 0, 200, 1000)
	idxPath := segPath(dir, 0, ".idx")
	want, err := os.ReadFile(idxPath)
	if err != nil {
		t.Fatal(err)
	}

	for name, damaged := range map[string][]byte{
		"missing":       nil,
		"cut mid-entry": want[:len(want)-3],
		"last lost":     want[:len(want)-8],
		"extra garbage": append(slices.Clone(want), 1, 2, 3, 4, 5, 6, 7, 8),
	} {
		t.Run(name, func(t *testing.T) {
			if err := os.WriteFile(idxPath, damaged, 0o600); err != nil {
				t.Fatal(err)
			}
			s, _ := reopen(t, dir, 0, 1000)
			got, err := os.ReadFile(idxPath)
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(got, want) {
				t.Errorf("rebuilt .idx (%d bytes) differs from the original (%d bytes)", len(got), len(want))
			}
			for off := uint64(0); off < 200; off += 17 {
				checkRead(t, s, off)
			}
		})
	}
}

// Appends after a restart must follow the same index spacing as appends
// that never restarted: same .log bytes, same .idx bytes.
func TestRecoverThenAppendMatchesNoRestart(t *testing.T) {
	straight := t.TempDir()
	writeClosed(t, straight, 0, 200, 1000)

	restarted := t.TempDir()
	writeClosed(t, restarted, 0, 100, 1000)
	s, _ := reopen(t, restarted, 0, 1000)
	appendRange(t, s, 100, 200)
	if err := s.close(); err != nil {
		t.Fatal(err)
	}

	for _, ext := range []string{".log", ".idx"} {
		a, err := os.ReadFile(segPath(straight, 0, ext))
		if err != nil {
			t.Fatal(err)
		}
		b, err := os.ReadFile(segPath(restarted, 0, ext))
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(a, b) {
			t.Errorf("%s after a restart differs from one written without a restart", ext)
		}
	}
}
