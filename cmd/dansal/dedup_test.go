package main

import (
	"database/sql"
	"testing"
	"time"

	_ "github.com/mattn/go-sqlite3"
)

// setupDedupTestDB creates a fresh in-memory DB and swaps it into the
// package-level db var for the duration of the test.
func setupDedupTestDB(t *testing.T) *sql.DB {
	t.Helper()
	old := db
	t.Cleanup(func() { db = old })

	conn, err := sql.Open("sqlite3", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { conn.Close() })
	db = conn

	if err := createTables(); err != nil {
		t.Fatalf("createTables: %v", err)
	}
	migrateDB()
	if berlinLoc == nil {
		berlinLoc, _ = time.LoadLocation("Europe/Berlin")
	}
	return conn
}

// TestFindExistingEventAgreesWithBothCallers asserts that insertEvent and
// previewDuplicateStatus — which both delegate to findExistingEvent — reach
// the same conclusion (matched vs. not) for the same input against the same
// seeded state, for each of tiers 1-4. This is the regression test the
// dedup-unification issue (#1005) asked for: before the shared finder, the
// two paths ran independently hand-copied SQL and had already drifted.
func TestFindExistingEventAgreesWithBothCallers(t *testing.T) {
	const start = int64(1_800_000_000)
	const threeHoursOK = start + 1000 // inside the ±3h window

	cases := []struct {
		name string
		seed EventInput
		req  EventCreateRequest
		tier DuplicateTier
	}{
		{
			name: "tier 1: UID match",
			seed: EventInput{Title: "Bal Folk", StartTime: start, EndTime: start + 3600, UID: "uid-1", IsPublished: true},
			req: EventCreateRequest{
				EventWriteRequest: EventWriteRequest{Title: "Different title now", StartTime: "2027-01-01T00:00:00Z"},
				UID:               "uid-1",
			},
			tier: TierUID,
		},
		{
			name: "tier 2: URL match within window",
			seed: EventInput{Title: "Bal Folk", StartTime: start, EndTime: start + 3600, URL: "https://example.com/e/1", IsPublished: true},
			req: EventCreateRequest{
				EventWriteRequest: EventWriteRequest{Title: "Different title now", URL: "https://example.com/e/1"},
			},
			tier: TierURL,
		},
		{
			name: "tier 4: title + time match, no location/UID/URL",
			seed: EventInput{Title: "Session Trad", StartTime: start, EndTime: start + 3600, IsPublished: true},
			req: EventCreateRequest{
				EventWriteRequest: EventWriteRequest{Title: "Session Trad"},
			},
			tier: TierTitle,
		},
		{
			name: "no match: different title, time, uid, url",
			seed: EventInput{Title: "Session Trad", StartTime: start, EndTime: start + 3600, IsPublished: true},
			req: EventCreateRequest{
				EventWriteRequest: EventWriteRequest{Title: "Completely Unrelated Event"},
			},
			tier: TierNone,
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			setupDedupTestDB(t)

			seedID, _, outcome, err := insertEvent(db, c.seed)
			if err != nil {
				t.Fatalf("seed insert: %v", err)
			}
			if outcome != outcomeNew {
				t.Fatalf("seed outcome = %s, want new", outcome)
			}

			// req carries no start/end time in most cases above (relying on
			// tier 1/2/4 not needing one); set a matching-window time when the
			// case needs it for its req to reach findExistingEvent's tiers.
			req := c.req
			if req.StartTime == "" {
				req.StartTime = time.Unix(threeHoursOK, 0).UTC().Format(time.RFC3339)
			}

			var locID int64
			startEpoch, startErr := parseTimeToUnix(req.StartTime)
			var startPtr *int64
			if startErr == nil {
				startPtr = &startEpoch
			}
			found, tier, err := findExistingEvent(db, req.Title, req.URL, startPtr, locID, req.UID, req.FetchSourceID)
			if err != nil {
				t.Fatalf("findExistingEvent: %v", err)
			}
			if tier != c.tier {
				t.Fatalf("findExistingEvent tier = %v, want %v", tier, c.tier)
			}
			if c.tier != TierNone && found.ID != seedID {
				t.Fatalf("findExistingEvent matched id %d, want seed id %d", found.ID, seedID)
			}

			// previewDuplicateStatus must agree: "new" iff no tier matched.
			status := previewDuplicateStatus(req)
			gotMatch := status != "new"
			wantMatch := c.tier != TierNone
			if gotMatch != wantMatch {
				t.Fatalf("previewDuplicateStatus(%+v) = %q (match=%v), want match=%v (tier=%v)", req, status, gotMatch, wantMatch, c.tier)
			}

			// insertEvent must agree too: outcomeNew iff no tier matched.
			insIn := EventInput{Title: req.Title, URL: req.URL, UID: req.UID}
			if startPtr != nil {
				insIn.StartTime = *startPtr
				insIn.EndTime = *startPtr + 3600
			}
			_, _, insOutcome, err := insertEvent(db, insIn)
			if err != nil {
				t.Fatalf("insertEvent: %v", err)
			}
			insMatch := insOutcome != outcomeNew
			if insMatch != wantMatch {
				t.Fatalf("insertEvent outcome = %s (match=%v), want match=%v (tier=%v)", insOutcome, insMatch, wantMatch, c.tier)
			}
		})
	}
}

// TestFindExistingEventTier5IsReviewHintOnly asserts tier 5 (fuzzy review
// candidate) is surfaced identically to both callers, but neither treats it
// as a real match: insertEvent still inserts a new row (flagging it for
// admin review), and previewDuplicateStatus still reports "new".
func TestFindExistingEventTier5IsReviewHintOnly(t *testing.T) {
	setupDedupTestDB(t)
	const start = int64(1_800_000_000)

	// Seed a "fetch_source" so tier 5 can fire, and a candidate event from
	// that same source with an overlapping (fuzzy-matching) title.
	res, err := db.Exec("INSERT INTO fetch_sources (url, type) VALUES ('http://example.com','ical')")
	if err != nil {
		t.Fatalf("insert fetch_source: %v", err)
	}
	fsID64, _ := res.LastInsertId()
	fsID := int(fsID64)

	seedID, _, outcome, err := insertEvent(db, EventInput{
		Title: "Grand Bal Folk de Printemps", StartTime: start, EndTime: start + 3600,
		FetchSourceID: fsID, IsPublished: true,
	})
	if err != nil {
		t.Fatalf("seed insert: %v", err)
	}
	if outcome != outcomeNew {
		t.Fatalf("seed outcome = %s, want new", outcome)
	}

	req := EventCreateRequest{
		EventWriteRequest: EventWriteRequest{
			Title:     "Bal Folk de Printemps", // fuzzy-overlaps the seed title
			StartTime: time.Unix(start+600, 0).UTC().Format(time.RFC3339),
		},
		FetchSourceID: fsID,
	}
	// No UID/URL/location match — only the fuzzy tier can fire.
	if status := previewDuplicateStatus(req); status != "new" {
		t.Fatalf("previewDuplicateStatus = %q, want new (tier 5 is a review hint, not a match)", status)
	}

	newID, _, outcome, err := insertEvent(db, EventInput{
		Title: "Bal Folk de Printemps", StartTime: start + 600, EndTime: start + 4200,
		FetchSourceID: fsID,
	})
	if err != nil {
		t.Fatalf("insertEvent: %v", err)
	}
	if outcome != outcomeNew {
		t.Fatalf("insertEvent outcome = %s, want new (tier 5 inserts as new, flags for review)", outcome)
	}
	if newID == seedID {
		t.Fatalf("tier 5 should insert a distinct row, got same id %d", newID)
	}

	var needsReview int
	var dupOfID sql.NullInt64
	db.QueryRow("SELECT needs_duplicate_review, duplicate_of_id FROM events WHERE id=?", newID).Scan(&needsReview, &dupOfID)
	if needsReview != 1 || !dupOfID.Valid || dupOfID.Int64 != int64(seedID) {
		t.Fatalf("new event not flagged for duplicate review against seed: needs_review=%d duplicate_of=%v", needsReview, dupOfID)
	}
}
