package log

import (
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/Gautam-J/chronicle/internal/record"
)

var (
	ErrLocked = errors.New("log: directory locked")
	ErrChain  = errors.New("log: hash chain corrupt")
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
	last     [32]byte
	lock     *os.File
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

	l := &Log{
		dir:  dir,
		opts: o,
	}

	fail := func(err error) (*Log, error) {
		l.Close()
		return nil, err
	}

	lock, err := os.OpenFile(filepath.Join(dir, "LOCK"), os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		return fail(err)
	}
	l.lock = lock

	if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		return fail(fmt.Errorf("%w: %s: %v", ErrLocked, dir, err))
	}

	des, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}

	names := make([]string, 0, len(des))
	for _, de := range des {
		name, ok := strings.CutSuffix(de.Name(), ".log")
		if !ok {
			continue
		}
		names = append(names, name)
	}

	for i, name := range names {
		base, err := strconv.ParseUint(name, 10, 64)
		if err != nil {
			return fail(err)
		}

		if len(l.segments) != 0 && l.segments[len(l.segments)-1].next != base {
			prev := l.segments[len(l.segments)-1]
			return fail(fmt.Errorf("%w: segment %d ends at offset %d, next segment starts at %d", record.ErrCorrupt, prev.base, prev.next, base))
		}

		seg, err := newSegment(dir, base, o.IndexInterval)
		if err != nil {
			return fail(err)
		}

		l.segments = append(l.segments, seg)

		torn, err := seg.recover()
		if err != nil {
			return fail(err)
		}

		if torn > 0 && i < len(names)-1 {
			return fail(fmt.Errorf("%w: sealed segment %d has %d bad bytes", record.ErrCorrupt, base, torn))
		}

		if torn > 0 {
			if err := seg.truncate(); err != nil {
				return fail(err)
			}
		}
	}

	if len(l.segments) == 0 {
		seg, err := newSegment(dir, 0, o.IndexInterval)
		if err != nil {
			return fail(err)
		}

		l.segments = append(l.segments, seg)
		if err := syncDir(dir); err != nil {
			return fail(err)
		}
	}

	l.next = l.segments[len(l.segments)-1].next
	l.firstAt = o.Now()

	if l.next > 0 {
		r, err := l.Read(l.next - 1)
		if err != nil {
			return fail(err)
		}

		l.last = r.Hash
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
		if err := syncDir(l.dir); err != nil {
			return 0, err
		}
		active = seg
	}

	if active.size() == 0 {
		l.firstAt = now
	}

	r.Offset = l.next
	r.PrevHash = l.last

	h, err := record.ChainHash(r)
	if err != nil {
		return 0, err
	}
	r.Hash = h

	if err := active.append(r); err != nil {
		return 0, err
	}

	l.last = r.Hash
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

	if l.lock != nil {
		err := l.lock.Close()
		errs = append(errs, err)
	}
	return errors.Join(errs...)
}

// Sync forces the active segment's writes from the OS page cache to the disk.
// Until it returns nil, a power cut can lose them.
func (l *Log) Sync() error {
	active := l.segments[len(l.segments)-1]
	return active.sync()
}

// Verify returns how many records it checked for ChainHash,
// or the offset where the chain breaks
func (l *Log) Verify() (uint64, error) {
	var prev [32]byte

	for off := range l.next {
		r, err := l.Read(off)
		if err != nil {
			return off, fmt.Errorf("offset %d: %w", off, err)
		}

		if r.PrevHash != prev {
			return off, fmt.Errorf("%w at offset %d: prev_hash does not match the hash of offset %d", ErrChain, off, int64(off)-1)
		}

		h, err := record.ChainHash(r)
		if err != nil {
			return off, err
		}

		if r.Hash != h {
			return off, fmt.Errorf("%w at offset %d: record does not match its hash", ErrChain, off)
		}

		prev = r.Hash
	}

	return l.next, nil
}

func syncDir(dir string) error {
	fd, err := os.Open(dir)
	if err != nil {
		return err
	}
	return errors.Join(fd.Sync(), fd.Close())
}
