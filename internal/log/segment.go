// Package log is Chronicle's append-only log: segment files of encoded
// records, each with a sparse offset index (design doc: Storage engine).
package log

import (
	"encoding/binary"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"

	"github.com/Gautam-J/chronicle/internal/record"
)

var (
	ErrNotFound  = errors.New("log: offset not found")
	ErrBadOffset = errors.New("log: offset out of order")
)

type entry struct {
	rel uint32
	pos uint32
}

// A segment is one .log file of records with consecutive offsets, starting
// at base, plus a .idx file: one 8-byte entry (u32 offset-base, u32 byte
// position) per interval bytes of log.
type segment struct {
	log         *os.File
	idx         *os.File
	base        uint64
	next        uint64
	sz          int64
	interval    int
	lastIndexed int64
	entries     []entry
}

func newSegment(dir string, base uint64, interval int) (*segment, error) {
	name := filepath.Join(dir, fmt.Sprintf("%020d", base))
	log, err := os.OpenFile(name+".log", os.O_RDWR|os.O_CREATE|os.O_APPEND, 0o600)
	if err != nil {
		return nil, err
	}

	idx, err := os.OpenFile(name+".idx", os.O_RDWR|os.O_CREATE|os.O_APPEND, 0o600)
	if err != nil {
		log.Close()
		return nil, err
	}
	s := &segment{
		log:      log,
		idx:      idx,
		base:     base,
		next:     base,
		interval: interval,
	}
	return s, nil
}

// append writes r at the end of the segment. r.Offset must be the next offset.
func (s *segment) append(r record.Record) error {
	if r.Offset != s.next {
		return fmt.Errorf("%w: got %d, want %d", ErrBadOffset, r.Offset, s.next)
	}

	b, err := record.Encode(r)
	if err != nil {
		return err
	}

	if len(s.entries) == 0 || s.sz-s.lastIndexed >= int64(s.interval) {
		rel := r.Offset - s.base

		var buf [8]byte
		binary.LittleEndian.PutUint32(buf[:], uint32(rel))
		binary.LittleEndian.PutUint32(buf[4:], uint32(s.sz))
		if _, err := s.idx.Write(buf[:]); err != nil {
			return err
		}

		s.entries = append(s.entries, entry{uint32(rel), uint32(s.sz)})
		s.lastIndexed = s.sz
	}

	if _, err := s.log.Write(b); err != nil {
		return err
	}
	s.sz += int64(len(b))
	s.next++
	return nil
}

// read returns the record at offset off.
func (s *segment) read(off uint64) (record.Record, error) {
	if off < s.base || off >= s.next {
		return record.Record{}, ErrNotFound
	}

	rel := uint32(off - s.base)

	// binary search to get the closest pos in sorted entries list
	j := sort.Search(len(s.entries), func(i int) bool { return s.entries[i].rel > rel })
	pos := int64(s.entries[j-1].pos)

	var h [4]byte
	for {
		if _, err := s.log.ReadAt(h[:], pos); err != nil {
			return record.Record{}, err
		}
		n := binary.LittleEndian.Uint32(h[:])

		if pos+4+int64(n) > s.sz {
			return record.Record{}, fmt.Errorf("%w: len %d at byte %d", record.ErrCorrupt, n, pos)
		}

		buf := make([]byte, 4+n)
		if _, err := s.log.ReadAt(buf, pos); err != nil {
			return record.Record{}, err
		}

		rec, used, err := record.Decode(buf)
		if err != nil {
			return record.Record{}, err
		}

		if rec.Offset == off {
			return rec, nil
		}

		pos += int64(used)
	}
}

// size returns the bytes in the .log file.
func (s *segment) size() int64 {
	return s.sz
}

func (s *segment) close() error {
	return errors.Join(s.log.Close(), s.idx.Close())
}

func (s *segment) sync() error {
	return errors.Join(s.log.Sync(), s.idx.Sync())
}
