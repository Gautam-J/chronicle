package log

import (
	"bytes"
	"context"
	"fmt"
	"slices"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Gautam-J/chronicle/internal/record"
)

// Appends per second and latency through the sequencer, per durability mode
// and number of concurrent writers (M1 task 7). Run on the Pi, on the disk
// the data will live on:
//
//	go test ./internal/log -run '^$' -bench SequencerModes -benchtime 3s
//
// shortcut: a fixed 300-byte payload (the median OwnTracks body in M0's
// data), not a replay of recorded data; switch once `chronicle replay` exists.
func BenchmarkSequencerModes(b *testing.B) {
	payload := bytes.Repeat([]byte("x"), 300)
	modes := []struct {
		name string
		mode Durability
	}{{"always", DurabilityAlways}, {"batch", DurabilityBatch}, {"none", DurabilityNone}}

	for _, m := range modes {
		for _, writers := range []int{1, 8, 64} {
			b.Run(fmt.Sprintf("mode=%s/writers=%d", m.name, writers), func(b *testing.B) {
				l, err := Open(b.TempDir(), Options{})
				if err != nil {
					b.Fatal(err)
				}
				defer l.Close()
				s := NewSequencer(l, SequencerOptions{Mode: m.mode})
				defer s.Close()

				lat := make([]time.Duration, b.N) // slot i is written by whichever writer took i
				var taken atomic.Int64
				var wg sync.WaitGroup

				b.ResetTimer()
				start := time.Now()
				for range writers {
					wg.Go(func() {
						for {
							i := taken.Add(1) - 1
							if i >= int64(b.N) {
								return
							}
							t0 := time.Now()
							if _, err := s.Append(context.Background(), record.Record{Type: "location", Payload: payload}); err != nil {
								b.Error(err)
								return
							}
							lat[i] = time.Since(t0)
						}
					})
				}
				wg.Wait()
				elapsed := time.Since(start)
				b.StopTimer()

				slices.Sort(lat)
				b.ReportMetric(float64(b.N)/elapsed.Seconds(), "appends/s")
				b.ReportMetric(float64(lat[len(lat)/2].Microseconds()), "p50-µs")
				b.ReportMetric(float64(lat[len(lat)*99/100].Microseconds()), "p99-µs")
				b.ReportMetric(0, "ns/op") // per-op time means little with many writers; appends/s says it
			})
		}
	}
}

// What fsyncing the .idx as well as the .log costs. The .idx is derived:
// recover rebuilds it from the .log on every Open.
func BenchmarkSegmentSync(b *testing.B) {
	for _, both := range []bool{false, true} {
		name := "log-only"
		if both {
			name = "log+idx"
		}
		b.Run(name, func(b *testing.B) {
			s, err := newSegment(b.TempDir(), 0, 4096)
			if err != nil {
				b.Fatal(err)
			}
			defer s.close()
			var off uint64
			for b.Loop() {
				if err := s.append(rec(off)); err != nil {
					b.Fatal(err)
				}
				off++
				if both {
					err = s.sync()
				} else {
					err = s.log.Sync()
				}
				if err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}
