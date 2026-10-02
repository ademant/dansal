package main

import (
	"database/sql"
	"encoding/json"
	"net/http"
	"strconv"
)

// #1427: resolving a flagged possible-duplicate pair (needs_duplicate_review
// / duplicate_of_id, set by flagDuplicateReview). Before, the only way out
// was a bulk merge; a pair can just as well be two real events in different
// rooms, one event with a copy/paste date, or two parallel events in the
// same hall. The comparison page, the edit form's "check duplicate status"
// button, "Cleaned", and the automatic clearing after an edit all decide
// "do these two still collide?" through pairCollision — the same rule
// findExistingEvent applies (tiers 3–5) — so they can never disagree.

// Collision reasons, as returned to the web UI.
const (
	dupReasonTime    = "time"          // starts less than 3h apart (precondition)
	dupReasonVenue   = "venue"         // same location_id (tier 3)
	dupReasonTitle   = "title"         // identical title (tier 4)
	dupReasonSimilar = "similar_title" // same feed + fuzzy title overlap (tier 5)
)

type dupEvent struct {
	ID          int
	Title       string
	Start       int64
	LocationID  int64
	SourceID    int
	Flagged     bool
	DuplicateOf int
}

func loadDupEvent(q querier, id int) (dupEvent, error) {
	var e dupEvent
	var loc, src, dupOf sql.NullInt64
	err := q.QueryRow(`SELECT id, title, start_time, location_id, fetch_source_id,
		COALESCE(needs_duplicate_review,0), duplicate_of_id FROM events WHERE id = ?`, id).
		Scan(&e.ID, &e.Title, &e.Start, &loc, &src, &e.Flagged, &dupOf)
	e.LocationID, e.SourceID, e.DuplicateOf = loc.Int64, int(src.Int64), int(dupOf.Int64)
	return e, err
}

// pairCollision reports whether a and b would still be treated as possible
// duplicates, and why. They must start less than 3h apart and additionally
// share the venue, the exact title, or (same feed) a fuzzy-overlapping title.
func pairCollision(a, b dupEvent) (bool, []string) {
	const threeHours = int64(3 * 60 * 60)
	d := a.Start - b.Start
	if d < 0 {
		d = -d
	}
	if d >= threeHours {
		return false, nil
	}
	reasons := []string{dupReasonTime}
	if a.LocationID > 0 && a.LocationID == b.LocationID {
		reasons = append(reasons, dupReasonVenue)
	}
	if a.Title != "" && a.Title == b.Title {
		reasons = append(reasons, dupReasonTitle)
	} else if a.SourceID > 0 && a.SourceID == b.SourceID && titlesFuzzyOverlap(a.Title, b.Title) {
		reasons = append(reasons, dupReasonSimilar)
	}
	return len(reasons) > 1, reasons
}

// DuplicateConflict is another event colliding with the checked one.
type DuplicateConflict struct {
	ID      int      `json:"id"`
	Title   string   `json:"title"`
	Reasons []string `json:"reasons"`
}

// DuplicateCheck is the result of GET /api/v1/events/{id}/duplicate-check.
type DuplicateCheck struct {
	EventID      int                 `json:"event_id"`
	Flagged      bool                `json:"flagged"`
	PartnerID    int                 `json:"partner_id,omitempty"`
	PartnerTitle string              `json:"partner_title,omitempty"`
	Collides     bool                `json:"collides"`
	Reasons      []string            `json:"reasons"`
	Others       []DuplicateConflict `json:"other_conflicts"`
}

func checkDuplicates(q querier, id int) (DuplicateCheck, error) {
	ev, err := loadDupEvent(q, id)
	if err != nil {
		return DuplicateCheck{}, err
	}
	res := DuplicateCheck{EventID: id, Flagged: ev.Flagged, PartnerID: ev.DuplicateOf, Reasons: []string{}, Others: []DuplicateConflict{}}
	if ev.DuplicateOf > 0 {
		if partner, err := loadDupEvent(q, ev.DuplicateOf); err == nil {
			res.PartnerTitle = partner.Title
			res.Collides, res.Reasons = pairCollision(ev, partner)
			if res.Reasons == nil {
				res.Reasons = []string{}
			}
		} else if err != sql.ErrNoRows {
			return DuplicateCheck{}, err
		} else {
			res.PartnerID = 0 // partner deleted meanwhile
		}
	}
	// Other events this one collides with now (e.g. after moving it into an
	// occupied room) — information only.
	rows, err := q.Query(`SELECT id FROM events WHERE id != ? AND id != ? AND ABS(start_time - ?) < 10800
		AND (location_id = ? OR title = ? OR (fetch_source_id = ? AND ? > 0)) LIMIT 20`,
		id, ev.DuplicateOf, ev.Start, ev.LocationID, ev.Title, ev.SourceID, ev.SourceID)
	if err != nil {
		return DuplicateCheck{}, err
	}
	var ids []int
	for rows.Next() {
		var oid int
		if rows.Scan(&oid) == nil {
			ids = append(ids, oid)
		}
	}
	rows.Close()
	for _, oid := range ids {
		other, err := loadDupEvent(q, oid)
		if err != nil {
			continue
		}
		if ok, reasons := pairCollision(ev, other); ok {
			res.Others = append(res.Others, DuplicateConflict{ID: oid, Title: other.Title, Reasons: reasons})
		}
	}
	return res, nil
}

// clearDuplicatePair removes the review flag from id and from its partner —
// the partner only while it is flagged against id, since it may meanwhile be
// flagged against a third event.
func clearDuplicatePair(q querier, id, partnerID int) error {
	_, err := q.Exec(`UPDATE events SET needs_duplicate_review = 0, duplicate_of_id = NULL
		WHERE id = ? OR (id = ? AND duplicate_of_id = ?)`, id, partnerID, id)
	return err
}

// recheckDuplicatePair clears a flagged pair once it no longer collides —
// called after every event update/patch, so fixing the date, the room or the
// venue by hand resolves the flag without an extra step (#1427).
func recheckDuplicatePair(q querier, id int) {
	res, err := checkDuplicates(q, id)
	if err != nil || !res.Flagged || res.Collides {
		return
	}
	clearDuplicatePair(q, id, res.PartnerID)
}

func requireAdminCaller(w http.ResponseWriter, r *http.Request) bool {
	if _, role := callerFromRequest(r); role != RoleAdmin {
		writeError(w, "Forbidden", http.StatusForbidden)
		return false
	}
	return true
}

// GET /api/v1/events/{id}/duplicate-check — admin only.
func duplicateCheckHandler(w http.ResponseWriter, r *http.Request) {
	if !requireAdminCaller(w, r) {
		return
	}
	id, err := strconv.Atoi(r.PathValue("id"))
	if err != nil {
		writeError(w, "invalid id", http.StatusBadRequest)
		return
	}
	res, err := checkDuplicates(db, id)
	if err == sql.ErrNoRows {
		writeError(w, "event not found", http.StatusNotFound)
		return
	} else if err != nil {
		writeInternalError(w, err)
		return
	}
	writeJSON(w, res)
}

// DuplicateResolveRequest is the body of POST /api/v1/events/{id}/duplicate-resolve.
type DuplicateResolveRequest struct {
	// Mode "accept": different events, clear the pair's flags; "resolved":
	// clear only if the pair no longer collides (409 otherwise).
	Mode string `json:"mode" enum:"accept,resolved"`
}

// POST /api/v1/events/{id}/duplicate-resolve — admin only.
// {"mode":"accept"}   — they are different events: clear the pair's flags.
// {"mode":"resolved"} — clear only if the pair no longer collides
// ("Cleaned"); 409 with the remaining check result otherwise.
func duplicateResolveHandler(w http.ResponseWriter, r *http.Request) {
	if !requireAdminCaller(w, r) {
		return
	}
	id, err := strconv.Atoi(r.PathValue("id"))
	if err != nil {
		writeError(w, "invalid id", http.StatusBadRequest)
		return
	}
	var req DuplicateResolveRequest
	if !decodeJSONBody(w, r, &req) {
		return
	}
	res, err := checkDuplicates(db, id)
	if err == sql.ErrNoRows {
		writeError(w, "event not found", http.StatusNotFound)
		return
	} else if err != nil {
		writeInternalError(w, err)
		return
	}
	switch req.Mode {
	case "accept":
	case "resolved":
		if res.Collides {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusConflict)
			json.NewEncoder(w).Encode(res)
			return
		}
	default:
		writeError(w, `mode must be "accept" or "resolved"`, http.StatusBadRequest)
		return
	}
	if err := clearDuplicatePair(db, id, res.PartnerID); err != nil {
		writeInternalError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
