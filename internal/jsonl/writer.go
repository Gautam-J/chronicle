// Package jsonl appends lines to a JSON-lines file. It is M0's storage;
// M1 replaces it with the binary segment log.
package jsonl

import (
	"bytes"
	"errors"
	"os"
	"sync"
)

type Writer struct {
	mu sync.Mutex
	f  *os.File
}

func Open(path string) (*Writer, error) {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0o600)
	if err != nil {
		return nil, err
	}
	return &Writer{f: f}, nil
}

func (w *Writer) Append(line []byte) error {
	if bytes.IndexByte(line, '\n') >= 0 {
		return errors.New("jsonl: line contains newline")
	}

	buf := make([]byte, 0, len(line)+1)
	buf = append(buf, line...)
	buf = append(buf, '\n')

	w.mu.Lock()
	defer w.mu.Unlock()

	_, err := w.f.Write(buf)
	return err
}

func (w *Writer) Close() error {
	return w.f.Close()
}
