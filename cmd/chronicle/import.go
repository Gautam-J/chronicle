package main

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"

	chlog "github.com/Gautam-J/chronicle/internal/log"
	"github.com/Gautam-J/chronicle/internal/record"
)

// importJSONL reads M0's points.jsonl from r and appends one record per line
// to l, an empty log. It parses every line before writing any, so one bad
// line leaves the log empty. It returns how many records it wrote.
func importJSONL(r io.Reader, l *chlog.Log) (int, error) {
	if _, err := l.Read(0); !errors.Is(err, chlog.ErrNotFound) {
		return 0, errors.New("import: the log is not empty")
	}

	var recs []record.Record
	sc := bufio.NewScanner(r)
	sc.Buffer(nil, 1<<20)

	for n := 1; sc.Scan(); n++ {
		var line struct {
			IngestTime int64           `json:"ingest_time"`
			Body       json.RawMessage `json:"body"`
		}

		if err := json.Unmarshal(sc.Bytes(), &line); err != nil {
			return 0, fmt.Errorf("line %d: %w", n, err)
		}

		var body struct {
			Type string `json:"_type"`
			Tst  *int64 `json:"tst"`
		}

		if err := json.Unmarshal(line.Body, &body); err != nil {
			return 0, fmt.Errorf("line %d: body: %w", n, err)
		}

		if body.Type == "" {
			return 0, fmt.Errorf("line %d: body has no _type", n)
		}

		event := line.IngestTime
		if body.Tst != nil {
			event = *body.Tst * 1_000_000
		}

		recs = append(recs, record.Record{
			Type:       body.Type,
			EventTime:  event,
			IngestTime: line.IngestTime,
			Payload:    line.Body,
		})
	}

	if err := sc.Err(); err != nil {
		return 0, err
	}

	for _, rec := range recs {
		if _, err := l.Append(rec); err != nil {
			return 0, err
		}
	}

	return len(recs), l.Sync()
}

func runImport(src, dir string) error {
	f, err := os.Open(src)
	if err != nil {
		return err
	}
	defer f.Close()

	l, err := chlog.Open(dir, chlog.Options{})
	if err != nil {
		return err
	}
	defer l.Close()

	n, err := importJSONL(f, l)
	if err != nil {
		return err
	}

	fmt.Printf("imported %d records into %s\n", n, dir)
	return nil
}
