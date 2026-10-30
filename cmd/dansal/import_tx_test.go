package main

import (
	"context"
	"testing"
	"time"
)

// TestEnsureOrgFromOrganizerUsesCallerQuerier covers #1708: the organiser lookup
// runs on the import's transaction, so it rolls back with the import.
func TestEnsureOrgFromOrganizerUsesCallerQuerier(t *testing.T) {
	setupDedupTestDB(t)
	v := veventWith(t, "ORGANIZER;CN=Tanzkreis Test:mailto:info@example.test")

	tx, err := db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	id, err := ensureOrgFromOrganizer(tx, v)
	if err != nil || id == nil {
		tx.Rollback()
		t.Fatalf("ensureOrgFromOrganizer in tx: id=%v err=%v", id, err)
	}
	if err := tx.Rollback(); err != nil {
		t.Fatal(err)
	}

	var n int
	if err := db.QueryRow("SELECT COUNT(*) FROM organizations WHERE name = ?", "Tanzkreis Test").Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Errorf("organisation created inside a rolled-back transaction still exists (%d rows)", n)
	}
}

// TestImportEntriesAfterCommitRunsOnlyForCommittedEntries covers #1708: the
// afterCommit hook (where image downloads run) sees only entries that committed,
// and sees them after the commit.
func TestImportEntriesAfterCommitRunsOnlyForCommittedEntries(t *testing.T) {
	setupDedupTestDB(t)
	src := FetchSource{ID: 5, URL: "https://feed.example.test/", MaxPastDays: 0, MaxFutureDays: 3650}
	start := time.Date(2030, 2, 1, 19, 0, 0, 0, time.UTC)
	reqs := []EventCreateRequest{
		{EventWriteRequest: EventWriteRequest{Title: "ok", URL: "https://feed.example.test/ok", StartTime: start.Format(time.RFC3339), EndTime: start.Add(time.Hour).Format(time.RFC3339)}, UID: "ok"},
		{EventWriteRequest: EventWriteRequest{Title: "bad", URL: "https://feed.example.test/bad", StartTime: start.Add(2 * time.Hour).Format(time.RFC3339), EndTime: start.Add(3 * time.Hour).Format(time.RFC3339)}, UID: "bad"},
	}
	finalize := func(tx querier, req EventCreateRequest, td *templateImportData, counts *ImportCounts, allEvents *[]Event) error {
		if req.Title == "bad" {
			return context.DeadlineExceeded
		}
		_, err := importSingleEvent(tx, req, td, "", counts, allEvents)
		return err
	}
	var seen []string
	afterCommit := func(req EventCreateRequest, evs []Event) {
		var n int
		db.QueryRow("SELECT COUNT(*) FROM events WHERE title = ?", req.Title).Scan(&n)
		if n != 1 {
			t.Errorf("afterCommit for %q ran before its event was committed", req.Title)
		}
		seen = append(seen, req.Title)
	}
	if _, _, err := importEntriesAfterCommit(context.Background(), src, reqs,
		func(r EventCreateRequest) EventCreateRequest { return r }, finalize, afterCommit); err != nil {
		t.Fatal(err)
	}
	if len(seen) != 1 || seen[0] != "ok" {
		t.Errorf("afterCommit saw %v, want only [ok]", seen)
	}
}
