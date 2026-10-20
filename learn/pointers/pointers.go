// Package pointers is the M0 warm-up on pointers vs values.
package pointers

type Point struct {
	Lat float64
	Lon float64
}

func MoveByValue(p Point, dLat float64) {
	p.Lat += dLat
}

func MoveByPointer(p *Point, dLat float64) {
	p.Lat += dLat
}
