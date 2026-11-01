package main

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	chlog "github.com/Gautam-J/chronicle/internal/log"
	"github.com/Gautam-J/chronicle/internal/record"
)

func newLog(t *testing.T) *chlog.Log {
	t.Helper()
	l, err := chlog.Open(t.TempDir(), chlog.Options{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { l.Close() })
	return l
}

func readAll(t *testing.T, l *chlog.Log) []record.Record {
	t.Helper()
	var out []record.Record
	for off := uint64(0); ; off++ {
		r, err := l.Read(off)
		if errors.Is(err, chlog.ErrNotFound) {
			return out
		}
		if err != nil {
			t.Fatalf("Read(%d): %v", off, err)
		}
		out = append(out, r)
	}
}

// Real lines from M0's points.jsonl, shortened. The body is stored byte for
// byte, including OwnTracks' escaped slashes.
const (
	locationLine  = `{"ingest_time":1792488059468468,"body":{"_type":"location","tid":"2C","lat":18.542728,"lon":73.94095,"tst":1792488050,"topic":"owntracks\/gautam\/A9"}}`
	waypointsLine = `{"ingest_time":1792488039258417,"body":{"_type":"waypoints","topic":"owntracks\/gautam\/A9\/waypoints","waypoints":[]}}`
)

func TestImportMapsFields(t *testing.T) {
	l := newLog(t)
	n, err := importJSONL(strings.NewReader(locationLine+"\n"+waypointsLine+"\n"), l)
	if err != nil || n != 2 {
		t.Fatalf("importJSONL = %d, %v; want 2, nil", n, err)
	}
	got := readAll(t, l)
	if len(got) != 2 {
		t.Fatalf("log has %d records, want 2", len(got))
	}

	loc, wps := got[0], got[1]
	if loc.Type != "location" || loc.IngestTime != 1792488059468468 || loc.EventTime != 1792488050_000000 {
		t.Errorf("location record: type %q, ingest %d, event %d; want location, 1792488059468468, 1792488050000000 (tst in µs)",
			loc.Type, loc.IngestTime, loc.EventTime)
	}
	if want := `{"_type":"location","tid":"2C","lat":18.542728,"lon":73.94095,"tst":1792488050,"topic":"owntracks\/gautam\/A9"}`; string(loc.Payload) != want {
		t.Errorf("location payload =\n%s\nwant the body byte for byte:\n%s", loc.Payload, want)
	}
	// No tst: the best estimate of when it happened is when it arrived.
	if wps.Type != "waypoints" || wps.EventTime != wps.IngestTime {
		t.Errorf("waypoints record: type %q, event %d, ingest %d; want waypoints with event = ingest", wps.Type, wps.EventTime, wps.IngestTime)
	}
}

// File order is ingest order. Offsets must follow it, not tst, which arrives
// out of order when the phone sends a backlog.
func TestImportKeepsFileOrder(t *testing.T) {
	var b strings.Builder
	for i := range 50 {
		fmt.Fprintf(&b, `{"ingest_time":%d,"body":{"_type":"location","tst":%d,"i":%d}}`+"\n", 1000+i, 900-i, i)
	}
	l := newLog(t)
	if _, err := importJSONL(strings.NewReader(b.String()), l); err != nil {
		t.Fatal(err)
	}
	for i, r := range readAll(t, l) {
		if want := fmt.Sprintf(`"i":%d}`, i); !strings.HasSuffix(string(r.Payload), want) {
			t.Fatalf("offset %d holds %s, want line %d", i, r.Payload, i+1)
		}
	}
	if n, err := l.Verify(); n != 50 || err != nil {
		t.Errorf("Verify = %d, %v; want 50, nil", n, err)
	}
}

// One bad line stops the import before anything is written, so a fixed file
// can be imported again into the same, still empty, log.
func TestImportBadLineWritesNothing(t *testing.T) {
	for name, bad := range map[string]string{
		"not json":   `{"ingest_time":1,"body":`,
		"no _type":   `{"ingest_time":1,"body":{"lat":1}}`,
		"body array": `{"ingest_time":1,"body":[1,2]}`,
	} {
		t.Run(name, func(t *testing.T) {
			l := newLog(t)
			in := locationLine + "\n" + waypointsLine + "\n" + bad + "\n"
			if _, err := importJSONL(strings.NewReader(in), l); err == nil || !strings.Contains(err.Error(), "line 3") {
				t.Errorf("error = %v, want one naming line 3", err)
			}
			if got := readAll(t, l); len(got) != 0 {
				t.Errorf("log has %d records after a failed import, want 0", len(got))
			}
		})
	}
}

// Importing twice would store every point twice. Refuse a log that already
// has records.
func TestImportRefusesNonEmptyLog(t *testing.T) {
	l := newLog(t)
	if _, err := importJSONL(strings.NewReader(locationLine+"\n"), l); err != nil {
		t.Fatal(err)
	}
	if _, err := importJSONL(strings.NewReader(locationLine+"\n"), l); err == nil {
		t.Error("second import into the same log returned no error")
	}
	if got := readAll(t, l); len(got) != 1 {
		t.Errorf("log has %d records, want 1", len(got))
	}
}

// The handler accepts bodies up to 64 KiB, so a stored line can be longer
// than bufio.Scanner's default 64 KiB limit.
func TestImportLongLine(t *testing.T) {
	big := strings.Repeat("x", 64<<10)
	line := `{"ingest_time":1,"body":{"_type":"dump","pad":"` + big + `"}}`
	l := newLog(t)
	if n, err := importJSONL(strings.NewReader(line+"\n"), l); n != 1 || err != nil {
		t.Fatalf("importJSONL of a %d-byte line = %d, %v; want 1, nil", len(line), n, err)
	}
}

func TestImportEmptyFile(t *testing.T) {
	l := newLog(t)
	if n, err := importJSONL(strings.NewReader(""), l); n != 0 || err != nil {
		t.Errorf("importJSONL(empty) = %d, %v; want 0, nil", n, err)
	}
}
