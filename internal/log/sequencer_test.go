package log

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/Gautam-J/chronicle/internal/record"
)

// fakeLog records the calls the sequencer makes. With gate set, Append waits
// until the test lets it through, so a test can hold the sequencer busy.
type fakeLog struct {
	mu        sync.Mutex
	events    []string
	next      uint64
	synced    uint64 // records covered by the latest successful Sync
	appendErr error
	syncErr   error

	entered chan struct{} // receives once per Append call, if set
	gate    chan struct{} // Append waits for a value or close, if set
}

func (f *fakeLog) Append(r record.Record) (uint64, error) {
	if f.entered != nil {
		f.entered <- struct{}{}
	}
	if f.gate != nil {
		<-f.gate
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.events = append(f.events, "append")
	if f.appendErr != nil {
		return 0, f.appendErr
	}
	off := f.next
	f.next++
	return off, nil
}

func (f *fakeLog) Sync() error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.events = append(f.events, "sync")
	if f.syncErr == nil {
		f.synced = f.next
	}
	return f.syncErr
}

// isSynced reports whether the record at off was covered by a Sync.
func (f *fakeLog) isSynced(off uint64) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return off < f.synced
}

func (f *fakeLog) calls() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.events)
}

func blockingLog() *fakeLog {
	return &fakeLog{entered: make(chan struct{}, 100), gate: make(chan struct{})}
}

// always is the mode the 3b tests were written for: one fsync per record.
func always(queue int) SequencerOptions {
	return SequencerOptions{Queue: queue, Mode: DurabilityAlways}
}

func startSequencer(t *testing.T, l appender, o SequencerOptions) *Sequencer {
	t.Helper()
	s := NewSequencer(l, o)
	// A sequencer stuck on an unread reply would make Close hang forever.
	t.Cleanup(func() { within(t, 2*time.Second, "Close", func() { s.Close() }) })
	return s
}

// within fails the test if f does not finish in d, instead of hanging.
func within(t *testing.T, d time.Duration, what string, f func()) {
	t.Helper()
	done := make(chan struct{})
	go func() { f(); close(done) }()
	select {
	case <-done:
	case <-time.After(d):
		t.Fatalf("%s did not finish within %v", what, d)
	}
}

// goAppend starts an Append in a goroutine and returns where its result lands.
func goAppend(s *Sequencer, ctx context.Context) <-chan error {
	errc := make(chan error, 1)
	go func() {
		_, err := s.Append(ctx, rec(0))
		errc <- err
	}()
	return errc
}

func waitErr(t *testing.T, errc <-chan error) error {
	t.Helper()
	select {
	case err := <-errc:
		return err
	case <-time.After(2 * time.Second):
		t.Fatal("Append did not return within 2s")
		return nil
	}
}

// waitEntered waits until the sequencer has called the fake's Append.
func waitEntered(t *testing.T, f *fakeLog) {
	t.Helper()
	select {
	case <-f.entered:
	case <-time.After(2 * time.Second):
		t.Fatal("the sequencer never called Append")
	}
}

// waitQueued waits until n requests sit in the queue (not yet taken).
func waitQueued(t *testing.T, s *Sequencer, n int) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for len(s.reqs) != n {
		if time.Now().After(deadline) {
			t.Fatalf("queue length = %d, want %d", len(s.reqs), n)
		}
		time.Sleep(time.Millisecond)
	}
}

func TestSequencerAppendsInOrder(t *testing.T) {
	l := openLog(t, t.TempDir(), Options{})
	s := startSequencer(t, l, always(16))
	for want := range uint64(10) {
		got, err := s.Append(context.Background(), rec(0))
		if err != nil {
			t.Fatalf("Append: %v", err)
		}
		if got != want {
			t.Errorf("Append returned offset %d, want %d", got, want)
		}
	}
}

// Mode "always": the caller hears back only after its record was fsynced.
func TestSequencerSyncsBeforeReply(t *testing.T) {
	f := &fakeLog{}
	s := startSequencer(t, f, always(16))
	for range 3 {
		if _, err := s.Append(context.Background(), rec(0)); err != nil {
			t.Fatal(err)
		}
		if c := f.calls(); c[len(c)-1] != "sync" {
			t.Fatalf("Append returned before Sync; calls so far: %v", c)
		}
	}
	want := []string{"append", "sync", "append", "sync", "append", "sync"}
	if got := f.calls(); !slices.Equal(got, want) {
		t.Errorf("calls = %v, want %v", got, want)
	}
}

func TestSequencerReturnsErrors(t *testing.T) {
	diskFull := errors.New("disk full")
	for name, f := range map[string]*fakeLog{
		"append fails": {appendErr: diskFull},
		"sync fails":   {syncErr: diskFull},
	} {
		t.Run(name, func(t *testing.T) {
			s := startSequencer(t, f, always(16))
			if _, err := s.Append(context.Background(), rec(0)); !errors.Is(err, diskFull) {
				t.Errorf("Append error = %v, want the log's error", err)
			}
		})
	}
}

// Many writers at once: every record gets a unique offset, and the record
// stored at each offset is the one whose Append returned that offset.
func TestSequencerConcurrentWriters(t *testing.T) {
	t.Run("always", func(t *testing.T) { concurrentWriters(t, always(16)) })
	t.Run("batch", func(t *testing.T) { concurrentWriters(t, SequencerOptions{Queue: 16}) })
}

func concurrentWriters(t *testing.T, o SequencerOptions) {
	// In mode always each Append waits for its own fsync (about 5 ms on the
	// Pi's SD card), so keep the count small: 160 appends take about a second.
	const writers, each = 8, 20
	l := openLog(t, t.TempDir(), Options{})
	s := startSequencer(t, l, o)

	var mu sync.Mutex
	sent := map[uint64]string{} // offset -> payload
	var wg sync.WaitGroup
	for w := range writers {
		wg.Go(func() {
			for i := range each {
				r := rec(0)
				r.Payload = fmt.Appendf(nil, `{"w":%d,"i":%d}`, w, i)
				off, err := s.Append(context.Background(), r)
				if err != nil {
					t.Errorf("writer %d: %v", w, err)
					return
				}
				mu.Lock()
				if prev, dup := sent[off]; dup {
					t.Errorf("offset %d returned twice (%s and %s)", off, prev, r.Payload)
				}
				sent[off] = string(r.Payload)
				mu.Unlock()
			}
		})
	}
	within(t, 15*time.Second, "writers", wg.Wait)

	if len(sent) != writers*each {
		t.Fatalf("%d offsets returned, want %d", len(sent), writers*each)
	}
	for off, payload := range sent {
		got, err := l.Read(off)
		if err != nil {
			t.Fatalf("Read(%d): %v", off, err)
		}
		if string(got.Payload) != payload {
			t.Errorf("offset %d holds %s, want %s", off, got.Payload, payload)
		}
	}
}

// A full queue rejects at once instead of making the caller wait. The HTTP
// handler turns ErrBusy into 429 with Retry-After: backpressure, not failure.
func TestSequencerBusy(t *testing.T) {
	f := blockingLog()
	s := startSequencer(t, f, always(1))

	first := goAppend(s, context.Background())
	waitEntered(t, f) // the sequencer is now stuck inside Append
	second := goAppend(s, context.Background())
	waitQueued(t, s, 1) // the queue (size 1) is full

	if _, err := s.Append(context.Background(), rec(0)); !errors.Is(err, ErrBusy) {
		t.Errorf("third Append error = %v, want ErrBusy", err)
	}

	close(f.gate)
	for i, errc := range []<-chan error{first, second} {
		if err := waitErr(t, errc); err != nil {
			t.Errorf("queued Append %d: %v", i+1, err)
		}
	}
}

// A caller that gives up stops waiting, but the sequencer must not get stuck
// trying to hand it a result nobody will read.
func TestSequencerCallerGivesUp(t *testing.T) {
	f := blockingLog()
	s := startSequencer(t, f, always(4))

	ctx, cancel := context.WithCancel(context.Background())
	gaveUp := goAppend(s, ctx)
	waitEntered(t, f)
	cancel()
	if err := waitErr(t, gaveUp); !errors.Is(err, context.Canceled) {
		t.Errorf("cancelled Append error = %v, want context.Canceled", err)
	}

	close(f.gate) // the abandoned request finishes now
	if err := waitErr(t, goAppend(s, context.Background())); err != nil {
		t.Errorf("Append after an abandoned one: %v", err)
	}
}

// Close lets queued requests finish, then refuses new ones. Calling it twice
// is safe.
func TestSequencerClose(t *testing.T) {
	f := blockingLog()
	s := NewSequencer(f, always(4))

	first := goAppend(s, context.Background())
	waitEntered(t, f)
	second := goAppend(s, context.Background())
	waitQueued(t, s, 1)

	closed := make(chan error, 1)
	go func() { closed <- s.Close() }()
	close(f.gate)

	for i, errc := range []<-chan error{first, second} {
		if err := waitErr(t, errc); err != nil {
			t.Errorf("Append %d queued before Close: %v", i+1, err)
		}
	}
	within(t, 2*time.Second, "Close", func() {
		if err := <-closed; err != nil {
			t.Errorf("Close: %v", err)
		}
	})

	if _, err := s.Append(context.Background(), rec(0)); !errors.Is(err, ErrClosed) {
		t.Errorf("Append after Close: error = %v, want ErrClosed", err)
	}
	within(t, 2*time.Second, "second Close", func() {
		if err := s.Close(); err != nil {
			t.Errorf("second Close: %v", err)
		}
	})
}
