package main

import (
	"math"
	"testing"
	"time"
)

func TestEventDeletionDeadline(t *testing.T) {
	created := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	cases := []struct {
		name         string
		startsIn     time.Duration
		wantDeadline time.Time
	}{
		{"created 5 months before start: normal 1-month window",
			5 * 30 * 24 * time.Hour, created.AddDate(0, 1, 0)},
		{"created 5 weeks before start: collision, 1-week floor",
			5 * 7 * 24 * time.Hour, created.AddDate(0, 0, 7)},
		{"created 3 weeks before start: collision, 1-week floor",
			3 * 7 * 24 * time.Hour, created.AddDate(0, 0, 7)},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			start := created.Add(c.startsIn)
			got := eventDeletionDeadline(created, start)
			if !got.Equal(c.wantDeadline) {
				t.Errorf("eventDeletionDeadline(%v, %v) = %v, want %v", created, start, got, c.wantDeadline)
			}
		})
	}

	t.Run("deadline never exceeds start time", func(t *testing.T) {
		start := created.Add(2 * 24 * time.Hour)
		got := eventDeletionDeadline(created, start)
		if got.After(start) {
			t.Errorf("deadline %v is after start time %v", got, start)
		}
	})
}

func TestHaversineKm(t *testing.T) {
	cases := []struct {
		name         string
		lat1, lon1   float64
		lat2, lon2   float64
		wantApproxKm float64
		toleranceKm  float64
	}{
		{
			name: "Berlin to Munich",
			lat1: 52.5200, lon1: 13.4050,
			lat2: 48.1351, lon2: 11.5820,
			wantApproxKm: 504.0,
			toleranceKm:  2.0,
		},
		{
			name: "same point",
			lat1: 48.0, lon1: 11.0,
			lat2: 48.0, lon2: 11.0,
			wantApproxKm: 0.0,
			toleranceKm:  0.001,
		},
		{
			name: "equator 1 degree lon",
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
