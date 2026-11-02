package lsm

import (
	"iter"
	"math/rand/v2"
)

const maxLevel = 12 // 4**12 = 16M nodes

type node[K, V any] struct {
	key  K
	val  V
	next []*node[K, V]
}

func newNode[K, V any](k K, v V, level int) *node[K, V] {
	return &node[K, V]{
		key:  k,
		val:  v,
		next: make([]*node[K, V], level),
	}
}

type SkipList[K, V any] struct {
	head  *node[K, V]
	level int
	n     int
	cmp   func(a, b K) int
}

func New[K, V any](cmp func(a, b K) int) *SkipList[K, V] {
	return &SkipList[K, V]{
		head:  newNode(*new(K), *new(V), maxLevel),
		level: 1,
		n:     0,
		cmp:   cmp,
	}
}

func (s *SkipList[K, V]) seek(k K, prev []*node[K, V]) *node[K, V] {
	x := s.head

	// level wise iteration
	for i := s.level - 1; i >= 0; i-- {
		// traverse nodes on a level
		for x.next[i] != nil && s.cmp(x.next[i].key, k) < 0 {
			x = x.next[i]
		}
		if len(prev) != 0 {
			prev[i] = x
		}
	}
	return x.next[0]
}

func (s *SkipList[K, V]) Put(k K, v V) {
	prev := make([]*node[K, V], maxLevel)
	x := s.seek(k, prev)

	if x != nil && s.cmp(x.key, k) == 0 {
		x.val = v
		return
	}

	lvl := 1
	for lvl < maxLevel && rand.IntN(4) == 0 { // p = 1/4
		lvl++
	}

	for i := s.level; i < lvl; i++ {
		prev[i] = s.head
	}

	s.level = max(s.level, lvl)
	node := newNode(k, v, lvl)

	for i := range lvl {
		node.next[i] = prev[i].next[i]
		prev[i].next[i] = node
	}
	s.n++
}

func (s *SkipList[K, V]) Get(k K) (V, bool) {
	x := s.seek(k, nil)
	if x != nil && s.cmp(x.key, k) == 0 {
		return x.val, true
	}
	var zero V
	return zero, false
}

func (s *SkipList[K, V]) Len() int {
	return s.n
}

// walk yields x and every node after it on the bottom lane, in key order.
func walk[K, V any](x *node[K, V], yield func(K, V) bool) {
	for x != nil {
		if !yield(x.key, x.val) {
			return
		}
		x = x.next[0]
	}
}

// key order
func (s *SkipList[K, V]) All() iter.Seq2[K, V] {
	return func(yield func(K, V) bool) { walk(s.head.next[0], yield) }
}

// first key >= k onward
func (s *SkipList[K, V]) From(k K) iter.Seq2[K, V] {
	return func(yield func(K, V) bool) { walk(s.seek(k, nil), yield) }
}
