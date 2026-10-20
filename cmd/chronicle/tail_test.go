package main

import (
	"bufio"
	"context"
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestTailFilePrintsNewLines(t *testing.T) {
	path := filepath.Join(t.TempDir(), "points.jsonl")
	if err := os.WriteFile(path, []byte("{\"old\":true}\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	// A pipe, not a bytes.Buffer: tailFile writes from its goroutine while the
	// test reads, and a pipe is safe to use from both sides at once.
	pr, pw := io.Pipe()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- tailFile(ctx, path, pw)
		pw.Close()
	}()

	// Give tailFile time to open the file and seek to its end.
	time.Sleep(200 * time.Millisecond)

	f, err := os.OpenFile(path, os.O_WRONLY|os.O_APPEND, 0)
	if err != nil {
		t.Fatal(err)
	}
	f.WriteString("{\"new\":1}\n{\"new\":2}\n")
	f.Close()

	lines := make(chan string)
	go func() {
		sc := bufio.NewScanner(pr)
		for sc.Scan() {
			lines <- sc.Text()
		}
		close(lines)
	}()

	for _, want := range []string{`{"new":1}`, `{"new":2}`} {
		select {
		case got := <-lines:
			if got != want {
				t.Errorf("line = %q, want %q (old lines must not be printed)", got, want)
			}
		case <-time.After(3 * time.Second):
			t.Fatalf("timed out waiting for %q", want)
		}
	}

	cancel()
	pr.Close()
	select {
	case err := <-done:
		if err != nil && err != io.ErrClosedPipe {
			t.Errorf("tailFile returned %v after cancel, want nil", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("tailFile did not return after cancel")
	}
}

func TestTailFileMissing(t *testing.T) {
	err := tailFile(context.Background(), filepath.Join(t.TempDir(), "nope.jsonl"), io.Discard)
	if err == nil {
		t.Error("tailFile on a missing file returned nil, want an error")
	}
}
