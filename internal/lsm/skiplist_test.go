package lsm

import (
	"bytes"
	"cmp"
	"encoding/binary"
	"math/rand/v2"
	"slices"
	"testing"
)

func ints() *SkipList[int, string] { return New[int, string](cmp.Compare[int]) }

func collect[K, V any](seq func(func(K, V) bool)) ([]K, []V) {
	var ks []K
	var vs []V
	for k, v := range seq {
		ks = append(ks, k)
		vs = append(vs, v)
	}
	return ks, vs
}

func TestEmpty(t *testing.T) {
	s := ints()
	if v, ok := s.Get(7); ok || v != "" {
		t.Errorf("Get(7) on an empty list = %q, %v; want \"\", false", v, ok)
	}
	if s.Len() != 0 {
		t.Errorf("Len = %d, want 0", s.Len())
	}
	if ks, _ := collect(s.All()); len(ks) != 0 {
		t.Errorf("All yielded %v, want nothing", ks)
	}
}

// A Go map is the model: every key it has, the list must have, with the
// same value; every key it lacks, the list must lack.
func TestPutGetMatchesMap(t *testing.T) {
	s := ints()
	model := map[int]string{}
	for i := range 2000 {
		k := rand.IntN(1000) // collisions on purpose: about half the Puts overwrite
		v := string(rune('a' + i%26))
		s.Put(k, v)
		model[k] = v
	}
	if s.Len() != len(model) {
		t.Errorf("Len = %d, want %d distinct keys", s.Len(), len(model))
	}
	for k := -5; k < 1005; k++ {
		got, ok := s.Get(k)
		want, wantOK := model[k]
		if got != want || ok != wantOK {
			t.Fatalf("Get(%d) = %q, %v; want %q, %v", k, got, ok, want, wantOK)
		}
	}
}

// The newest Put wins and the key is stored once. In the index this is what
// lets a later write of the same key replace the earlier one.
func TestPutOverwrites(t *testing.T) {
	s := ints()
	s.Put(5, "old")
	s.Put(5, "new")
	if v, _ := s.Get(5); v != "new" {
		t.Errorf("Get(5) = %q, want new", v)
	}
	if s.Len() != 1 {
		t.Errorf("Len = %d after two Puts of one key, want 1", s.Len())
	}
	if ks, _ := collect(s.All()); len(ks) != 1 {
		t.Errorf("All yielded %v, want the key once", ks)
	}
}

// All walks in key order whatever the insert order. Flushing the memtable to
// an SSTable depends on this.
func TestAllIsSorted(t *testing.T) {
	s := ints()
	keys := rand.Perm(500)
	for _, k := range keys {
		s.Put(k, "v")
	}
	slices.Sort(keys)
	for pass := range 2 { // an iterator must be reusable
		ks, _ := collect(s.All())
		if !slices.Equal(ks, keys) {
			t.Fatalf("pass %d: All yielded %d keys, not 0..499 in order; first few: %v", pass, len(ks), ks[:min(10, len(ks))])
		}
	}
}

// From(k) starts at the first key >= k: the start of a range read.
func TestFrom(t *testing.T) {
	s := ints()
	for k := 0; k < 100; k += 10 {
		s.Put(k, "v")
	}
	for _, c := range []struct {
		from int
		want []int
	}{
		{35, []int{40, 50, 60, 70, 80, 90}},
		{40, []int{40, 50, 60, 70, 80, 90}}, // an exact match is included
		{-5, []int{0, 10, 20, 30, 40, 50, 60, 70, 80, 90}},
		{90, []int{90}},
		{91, nil},
	} {
		if ks, _ := collect(s.From(c.from)); !slices.Equal(ks, c.want) {
			t.Errorf("From(%d) yielded %v, want %v", c.from, ks, c.want)
		}
	}
}

// A range loop that breaks must stop the walk. Ignoring yield's false return
// makes Go panic: "range function continued iteration".
func TestBreakStopsIteration(t *testing.T) {
	s := ints()
	for k := range 10 {
		s.Put(k, "v")
	}
	var seen []int
	for k := range s.From(3) {
		seen = append(seen, k)
		if len(seen) == 2 {
			break
		}
	}
	if !slices.Equal(seen, []int{3, 4}) {
		t.Errorf("saw %v, want [3 4]", seen)
	}
}

// The real index keys are byte strings compared with bytes.Compare, encoded
// big-endian so byte order equals number order.
func TestByteKeys(t *testing.T) {
	s := New[[]byte, uint64](bytes.Compare)
	key := func(n uint64) []byte { return binary.BigEndian.AppendUint64([]byte("S"), n) }
	for _, n := range []uint64{256, 1, 65536, 255} {
		s.Put(key(n), n)
	}
	_, vs := collect(s.All())
	if !slices.Equal(vs, []uint64{1, 255, 256, 65536}) {
		t.Errorf("byte keys walked as %v, want numeric order [1 255 256 65536]", vs)
	}
	if v, ok := s.Get(key(255)); !ok || v != 255 {
		t.Errorf("Get(key(255)) = %d, %v; want 255, true (a fresh slice with equal bytes)", v, ok)
	}
}

// A skip list's point: a lookup costs about log n comparisons, not n. Keys go
// in ascending order, which turns a plain sorted linked list into its worst
// case. Counting comparisons proves the express lanes are used without
// looking inside the struct.
func TestSearchIsLogarithmic(t *testing.T) {
	const n = 1 << 14
	var calls int
	s := New[int, int](func(a, b int) int { calls++; return cmp.Compare(a, b) })
	for k := range n {
		s.Put(k, k)
	}
	putAvg := float64(calls) / n

	calls = 0
	for k := range n {
		if v, ok := s.Get(k); !ok || v != k {
			t.Fatalf("Get(%d) = %d, %v", k, v, ok)
		}
	}
	getAvg := float64(calls) / n

	// log2(16384) = 14; a skip list averages a small multiple of that.
	// A list with every node on level 1 averages n/2 = 8192.
	const limit = 80
	t.Logf("comparisons per Put %.1f, per Get %.1f", putAvg, getAvg)
	if putAvg > limit || getAvg > limit {
		t.Errorf("comparisons per Put %.1f, per Get %.1f; want both under %d for n = %d", putAvg, getAvg, limit, n)
	}
}
