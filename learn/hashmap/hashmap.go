// Package hashmap is the M0 CTCI warm-up: a string -> int hash map with chaining.
package hashmap

const (
	offset64 uint64 = 14695981039346656037
	prime64  uint64 = 1099511628211
)

// FNV-1a, 64-bit hashing algorithm
func hash(s string) uint64 {
	h := offset64
	for i := 0; i < len(s); i++ {
		b := s[i]
		h ^= uint64(b)
		h *= prime64
	}
	return h
}

type entry struct {
	key  string
	val  int
	next *entry
}

type Map struct {
	buckets []*entry
	size    int
}

func (m *Map) index(key string) uint64 {
	return hash(key) % uint64(len(m.buckets))
}

func New(nBuckets int) *Map {
	return &Map{buckets: make([]*entry, nBuckets)}
}

func (m *Map) Put(key string, val int) {
	i := m.index(key)
	e := m.buckets[i]
	for e != nil {
		if e.key == key {
			e.val = val
			return
		}
		e = e.next
	}
	m.buckets[i] = &entry{key: key, val: val, next: m.buckets[i]}
	m.size++
}

func (m *Map) Get(key string) (int, bool) {
	i := m.index(key)
	e := m.buckets[i]
	for e != nil {
		if e.key == key {
			return e.val, true
		}
		e = e.next
	}
	return 0, false
}

func (m *Map) Delete(key string) bool {
	i := m.index(key)
	e := m.buckets[i]

	var prev *entry
	for e != nil {
		if e.key == key {
			if prev == nil {
				m.buckets[i] = e.next
			} else {
				prev.next = e.next
			}
			m.size--
			return true
		}
		prev, e = e, e.next
	}
	return false
}

func (m *Map) Len() int {
	return m.size
}
