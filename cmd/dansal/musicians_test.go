package main

import (
	"encoding/json"
	"net/http/httptest"
	"testing"
	"time"
)

// TestGetMusiciansWithEventCountsNextEventAt asserts the with_event_counts
// subquery (#1040) surfaces the earliest future published event's start
// time as next_event_at, using the same "future" definition (start_time >
// now, published) as future_event_count, and leaves it empty when there is
// no future event.
func TestGetMusiciansWithEventCountsNextEventAt(t *testing.T) {
	setupDedupTestDB(t)
	if musicianAvatars == nil {
		musicianAvatars = newAvatarSet(t.TempDir(), "/api/v1/musician-avatars/")
	}

	var musID int64
	res, err := db.Exec("INSERT INTO musicians (bandname) VALUES ('Test Band')")
	if err != nil {
		t.Fatalf("insert musician: %v", err)
	}
	musID, _ = res.LastInsertId()

	now := time.Now().In(berlinLoc)
	pastID, _, _, err := insertEvent(db, EventInput{
		Title: "Past Gig", StartTime: now.Add(-48 * time.Hour).Unix(), EndTime: now.Add(-47 * time.Hour).Unix(), IsPublished: true,
	})
	if err != nil {
		t.Fatalf("insert past event: %v", err)
	}
	nearID, _, _, err := insertEvent(db, EventInput{
		Title: "Near Future Gig", StartTime: now.Add(48 * time.Hour).Unix(), EndTime: now.Add(49 * time.Hour).Unix(), IsPublished: true,
	})
	if err != nil {
		t.Fatalf("insert near future event: %v", err)
	}
	farID, _, _, err := insertEvent(db, EventInput{
		Title: "Far Future Gig", StartTime: now.Add(240 * time.Hour).Unix(), EndTime: now.Add(241 * time.Hour).Unix(), IsPublished: true,
	})
	if err != nil {
		t.Fatalf("insert far future event: %v", err)
	}
	for _, eid := range []int{pastID, nearID, farID} {
		if _, err := db.Exec("INSERT INTO event_musicians (event_id, musician_id) VALUES (?, ?)", eid, musID); err != nil {
			t.Fatalf("link event_musicians: %v", err)
		}
	}

	req := httptest.NewRequest("GET", "/api/v1/musicians?with_event_counts=true", nil)
	w := httptest.NewRecorder()
	getMusicians(w, req)

	var got []Musician
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode response: %v (body=%s)", err, w.Body.String())
	}
	if len(got) != 1 {
		t.Fatalf("got %d musicians, want 1", len(got))
	}
	m := got[0]
	if m.FutureEventCount != 2 {
		t.Fatalf("future_event_count = %d, want 2", m.FutureEventCount)
	}
	if m.PastEventCount != 1 {
		t.Fatalf("past_event_count = %d, want 1", m.PastEventCount)
	}
	wantNext := epochToLocal(now.Add(48 * time.Hour).Unix())
	if m.NextEventAt != wantNext {
		t.Fatalf("next_event_at = %q, want %q (earliest future event, not the farther one)", m.NextEventAt, wantNext)
	}

	// A musician with no future events should get an empty next_event_at.
	res2, err := db.Exec("INSERT INTO musicians (bandname) VALUES ('No Future Gigs Band')")
	if err != nil {
		t.Fatalf("insert musician 2: %v", err)
	}
	mus2ID, _ := res2.LastInsertId()
	if _, err := db.Exec("INSERT INTO event_musicians (event_id, musician_id) VALUES (?, ?)", pastID, mus2ID); err != nil {
		t.Fatalf("link event_musicians 2: %v", err)
	}

	req2 := httptest.NewRequest("GET", "/api/v1/musicians?with_event_counts=true", nil)
	w2 := httptest.NewRecorder()
	getMusicians(w2, req2)
	var got2 []Musician
	if err := json.Unmarshal(w2.Body.Bytes(), &got2); err != nil {
		t.Fatalf("decode response 2: %v", err)
	}
	var m2 *Musician
	for i := range got2 {
		if got2[i].ID == int(mus2ID) {
			m2 = &got2[i]
		}
	}
	if m2 == nil {
		t.Fatalf("musician 2 not found in response")
	}
	if m2.NextEventAt != "" {
		t.Fatalf("next_event_at = %q, want empty (no future events)", m2.NextEventAt)
	}
}
