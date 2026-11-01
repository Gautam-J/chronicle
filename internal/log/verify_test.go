package log

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Gautam-J/chronicle/internal/record"
)

func verifyErr(t *testing.T, l *Log) (uint64, error) {
	t.Helper()
	n, err := l.Verify()
	if err == nil {
		t.Fatalf("Verify = %d records, no error; want a broken chain", n)
	}
	return n, err
}

// tamper edits the record at off in place, the way an attacker with write
// access would: it re-encodes the record, so its CRC is valid again and Open
// accepts it. Only the hash chain can tell.
func tamper(t *testing.T, dir string, off uint64, edit func(*record.Record)) {
	t.Helper()
	for _, name := range segmentNames(t, dir) {
		path := filepath.Join(dir, name)
		b, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		for pos := 0; pos < len(b); {
			r, n, err := record.Decode(b[pos:])
			if err != nil {
				t.Fatalf("%s at byte %d: %v", name, pos, err)
			}
			if r.Offset == off {
				edit(&r)
				nb, err := record.Encode(r)
				if err != nil {
					t.Fatal(err)
				}
				if len(nb) != n {
					t.Fatalf("the edit changed the record's length from %d to %d bytes", n, len(nb))
				}
				copy(b[pos:], nb)
				if err := os.WriteFile(path, b, 0o600); err != nil {
					t.Fatal(err)
				}
				return
			}
			pos += n
		}
	}
	t.Fatalf("offset %d not found", off)
}

func TestVerifyEmpty(t *testing.T) {
	l := openLog(t, t.TempDir(), Options{})
	if n, err := l.Verify(); n != 0 || err != nil {
		t.Errorf("Verify on an empty log = %d, %v; want 0, nil", n, err)
	}
}

func TestVerifyIntact(t *testing.T) {
	dir := t.TempDir()
	l := openLog(t, dir, small)
	appendN(t, l, 30)
	l = restart(t, l, dir, small)
	appendN(t, l, 5)
	if n, err := l.Verify(); n != 35 || err != nil {
		t.Errorf("Verify = %d, %v; want 35, nil", n, err)
	}
}

// Someone changes an old payload and fixes its CRC. The record no longer
// matches its own hash.
func TestVerifyFindsEditedPayload(t *testing.T) {
	dir := t.TempDir()
	l := openLog(t, dir, small)
	appendN(t, l, 30)
	if err := l.Close(); err != nil {
		t.Fatal(err)
	}
	tamper(t, dir, 12, func(r *record.Record) { r.Payload[2] = 'N' })

	l = openLog(t, dir, small) // Open is happy: every CRC is valid
	n, err := verifyErr(t, l)
	if !errors.Is(err, ErrChain) || n != 12 || !strings.Contains(err.Error(), "offset 12") {
		t.Errorf("Verify = %d, %v; want 12 and ErrChain naming offset 12", n, err)
	}
}

// A smarter edit also recomputes the record's own hash. Now the record
// matches its hash, but the next record's prev_hash still holds the old one.
func TestVerifyFindsRehashedRecord(t *testing.T) {
	dir := t.TempDir()
	l := openLog(t, dir, small)
	appendN(t, l, 30)
	if err := l.Close(); err != nil {
		t.Fatal(err)
	}
	tamper(t, dir, 12, func(r *record.Record) {
		r.Payload[2] = 'N'
		h, err := record.ChainHash(*r)
		if err != nil {
			t.Fatal(err)
		}
		r.Hash = h
	})

	l = openLog(t, dir, small)
	n, err := verifyErr(t, l)
	if !errors.Is(err, ErrChain) || n != 13 || !strings.Contains(err.Error(), "offset 13") {
		t.Errorf("Verify = %d, %v; want 13 and ErrChain naming offset 13", n, err)
	}
}
