package slicegrowth

import "testing"

func TestGrow(t *testing.T) {
	const n = 20
	steps := Grow(n)

	if len(steps) != n {
		t.Fatalf("len(steps) = %d, want %d", len(steps), n)
	}

	prevCap := 0
	for i, s := range steps {
		t.Logf("append #%2d: len=%2d cap=%2d moved=%v", i+1, s.Len, s.Cap, s.Moved)

		if s.Len != i+1 {
			t.Errorf("step %d: Len = %d, want %d", i, s.Len, i+1)
		}
		if s.Cap < s.Len {
			t.Errorf("step %d: Cap = %d < Len = %d", i, s.Cap, s.Len)
		}
		if s.Cap < prevCap {
			t.Errorf("step %d: Cap shrank from %d to %d", i, prevCap, s.Cap)
		}
		// append moves the data exactly when it has to allocate a bigger array.
		if grew := s.Cap != prevCap; s.Moved != grew {
			t.Errorf("step %d: Moved = %v, but cap changed = %v (%d -> %d)",
				i, s.Moved, grew, prevCap, s.Cap)
		}
		prevCap = s.Cap
	}
}

func TestGrowZero(t *testing.T) {
	if got := Grow(0); len(got) != 0 {
		t.Errorf("Grow(0) returned %d steps, want 0", len(got))
	}
}
