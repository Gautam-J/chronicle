package hashmap

import (
	"hash/fnv"
	"strconv"
	"testing"
)

func TestHashMatchesStdlib(t *testing.T) {
	inputs := []string{"", "a", "foobar", "loc/gautam/phone", "héllo"}

	for _, s := range inputs {
		h := fnv.New64a()
		h.Write([]byte(s))
		want := h.Sum64()

		if got := hash(s); got != want {
			t.Errorf("hash(%q) = %#x, want %#x", s, got, want)
		}
	}
}

func TestMap(t *testing.T) {
	type get struct {
		key   string
		want  int
		found bool
	}

	tests := []struct {
		name    string
		buckets int
		ops     func(m *Map)
		gets    []get
		wantLen int
	}{
		{
			name:    "empty map",
			buckets: 8,
			ops:     func(m *Map) {},
			gets:    []get{{"a", 0, false}},
			wantLen: 0,
		},
		{
			name:    "put then get",
			buckets: 8,
			ops: func(m *Map) {
				m.Put("a", 1)
				m.Put("b", 2)
			},
			gets:    []get{{"a", 1, true}, {"b", 2, true}, {"c", 0, false}},
			wantLen: 2,
		},
		{
			name:    "put overwrites, len unchanged",
			buckets: 8,
			ops: func(m *Map) {
				m.Put("a", 1)
				m.Put("a", 9)
			},
			gets:    []get{{"a", 9, true}},
			wantLen: 1,
		},
		{
			name:    "one bucket forces every key into one chain",
			buckets: 1,
			ops: func(m *Map) {
				m.Put("a", 1)
				m.Put("b", 2)
				m.Put("c", 3)
			},
			gets:    []get{{"a", 1, true}, {"b", 2, true}, {"c", 3, true}},
			wantLen: 3,
		},
		{
			name:    "delete head, middle and tail of a chain",
			buckets: 1,
			ops: func(m *Map) {
				for _, k := range []string{"a", "b", "c", "d", "e"} {
					m.Put(k, 1)
				}
				m.Delete("a")
				m.Delete("c")
				m.Delete("e")
			},
			gets:    []get{{"a", 0, false}, {"b", 1, true}, {"c", 0, false}, {"d", 1, true}, {"e", 0, false}},
			wantLen: 2,
		},
		{
			name:    "put after delete",
			buckets: 4,
			ops: func(m *Map) {
				m.Put("a", 1)
				m.Delete("a")
				m.Put("a", 2)
			},
			gets:    []get{{"a", 2, true}},
			wantLen: 1,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			m := New(tc.buckets)
			tc.ops(m)

			for _, g := range tc.gets {
				got, found := m.Get(g.key)
				if got != g.want || found != g.found {
					t.Errorf("Get(%q) = (%d, %v), want (%d, %v)", g.key, got, found, g.want, g.found)
				}
			}
			if got := m.Len(); got != tc.wantLen {
				t.Errorf("Len() = %d, want %d", got, tc.wantLen)
			}
		})
	}
}

func TestDeleteReturn(t *testing.T) {
	m := New(8)
	m.Put("a", 1)

	if !m.Delete("a") {
		t.Error(`Delete("a") = false on a present key, want true`)
	}
	if m.Delete("a") {
		t.Error(`Delete("a") = true on an already-deleted key, want false`)
	}
}

// Many more keys than buckets: every chain is long, and the map must stay
// consistent with Go's built-in map used as the reference.
func TestManyKeys(t *testing.T) {
	const n = 1000
	m := New(8)
	ref := map[string]int{}

	for i := range n {
		k := "key-" + strconv.Itoa(i)
		m.Put(k, i)
		ref[k] = i
	}
	for i := 0; i < n; i += 2 {
		k := "key-" + strconv.Itoa(i)
		m.Delete(k)
		delete(ref, k)
	}

	if m.Len() != len(ref) {
		t.Fatalf("Len() = %d, want %d", m.Len(), len(ref))
	}
	for i := range n {
		k := "key-" + strconv.Itoa(i)
		want, wantOK := ref[k]
		if got, ok := m.Get(k); got != want || ok != wantOK {
			t.Errorf("Get(%q) = (%d, %v), want (%d, %v)", k, got, ok, want, wantOK)
		}
	}
}
