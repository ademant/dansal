package main

import (
	"testing"
	"time"
)

// The feed's VEVENT SEQUENCE (#1578): re-importing an event whose sequence has
// not moved (and whose LAST-MODIFIED hasn't advanced) skips the update.
func TestInsertEventSkipsUnchangedSequence(t *testing.T) {
	setupDedupTestDB(t)
	start := time.Date(2030, 6, 1, 19, 0, 0, 0, time.UTC).Unix()
	in := EventInput{
		Title:         "Seq Ball",
		StartTime:     start,
		EndTime:       start + 3*3600,
		UID:           "seq-1",
		FetchSourceID: 3,
		ICalSequence:  2,
		IsPublished:   true,
	}
	id, _, outcome, err := insertEvent(db, in)
	if err != nil || outcome == outcomeUnchanged {
		t.Fatalf("first insert: id=%d outcome=%s err=%v", id, outcome, err)
	}

	// Same sequence, no LAST-MODIFIED: nothing changed, so the update is skipped.
	_, _, outcome, err = insertEvent(db, in)
	if err != nil || outcome != outcomeUnchanged {
		t.Errorf("same sequence: outcome=%s err=%v, want unchanged", outcome, err)
	}

	// A bumped sequence is a real revision and must update.
	in.ICalSequence = 3
	in.Title = "Seq Ball (revised)"
	_, _, outcome, err = insertEvent(db, in)
	if err != nil || outcome == outcomeUnchanged {
		t.Errorf("bumped sequence: outcome=%s err=%v, want an update", outcome, err)
	}
}
