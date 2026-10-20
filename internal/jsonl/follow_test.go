package jsonl

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"
)

const poll = 10 * time.Millisecond

// startFollow opens path, seeks to its start or end (whence), runs Follow in a goroutine and
// sends every emitted line to the returned channel.
func startFollow(t *testing.T, path string, whence int) (lines <-chan string, done <-chan error, cancel context.CancelFunc) {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { f.Close() })
	if _, err := f.Seek(0, whence); err != nil {
		t.Fatalf("seek: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	out := make(chan string, 100)
	errc := make(chan error, 1)
	go func() {
		errc <- Follow(ctx, f, poll, func(line []byte) { out <- string(line) })
	}()
	t.Cleanup(cancel)
	return out, errc, cancel
}

// next waits for one emitted line, or fails after a second.
func next(t *testing.T, lines <-chan string) string {
	t.Helper()
	select {
	case l := <-lines:
		return l
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for a line")
		return ""
	}
}

// none checks that nothing is emitted for a few poll intervals.
func none(t *testing.T, lines <-chan string) {
	t.Helper()
	select {
	case l := <-lines:
		t.Fatalf("unexpected line %q", l)
	case <-time.After(10 * poll):
	}
}

func appendRaw(t *testing.T, path, s string) {
	t.Helper()
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_APPEND, 0)
	if err != nil {
		t.Fatalf("open for append: %v", err)
	}
	defer f.Close()
	if _, err := f.WriteString(s); err != nil {
		t.Fatalf("write: %v", err)
	}
}

func newFile(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "points.jsonl")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("write file: %v", err)
	}
	return path
}

func TestFollowFromEndSkipsExisting(t *testing.T) {
	path := newFile(t, "{\"old\":1}\n{\"old\":2}\n")
	lines, _, _ := startFollow(t, path, io.SeekEnd)

	none(t, lines)
	appendRaw(t, path, "{\"new\":1}\n")
	if got := next(t, lines); got != `{"new":1}` {
		t.Errorf("line = %q, want %q", got, `{"new":1}`)
	}
}

func TestFollowFromStartReplays(t *testing.T) {
	path := newFile(t, "{\"a\":1}\n{\"b\":2}\n")
	lines, _, _ := startFollow(t, path, io.SeekStart)

	for _, want := range []string{`{"a":1}`, `{"b":2}`} {
		if got := next(t, lines); got != want {
			t.Errorf("line = %q, want %q", got, want)
		}
	}
}

func TestFollowKeepsOrder(t *testing.T) {
	path := newFile(t, "")
	lines, _, _ := startFollow(t, path, io.SeekEnd)

	appendRaw(t, path, "{\"n\":1}\n{\"n\":2}\n")
	appendRaw(t, path, "{\"n\":3}\n")
	for _, want := range []string{`{"n":1}`, `{"n":2}`, `{"n":3}`} {
		if got := next(t, lines); got != want {
			t.Errorf("line = %q, want %q", got, want)
		}
	}
}

// A writer can be halfway through a line when Follow reads. The half must be
// held back and emitted once, whole, when the rest arrives.
func TestFollowHoldsPartialLine(t *testing.T) {
	path := newFile(t, "")
	lines, _, _ := startFollow(t, path, io.SeekEnd)

	appendRaw(t, path, `{"lat":12.`)
	none(t, lines)
	appendRaw(t, path, "97}\n")
	if got := next(t, lines); got != `{"lat":12.97}` {
		t.Errorf("line = %q, want %q", got, `{"lat":12.97}`)
	}
	none(t, lines)
}

func TestFollowStopsOnCancel(t *testing.T) {
	path := newFile(t, "")
	_, done, cancel := startFollow(t, path, io.SeekEnd)

	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Errorf("Follow returned %v after cancel, want nil", err)
		}
	case <-time.After(time.Second):
		t.Fatal("Follow did not return within 1s of cancel")
	}
}

type failingReader struct{}

func (failingReader) Read([]byte) (int, error) { return 0, errors.New("disk on fire") }

func TestFollowReturnsReadError(t *testing.T) {
	err := Follow(context.Background(), failingReader{}, poll, func([]byte) {})
	if err == nil || err.Error() != "disk on fire" {
		t.Errorf("Follow error = %v, want the reader's error", err)
	}
}
