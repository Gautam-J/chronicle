package pointers

import "testing"

func TestMove(t *testing.T) {
	start := Point{Lat: 12.97, Lon: 77.59}

	tests := []struct {
		name    string
		move    func(p *Point, dLat float64)
		dLat    float64
		wantLat float64
	}{
		{
			name:    "by value leaves caller unchanged",
			move:    func(p *Point, d float64) { MoveByValue(*p, d) },
			dLat:    1.5,
			wantLat: start.Lat,
		},
		{
			name:    "by pointer changes caller",
			move:    MoveByPointer,
			dLat:    1.5,
			wantLat: start.Lat + 1.5,
		},
		{
			name:    "by pointer with negative delta",
			move:    MoveByPointer,
			dLat:    -2,
			wantLat: start.Lat - 2,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			p := start // fresh copy per case, so cases can't affect each other

			tc.move(&p, tc.dLat)

			if p.Lat != tc.wantLat {
				t.Errorf("Lat = %v, want %v", p.Lat, tc.wantLat)
			}
			if p.Lon != start.Lon {
				t.Errorf("Lon = %v, want %v (must not change)", p.Lon, start.Lon)
			}
		})
	}
}
