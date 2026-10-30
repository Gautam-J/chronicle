package log

import (
	"context"
	"errors"
	"slices"
	"sync"
	"testing"
	"time"
)

func TestSequencerDefaults(t *testing.T) {
	got := sequencerDefaults(SequencerOptions{})
	want := SequencerOptions{Queue: 1024, Mode: DurabilityBatch, MaxBatchDelay: 5 * time.Millisecond, MaxBatchBytes: 64 << 10}
	if got != want {
		t.Errorf("sequencerDefaults(zero) = %+v, want %+v", got, want)
	}
	set := SequencerOptions{Queue: 2, Mode: DurabilityNone, MaxBatchDelay: time.Second, MaxBatchBytes: 10}
	if got := sequencerDefaults(set); got != set {
		t.Errorf("sequencerDefaults changed values that were set: %+v", got)
	}
}

// appendTogether runs n Appends at the same moment and returns their errors.
// Each caller checks that its record was fsynced before Append returned.
func appendTogether(t *testing.T, s *Sequencer, f *fakeLog, n int) []error {
	t.Helper()
	errs := make([]error, n)
	var wg sync.WaitGroup
	for i := range n {
		wg.Go(func() {
			off, err := s.Append(context.Background(), rec(0))
			errs[i] = err
			if err == nil && !f.isSynced(off) {
				t.Errorf("Append returned offset %d before it was fsynced", off)
			}
		})
	}
	within(t, 5*time.Second, "appends", wg.Wait)
	return errs
}

func count(events []string, what string) int {
	n := 0
	for _, e := range events {
		if e == what {
			n++
		}
	}
	return n
}

// Group commit: writers that arrive within one batch window share one fsync.
func TestBatchSharesOneSync(t *testing.T) {
	f := &fakeLog{}
	s := startSequencer(t, f, SequencerOptions{MaxBatchDelay: 50 * time.Millisecond})

	for i, err := range appendTogether(t, s, f, 10) {
		if err != nil {
			t.Errorf("Append %d: %v", i, err)
		}
	}
	c := f.calls()
	if n := count(c, "append"); n != 10 {
		t.Errorf("%d appends, want 10", n)
	}
	if n := count(c, "sync"); n == 0 || n > 3 {
		t.Errorf("%d syncs for 10 writers arriving together, want 1 to 3; calls: %v", n, c)
	}
}

// A lone writer waits for the batch window at most, not forever.
func TestBatchLoneWriterWaitsAtMostDelay(t *testing.T) {
	const delay = 30 * time.Millisecond
	f := &fakeLog{}
	s := startSequencer(t, f, SequencerOptions{MaxBatchDelay: delay})

	start := time.Now()
	appendTogether(t, s, f, 1)
	if took := time.Since(start); took > delay+500*time.Millisecond {
		t.Errorf("a lone Append took %v with a %v batch window", took, delay)
	}
}

// A batch that reaches MaxBatchBytes is written at once, without waiting
// for the rest of the window.
func TestBatchCutAtMaxBytes(t *testing.T) {
	f := &fakeLog{}
	s := startSequencer(t, f, SequencerOptions{MaxBatchDelay: 10 * time.Second, MaxBatchBytes: 1})

	start := time.Now()
	appendTogether(t, s, f, 1)
	if took := time.Since(start); took > time.Second {
		t.Errorf("Append took %v; a record over MaxBatchBytes should not wait for the 10s window", took)
	}
}

// Every caller in a batch hears about a failure that affects its record.
func TestBatchErrorsReachEveryCaller(t *testing.T) {
	diskFull := errors.New("disk full")
	for name, f := range map[string]*fakeLog{
		"append fails": {appendErr: diskFull},
		"sync fails":   {syncErr: diskFull},
	} {
		t.Run(name, func(t *testing.T) {
			s := startSequencer(t, f, SequencerOptions{MaxBatchDelay: 50 * time.Millisecond})
			for i, err := range appendTogether(t, s, f, 5) {
				if !errors.Is(err, diskFull) {
					t.Errorf("Append %d error = %v, want the log's error", i, err)
				}
			}
		})
	}
}

// Mode none is for benchmarks: it never fsyncs.
func TestNoneNeverSyncs(t *testing.T) {
	f := &fakeLog{}
	s := startSequencer(t, f, SequencerOptions{Mode: DurabilityNone})
	for want := range uint64(5) {
		got, err := s.Append(context.Background(), rec(0))
		if err != nil || got != want {
			t.Fatalf("Append = %d, %v; want %d, nil", got, err, want)
		}
	}
	if c := f.calls(); slices.Contains(c, "sync") {
		t.Errorf("mode none called Sync: %v", c)
	}
}

// Close must write a half-collected batch at once, not wait out the window.
func TestBatchCloseFlushes(t *testing.T) {
	f := &fakeLog{}
	s := NewSequencer(f, SequencerOptions{MaxBatchDelay: 10 * time.Second})

	pending := goAppend(s, context.Background())
	time.Sleep(50 * time.Millisecond) // let the sequencer start collecting a batch

	start := time.Now()
	within(t, 2*time.Second, "Close", func() {
		if err := s.Close(); err != nil {
			t.Errorf("Close: %v", err)
		}
	})
	if err := waitErr(t, pending); err != nil {
		t.Errorf("Append pending at Close: %v", err)
	}
	if took := time.Since(start); took > time.Second {
		t.Errorf("Close took %v; it should not wait for the 10s window", took)
	}
}
