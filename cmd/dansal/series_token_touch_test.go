package main

import (
	"testing"
	"time"
)

// An edit made through a series magic link must stamp changed_at and record the
// series-token actor, so feeds and webhooks see it (#1599).
func TestSeriesTokenEditStampsEvent(t *testing.T) {
	setupDedupTestDB(t)
	start := time.Date(2030, 5, 1, 19, 0, 0, 0, time.UTC).Unix()
	id, _, _, err := insertEvent(db, EventInput{Title: "Series Edit", StartTime: start, EndTime: start + 3600, UID: "series-touch-1", IsPublished: true})
	if err != nil {
		t.Fatal(err)
	}

	var changedAt int64
	var changedBy string
	db.QueryRow("SELECT COALESCE(changed_at,0), COALESCE(changed_by,'') FROM events WHERE id = ?", id).Scan(&changedAt, &changedBy)
	if changedAt != 0 && changedBy == seriesTokenActor {
		t.Fatalf("precondition: event already stamped (changed_by=%q)", changedBy)
	}

	touchEventFromSeriesToken(id)

	db.QueryRow("SELECT COALESCE(changed_at,0), COALESCE(changed_by,'') FROM events WHERE id = ?", id).Scan(&changedAt, &changedBy)
	if changedAt == 0 {
		t.Error("changed_at was not stamped")
	}
	if changedBy != seriesTokenActor {
		t.Errorf("changed_by = %q, want %q", changedBy, seriesTokenActor)
	}
}
