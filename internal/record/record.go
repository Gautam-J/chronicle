// Package record encodes and decodes one log record: the binary envelope
// that segment files are made of. The byte layout is in the design doc
// (Data model, "Record layout").
package record

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"hash/crc32"
)

// Record is one event in the log. Times are Unix microseconds.
type Record struct {
	Flags      uint8
	Offset     uint64
	EventTime  int64
	IngestTime int64
	StreamID   uint32
	Type       string // at most 255 bytes
	Key        []byte // idempotency key, at most 65535 bytes
	Schema     uint16
	Payload    []byte
	PrevHash   [32]byte
	Hash       [32]byte
}

var (
	ErrTooLong   = errors.New("record: field too long")
	ErrTruncated = errors.New("record: truncated")
	ErrChecksum  = errors.New("record: checksum mismatch")
	ErrVersion   = errors.New("record: unknown version")
	ErrCorrupt   = errors.New("record: corrupt lengths")
)

var castagnoli = crc32.MakeTable(crc32.Castagnoli)

const minBody = 107 // crc through hash, with empty type, key, and payload

// Encode returns the bytes of r, ready to append to a segment.
func Encode(r Record) ([]byte, error) {
	if len(r.Type) > 255 || len(r.Key) > (64<<10)-1 { // 64 * (2**10 = 1024)
		return nil, ErrTooLong
	}

	// create a buffer of 8 bytes, 4 for len, 4 for crc + the exact capacity required to avoid rebuilding the dynamic array
	b := make([]byte, 8, minBody+4+len(r.Type)+len(r.Key)+len(r.Payload))

	// add version = 1
	b = append(b, 1)

	// append bytes of rest of the data in Record
	b = append(b, r.Flags)
	b = binary.LittleEndian.AppendUint64(b, r.Offset)
	b = binary.LittleEndian.AppendUint64(b, uint64(r.EventTime))
	b = binary.LittleEndian.AppendUint64(b, uint64(r.IngestTime))
	b = binary.LittleEndian.AppendUint32(b, r.StreamID)
	b = append(b, byte(len(r.Type)))
	b = append(b, r.Type...)
	b = binary.LittleEndian.AppendUint16(b, uint16(len(r.Key)))
	b = append(b, r.Key...)
	b = binary.LittleEndian.AppendUint16(b, r.Schema)
	b = binary.LittleEndian.AppendUint32(b, uint32(len(r.Payload)))
	b = append(b, r.Payload...)
	b = append(b, r.PrevHash[:]...)
	b = append(b, r.Hash[:]...)

	// backfill length of record
	binary.LittleEndian.PutUint32(b[0:], uint32(len(b)-4))

	// backfill checksum of record, ignoring len and checksum itself
	checkSum := crc32.Checksum(b[8:], castagnoli)
	binary.LittleEndian.PutUint32(b[4:], checkSum)

	return b, nil
}

// Decode reads the first record in b. It returns the record and the number of
// bytes it used, so the caller can step to the next record.
func Decode(b []byte) (Record, int, error) {
	// validate if bytes data is correctly structured and not corrupt
	if len(b) < 4 {
		return Record{}, 0, ErrTruncated
	}

	// read the first 4 bytes to get length
	n := int(binary.LittleEndian.Uint32(b))
	if len(b)-4 < n {
		return Record{}, 0, ErrTruncated
	}

	if n < minBody {
		return Record{}, 0, ErrCorrupt
	}

	body := b[4 : 4+n]

	checkSum := crc32.Checksum(body[4:], castagnoli)
	if binary.LittleEndian.Uint32(body[0:4]) != checkSum {
		return Record{}, 0, ErrChecksum
	}

	p := body[4:]
	if p[0] != 1 {
		return Record{}, 0, ErrVersion
	}

	// build Record{}
	r := Record{}
	r.Flags = p[1]
	r.Offset = binary.LittleEndian.Uint64(p[2:10])
	r.EventTime = int64(binary.LittleEndian.Uint64(p[10:]))
	r.IngestTime = int64(binary.LittleEndian.Uint64(p[18:]))
	r.StreamID = binary.LittleEndian.Uint32(p[26:])

	p = p[30:]
	tl := int(p[0])
	p = p[1:]

	if len(p) < tl+2 {
		return Record{}, 0, ErrCorrupt
	}

	r.Type = string(p[:tl])
	p = p[tl:]

	kl := int(binary.LittleEndian.Uint16(p))
	p = p[2:]
	if len(p) < kl+2+4 {
		return Record{}, 0, ErrCorrupt
	}

	r.Key = bytes.Clone(p[:kl])
	p = p[kl:]

	r.Schema = binary.LittleEndian.Uint16(p)
	p = p[2:]

	pl := int(binary.LittleEndian.Uint32(p))
	p = p[4:]
	if len(p) != pl+64 {
		return Record{}, 0, ErrCorrupt
	}

	r.Payload = bytes.Clone(p[:pl])
	p = p[pl:]

	copy(r.PrevHash[:], p[:32])
	copy(r.Hash[:], p[32:])

	return r, 4 + n, nil
}

// ChainHash returns SHA-256(prev_hash || every byte from ver up to
// and including payload): the bytes of Encode without len, crc, and the 2 hashes
func ChainHash(r Record) ([32]byte, error) {
	b, err := Encode(r)
	if err != nil {
		return [32]byte{}, err
	}

	h := sha256.New()
	if _, err := h.Write(r.PrevHash[:]); err != nil {
		return [32]byte{}, err
	}

	if _, err := h.Write(b[8 : len(b)-64]); err != nil {
		return [32]byte{}, err
	}

	return [32]byte(h.Sum(nil)), nil
}
