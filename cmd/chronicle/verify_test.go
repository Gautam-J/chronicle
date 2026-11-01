package main

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/Gautam-J/chronicle/internal/log"
	"github.com/Gautam-J/chronicle/internal/record"
)

func TestVerifyLogIntact(t *testing.T) {
	dir := t.TempDir()
	l, err := log.Open(dir, log.Options{})
	if err != nil {
		t.Fatal(err)
	}
	for range 3 {
		if _, err := l.Append(record.Record{Type: "loc", Payload: []byte("{}")}); err != nil {
			t.Fatal(err)
		}
	}
	if err := l.Close(); err != nil {
		t.Fatal(err)
	}

	var out bytes.Buffer
	if err := verifyLog(dir, &out); err != nil {
		t.Fatalf("verifyLog: %v", err)
	}
	if got, want := out.String(), "ok: 3 records, hash chain intact\n"; got != want {
		t.Errorf("output = %q, want %q", got, want)
	}
}

// log.Open creates a missing directory. For verify that would turn a typo in
// the path into "ok: 0 records". It must fail instead, and create nothing.
func TestVerifyLogMissingDir(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "typo")
	var out bytes.Buffer
	if err := verifyLog(dir, &out); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("verifyLog(missing dir) error = %v, want os.ErrNotExist", err)
	}
	if _, err := os.Stat(dir); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("verifyLog created %s", dir)
	}
	if out.Len() != 0 {
		t.Errorf("printed %q for a missing directory", out.String())
	}
}
