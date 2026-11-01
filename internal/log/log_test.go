package log

import (
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

// fakeClock lets a test move time forward by hand.
type fakeClock struct{ t time.Time }

func (c *fakeClock) now() time.Time          { return c.t }
func (c *fakeClock) advance(d time.Duration) { c.t = c.t.Add(d) }

func newClock() *fakeClock {
	return &fakeClock{t: time.Date(2026, 11, 9, 8, 0, 0, 0, time.UTC)}
}

func openLog(t *testing.T, dir string, o Options) *Log {
	t.Helper()
	l, err := Open(dir, o)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { l.Close() })
	return l
}

func appendN(t *testing.T, l *Log, n int) {
	t.Helper()
	for range n {
		if _, err := l.Append(rec(0)); err != nil {
			t.Fatalf("Append: %v", err)
		}
	}
}

// segmentNames lists the .log files in dir, sorted.
func segmentNames(t *testing.T, dir string) []string {
	t.Helper()
	des, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, de := range des {
		if strings.HasSuffix(de.Name(), ".log") {
			out = append(out, de.Name())
		}
	}
	slices.Sort(out)
	return out
}

func TestWithDefaults(t *testing.T) {
	got := withDefaults(Options{})
	if got.MaxSegmentBytes != 64<<20 || got.MaxSegmentAge != time.Hour || got.IndexInterval != 4096 || got.Now == nil {
		t.Errorf("withDefaults(Options{}) = %+v, want 64 MiB, 1h, 4096 and a clock", got)
	}

	c := newClock()
	set := Options{MaxSegmentBytes: 1000, MaxSegmentAge: time.Minute, IndexInterval: 100, Now: c.now}
	got = withDefaults(set)
	if got.MaxSegmentBytes != 1000 || got.MaxSegmentAge != time.Minute || got.IndexInterval != 100 || got.Now() != c.now() {
		t.Errorf("withDefaults changed values that were set: %+v", got)
	}
}

// Index positions are u32 (see segment), so a segment must never pass 4 GiB.
func TestOpenRejectsHugeSegments(t *testing.T) {
	_, err := Open(t.TempDir(), Options{MaxSegmentBytes: 5 << 30})
	if err == nil {
		t.Fatal("Open with MaxSegmentBytes = 5 GiB returned no error")
	}
}

func TestOpenCreatesDir(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "data", "p0")
	openLog(t, dir, Options{})
	fi, err := os.Stat(dir)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if mode := fi.Mode().Perm(); mode != 0o700 {
		t.Errorf("dir mode = %o, want 700", mode)
	}
}

// The log, not the caller, decides offsets: 0, 1, 2, ...
func TestAppendAssignsOffsets(t *testing.T) {
	l := openLog(t, t.TempDir(), Options{})
	for want := range uint64(5) {
		r := rec(999) // the caller's Offset is ignored
		got, err := l.Append(r)
		if err != nil {
			t.Fatalf("Append: %v", err)
		}
		if got != want {
			t.Errorf("Append returned offset %d, want %d", got, want)
		}
	}
}

// With room for exactly 3 records, segments hold 0-2, 3-5, 6-8, then 9.
func TestRollsOverBySize(t *testing.T) {
	dir := t.TempDir()
	recLen := int64(encodedLen(t, rec(0)))
	l := openLog(t, dir, Options{MaxSegmentBytes: 3 * recLen})
	appendN(t, l, 10)

	want := []string{
		"00000000000000000000.log",
		"00000000000000000003.log",
		"00000000000000000006.log",
		"00000000000000000009.log",
	}
	if got := segmentNames(t, dir); !slices.Equal(got, want) {
		t.Errorf("segments = %v, want %v", got, want)
	}
}

// A segment's age counts from its first record, not from when the file was
// created: the server can sit idle for hours before the first point arrives.
func TestRollsOverByAge(t *testing.T) {
	dir := t.TempDir()
	c := newClock()
	l := openLog(t, dir, Options{MaxSegmentAge: time.Hour, Now: c.now})

	c.advance(2 * time.Hour) // idle after Open
	appendN(t, l, 1)         // offset 0: first record, starts the clock
	c.advance(59 * time.Minute)
	appendN(t, l, 1) // offset 1: 59 min, same segment
	c.advance(time.Minute)
	appendN(t, l, 1) // offset 2: 60 min, new segment
	c.advance(3 * time.Hour)
	appendN(t, l, 1) // offset 3: new segment

	want := []string{
		"00000000000000000000.log",
		"00000000000000000002.log",
		"00000000000000000003.log",
	}
	if got := segmentNames(t, dir); !slices.Equal(got, want) {
		t.Errorf("segments = %v, want %v", got, want)
	}
}

func TestReadAcrossSegments(t *testing.T) {
	recLen := int64(encodedLen(t, rec(0)))
	l := openLog(t, t.TempDir(), Options{MaxSegmentBytes: 7 * recLen, IndexInterval: 1000})
	appendN(t, l, 50)
	for off := range uint64(50) {
		got, err := l.Read(off)
		if err != nil {
			t.Fatalf("Read(%d): %v", off, err)
		}
		if got.Offset != off {
			t.Errorf("Read(%d) returned offset %d", off, got.Offset)
		}
	}
}

func TestReadMissing(t *testing.T) {
	l := openLog(t, t.TempDir(), Options{})
	if _, err := l.Read(0); !errors.Is(err, ErrNotFound) {
		t.Errorf("Read(0) on an empty log: error = %v, want ErrNotFound", err)
	}
	appendN(t, l, 3)
	if _, err := l.Read(3); !errors.Is(err, ErrNotFound) {
		t.Errorf("Read(3) after 3 appends: error = %v, want ErrNotFound", err)
	}
}

func TestCloseClosesAllSegments(t *testing.T) {
	recLen := int64(encodedLen(t, rec(0)))
	l, err := Open(t.TempDir(), Options{MaxSegmentBytes: recLen})
	if err != nil {
		t.Fatal(err)
	}
	appendN(t, l, 3) // 3 segments
	if err := l.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	for i, s := range l.segments {
		if _, err := s.log.Stat(); !errors.Is(err, os.ErrClosed) {
			t.Errorf("segment %d .log still open after Close", i)
		}
	}
}
