package jsonl

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
)

func openTemp(t *testing.T) (*Writer, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "points.jsonl")
	w, err := Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	return w, path
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	return string(b)
}

func TestOpenCreatesPrivateFile(t *testing.T) {
	w, path := openTemp(t)
	defer w.Close()

	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("file not created: %v", err)
	}
	// Location history is personal data: owner read/write only.
	if got := info.Mode().Perm(); got != 0o600 {
		t.Errorf("permissions = %o, want 600", got)
	}
}

func TestAppendWritesLines(t *testing.T) {
	w, path := openTemp(t)

	for _, l := range []string{`{"a":1}`, `{"b":2}`} {
		if err := w.Append([]byte(l)); err != nil {
			t.Fatalf("Append(%s): %v", l, err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	if got, want := readFile(t, path), "{\"a\":1}\n{\"b\":2}\n"; got != want {
		t.Errorf("file = %q, want %q", got, want)
	}
}

func TestReopenAppends(t *testing.T) {
	w, path := openTemp(t)
	_ = w.Append([]byte(`{"first":true}`))
	_ = w.Close()

	w2, err := Open(path)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	_ = w2.Append([]byte(`{"second":true}`))
	_ = w2.Close()

	if got, want := readFile(t, path), "{\"first\":true}\n{\"second\":true}\n"; got != want {
		t.Errorf("file = %q, want %q (reopen must append, not truncate)", got, want)
	}
}

func TestAppendRejectsNewline(t *testing.T) {
	w, path := openTemp(t)

	if err := w.Append([]byte("{\"a\":\n1}")); err == nil {
		t.Error("Append with an embedded newline returned nil error, want an error")
	}
	_ = w.Close()

	if got := readFile(t, path); got != "" {
		t.Errorf("file = %q, want empty (rejected line must not be written)", got)
	}
}

func TestAppendAfterClose(t *testing.T) {
	w, _ := openTemp(t)
	_ = w.Close()

	if err := w.Append([]byte(`{"late":true}`)); err == nil {
		t.Error("Append after Close returned nil error, want an error")
	}
}

// The HTTP server runs each request in its own goroutine, so many Appends can
// happen at once. Every line must land whole: never split or interleaved.
func TestConcurrentAppends(t *testing.T) {
	const goroutines, perG = 50, 20
	w, path := openTemp(t)

	want := map[string]bool{}
	for g := range goroutines {
		for i := range perG {
			want[fmt.Sprintf(`{"g":%d,"i":%d,"pad":"%0100d"}`, g, i, 0)] = true
		}
	}

	var wg sync.WaitGroup
	for g := range goroutines {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range perG {
				line := fmt.Sprintf(`{"g":%d,"i":%d,"pad":"%0100d"}`, g, i, 0)
				if err := w.Append([]byte(line)); err != nil {
					t.Errorf("Append: %v", err)
				}
			}
		}()
	}
	wg.Wait()
	if err := w.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	f, err := os.Open(path)
	if err != nil {
		t.Fatalf("Open for read: %v", err)
	}
	defer f.Close()

	sc := bufio.NewScanner(f)
	n := 0
	for sc.Scan() {
		n++
		if !want[sc.Text()] {
			t.Fatalf("line %d is corrupt or unexpected: %q", n, sc.Text())
		}
		delete(want, sc.Text())
	}
	if n != goroutines*perG {
		t.Errorf("read %d lines, want %d", n, goroutines*perG)
	}
	if len(want) != 0 {
		t.Errorf("%d lines missing", len(want))
	}
}
