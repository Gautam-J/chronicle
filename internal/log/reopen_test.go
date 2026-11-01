package log

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/Gautam-J/chronicle/internal/record"
)

// small makes segments roll every few records, so tests get several.
var small = Options{MaxSegmentBytes: 2000, IndexInterval: 500}

// restart closes l and opens dir again, the way the server starts after a
// crash or a deploy.
func restart(t *testing.T, l *Log, dir string, o Options) *Log {
	t.Helper()
	if err := l.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	return openLog(t, dir, o)
}

func checkLogRead(t *testing.T, l *Log, from, to uint64) {
	t.Helper()
	for off := from; off < to; off++ {
		r, err := l.Read(off)
		if err != nil || r.Offset != off {
			t.Fatalf("Read(%d) = offset %d, %v", off, r.Offset, err)
		}
	}
}

func appendWant(t *testing.T, l *Log, want uint64) {
	t.Helper()
	got, err := l.Append(rec(0))
	if err != nil {
		t.Fatalf("Append: %v", err)
	}
	if got != want {
		t.Fatalf("Append after restart returned offset %d, want %d", got, want)
	}
}

func TestReopenEmpty(t *testing.T) {
	dir := t.TempDir()
	l := restart(t, openLog(t, dir, Options{}), dir, Options{})
	appendWant(t, l, 0)
	if got := segmentNames(t, dir); !slices.Equal(got, []string{"00000000000000000000.log"}) {
		t.Errorf("segments = %v, want just the first one", got)
	}
}

// The bug this fixes: Open used to start at offset 0 on a directory that
// already had records, so offsets repeated.
func TestReopenContinuesOffsets(t *testing.T) {
	dir := t.TempDir()
	l := openLog(t, dir, Options{})
	appendN(t, l, 10)
	l = restart(t, l, dir, Options{})
	appendWant(t, l, 10)
	checkLogRead(t, l, 0, 11)
}

func TestReopenAcrossSegments(t *testing.T) {
	dir := t.TempDir()
	l := openLog(t, dir, small)
	appendN(t, l, 30)
	before := segmentNames(t, dir)
	if len(before) < 3 {
		t.Fatalf("only %d segments; the test needs several", len(before))
	}

	l = restart(t, l, dir, small)
	if got := segmentNames(t, dir); !slices.Equal(got, before) {
		t.Errorf("segments after reopen = %v, want %v", got, before)
	}
	checkLogRead(t, l, 0, 30)
	appendWant(t, l, 30)
	checkLogRead(t, l, 30, 31)
}

// A crash part way through a record: Open drops the torn bytes and the next
// Append reuses that offset, which no caller was ever given.
func TestReopenTruncatesTornTail(t *testing.T) {
	dir := t.TempDir()
	l := openLog(t, dir, small)
	appendN(t, l, 10)
	if err := l.Close(); err != nil {
		t.Fatal(err)
	}
	names := segmentNames(t, dir)
	last := filepath.Join(dir, names[len(names)-1])
	full := fileSize(t, last)
	if err := os.Truncate(last, full-5); err != nil {
		t.Fatal(err)
	}

	l = openLog(t, dir, small)
	if got, want := fileSize(t, last), full-int64(encodedLen(t, rec(9))); got != want {
		t.Errorf("last .log is %d bytes after Open, want %d (torn record cut)", got, want)
	}
	appendWant(t, l, 9)
	checkLogRead(t, l, 0, 10)
}

// Sealed segments were fsynced before the roll, so a bad record in one is
// disk corruption, not a crash. Open must refuse, and must not cut away
// records that callers were told are safe.
func TestReopenRejectsDamagedSealedSegment(t *testing.T) {
	dir := t.TempDir()
	l := openLog(t, dir, small)
	appendN(t, l, 30)
	if err := l.Close(); err != nil {
		t.Fatal(err)
	}
	first := filepath.Join(dir, segmentNames(t, dir)[0])
	corruptAt(t, first, int64(encodedLen(t, rec(0))+50), 0x01) // inside record 1
	want, err := os.ReadFile(first)
	if err != nil {
		t.Fatal(err)
	}

	if l, err := Open(dir, small); !errors.Is(err, record.ErrCorrupt) {
		if l != nil {
			l.Close()
		}
		t.Fatalf("Open error = %v, want record.ErrCorrupt", err)
	}
	if got, _ := os.ReadFile(first); !bytes.Equal(got, want) {
		t.Errorf("Open changed the damaged sealed segment (%d bytes, was %d)", len(got), len(want))
	}
}

// A missing segment leaves a hole in the offsets. Open must not paper over it.
func TestReopenRejectsMissingSegment(t *testing.T) {
	dir := t.TempDir()
	l := openLog(t, dir, small)
	appendN(t, l, 30)
	if err := l.Close(); err != nil {
		t.Fatal(err)
	}
	middle := segmentNames(t, dir)[1]
	for _, ext := range []string{".log", ".idx"} {
		if err := os.Remove(filepath.Join(dir, middle[:20]+ext)); err != nil {
			t.Fatal(err)
		}
	}

	if l, err := Open(dir, small); !errors.Is(err, record.ErrCorrupt) {
		if l != nil {
			l.Close()
		}
		t.Fatalf("Open error = %v, want record.ErrCorrupt", err)
	}
}

// A crash right after a roll leaves an empty newest segment. That is fine:
// it becomes the active segment, and the next record goes into it.
func TestReopenEmptyActiveSegment(t *testing.T) {
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
	appendWant(t, l, 10)
	if got := fileSize(t, filepath.Join(dir, "00000000000000000010.log")); got == 0 {
		t.Error("record 10 did not go into the empty segment 10")
	}
}

// The age limit counts from Open for a segment that already has records:
// the first record's time is not stored, and a zero time would roll on
// every restart.
func TestReopenAgeCountsFromOpen(t *testing.T) {
	dir := t.TempDir()
	c := newClock()
	o := Options{MaxSegmentAge: time.Hour, Now: c.now}
	l := openLog(t, dir, o)
	appendN(t, l, 3)
	c.advance(50 * time.Minute)

	l = restart(t, l, dir, o)
	appendN(t, l, 1)
	if n := len(segmentNames(t, dir)); n != 1 {
		t.Fatalf("%d segments right after a restart, want 1 (no roll yet)", n)
	}
	c.advance(time.Hour)
	appendN(t, l, 1)
	if n := len(segmentNames(t, dir)); n != 2 {
		t.Errorf("%d segments an hour after the restart, want 2", n)
	}
}
