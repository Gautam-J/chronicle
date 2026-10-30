package log

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/Gautam-J/chronicle/internal/record"
)

var (
	ErrBusy   = errors.New("log: sequencer queue full")
	ErrClosed = errors.New("log: sequencer closed")
)

// appender is what the sequencer needs from a log. *Log satisfies it; tests
// pass a fake.
type appender interface {
	Append(record.Record) (uint64, error)
	Sync() error
}

type result struct {
	off uint64
	err error
}

// request is one Append waiting in the queue.
type request struct {
	rec   record.Record
	reply chan result
}

// Sequencer is the single goroutine that owns the log. Callers hand it
// records over a bounded channel and wait for the offset back.
type Sequencer struct {
	log    appender
	reqs   chan request // the bounded queue
	closed bool
	lock   sync.RWMutex
	done   chan struct{}
	opts   SequencerOptions
}

// Durability says when a caller is told its record is safe (design doc:
// Durability modes). The zero value is the default, batch.
type Durability int

const (
	DurabilityBatch  Durability = iota // after the group-commit fsync
	DurabilityAlways                   // after an fsync of its own record
	DurabilityNone                     // after the write, no fsync; benchmarks only
)

// SequencerOptions configure a Sequencer. A zero field means "use the default".
type SequencerOptions struct {
	Queue         int           // channel size; default 1024
	Mode          Durability    // default DurabilityBatch
	MaxBatchDelay time.Duration // batch mode: collect for at most this long; default 5 ms
	MaxBatchBytes int           // batch mode: or until about this many bytes; default 64 KiB
}

// sequencerDefaults returns o with every zero field set to its default.
func sequencerDefaults(o SequencerOptions) SequencerOptions {
	if o.Queue == 0 {
		o.Queue = 1 << 10
	}

	if o.MaxBatchDelay == 0 {
		o.MaxBatchDelay = time.Millisecond * 5
	}

	if o.MaxBatchBytes == 0 {
		o.MaxBatchBytes = 64 << 10
	}
	return o
}

// NewSequencer starts the sequencer goroutine.
func NewSequencer(l appender, o SequencerOptions) *Sequencer {
	o = sequencerDefaults(o)
	s := &Sequencer{}
	s.opts = o
	s.log = l
	s.reqs = make(chan request, o.Queue)
	s.done = make(chan struct{})
	go s.run()
	return s
}

func (s *Sequencer) run() {
	defer close(s.done)

	for {
		req, ok := <-s.reqs
		if !ok {
			return
		}
		batch := []request{req}
		size := approxSize(req.rec)
		closed := false

		if s.opts.Mode == DurabilityBatch {
			timer := time.NewTimer(s.opts.MaxBatchDelay)
		collect:
			for size < s.opts.MaxBatchBytes {
				select {
				case r, ok := <-s.reqs:
					if !ok {
						closed = true
						break collect
					}
					batch = append(batch, r)
					size += approxSize(r.rec)
				case <-timer.C:
					break collect
				}
			}
			timer.Stop()
		}
		s.commit(batch)
		if closed {
			return
		}
	}
}

func approxSize(r record.Record) int {
	return 111 + len(r.Type) + len(r.Key) + len(r.Payload)
}

// Append queues r and waits until it is written and fsynced. It returns
// ErrBusy at once if the queue is full, and ctx.Err() if ctx ends first.
func (s *Sequencer) Append(ctx context.Context, r record.Record) (uint64, error) {
	if err := ctx.Err(); err != nil {
		return 0, err
	}

	req := request{rec: r, reply: make(chan result, 1)}

	s.lock.RLock()
	if s.closed {
		s.lock.RUnlock()
		return 0, ErrClosed
	}

	select {
	case s.reqs <- req:
	default:
		s.lock.RUnlock()
		return 0, ErrBusy
	}

	s.lock.RUnlock()
	select {
	case res := <-req.reply:
		return res.off, res.err
	case <-ctx.Done():
		return 0, ctx.Err()
	}
}

// Close stops taking new records, lets queued ones finish, and waits for the
// goroutine to exit. It is safe to call more than once.
func (s *Sequencer) Close() error {
	s.lock.Lock()
	if !s.closed {
		s.closed = true
		close(s.reqs)
	}

	s.lock.Unlock()
	<-s.done
	return nil
}

func (s *Sequencer) commit(batch []request) {
	results := make([]result, len(batch))

	written := false
	for i, req := range batch {
		off, err := s.log.Append(req.rec)
		results[i] = result{off: off, err: err}
		if err == nil {
			written = true
		}
	}

	if written && s.opts.Mode != DurabilityNone {
		if err := s.log.Sync(); err != nil {
			for i := range results {
				if results[i].err == nil {
					results[i].err = err
				}
			}
		}
	}

	for i, req := range batch {
		req.reply <- results[i]
	}
}
