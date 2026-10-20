package linkedlist

import (
	"slices"
	"testing"
)

func TestList(t *testing.T) {
	tests := []struct {
		name string
		ops  func(l *List)
		want []int
	}{
		{
			name: "zero value is empty",
			ops:  func(l *List) {},
			want: nil,
		},
		{
			name: "push front reverses order",
			ops: func(l *List) {
				l.PushFront(1)
				l.PushFront(2)
				l.PushFront(3)
			},
			want: []int{3, 2, 1},
		},
		{
			name: "push back keeps order",
			ops: func(l *List) {
				l.PushBack(1)
				l.PushBack(2)
				l.PushBack(3)
			},
			want: []int{1, 2, 3},
		},
		{
			name: "mixed pushes",
			ops: func(l *List) {
				l.PushBack(2)
				l.PushFront(1)
				l.PushBack(3)
			},
			want: []int{1, 2, 3},
		},
		{
			name: "remove head",
			ops: func(l *List) {
				l.PushBack(1)
				l.PushBack(2)
				l.Remove(1)
			},
			want: []int{2},
		},
		{
			name: "remove middle",
			ops: func(l *List) {
				l.PushBack(1)
				l.PushBack(2)
				l.PushBack(3)
				l.Remove(2)
			},
			want: []int{1, 3},
		},
		{
			name: "remove tail, then push back",
			ops: func(l *List) {
				l.PushBack(1)
				l.PushBack(2)
				l.Remove(2)
				l.PushBack(4)
			},
			want: []int{1, 4},
		},
		{
			name: "remove only the first duplicate",
			ops: func(l *List) {
				l.PushBack(7)
				l.PushBack(8)
				l.PushBack(7)
				l.Remove(7)
			},
			want: []int{8, 7},
		},
		{
			name: "remove last item empties list",
			ops: func(l *List) {
				l.PushBack(1)
				l.Remove(1)
			},
			want: nil,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var l List // zero value, no constructor
			tc.ops(&l)

			if got := l.Values(); !slices.Equal(got, tc.want) {
				t.Errorf("Values() = %v, want %v", got, tc.want)
			}
			if got := l.Len(); got != len(tc.want) {
				t.Errorf("Len() = %d, want %d", got, len(tc.want))
			}
		})
	}
}

func TestRemoveReturn(t *testing.T) {
	tests := []struct {
		name  string
		items []int
		rm    int
		want  bool
	}{
		{name: "empty list", items: nil, rm: 1, want: false},
		{name: "missing value", items: []int{1, 2}, rm: 3, want: false},
		{name: "present value", items: []int{1, 2}, rm: 2, want: true},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var l List
			for _, v := range tc.items {
				l.PushBack(v)
			}
			if got := l.Remove(tc.rm); got != tc.want {
				t.Errorf("Remove(%d) = %v, want %v", tc.rm, got, tc.want)
			}
		})
	}
}
