package record

import (
	"bytes"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"hash/crc32"
	"strings"
	"testing"
)

// Byte positions inside an encoded record (see the layout in the design doc).
const (
	posLen     = 0  // u32: number of bytes after this field
	posCRC     = 4  // u32: CRC32C of every byte after this field
	posVer     = 8  // u8
	posTypeLen = 38 // u8, after ver, flags, offset, event_time, ingest_time, stream_id
)

func sample() Record {
	r := Record{
		Flags:      0x02,
		Offset:     52311,
		EventTime:  1792491998000000,
		IngestTime: 1792491998500000,
		StreamID:   7,
		Type:       "location",
		Key:        []byte("2C:1792491998"),
		Schema:     1,
		Payload:    []byte(`{"lat":12.97,"lon":77.59}`),
	}
	for i := range r.PrevHash {
		r.PrevHash[i] = byte(i)
		r.Hash[i] = byte(255 - i)
	}
	return r
}

// same compares two records field by field. A nil slice and an empty slice
// count as equal, because the byte layout cannot tell them apart.
func same(a, b Record) bool {
	return a.Flags == b.Flags && a.Offset == b.Offset &&
		a.EventTime == b.EventTime && a.IngestTime == b.IngestTime &&
		a.StreamID == b.StreamID && a.Type == b.Type &&
		bytes.Equal(a.Key, b.Key) && a.Schema == b.Schema &&
		bytes.Equal(a.Payload, b.Payload) &&
		a.PrevHash == b.PrevHash && a.Hash == b.Hash
}

func mustEncode(t testing.TB, r Record) []byte {
	t.Helper()
	b, err := Encode(r)
	if err != nil {
		t.Fatalf("Encode: %v", err)
	}
	return b
}

// reseal recomputes the CRC after a test edits a record on purpose, so the
// decoder's later checks (version, lengths) can be reached.
func reseal(b []byte) {
	sum := crc32.Checksum(b[posVer:], crc32.MakeTable(crc32.Castagnoli))
	binary.LittleEndian.PutUint32(b[posCRC:], sum)
}

// The golden test pins the exact bytes: field order, widths and little-endian
// order. If it fails, read the got/want hex side by side.
func TestEncodeGolden(t *testing.T) {
	r := Record{
		Offset: 1, EventTime: 2, IngestTime: 3, StreamID: 4,
		Type: "loc", Key: []byte("2C"), Schema: 1, Payload: []byte("{}"),
	}
	for i := range r.Hash {
		r.Hash[i] = 0x11
	}
	want := strings.Join([]string{
		"72000000",         // len = 114 bytes follow
		"b8def354",         // crc32c
		"01",               // ver
		"00",               // flags
		"0100000000000000", // offset
		"0200000000000000", // event_time
		"0300000000000000", // ingest_time
		"04000000",         // stream_id
		"03", "6c6f63",     // type_len, "loc"
		"0200", "3243", // key_len, "2C"
		"0100",             // schema
		"02000000", "7b7d", // payload_len, "{}"
		strings.Repeat("00", 32), // prev_hash
		strings.Repeat("11", 32), // hash
	}, "")

	got := hex.EncodeToString(mustEncode(t, r))
	if got != want {
		t.Errorf("Encode bytes\n got %s\nwant %s", got, want)
	}
}

func TestRoundTrip(t *testing.T) {
	long := sample()
	long.Type = strings.Repeat("t", 255)
	long.Payload = bytes.Repeat([]byte("x"), 70_000) // bigger than a u16 length

	tests := map[string]Record{
		"sample":         sample(),
		"zero":           {},
		"max type, long": long,
		"utf-8 type":     {Type: "lieu·ça", Payload: []byte("{}")},
		"negative times": {EventTime: -1, IngestTime: -1 << 62},
	}
	for name, r := range tests {
		t.Run(name, func(t *testing.T) {
			b := mustEncode(t, r)
			got, n, err := Decode(b)
			if err != nil {
				t.Fatalf("Decode: %v", err)
			}
			if n != len(b) {
				t.Errorf("Decode consumed %d bytes, want %d", n, len(b))
			}
			if !same(got, r) {
				t.Errorf("round trip changed the record\n got %+v\nwant %+v", got, r)
			}
		})
	}
}

func TestEncodeRejectsLongFields(t *testing.T) {
	longType := sample()
	longType.Type = strings.Repeat("t", 256)
	longKey := sample()
	longKey.Key = make([]byte, 65536)

	for name, r := range map[string]Record{"type 256": longType, "key 65536": longKey} {
		t.Run(name, func(t *testing.T) {
			if _, err := Encode(r); !errors.Is(err, ErrTooLong) {
				t.Errorf("Encode error = %v, want ErrTooLong", err)
			}
		})
	}
}

// A segment is records back to back. Decode reports how many bytes it used,
// so the caller can step to the next record.
func TestDecodeSteps(t *testing.T) {
	a, b := sample(), sample()
	b.Offset++
	b.Payload = []byte(`{"second":true}`)
	buf := append(mustEncode(t, a), mustEncode(t, b)...)

	got, n, err := Decode(buf)
	if err != nil || !same(got, a) {
		t.Fatalf("first Decode = %+v, %v; want record a", got, err)
	}
	got, m, err := Decode(buf[n:])
	if err != nil || !same(got, b) {
		t.Fatalf("second Decode = %+v, %v; want record b", got, err)
	}
	if n+m != len(buf) {
		t.Errorf("consumed %d+%d bytes, want %d", n, m, len(buf))
	}
}

// A crash can cut the last record anywhere. Every cut must say "truncated",
// never panic, so recovery knows where to truncate the file.
func TestDecodeTruncated(t *testing.T) {
	b := mustEncode(t, sample())
	for cut := 0; cut < len(b); cut++ {
		if _, _, err := Decode(b[:cut]); !errors.Is(err, ErrTruncated) {
			t.Fatalf("Decode(first %d of %d bytes) error = %v, want ErrTruncated", cut, len(b), err)
		}
	}
}

// Flip every bit after the len field, one at a time. The CRC must catch each.
func TestDecodeFlippedBit(t *testing.T) {
	orig := mustEncode(t, sample())
	for i := posCRC; i < len(orig); i++ {
		for bit := range 8 {
			b := bytes.Clone(orig)
			b[i] ^= 1 << bit
			if _, _, err := Decode(b); !errors.Is(err, ErrChecksum) {
				t.Fatalf("flip byte %d bit %d: error = %v, want ErrChecksum", i, bit, err)
			}
		}
	}
}

// A flip inside the len field moves the record boundary. Which error comes
// back depends on the new length, but there must be one.
func TestDecodeFlippedLen(t *testing.T) {
	orig := mustEncode(t, sample())
	for i := posLen; i < posCRC; i++ {
		for bit := range 8 {
			b := bytes.Clone(orig)
			b[i] ^= 1 << bit
			if _, _, err := Decode(b); err == nil {
				t.Fatalf("flip len byte %d bit %d: Decode returned no error", i, bit)
			}
		}
	}
}

func TestDecodeUnknownVersion(t *testing.T) {
	b := mustEncode(t, sample())
	b[posVer] = 2
	reseal(b)
	if _, _, err := Decode(b); !errors.Is(err, ErrVersion) {
		t.Errorf("error = %v, want ErrVersion", err)
	}
}

// The CRC matches, but the lengths inside the record do not add up. Only a
// bug in an encoder (or a fuzzer) produces these.
func TestDecodeCorrupt(t *testing.T) {
	t.Run("len too small for the fixed fields", func(t *testing.T) {
		b := []byte{3, 0, 0, 0, 0, 0, 0}
		if _, _, err := Decode(b); !errors.Is(err, ErrCorrupt) {
			t.Errorf("error = %v, want ErrCorrupt", err)
		}
	})
	t.Run("type_len runs past the end", func(t *testing.T) {
		b := mustEncode(t, sample())
		b[posTypeLen] = 255
		reseal(b)
		if _, _, err := Decode(b); !errors.Is(err, ErrCorrupt) {
			t.Errorf("error = %v, want ErrCorrupt", err)
		}
	})
	t.Run("trailing bytes inside len", func(t *testing.T) {
		b := append(mustEncode(t, sample()), 0xEE)
		binary.LittleEndian.PutUint32(b[posLen:], uint32(len(b)-posCRC))
		reseal(b)
		if _, _, err := Decode(b); !errors.Is(err, ErrCorrupt) {
			t.Errorf("error = %v, want ErrCorrupt", err)
		}
	})
}

// In Python, b[3:7] makes a new bytes object. In Go, b[3:7] is a view into
// the same array. A decoded record must stay intact after the caller reuses
// its read buffer, so Decode has to copy Key and Payload.
func TestDecodeDoesNotAliasInput(t *testing.T) {
	r := sample()
	b := mustEncode(t, r)
	got, _, err := Decode(b)
	if err != nil {
		t.Fatalf("Decode: %v", err)
	}
	for i := range b {
		b[i] = 0
	}
	if !same(got, r) {
		t.Errorf("record changed after its input buffer was zeroed: %+v", got)
	}
}

// Run the fuzzer with:
//
//	go test ./internal/record -run '^$' -fuzz FuzzDecode -fuzztime 60s
//
// Without -fuzz, `go test` runs only the seed inputs below.
func FuzzDecode(f *testing.F) {
	f.Add(mustEncode(f, sample()))
	f.Add(mustEncode(f, Record{}))
	f.Add([]byte{})
	f.Add([]byte{0xff, 0xff, 0xff, 0xff})

	f.Fuzz(func(t *testing.T, b []byte) {
		r, n, err := Decode(b) // must never panic
		if err != nil {
			for _, known := range []error{ErrTruncated, ErrChecksum, ErrVersion, ErrCorrupt} {
				if errors.Is(err, known) {
					return
				}
			}
			t.Fatalf("Decode returned an unlisted error: %v", err)
		}
		if n < 4 || n > len(b) {
			t.Fatalf("Decode consumed %d bytes of %d", n, len(b))
		}
		// The encoding is canonical: one record has exactly one byte form.
		again, err := Encode(r)
		if err != nil {
			t.Fatalf("Encode of a decoded record: %v", err)
		}
		if !bytes.Equal(again, b[:n]) {
			t.Fatalf("re-encoded bytes differ\n got %x\nwant %x", again, b[:n])
		}
	})
}
