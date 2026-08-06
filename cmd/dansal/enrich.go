package main

import (
	"encoding/json"
	"net/http"
	"strconv"
)

type EnrichEventReq struct {
	AddMusicianIDs []int    `json:"add_musician_ids,omitempty"`
	Pricing        *Pricing `json:"pricing,omitempty"`
}

// enrichEvent adds musicians additively and sets pricing if currently null.
// It is deliberately not transactional — partial success is acceptable here
// (musicians get linked even if the pricing update fails or is skipped).
func enrichEvent(w http.ResponseWriter, r *http.Request) {
	_, callerRole := callerFromRequest(r)
	if callerRole != RoleAdmin && callerRole != RolePublisher {
		writeError(w, "Forbidden", http.StatusForbidden)
		return
	}
	id, err := strconv.Atoi(r.PathValue("id"))
	if err != nil || id <= 0 {
		writeError(w, "invalid event id", http.StatusBadRequest)
		return
	}
	var req EnrichEventReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, "bad request body", http.StatusBadRequest)
		return
	}
	for _, mid := range req.AddMusicianIDs {
		db.Exec("INSERT OR IGNORE INTO event_musicians(event_id, musician_id) VALUES(?,?)", id, mid)
	}
	if req.Pricing != nil {
		if b, merr := json.Marshal(req.Pricing); merr == nil {
			db.Exec("UPDATE events SET pricing=jsonb(?) WHERE id=? AND pricing IS NULL", string(b), id)
		}
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{"ok": true})
}
