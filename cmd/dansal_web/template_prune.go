package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"time"

	"github.com/ademant/dansal/internal/logging"
)

// pruneOrphanTemplates removes event templates whose organisation or location
// no longer exists in the API (#1792). Templates make no sense without them.
// dansal_admin delete detaches the events, but it cannot reach web.db, which
// holds the templates, so the web tier does this itself: once at startup and
// then hourly.
//
// Nothing is pruned when a lookup fails: the lists are read in full (pageAll),
// so a failed page must not look like "everything is gone" (#1827).
func pruneOrphanTemplates(ctx context.Context, db *sql.DB, client *DansalClient) (int, error) {
	orgs, err := client.GetOrganizations(ctx)
	if err != nil {
		return 0, err
	}
	locs, err := client.GetLocations(ctx)
	if err != nil {
		return 0, err
	}
	orgIDs := make(map[int]bool, len(orgs))
	for _, o := range orgs {
		orgIDs[o.ID] = true
	}
	locIDs := make(map[int]bool, len(locs))
	for _, l := range locs {
		locIDs[l.ID] = true
	}

	rows, err := db.QueryContext(ctx, "SELECT id, org_id, data FROM event_templates")
	if err != nil {
		return 0, err
	}
	var orphans []int
	for rows.Next() {
		var id int
		var orgID sql.NullInt64
		var data string
		if err := rows.Scan(&id, &orgID, &data); err != nil {
			rows.Close()
			return 0, err
		}
		// The location lives in the template's data JSON (loc_id), not in a column.
		var d struct {
			LocID int `json:"loc_id"`
		}
		_ = json.Unmarshal([]byte(data), &d)
		orgGone := orgID.Valid && orgID.Int64 > 0 && !orgIDs[int(orgID.Int64)]
		locGone := d.LocID > 0 && !locIDs[d.LocID]
		if orgGone || locGone {
			orphans = append(orphans, id)
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return 0, err
	}

	removed := 0
	for _, id := range orphans {
		if _, err := db.ExecContext(ctx, "DELETE FROM event_templates WHERE id = ?", id); err != nil {
			return removed, err
		}
		removed++
	}
	if removed > 0 {
		logging.Infof("dansal-web: pruned %d event template(s) whose organisation or location was deleted", removed)
	}
	return removed, nil
}

// startTemplatePruning runs pruneOrphanTemplates at startup and then hourly.
func startTemplatePruning(db *sql.DB, client *DansalClient) {
	go func() {
		run := func() {
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
			defer cancel()
			if _, err := pruneOrphanTemplates(ctx, db, client); err != nil {
				logging.Warnf("dansal-web: template prune: %v", err)
			}
		}
		run()
		for range time.Tick(time.Hour) {
			run()
		}
	}()
}
