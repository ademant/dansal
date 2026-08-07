package main

import "database/sql"

// DuplicateTier identifies which tier of the dedup hierarchy matched an
// existing event (or that none did). See findExistingEvent.
type DuplicateTier int

const (
	TierNone DuplicateTier = iota
	TierUID
	TierURL
	TierLocation
	TierTitle
	// TierFuzzyReview is not an auto-merge match — it flags a low-confidence
	// candidate for admin review. ExistingEvent.ID/Title are the *candidate*
	// row in this case, not a confirmed duplicate.
	TierFuzzyReview
)

// ExistingEvent holds the event columns needed by both insertEvent (to
// decide update-vs-insert and which fields to preserve) and
// previewDuplicateStatus (to decide the UI's new/exists/updated label).
type ExistingEvent struct {
	ID                 int
	ShortCode          string
	Title              string
	StartTime          int64
	IsPublished        bool
	IsCancelled        bool
	SourceLastModified int64
	ChangedAt          int64
}

const existingEventCols = "id, COALESCE(short_code,''), title, start_time, is_published, is_cancelled, COALESCE(source_last_modified,0), COALESCE(changed_at,0)"

func scanExistingEvent(row *sql.Row) (ExistingEvent, error) {
	var e ExistingEvent
	err := row.Scan(&e.ID, &e.ShortCode, &e.Title, &e.StartTime, &e.IsPublished, &e.IsCancelled, &e.SourceLastModified, &e.ChangedAt)
	return e, err
}

// findExistingEvent runs the 5-tier dedup hierarchy (documented in
// AGENTS.md/CLAUDE.md) that both insertEvent and previewDuplicateStatus must
// agree on — previously each ran its own hand-copied version of tiers 1-4
// and only insertEvent implemented tier 5, so the two paths had already
// drifted (#1005). Do not change the tier semantics here without updating
// both callers' expectations.
//
//  1. UID exact match
//  2. URL exact match, ±3h window around startTime
//  3. locationID + time, ±3h (no title check — titles get rewritten over an
//     event's lifetime)
//  4. title + time, ±3h (used when locationID is unknown or tier 3 missed)
//  5. fuzzy review candidate: same fetchSourceID + time window + fuzzy title
//     overlap — too low-confidence to auto-merge, so this is a hint for the
//     caller to flag for admin review, not a real match (TierFuzzyReview).
//
// startTime is a pointer because previewDuplicateStatus may be asked to
// preview a request whose date string failed to parse; when nil, only the
// UID tier (which needs no time comparison) is attempted. insertEvent always
// has an already-parsed startTime and passes a non-nil pointer.
func findExistingEvent(q querier, title, url string, startTime *int64, locationID int64, uid string, fetchSourceID int) (ExistingEvent, DuplicateTier, error) {
	const threeHours = int64(3 * 60 * 60)

	if uid != "" {
		e, err := scanExistingEvent(q.QueryRow(
			"SELECT "+existingEventCols+" FROM events WHERE uid = ?", uid,
		))
		if err == nil {
			return e, TierUID, nil
		}
		if err != sql.ErrNoRows {
			return ExistingEvent{}, TierNone, err
		}
	}

	if startTime == nil {
		return ExistingEvent{}, TierNone, nil
	}
	st := *startTime

	// Tier 2: URL, ±3h window — otherwise a feed that reuses one generic URL
	// (e.g. its homepage) for every event permanently locks this tier onto
	// the first event ever imported with that URL, silently absorbing every
	// later, unrelated event sharing the same URL (#702).
	if url != "" {
		e, err := scanExistingEvent(q.QueryRow(
			"SELECT "+existingEventCols+" FROM events WHERE url = ? AND ABS(start_time - ?) < ?", url, st, threeHours,
		))
		if err == nil {
			return e, TierURL, nil
		}
		if err != sql.ErrNoRows {
			return ExistingEvent{}, TierNone, err
		}
	}

	// Tier 3: known location + time, no title check.
	if locationID > 0 {
		e, err := scanExistingEvent(q.QueryRow(
			"SELECT "+existingEventCols+" FROM events WHERE location_id = ? AND ABS(start_time - ?) < ?", locationID, st, threeHours,
		))
		if err == nil {
			return e, TierLocation, nil
		}
		if err != sql.ErrNoRows {
			return ExistingEvent{}, TierNone, err
		}
	}

	// Tier 4: title + time only. Fires when locationID is unknown (feed gave
	// no resolvable location name) or tier 3 missed (location name mismatch
	// between feed and stored event, e.g. after entity-decoding or rename).
	{
		e, err := scanExistingEvent(q.QueryRow(
			"SELECT "+existingEventCols+" FROM events WHERE title = ? AND ABS(start_time - ?) < ?", title, st, threeHours,
		))
		if err == nil {
			return e, TierTitle, nil
		}
		if err != sql.ErrNoRows {
			return ExistingEvent{}, TierNone, err
		}
	}

	// Tier 5: low-confidence review candidate. Same originating feed + same
	// time window + fuzzy title overlap is too weak a signal to auto-merge
	// on, so the caller inserts as new and flags both rows for an admin to
	// resolve via the existing merge UI.
	if fetchSourceID > 0 && title != "" {
		rows, err := q.Query(
			"SELECT id, title FROM events WHERE fetch_source_id = ? AND ABS(start_time - ?) < ?", fetchSourceID, st, threeHours,
		)
		if err == nil {
			defer rows.Close()
			for rows.Next() {
				var candID int
				var candTitle string
				if rows.Scan(&candID, &candTitle) == nil && titlesFuzzyOverlap(title, candTitle) {
					return ExistingEvent{ID: candID, Title: candTitle}, TierFuzzyReview, nil
				}
			}
		}
	}

	return ExistingEvent{}, TierNone, nil
}
