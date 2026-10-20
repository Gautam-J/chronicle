// Package slicegrowth is the M0 warm-up on slice length, capacity and reallocation.
package slicegrowth

type Step struct {
	Len   int
	Cap   int
	Moved bool
}

func Grow(n int) []Step {
	var watchList []int
	var steps []Step

	var before *int
	for i := range n {
		if len(watchList) > 0 {
			before = &watchList[0]
		}
		watchList = append(watchList, i)
		after := &watchList[0]
		s := Step{Len: len(watchList), Cap: cap(watchList), Moved: before != after}
		steps = append(steps, s)
	}
	return steps
}
