// Command killloop tests crash recovery. Each run starts a writer process,
// kills it with SIGKILL at a random moment, reopens the log, and checks that
// every record the writer was told is saved is still there, at the offset it
// was given, with no gaps.
//
//	go run ./test/killloop -runs 1000
package main

import (
	"bufio"
	"context"
	"errors"
	"flag"
	"fmt"
	"math/rand/v2"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"time"

	chlog "github.com/Gautam-J/chronicle/internal/log"
	"github.com/Gautam-J/chronicle/internal/record"
)

// Small segments, so kills also land around rollovers.
var opts = chlog.Options{MaxSegmentBytes: 1 << 20}

func main() {
	writer := flag.Bool("writer", false, "run as the writer child (used by the loop itself)")
	dir := flag.String("dir", "", "log directory; default: a new temp dir, removed on success")
	runs := flag.Int("runs", 1000, "number of kills")
	maxLife := flag.Duration("max-life", 300*time.Millisecond, "kill each writer at a random time up to this")
	flag.Parse()

	if *writer {
		runWriter(*dir)
		return
	}

	keep := *dir != ""
	if !keep {
		d, err := os.MkdirTemp("", "killloop-")
		if err != nil {
			fail("%v", err)
		}
		*dir = d
	}

	acked := map[uint64]string{} // offset -> payload, across all runs
	var next uint64
	start := time.Now()
	for run := 1; run <= *runs; run++ {
		acks := killOne(*dir, run, rand.N(*maxLife))
		next = check(*dir, run, acks, acked, next)
		if run%50 == 0 || run == *runs {
			fmt.Printf("run %d/%d: %d records, %d acknowledged, %v\n",
				run, *runs, next, len(acked), time.Since(start).Round(time.Second))
		}
	}

	// A later run's recovery must not have damaged an earlier run's records.
	l := open(*dir, 0)
	for off, payload := range acked {
		verify(l, 0, off, payload)
	}
	l.Close()

	fmt.Printf("ok: %d kills, %d acknowledged records all present\n", *runs, len(acked))
	if !keep {
		os.RemoveAll(*dir)
	}
}

type ack struct {
	off     uint64
	payload string
}

// killOne starts a writer, kills it after life, and returns every ack it
// printed before it died.
func killOne(dir string, run int, life time.Duration) []ack {
	cmd := exec.Command(os.Args[0], "-writer", "-dir", dir)
	cmd.Stderr = os.Stderr
	out, err := cmd.StdoutPipe()
	if err != nil {
		fail("run %d: %v", run, err)
	}
	if err := cmd.Start(); err != nil {
		fail("run %d: start writer: %v", run, err)
	}

	done := make(chan []ack)
	go func() {
		var acks []ack
		sc := bufio.NewScanner(out)
		for sc.Scan() {
			off, payload, ok := strings.Cut(sc.Text(), " ")
			n, err := strconv.ParseUint(off, 10, 64)
			if !ok || err != nil {
				fail("run %d: bad line from writer: %q", run, sc.Text())
			}
			acks = append(acks, ack{n, payload})
		}
		done <- acks
	}()

	time.Sleep(life)
	cmd.Process.Kill() // SIGKILL: no defers, no Close, no flush
	acks := <-done     // the pipe hits EOF once the writer is dead
	cmd.Wait()
	if cmd.ProcessState.Exited() {
		fail("run %d: the writer exited by itself (code %d) instead of being killed; see its stderr above",
			run, cmd.ProcessState.ExitCode())
	}
	return acks
}

// check reopens the log and returns its next offset.
func check(dir string, run int, acks []ack, acked map[uint64]string, prevNext uint64) uint64 {
	l := open(dir, run)
	defer l.Close()

	for _, a := range acks {
		if old, dup := acked[a.off]; dup {
			fail("run %d: offset %d acknowledged twice: %s, then %s", run, a.off, old, a.payload)
		}
		acked[a.off] = a.payload
		verify(l, run, a.off, a.payload)
	}

	// Every offset from the last run's end up to this run's end must read
	// back; the first ErrNotFound is the new end.
	next := prevNext
	for ; ; next++ {
		_, err := l.Read(next)
		if errors.Is(err, chlog.ErrNotFound) {
			break
		}
		if err != nil {
			fail("run %d: Read(%d): %v", run, next, err)
		}
	}
	for _, a := range acks {
		if a.off >= next {
			fail("run %d: acknowledged offset %d is past the end of the log (%d)", run, a.off, next)
		}
	}
	return next
}

func open(dir string, run int) *chlog.Log {
	l, err := chlog.Open(dir, opts)
	if err != nil {
		fail("run %d: Open after kill: %v", run, err)
	}
	return l
}

func verify(l *chlog.Log, run int, off uint64, payload string) {
	r, err := l.Read(off)
	if err != nil {
		fail("run %d: acknowledged offset %d: %v", run, off, err)
	}
	if string(r.Payload) != payload {
		fail("run %d: offset %d holds %s, want %s", run, off, r.Payload, payload)
	}
}

// runWriter appends from several goroutines, so batches form, and prints
// "offset payload" for each record only after Append returns.
func runWriter(dir string) {
	l, err := chlog.Open(dir, opts)
	if err != nil {
		fmt.Fprintf(os.Stderr, "writer: Open: %v\n", err)
		os.Exit(2)
	}
	s := chlog.NewSequencer(l, chlog.SequencerOptions{})

	var mu sync.Mutex
	pid := os.Getpid()
	for w := range 4 {
		go func() {
			for i := 0; ; i++ {
				payload := fmt.Sprintf(`{"pid":%d,"w":%d,"i":%d}`, pid, w, i)
				off, err := s.Append(context.Background(), record.Record{Type: "killloop", Payload: []byte(payload)})
				if err != nil {
					fmt.Fprintf(os.Stderr, "writer: Append: %v\n", err)
					os.Exit(2)
				}
				mu.Lock()
				fmt.Fprintf(os.Stdout, "%d %s\n", off, payload) // os.Stdout is unbuffered: one write per ack
				mu.Unlock()
			}
		}()
	}
	select {} // run until killed
}

func fail(format string, args ...any) {
	fmt.Fprintf(os.Stderr, "FAIL: "+format+"\n", args...)
	if d := flag.Lookup("dir"); d != nil && d.Value.String() != "" {
		fmt.Fprintf(os.Stderr, "log kept in %s\n", d.Value)
	}
	os.Exit(1)
}
