package record

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"strings"
	"testing"
)

// The record from TestEncodeGolden. Its hash was computed outside Go, in
// Python, from the golden bytes:
//
//	sha256(bytes(32) + bytes.fromhex("01 00 0100000000000000 ... 02000000 7b7d"))
func TestChainHashGolden(t *testing.T) {
	r := Record{
		Offset: 1, EventTime: 2, IngestTime: 3, StreamID: 4,
		Type: "loc", Key: []byte("2C"), Schema: 1, Payload: []byte("{}"),
	}
	h, err := ChainHash(r)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := hex.EncodeToString(h[:]), "d109f02cc011ec9fcf0540604178501a3e2e2234082c8d1d294029ffb2eb0300"; got != want {
		t.Errorf("ChainHash = %s, want %s", got, want)
	}
}

// hash = SHA-256(prev_hash || every byte from ver up to and including payload)
// (design doc, Record layout). Not len, not crc, not the hash field itself.
func TestChainHashCoversExactlyTheSpec(t *testing.T) {
	r := sample()
	b := mustEncode(t, r)
	want := sha256.Sum256(append(r.PrevHash[:], b[posVer:len(b)-64]...))
	got, err := ChainHash(r)
	if err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Errorf("ChainHash = %x, want sha256(prev_hash || ver..payload) = %x", got, want)
	}
}

// A record's own hash field must not feed into its hash, or the value
// would depend on itself.
func TestChainHashIgnoresHashField(t *testing.T) {
	a, b := sample(), sample()
	b.Hash = [32]byte{}
	ha, _ := ChainHash(a)
	hb, _ := ChainHash(b)
	if ha != hb {
		t.Error("ChainHash changed when only the Hash field changed")
	}
}

// Every field the chain protects must change the hash: editing any of them
// in an old record breaks every hash after it.
func TestChainHashChangesWithEachField(t *testing.T) {
	base, _ := ChainHash(sample())
	for name, edit := range map[string]func(*Record){
		"prev_hash":   func(r *Record) { r.PrevHash[31] ^= 1 },
		"flags":       func(r *Record) { r.Flags ^= 1 },
		"offset":      func(r *Record) { r.Offset++ },
		"event_time":  func(r *Record) { r.EventTime++ },
		"ingest_time": func(r *Record) { r.IngestTime++ },
		"stream_id":   func(r *Record) { r.StreamID++ },
		"type":        func(r *Record) { r.Type = strings.ToUpper(r.Type) },
		"key":         func(r *Record) { r.Key = append(r.Key, 'x') },
		"schema":      func(r *Record) { r.Schema++ },
		"payload":     func(r *Record) { r.Payload[0] ^= 1 },
	} {
		r := sample()
		edit(&r)
		if h, _ := ChainHash(r); h == base {
			t.Errorf("changing %s did not change the hash", name)
		}
	}
}

func TestChainHashRejectsLongFields(t *testing.T) {
	r := sample()
	r.Type = strings.Repeat("t", 256)
	if _, err := ChainHash(r); !errors.Is(err, ErrTooLong) {
		t.Errorf("error = %v, want ErrTooLong", err)
	}
}
