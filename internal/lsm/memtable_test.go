package lsm

import (
	"bytes"
	"fmt"
	"slices"
	"testing"
)

func openMem(t *testing.T, dir string) *Memtable {
	t.Helper()
	m, err := OpenMemtable(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { m.Close() })
	return m
}

func put(t *testing.T, m *Memtable, k, v string) {
	t.Helper()
	if err := m.Put([]byte(k), []byte(v)); err != nil {
		t.Fatalf("Put(%q): %v", k, err)
	}
}

// reopen closes m and opens dir again, so the contents come only from the WAL.
func reopen(t *testing.T, m *Memtable, dir string) *Memtable {
	t.Helper()
	if err := m.Close(); err != nil {
		t.Fatal(err)
	}
	return openMem(t, dir)
}

func dump(m *Memtable) []string {
	var out []string
	for k, v := range m.All() {
		out = append(out, string(k)+"="+string(v))
	}
	return out
}

func TestMemtablePutGet(t *testing.T) {
	m := openMem(t, t.TempDir())
	put(t, m, "b", "2")
	put(t, m, "a", "1")
	if v, ok := m.Get([]byte("a")); !ok || string(v) != "1" {
		t.Errorf("Get(a) = %q, %v; want 1, true", v, ok)
	}
	if _, ok := m.Get([]byte("c")); ok {
		t.Error("Get(c) found a key that was never put")
	}
	if got := dump(m); !slices.Equal(got, []string{"a=1", "b=2"}) {
		t.Errorf("All = %v, want [a=1 b=2]", got)
	}
}

// The point of the WAL: everything put before Close is back after Open, from
// disk alone.
func TestMemtableSurvivesReopen(t *testing.T) {
	dir := t.TempDir()
	m := openMem(t, dir)
	var want []string
	for i := range 300 {
		k, v := fmt.Sprintf("k%03d", 299-i), fmt.Sprintf("v%d", i) // reverse order on purpose
		put(t, m, k, v)
		want = append(want, k+"="+v)
	}
	slices.Sort(want)

	m = reopen(t, m, dir)
	if m.Len() != 300 {
		t.Errorf("Len after reopen = %d, want 300", m.Len())
	}
	if got := dump(m); !slices.Equal(got, want) {
		t.Errorf("after reopen, All has %d entries; first few %v, want %v", len(got), got[:min(3, len(got))], want[:3])
	}

	put(t, m, "zzz", "after") // still writable, and the new entry is kept too
	m = reopen(t, m, dir)
	if v, ok := m.Get([]byte("zzz")); !ok || string(v) != "after" {
		t.Errorf("Get(zzz) after a second reopen = %q, %v; want after, true", v, ok)
	}
}

// Replay applies the WAL in the order it was written, so the last Put of a
// key wins, as it did before the restart.
func TestMemtableReplayKeepsLastWrite(t *testing.T) {
	dir := t.TempDir()
	m := openMem(t, dir)
	put(t, m, "k", "first")
	put(t, m, "k", "second")
	put(t, m, "k", "third")
	m = reopen(t, m, dir)
	if v, _ := m.Get([]byte("k")); string(v) != "third" {
		t.Errorf("Get(k) after reopen = %q, want third", v)
	}
	if m.Len() != 1 {
		t.Errorf("Len = %d, want 1", m.Len())
	}
}

// The S|stream|time|offset index keys map to an empty value. Empty must
// still count as present, before and after a restart.
func TestMemtableEmptyValue(t *testing.T) {
	dir := t.TempDir()
	m := openMem(t, dir)
	if err := m.Put([]byte("S-key"), nil); err != nil {
		t.Fatal(err)
	}
	for _, when := range []string{"before", "after"} {
		if v, ok := m.Get([]byte("S-key")); !ok || len(v) != 0 {
			t.Errorf("%s reopen: Get = %q, %v; want empty, true", when, v, ok)
		}
		m = reopen(t, m, dir)
	}
}

// Callers reuse buffers. The memtable must keep its own copy of each key and
// value, or a later write into the caller's buffer silently changes the index.
func TestMemtableCopiesInput(t *testing.T) {
	m := openMem(t, t.TempDir())
	k, v := []byte("key1"), []byte("val1")
	if err := m.Put(k, v); err != nil {
		t.Fatal(err)
	}
	copy(k, "XXXX")
	copy(v, "YYYY")
	if got, ok := m.Get([]byte("key1")); !ok || !bytes.Equal(got, []byte("val1")) {
		t.Errorf("after the caller reused its buffers, Get(key1) = %q, %v; want val1, true", got, ok)
	}
	if got := dump(m); !slices.Equal(got, []string{"key1=val1"}) {
		t.Errorf("All = %v, want [key1=val1]", got)
	}
}

// Write-ahead means the WAL first. If the WAL write fails, the entry must not
// show up in memory either, or a reader sees data a restart will lose.
func TestMemtablePutFailsWhenWALFails(t *testing.T) {
	m := openMem(t, t.TempDir())
	put(t, m, "a", "1")
	if err := m.Close(); err != nil {
		t.Fatal(err)
	}
	if err := m.Put([]byte("b"), []byte("2")); err == nil {
		t.Fatal("Put after Close returned no error")
	}
	if _, ok := m.Get([]byte("b")); ok {
		t.Error("a Put whose WAL write failed is visible in memory")
	}
}

func TestMemtableSync(t *testing.T) {
	dir := t.TempDir()
	m := openMem(t, dir)
	put(t, m, "a", "1")
	if err := m.Sync(); err != nil {
		t.Fatalf("Sync: %v", err)
	}
	m = reopen(t, m, dir)
	if _, ok := m.Get([]byte("a")); !ok {
		t.Error("synced entry missing after reopen")
	}
}
