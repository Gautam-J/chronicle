package log

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/Gautam-J/chronicle/internal/record"
)

// rec builds a record with a payload that names its offset, so a read of the
// wrong record is easy to spot.
func rec(off uint64) record.Record {
	return record.Record{
		Offset:  off,
		Type:    "location",
		Payload: fmt.Appendf(nil, `{"n":%d,"pad":"%0150d"}`, off, 0),
	}
}

func encodedLen(t *testing.T, r record.Record) int {
	t.Helper()
	b, err := record.Encode(r)
	if err != nil {
		t.Fatalf("Encode: %v", err)
	}
	return len(b)
}

func openSegment(t *testing.T, base uint64, interval int) (*segment, string) {
	t.Helper()
	dir := t.TempDir()
	s, err := newSegment(dir, base, interval)
	if err != nil {
		t.Fatalf("newSegment: %v", err)
	}
	t.Cleanup(func() { s.close() })
	return s, dir
}

func appendRange(t *testing.T, s *segment, from, to uint64) {
	t.Helper()
	for off := from; off < to; off++ {
		if err := s.append(rec(off)); err != nil {
			t.Fatalf("append offset %d: %v", off, err)
		}
	}
}

func checkRead(t *testing.T, s *segment, off uint64) {
	t.Helper()
	got, err := s.read(off)
	if err != nil {
		t.Fatalf("read(%d): %v", off, err)
	}
	if want := rec(off); got.Offset != off || !bytes.Equal(got.Payload, want.Payload) {
		t.Fatalf("read(%d) = offset %d, payload %s", off, got.Offset, got.Payload)
	}
}

// indexEntries parses an .idx file: 8-byte entries, each a little-endian u32
// offset relative to the segment's base, then a u32 byte position in the .log.
func indexEntries(t *testing.T, path string) [][2]uint32 {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read index: %v", err)
	}
	if len(b)%8 != 0 {
		t.Fatalf("index is %d bytes, want a multiple of 8", len(b))
	}
	var out [][2]uint32
	for i := 0; i < len(b); i += 8 {
		out = append(out, [2]uint32{
			binary.LittleEndian.Uint32(b[i:]),
			binary.LittleEndian.Uint32(b[i+4:]),
		})
	}
	return out
}

// Files are named by the segment's first offset, padded to 20 digits so that
// sorting the names as text also sorts them by offset.
func TestSegmentFiles(t *testing.T) {
	_, dir := openSegment(t, 52311, 4096)
	for _, name := range []string{"00000000000000052311.log", "00000000000000052311.idx"} {
		fi, err := os.Stat(filepath.Join(dir, name))
		if err != nil {
			t.Fatalf("stat %s: %v", name, err)
		}
		if mode := fi.Mode().Perm(); mode != 0o600 {
			t.Errorf("%s mode = %o, want 600 (location data is personal)", name, mode)
		}
	}
}

func TestSegmentAppendRead(t *testing.T) {
	s, _ := openSegment(t, 1000, 4096)
	appendRange(t, s, 1000, 1100)
	for off := uint64(1000); off < 1100; off++ {
		checkRead(t, s, off)
	}
}

// Reads must not move the position that appends write at.
func TestSegmentInterleaved(t *testing.T) {
	s, _ := openSegment(t, 0, 4096)
	for off := uint64(0); off < 50; off++ {
		appendRange(t, s, off, off+1)
		checkRead(t, s, 0)
		checkRead(t, s, off)
	}
}

func TestSegmentSize(t *testing.T) {
	s, dir := openSegment(t, 0, 4096)
	want := 0
	for off := uint64(0); off < 20; off++ {
		appendRange(t, s, off, off+1)
		want += encodedLen(t, rec(off))
		if s.size() != int64(want) {
			t.Fatalf("after %d appends size() = %d, want %d", off+1, s.size(), want)
		}
	}
	fi, err := os.Stat(filepath.Join(dir, "00000000000000000000.log"))
	if err != nil {
		t.Fatal(err)
	}
	if fi.Size() != int64(want) {
		t.Errorf("file is %d bytes, size() says %d", fi.Size(), want)
	}
}

func TestSegmentRejectsWrongOffset(t *testing.T) {
	s, _ := openSegment(t, 100, 4096)
	if err := s.append(rec(99)); !errors.Is(err, ErrBadOffset) {
		t.Errorf("append below base: error = %v, want ErrBadOffset", err)
	}
	appendRange(t, s, 100, 101)
	for _, off := range []uint64{100, 102} {
		if err := s.append(rec(off)); !errors.Is(err, ErrBadOffset) {
			t.Errorf("append %d after 100: error = %v, want ErrBadOffset", off, err)
		}
	}
}

func TestSegmentReadMissing(t *testing.T) {
	s, _ := openSegment(t, 100, 4096)
	appendRange(t, s, 100, 110)
	for _, off := range []uint64{0, 99, 110, 1 << 40} {
		if _, err := s.read(off); !errors.Is(err, ErrNotFound) {
			t.Errorf("read(%d) error = %v, want ErrNotFound", off, err)
		}
	}
}

// A flipped bit in a len field must not make read allocate whatever size the
// disk claims. Here the claim is about 256 MiB; a high bit could claim 4 GiB
// on a 4 GB Pi.
func TestSegmentReadCorruptLen(t *testing.T) {
	s, dir := openSegment(t, 0, 1<<20) // one index entry, so read steps over record 0
	appendRange(t, s, 0, 3)

	f, err := os.OpenFile(filepath.Join(dir, "00000000000000000000.log"), os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteAt([]byte{0x10}, 3); err != nil { // high byte of record 0's len
		t.Fatal(err)
	}
	f.Close()

	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	_, err = s.read(2)
	runtime.ReadMemStats(&after)

	if !errors.Is(err, record.ErrCorrupt) {
		t.Errorf("read error = %v, want record.ErrCorrupt", err)
	}
	if grew := after.TotalAlloc - before.TotalAlloc; grew > 1<<20 {
		t.Errorf("read allocated %d MiB for a corrupt length", grew>>20)
	}
}

// The sparse index has one entry per interval bytes of log, not one per
// record. Each entry must point at the start of the record it names.
func TestSegmentSparseIndex(t *testing.T) {
	const interval = 1000
	s, dir := openSegment(t, 500, interval)
	appendRange(t, s, 500, 700)
	recLen := encodedLen(t, rec(500))
	if err := s.close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	entries := indexEntries(t, filepath.Join(dir, "00000000000000000500.idx"))
	if len(entries) == 0 || entries[0] != [2]uint32{0, 0} {
		t.Fatalf("first index entry = %v, want [0 0] (the first record is always indexed)", entries)
	}
	logBytes, err := os.ReadFile(filepath.Join(dir, "00000000000000000500.log"))
	if err != nil {
		t.Fatal(err)
	}
	for i, e := range entries {
		r, _, err := record.Decode(logBytes[e[1]:])
		if err != nil || r.Offset != 500+uint64(e[0]) {
			t.Fatalf("entry %d %v: record at that position = offset %d, %v", i, e, r.Offset, err)
		}
		if i == 0 {
			continue
		}
		gap := int(e[1] - entries[i-1][1])
		if gap < interval || gap >= interval+recLen {
			t.Errorf("entries %d and %d are %d bytes apart, want at least %d and under %d",
				i-1, i, gap, interval, interval+recLen)
		}
	}
	if last := entries[len(entries)-1][1]; len(logBytes)-int(last) >= interval+recLen {
		t.Errorf("last entry at %d leaves %d unindexed bytes", last, len(logBytes)-int(last))
	}
}

// Reads the last record of a full 64 MiB segment: the worst case for the
// index lookup. Compare before and after a change with:
//
//	go test ./internal/log -run '^$' -bench SegmentReadLast -count 5
func BenchmarkSegmentReadLast(b *testing.B) {
	s, err := newSegment(b.TempDir(), 0, 4096)
	if err != nil {
		b.Fatal(err)
	}
	defer s.close()
	var off uint64
	for s.size() < 64<<20 {
		if err := s.append(rec(off)); err != nil {
			b.Fatal(err)
		}
		off++
	}
	last := off - 1

	for b.Loop() {
		if _, err := s.read(last); err != nil {
			b.Fatal(err)
		}
	}
	b.ReportMetric(float64(len(s.entries)), "entries")
}
