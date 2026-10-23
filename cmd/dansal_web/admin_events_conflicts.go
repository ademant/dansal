package main

import (
	"net/http"
	"strconv"
)

// adminEventBulkConflictsHandler serves POST /admin/events/bulk-conflicts. The
// maintenance form asks it, before submitting, how many selected events would
// clash on "no live music" with musicians, so the popup can offer skip or
// overwrite (#1612). It writes nothing.
func adminEventBulkConflictsHandler(client *DansalClient) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if _, ok := requireLogin(w, r); !ok {
			return
		}
		if err := r.ParseForm(); err != nil {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		var ids []int
		for _, s := range r.Form["event_ids"] {
			if id, err := strconv.Atoi(s); err == nil {
				ids = append(ids, id)
			}
		}
		payload := map[string]any{"ids": ids}
		switch r.FormValue("no_live_music") {
		case "1":
			payload["no_live_music"] = true
		case "0":
			payload["no_live_music"] = false
		}
		if v := r.FormValue("musician_id"); v != "" {
			if id, err := strconv.Atoi(v); err == nil && id > 0 {
				payload["add_musicians"] = []int{id}
			}
		}
		conflicts, err := client.BulkEventConflicts(r.Context(), payload, getSessionToken(r))
		if err != nil {
			http.Error(w, "could not check conflicts", http.StatusBadGateway)
			return
		}
		writeJSONResponse(w, http.StatusOK, map[string]any{"count": len(conflicts), "ids": conflicts})
	}
}
