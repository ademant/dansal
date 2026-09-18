package main

import (
	"database/sql"
	"encoding/json"
	"log"
	"net/http"
)

// PendingFetchSuggestion is the API shape of one pending_fetch_suggestions
// row returned by the listing endpoint (#1333 phase 2).
type PendingFetchSuggestion struct {
	ID         int    `json:"id"`
	Email      string `json:"email"`
	FeedURL    string `json:"feed_url"`
	FeedType   string `json:"feed_type"`
	EventCount int    `json:"event_count"`
	OrgID      *int   `json:"org_id,omitempty"`
	OrgName    string `json:"org_name"`
	IsNewOrg   bool   `json:"is_new_org"`
	Status     string `json:"status"`
	CreatedAt  string `json:"created_at"`
}

// GET /api/v1/fetchurl-suggestions — mirrors listPendingRegsHandler's own
// admin-vs-org-member split: admins see every pending suggestion including
// new-org proposals; a non-admin only sees suggestions targeting an existing
// org they belong to (a new-org proposal has no org membership to check
// against, so it stays admin-only, same rule pending_registrations applies).
func listPendingFetchSuggestionsHandler(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	callerID, callerRole := callerFromRequest(r)

	var rows *sql.Rows
	var err error
	if callerRole == RoleAdmin {
		rows, err = db.Query(
			`SELECT pfs.id, pfs.email, pfs.feed_url, pfs.feed_type, pfs.event_count,
			 pfs.org_id, COALESCE(NULLIF(pfs.org_name,''), o.name, ''), pfs.created_at
			 FROM pending_fetch_suggestions pfs
			 LEFT JOIN organizations o ON o.id = pfs.org_id
			 WHERE pfs.status = 'pending'
			 ORDER BY pfs.created_at ASC`,
		)
	} else {
		rows, err = db.Query(
			`SELECT pfs.id, pfs.email, pfs.feed_url, pfs.feed_type, pfs.event_count,
			 pfs.org_id, COALESCE(NULLIF(pfs.org_name,''), o.name, ''), pfs.created_at
			 FROM pending_fetch_suggestions pfs
			 JOIN organizations o ON o.id = pfs.org_id
			 JOIN organization_members om ON om.organization_id = pfs.org_id AND om.user_id = ?
			 WHERE pfs.status = 'pending'
			 ORDER BY pfs.created_at ASC`,
			callerID,
		)
	}
	if err != nil {
		writeError(w, "db error", http.StatusInternalServerError)
		return
	}
	defer rows.Close()

	suggestions := []PendingFetchSuggestion{}
	for rows.Next() {
		var s PendingFetchSuggestion
		var orgID sql.NullInt64
		if err := rows.Scan(&s.ID, &s.Email, &s.FeedURL, &s.FeedType, &s.EventCount, &orgID, &s.OrgName, &s.CreatedAt); err != nil {
			continue
		}
		if orgID.Valid {
			id := int(orgID.Int64)
			s.OrgID = &id
		} else {
			s.IsNewOrg = true
		}
		s.Status = "pending"
		suggestions = append(suggestions, s)
	}
	json.NewEncoder(w).Encode(suggestions)
}

// pendingFetchSuggestionRow is the full row loaded by loadPendingFetchSuggestion.
type pendingFetchSuggestionRow struct {
	ID                                                                 int
	FeedURL, FeedType                                                  string
	OrgID                                                              sql.NullInt64
	OrgName, OrgActorName, OrgDescription, OrgWebsite, OrgContactEmail string
	LocationMappingsJSON                                               string
	Status                                                             string
}

func loadPendingFetchSuggestion(id int) (pendingFetchSuggestionRow, error) {
	var row pendingFetchSuggestionRow
	err := db.QueryRow(
		`SELECT id, feed_url, feed_type, org_id, org_name, org_actor_name,
		 org_description, org_website, org_contact_email, location_mappings, status
		 FROM pending_fetch_suggestions WHERE id = ?`, id,
	).Scan(
		&row.ID, &row.FeedURL, &row.FeedType, &row.OrgID, &row.OrgName, &row.OrgActorName,
		&row.OrgDescription, &row.OrgWebsite, &row.OrgContactEmail, &row.LocationMappingsJSON, &row.Status,
	)
	return row, err
}

// canReviewFetchSuggestion applies the same admin-or-org-member rule as
// listPendingFetchSuggestionsHandler to one specific row's approve/reject
// endpoints.
func canReviewFetchSuggestion(callerID int, callerRole string, row pendingFetchSuggestionRow) bool {
	if callerRole == RoleAdmin {
		return true
	}
	if !row.OrgID.Valid {
		return false // new-org proposals are admin-only
	}
	return isOrgMember(callerID, int(row.OrgID.Int64))
}

// POST /api/v1/fetchurl-suggestions/{id}/approve (#1333 phase 2).
func approveFetchSuggestionHandler(w http.ResponseWriter, r *http.Request) {
	callerID, callerRole := callerFromRequest(r)
	id, ok := requireIntPathValue(w, r, "id", "invalid id")
	if !ok {
		return
	}

	row, err := loadPendingFetchSuggestion(id)
	if err == sql.ErrNoRows {
		writeError(w, "pending suggestion not found", http.StatusNotFound)
		return
	} else if err != nil {
		writeError(w, "db error", http.StatusInternalServerError)
		return
	}
	if row.Status != "pending" {
		writeError(w, "suggestion already reviewed", http.StatusConflict)
		return
	}
	if !canReviewFetchSuggestion(callerID, callerRole, row) {
		writeError(w, "Forbidden", http.StatusForbidden)
		return
	}

	var mappings []FetchSuggestLocationMapping
	json.Unmarshal([]byte(row.LocationMappingsJSON), &mappings)

	tx, err := db.Begin()
	if err != nil {
		writeError(w, "db error", http.StatusInternalServerError)
		return
	}

	var orgID int
	if row.OrgID.Valid {
		orgID = int(row.OrgID.Int64)
	} else {
		var newOrgID int64
		if err := tx.QueryRow(
			"INSERT INTO organizations (name, actor_name, description, website, contact_email) VALUES (?,?,?,?,?) RETURNING id",
			row.OrgName, row.OrgActorName, row.OrgDescription, row.OrgWebsite, row.OrgContactEmail,
		).Scan(&newOrgID); err != nil {
			tx.Rollback()
			writeError(w, "failed to create organization: "+err.Error(), http.StatusInternalServerError)
			return
		}
		orgID = int(newOrgID)
	}

	// Each mapping either points at an existing location (add the feed's own
	// name as an alias so future fetches of this same feed auto-match without
	// needing another manual review — same pattern adminImportConfirmHandler
	// already uses) or supplies details for a brand new one (create it via the
	// same find-or-create ensureLocation the regular import path uses, then
	// alias it the same way — this is what lets a feed event with literally no
	// location data still resolve correctly: the feed's plain place name is
	// enough to match the alias even without an address).
	for _, m := range mappings {
		if m.FeedName == "" {
			continue
		}
		var locID int64
		if m.MatchedLocationID != nil {
			locID = int64(*m.MatchedLocationID)
		} else if m.NewLocation != nil {
			id, err := ensureLocation(tx, EventLocationRequest{
				Location: m.NewLocation.Location,
				Address:  m.NewLocation.Address,
				Zipcode:  m.NewLocation.Zipcode,
				Town:     m.NewLocation.Town,
				Country:  m.NewLocation.Country,
			})
			if err != nil || id == 0 {
				continue
			}
			locID = id
		} else {
			continue
		}
		tx.Exec("INSERT OR IGNORE INTO location_aliases (location_id, alias) VALUES (?, ?)", locID, m.FeedName)
	}

	if _, err := tx.Exec("UPDATE pending_fetch_suggestions SET status = 'approved' WHERE id = ?", id); err != nil {
		tx.Rollback()
		writeError(w, "db error", http.StatusInternalServerError)
		return
	}
	if err := tx.Commit(); err != nil {
		writeError(w, "db error", http.StatusInternalServerError)
		return
	}

	// upsertFetchSource/importFromSource operate on the package-level db, not
	// the tx above, so the org/alias inserts must already be committed by now.
	sourceID, err := upsertFetchSource(row.FeedURL, row.FeedType, nil, &orgID, callerID)
	if err != nil {
		writeError(w, "organization and locations were created, but the fetch source could not be: "+err.Error(), http.StatusInternalServerError)
		return
	}
	src := FetchSource{ID: int(sourceID), URL: row.FeedURL, Type: row.FeedType, OrganizationID: &orgID}
	allEvents, counts, fetchErr := importFromSource(r.Context(), src)
	if fetchErr != nil {
		recordFetchResult(src, 0, fetchErr)
		log.Printf("fetchurl-suggestion: approved id=%d source_id=%d url=%q result=fetch-error caller=%d err=%v", id, sourceID, row.FeedURL, callerID, fetchErr)
	} else {
		recordFetchResult(src, len(allEvents), nil)
		log.Printf("fetchurl-suggestion: approved id=%d source_id=%d url=%q result=ok events=%d caller=%d", id, sourceID, row.FeedURL, len(allEvents), callerID)
	}

	writeJSONStatus(w, http.StatusOK, map[string]any{
		"organization_id": orgID,
		"fetch_source_id": sourceID,
		"import_counts":   counts,
	})
}

// POST /api/v1/fetchurl-suggestions/{id}/reject (#1333 phase 2).
func rejectFetchSuggestionHandler(w http.ResponseWriter, r *http.Request) {
	callerID, callerRole := callerFromRequest(r)
	id, ok := requireIntPathValue(w, r, "id", "invalid id")
	if !ok {
		return
	}

	row, err := loadPendingFetchSuggestion(id)
	if err == sql.ErrNoRows {
		writeError(w, "pending suggestion not found", http.StatusNotFound)
		return
	} else if err != nil {
		writeError(w, "db error", http.StatusInternalServerError)
		return
	}
	if row.Status != "pending" {
		writeError(w, "suggestion already reviewed", http.StatusConflict)
		return
	}
	if !canReviewFetchSuggestion(callerID, callerRole, row) {
		writeError(w, "Forbidden", http.StatusForbidden)
		return
	}

	if _, err := db.Exec("UPDATE pending_fetch_suggestions SET status = 'rejected' WHERE id = ?", id); err != nil {
		writeError(w, "db error", http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusOK)
}
