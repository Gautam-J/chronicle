// Package linkedlist is the M0 CTCI warm-up: a singly linked list of ints.
package linkedlist

type node struct {
	val  int
	next *node
}

type List struct {
	head *node
	size int
}

func (l *List) PushFront(v int) {
	n := &node{val: v, next: l.head}
	l.head = n
	l.size++
}

func (l *List) PushBack(v int) {
	tail := &node{val: v}
	if l.head == nil {
		l.head = tail
	} else {
		n := l.head
		for n.next != nil {
			n = n.next
		}
		n.next = tail
	}
	l.size++
}

func (l *List) Remove(v int) bool {
	var prev *node
	n := l.head

	for n != nil {
		if n.val == v {
			if prev == nil {
				l.head = n.next
			} else {
				prev.next = n.next
			}
			l.size--
			return true
		}
		prev, n = n, n.next
	}
	return false
}

func (l *List) Len() int {
	return l.size
}

func (l *List) Values() []int {
	var v []int
	for n := l.head; n != nil; n = n.next {
		v = append(v, n.val)
	}
	return v
}
