package log

import (
	"errors"
	"fmt"
	"math"
	"os"
	"sort"
	"time"

	"github.com/Gautam-J/chronicle/internal/record"
)

// Options configure a Log. A zero field means "use the default".
type Options struct {
	MaxSegmentBytes int64         // seal the active segment at this size; default 64 MiB
	MaxSegmentAge   time.Duration // or this long after its first record; default 1 hour
	IndexInterval   int           // bytes of log per sparse index entry; default 4096
	Now             func() time.Time
}

// Log is an append-only sequence of segments. Only the last segment is
// written; the earlier ones are sealed and only read.
type Log struct {
	dir      string
	opts     Options
	segments []*segment // oldest first; the last one is active
	next     uint64
	firstAt  time.Time
}

// withDefaults returns o with every zero field set to its default.
func withDefaults(o Options) Options {
	// n << 10 = n KiB
	// n << 20 = n MiB
	// n << 30 = n GiB
	if o.MaxSegmentBytes == 0 {
		o.MaxSegmentBytes = 64 << 20
	}

	if o.MaxSegmentAge == 0 {
		o.MaxSegmentAge = time.Hour
	}

	if o.IndexInterval == 0 {
		o.IndexInterval = 4 << 10
	}

	if o.Now == nil {
		o.Now = time.Now
	}
	return o
}

// Open starts a new log in dir, creating dir if needed.
func Open(dir string, o Options) (*Log, error) {
	o = withDefaults(o)
	if o.MaxSegmentBytes > math.MaxUint32 {
		return nil, fmt.Errorf("log: MaxSegmentBytes %d is over 4 GiB (index positions are u32)", o.MaxSegmentBytes)
	}

	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}

	seg, err := newSegment(dir, 0, o.IndexInterval)
	if err != nil {
		return nil, err
	}

	l := &Log{
		dir:      dir,
		opts:     o,
		segments: []*segment{seg},
	}

	return l, nil
}

// Append writes r with the next offset, rolling to a new segment first if the
// active one is full or old. It returns the offset it assigned.
func (l *Log) Append(r record.Record) (uint64, error) {
	active := l.segments[len(l.segments)-1]
	now := l.opts.Now()

	if active.size() > 0 && (active.size() >= l.opts.MaxSegmentBytes || now.Sub(l.firstAt) >= l.opts.MaxSegmentAge) {
		if err := active.sync(); err != nil {
			return 0, err
		}

		seg, err := newSegment(l.dir, l.next, l.opts.IndexInterval)
		if err != nil {
			return 0, err
		}

		l.segments = append(l.segments, seg)
		active = seg
	}

	if active.size() == 0 {
		l.firstAt = now
	}

	r.Offset = l.next
	if err := active.append(r); err != nil {
		return 0, err
	}

	l.next++
	return r.Offset, nil
}

// Read returns the record at offset off.
func (l *Log) Read(off uint64) (record.Record, error) {
	i := sort.Search(len(l.segments), func(i int) bool { return l.segments[i].base > off })
	if i == 0 {
		return record.Record{}, ErrNotFound
	}
	return l.segments[i-1].read(off)
}

// Close closes every segment.
func (l *Log) Close() error {
	var errs []error
	for _, s := range l.segments {
		if err := s.close(); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

// Sync forces the active segment's writes from the OS page cache to the disk.
// Until it returns nil, a power cut can lose them.
func (l *Log) Sync() error {
	active := l.segments[len(l.segments)-1]
	return active.sync()
}
