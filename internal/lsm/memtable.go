package lsm

import (
	"bytes"
	"errors"
	"iter"

	chlog "github.com/Gautam-J/chronicle/internal/log"
	"github.com/Gautam-J/chronicle/internal/record"
)

type Memtable struct {
	wal *chlog.Log
	sl  *SkipList[[]byte, []byte]
}

func OpenMemtable(dir string) (*Memtable, error) {
	wal, err := chlog.Open(dir, chlog.Options{})
	if err != nil {
		return nil, err
	}

	sl := New[[]byte, []byte](bytes.Compare)

	var off uint64 = 0
	for {
		r, err := wal.Read(off)
		if errors.Is(err, chlog.ErrNotFound) {
			break
		}
		if err != nil {
			wal.Close()
			return nil, err
		}

		sl.Put(r.Key, r.Payload)
		off++
	}

	return &Memtable{wal: wal, sl: sl}, nil
}

func (m *Memtable) Put(k, v []byte) error {
	k, v = bytes.Clone(k), bytes.Clone(v)
	rec := record.Record{Type: "put", Key: k, Payload: v}

	if _, err := m.wal.Append(rec); err != nil {
		return err
	}

	m.sl.Put(k, v)
	return nil
}

func (m *Memtable) Get(k []byte) ([]byte, bool) {
	return m.sl.Get(k)
}

func (m *Memtable) All() iter.Seq2[[]byte, []byte] {
	return m.sl.All()
}

func (m *Memtable) From(k []byte) iter.Seq2[[]byte, []byte] {
	return m.sl.From(k)
}

func (m *Memtable) Len() int {
	return m.sl.Len()
}

func (m *Memtable) Sync() error {
	return m.wal.Sync()
}

func (m *Memtable) Close() error {
	return m.wal.Close()
}
