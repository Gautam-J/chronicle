package log

import (
	"context"
	"errors"
	"sync"

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
}

// NewSequencer starts the sequencer goroutine. queue is the channel size.
func NewSequencer(l appender, queue int) *Sequencer {
	s := &Sequencer{}
	s.log = l
	s.reqs = make(chan request, queue)
	s.done = make(chan struct{})
	go s.run()
	return s
}

func (s *Sequencer) run() {
	defer close(s.done)

	for req := range s.reqs {
		off, err := s.log.Append(req.rec)
		if err == nil {
			err = s.log.Sync()
		}
		req.reply <- result{off: off, err: err}
	}
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
