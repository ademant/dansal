package main

import (
	"database/sql"
	"net/http"
	"strconv"
)

// EntityRedirect is an explicit, durable forwarding record made when an
// administrator merges two public resources.  It deliberately stores IDs,
// rather than URLs, so the web application can always build the current
// canonical URL (and chains can be followed after subsequent merges).
type EntityRedirect struct {
	Entity string `json:"entity"`
	OldID  int    `json:"old_id"`
	NewID  int    `json:"new_id"`
}

func redirectEntityTable(entity string) string {
	switch entity {
	case "event":
		return "events"
	case "location":
		return "locations"
	default:
		return ""
	}
}

func putEntityRedirect(w http.ResponseWriter, r *http.Request) {
	_, role := callerFromRequest(r)
	if role != RoleAdmin {
		writeError(w, "Forbidden", http.StatusForbidden)
		return
	}
	var redir EntityRedirect
	if !decodeJSONBody(w, r, &redir) {
		return
	}
	if redirectEntityTable(redir.Entity) == "" || redir.OldID <= 0 || redir.NewID <= 0 || redir.OldID == redir.NewID {
		writeError(w, "invalid entity redirect", http.StatusBadRequest)
		return
	}
	var n int
	if err := db.QueryRow("SELECT COUNT(*) FROM "+redirectEntityTable(redir.Entity)+" WHERE id=?", redir.NewID).Scan(&n); err != nil || n == 0 {
		writeError(w, "redirect target not found", http.StatusBadRequest)
		return
	}
	if _, err := db.Exec(`INSERT INTO entity_redirects(entity,old_id,new_id,created_at)
		VALUES(?,?,?,unixepoch()) ON CONFLICT(entity,old_id) DO UPDATE SET new_id=excluded.new_id, created_at=excluded.created_at`,
		redir.Entity, redir.OldID, redir.NewID); err != nil {
		writeInternalError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// getEntityRedirect resolves an old public ID to its live survivor. A missing
// mapping is intentionally a 404: callers then decide whether their original
// resource should be rendered as a normal 404 or as Gone.
func getEntityRedirect(w http.ResponseWriter, r *http.Request) {
	entity := r.PathValue("entity")
	table := redirectEntityTable(entity)
	id, err := strconv.Atoi(r.PathValue("id"))
	if table == "" || err != nil || id <= 0 {
		http.NotFound(w, r)
		return
	}
	seen := map[int]bool{}
	current := id
	for depth := 0; depth < 16; depth++ {
		if seen[current] {
			writeError(w, "entity redirect loop", http.StatusConflict)
			return
		}
		seen[current] = true
		var next int
		err := db.QueryRow("SELECT new_id FROM entity_redirects WHERE entity=? AND old_id=?", entity, current).Scan(&next)
		if err == sql.ErrNoRows {
			break
		}
		if err != nil {
			writeInternalError(w, err)
			return
		}
		if next == 0 {
			writeError(w, "entity is gone", http.StatusGone)
			return
		}
		current = next
	}
	if current == id {
		http.NotFound(w, r)
		return
	}
	var n int
	if err := db.QueryRow("SELECT COUNT(*) FROM "+table+" WHERE id=?", current).Scan(&n); err != nil || n == 0 {
		writeError(w, "redirect target is gone", http.StatusGone)
		return
	}
	writeJSON(w, EntityRedirect{Entity: entity, OldID: id, NewID: current})
}

// insertEntityTombstone records a hard delete in entity_redirects so
// getEntityRedirect returns 410 Gone reliably, without relying solely on the
// sqlite_sequence heuristic. new_id=0 is the sentinel for "gone"; 0 is never
// a valid AUTOINCREMENT id.
func insertEntityTombstone(entity string, id int) {
	db.Exec(`INSERT INTO entity_redirects(entity,old_id,new_id,created_at)
		VALUES(?,?,0,unixepoch()) ON CONFLICT(entity,old_id) DO NOTHING`,
		entity, id)
}

// idWasAllocated provides a best-effort retroactive tombstone for legacy
// hard-deletes. AUTOINCREMENT never reuses IDs; explicit redirect records are
// still authoritative because a sequence can contain harmless gaps.
func idWasAllocated(table string, id string) bool {
	n, err := strconv.ParseInt(id, 10, 64)
	if err != nil || n <= 0 {
		return false
	}
	var seq sql.NullInt64
	if err := db.QueryRow("SELECT seq FROM sqlite_sequence WHERE name=?", table).Scan(&seq); err != nil || !seq.Valid {
		return false
	}
	return n <= seq.Int64
}
