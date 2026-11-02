package lsm

import (
	"bufio"
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"hash/crc32"
	"iter"
	"os"
	"sort"
)

const (
	blockSize  = 4 << 10
	footerSize = 20
	magic      = 0x43535354 // CSST
	bitsPerKey = 10
	probes     = 7
	fnvOffset  = 14695981039346656037
	fnvPrime   = 1099511628211
)

var (
	ErrOrder   = errors.New("lsm: keys not strictly increasing")
	ErrCorrupt = errors.New("lsm: corrupt table")
)

var castagnoli = crc32.MakeTable(crc32.Castagnoli)

// blockHandle says where one data block is: its first key, and its byte
// offset and length (CRC included) in the file.
type blockHandle struct {
	first []byte
	off   uint64
	len   uint64
}

// Table is an open SSTable. The index lives in memory; blocks are read on
// demand.
type Table struct {
	f          *os.File
	index      []blockHandle
	filter     []byte
	blockReads int
}

// WriteTable writes entries, which must be in strictly increasing key order,
// as an SSTable at path. On any error it removes the file.
func WriteTable(path string, entries iter.Seq2[[]byte, []byte]) (err error) {
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer func() {
		if err != nil {
			f.Close()
			os.Remove(path)
		}
	}()

	var off uint64
	var block, index, prev, first []byte

	w := bufio.NewWriter(f)
	started := false
	var hashes []uint64

	flush := func() error {
		crc := crc32.Checksum(block, castagnoli)
		block = binary.LittleEndian.AppendUint32(block, crc)
		index = binary.AppendUvarint(index, uint64(len(first)))
		index = append(index, first...)
		index = binary.AppendUvarint(index, off)
		index = binary.AppendUvarint(index, uint64(len(block)))

		if _, err := w.Write(block); err != nil {
			return err
		}

		off += uint64(len(block))
		block = block[:0]
		return nil
	}

	for k, v := range entries {
		if started && bytes.Compare(prev, k) >= 0 {
			return fmt.Errorf("%w: %q after %q", ErrOrder, k, prev)
		}

		started = true
		hashes = append(hashes, bloomHash(k))
		prev = append(prev[:0], k...)
		if len(block) == 0 {
			first = append(first[:0], k...)
		}

		block = binary.AppendUvarint(block, uint64(len(k)))
		block = binary.AppendUvarint(block, uint64(len(v)))
		block = append(block, k...)
		block = append(block, v...)

		if len(block) >= blockSize {
			if err := flush(); err != nil {
				return err
			}
		}
	}

	if len(block) > 0 {
		if err := flush(); err != nil {
			return err
		}
	}

	filter := buildFilter(hashes)
	crc := crc32.Checksum(filter, castagnoli)
	filter = binary.LittleEndian.AppendUint32(filter, crc)

	if _, err := w.Write(filter); err != nil {
		return err
	}

	ioff := off + uint64(len(filter))
	crc = crc32.Checksum(index, castagnoli)
	index = binary.LittleEndian.AppendUint32(index, crc)

	var foot [footerSize]byte
	binary.LittleEndian.PutUint64(foot[0:], ioff)
	binary.LittleEndian.PutUint32(foot[8:], uint32(len(index)))
	binary.LittleEndian.PutUint32(foot[12:], uint32(len(filter)))
	binary.LittleEndian.PutUint32(foot[16:], magic)

	if _, err := w.Write(index); err != nil {
		return err
	}

	if _, err := w.Write(foot[:]); err != nil {
		return err
	}

	if err := w.Flush(); err != nil {
		return err
	}
	if err := f.Sync(); err != nil {
		return err
	}
	return f.Close()
}

// OpenTable opens the SSTable at path and loads its index.
func OpenTable(path string) (*Table, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}

	t, err := load(f)
	if err != nil {
		f.Close()
		return nil, fmt.Errorf("%s: %w", path, err)
	}

	return t, nil
}

func load(f *os.File) (*Table, error) {
	st, err := f.Stat()
	if err != nil {
		return nil, err
	}

	size := st.Size()
	if size < footerSize {
		return nil, fmt.Errorf("%w: %d bytes, shorter than the footer", ErrCorrupt, size)
	}

	var foot [footerSize]byte
	if _, err := f.ReadAt(foot[:], size-footerSize); err != nil {
		return nil, err
	}

	ioff := binary.LittleEndian.Uint64(foot[0:])
	ilen := uint64(binary.LittleEndian.Uint32(foot[8:]))
	flen := uint64(binary.LittleEndian.Uint32(foot[12:]))

	if binary.LittleEndian.Uint32(foot[16:]) != magic {
		return nil, fmt.Errorf("%w: bad magic", ErrCorrupt)
	}

	if ioff < flen || ioff+ilen != uint64(size-footerSize) {
		return nil, fmt.Errorf("%w: index at %d+%d in a %d-byte file", ErrCorrupt, ioff, ilen, size)
	}

	raw := make([]byte, ilen)
	if _, err := f.ReadAt(raw, int64(ioff)); err != nil {
		return nil, err
	}

	b, err := checked(raw)
	if err != nil {
		return nil, fmt.Errorf("index: %w", err)
	}

	fraw := make([]byte, flen)
	if _, err := f.ReadAt(fraw, int64(ioff-flen)); err != nil {
		return nil, err
	}

	filter, err := checked(fraw)
	if err != nil {
		return nil, fmt.Errorf("filter: %w", err)
	}

	if len(filter) < 2 || filter[len(filter)-1] == 0 {
		return nil, fmt.Errorf("%w: filter of %d bytes", ErrCorrupt, len(filter))
	}

	t := &Table{f: f, filter: filter}

	for len(b) > 0 {
		kl, rest, ok := uvarint(b)
		if !ok || kl > uint64(len(rest)) {
			return nil, fmt.Errorf("%w: index item %d", ErrCorrupt, len(t.index))
		}

		var ok1, ok2 bool
		h := blockHandle{first: rest[:kl]}
		h.off, rest, ok1 = uvarint(rest[kl:])
		h.len, rest, ok2 = uvarint(rest)

		if !ok1 || !ok2 || h.off+h.len > ioff-flen {
			return nil, fmt.Errorf("%w: index item %d", ErrCorrupt, len(t.index))
		}

		t.index = append(t.index, h)
		b = rest
	}
	return t, nil
}

func checked(b []byte) ([]byte, error) {
	if len(b) < 4 {
		return nil, fmt.Errorf("%w: %d bytes, too short for a checksum", ErrCorrupt, len(b))
	}

	body := b[:len(b)-4]
	sum := binary.LittleEndian.Uint32(b[len(b)-4:])

	if crc32.Checksum(body, castagnoli) != sum {
		return nil, fmt.Errorf("%w: checksum mismatch", ErrCorrupt)
	}

	return body, nil
}

func uvarint(b []byte) (uint64, []byte, bool) {
	n, size := binary.Uvarint(b)
	if size <= 0 {
		return 0, nil, false
	}
	return n, b[size:], true
}

// Get returns the value stored for k. It reads at most one block.
func (t *Table) Get(k []byte) ([]byte, bool, error) {
	i := sort.Search(len(t.index), func(i int) bool {
		return bytes.Compare(t.index[i].first, k) > 0
	}) - 1

	if i < 0 {
		return nil, false, nil
	}

	// bloom filter says definitely does not exist
	if !mayContain(t.filter, k) {
		return nil, false, nil
	}

	h := t.index[i]
	raw := make([]byte, h.len)
	t.blockReads++

	if _, err := t.f.ReadAt(raw, int64(h.off)); err != nil {
		return nil, false, err
	}

	b, err := checked(raw)
	if err != nil {
		return nil, false, fmt.Errorf("block %d: %w", i, err)
	}

	for len(b) > 0 {
		kl, rest, ok1 := uvarint(b)
		vl, rest, ok2 := uvarint(rest)
		if !ok1 || !ok2 || kl > uint64(len(rest)) || vl > uint64(len(rest))-kl {
			return nil, false, fmt.Errorf("%w: block %d", ErrCorrupt, i)
		}

		key, val := rest[:kl], rest[kl:kl+vl]
		b = rest[kl+vl:]

		switch bytes.Compare(key, k) {
		case 0:
			return val, true, nil
		case 1:
			return nil, false, nil
		}
	}
	return nil, false, nil
}

// Close closes the table's file.
func (t *Table) Close() error {
	return t.f.Close()
}

// FNV-1a, 64-bit hashing algorithm
func bloomHash(k []byte) uint64 {
	var h uint64 = fnvOffset
	for _, c := range k {
		h = (h ^ uint64(c)) * fnvPrime
	}
	return h
}

// buildFilter returns a Bloom filter over the given key hashes: a bit array,
// then one byte holding the number of probes.
func buildFilter(hashes []uint64) []byte {
	nBytes := (max(len(hashes)*bitsPerKey, 64) + 7) / 8
	nBits := uint32(nBytes * 8)

	f := make([]byte, nBytes+1)
	for _, h := range hashes {
		// split one 64-bit hash into two 32-bit hashes
		h1, h2 := uint32(h), uint32(h>>32)

		for i := range uint32(probes) {
			// set each bit per probe
			bit := (h1 + i*h2) % nBits
			f[bit/8] |= 1 << (bit % 8)
		}
	}
	f[nBytes] = probes
	return f
}

// mayContain reports whether k may be in the filter. false is certain;
// true is wrong about 1% of the time.
func mayContain(f, k []byte) bool {
	nBits := uint32(len(f)-1) * 8
	h := bloomHash(k)
	h1, h2 := uint32(h), uint32(h>>32)

	for i := range uint32(f[len(f)-1]) {
		bit := (h1 + i*h2) % nBits

		// if any of the 7 bits is 0, key does not exist
		if f[bit/8]&(1<<(bit%8)) == 0 {
			return false
		}
	}
	return true
}
