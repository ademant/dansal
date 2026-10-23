package main

import "testing"

// #1612: a bulk change conflicts when the event would have both "no live music"
// and musicians. The rule is checked before any write, per event.
func TestBulkNoLiveMusicConflict(t *testing.T) {
	setupDedupTestDB(t)

	plain, _, _, err := insertEvent(db, EventInput{Title: "plain", StartTime: 2000000000, EndTime: 2000003600, IsPublished: true})
	if err != nil {
		t.Fatal(err)
	}
	flagged, _, _, err := insertEvent(db, EventInput{Title: "flagged", StartTime: 2000086400, EndTime: 2000090000, IsPublished: true, NoLiveMusic: true})
	if err != nil {
		t.Fatal(err)
	}
	withMusician, _, _, err := insertEvent(db, EventInput{Title: "musician", StartTime: 2000172800, EndTime: 2000176400, IsPublished: true})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("INSERT INTO musicians (id, bandname) VALUES (1, 'Band 1')"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("INSERT INTO event_musicians (event_id, musician_id) VALUES (?, 1)", withMusician); err != nil {
		t.Fatal(err)
	}

	yes, no := true, false
	for _, tc := range []struct {
		name          string
		id            int
		flag          *bool
		addsMusicians bool
		want          bool
	}{
		{"flagging a plain event is fine", plain, &yes, false, false},
		{"flagging an event with musicians conflicts", withMusician, &yes, false, true},
		{"adding a musician to a flagged event conflicts", flagged, nil, true, true},
		{"adding a musician to a plain event is fine", plain, nil, true, false},
		{"unflagging a flagged event then adding a musician is fine", flagged, &no, true, false},
		{"leaving a flagged event alone is no conflict", flagged, nil, false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := bulkNoLiveMusicConflict(tc.id, tc.flag, tc.addsMusicians); got != tc.want {
				t.Errorf("conflict = %v, want %v", got, tc.want)
			}
		})
	}
}
