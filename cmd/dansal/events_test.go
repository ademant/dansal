package main

import (
	"math"
	"testing"
)

func TestHaversineKm(t *testing.T) {
	cases := []struct {
		name         string
		lat1, lon1   float64
		lat2, lon2   float64
		wantApproxKm float64
		toleranceKm  float64
	}{
		{
			name:         "Berlin to Munich",
			lat1: 52.5200, lon1: 13.4050,
			lat2: 48.1351, lon2: 11.5820,
			wantApproxKm: 504.0,
			toleranceKm:  2.0,
		},
		{
			name:         "same point",
			lat1: 48.0, lon1: 11.0,
			lat2: 48.0, lon2: 11.0,
			wantApproxKm: 0.0,
			toleranceKm:  0.001,
		},
		{
			name:         "equator 1 degree lon",
			lat1: 0.0, lon1: 0.0,
			lat2: 0.0, lon2: 1.0,
			wantApproxKm: 111.195,
			toleranceKm:  0.1,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := haversineKm(tc.lat1, tc.lon1, tc.lat2, tc.lon2)
			if math.Abs(got-tc.wantApproxKm) > tc.toleranceKm {
				t.Errorf("haversineKm(%.4f,%.4f,%.4f,%.4f) = %.3f km, want %.3f ±%.3f",
					tc.lat1, tc.lon1, tc.lat2, tc.lon2, got, tc.wantApproxKm, tc.toleranceKm)
			}
		})
	}
}
