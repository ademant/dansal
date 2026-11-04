package main

import (
	"fmt"
	"sort"
)

// adminDeleteEntity implements `dansal_admin delete --type ... --id N` (#1792).
//
// The rules follow the issue discussion, not a generic cascade:
//   - organisation: its fetch URLs (fetch_sources) are deleted, since a
//     removed org means the org no longer wants its feed published. Events
//     and series stay and lose the org (org-free). Event templates that point
//     at the org are removed by dansal_web (it owns event_templates).
//   - location: events and timetable entries stay and lose the location; rooms
//     (child locations) go with their building. Templates pointing at the
//     location are removed by dansal_web, as for organisations.
//   - series: a bracket around events, so the events stay and leave the series.
//   - musician, instructor: events stay; only the links are removed.
//
// Without confirm the statements run in a transaction that is rolled back, so
// the response is an exact preview of what confirm would change.
//
// The whole command is refused unless server.allow_admin_delete is set; that
// flag is the prod guard (#1792), not a domain check.
func adminDeleteEntity(req adminRequest) adminResponse {
	if !config.Server.AllowAdminDelete {
		return adminResponse{OK: false, Error: "delete is disabled on this instance (set server.allow_admin_delete)"}
	}
	if req.EntityID <= 0 {
		return adminResponse{OK: false, Error: "id is required"}
	}
	table, ok := entityDeleteTables[req.Kind]
	if !ok {
		return adminResponse{OK: false, Error: fmt.Sprintf("unknown type %q (organisation, location, series, musician, instructor)", req.Kind)}
	}

	tx, err := db.Begin()
	if err != nil {
		return adminResponse{OK: false, Error: err.Error()}
	}
	defer tx.Rollback() // no-op after a successful Commit

	var n int
	if err := tx.QueryRow("SELECT COUNT(*) FROM "+table+" WHERE id = ?", req.EntityID).Scan(&n); err != nil {
		return adminResponse{OK: false, Error: err.Error()}
	}
	if n == 0 {
		return adminResponse{OK: false, Error: req.Kind + " not found"}
	}

	counts := map[string]int64{}
	step := func(label, query string, args ...any) error {
		res, err := tx.Exec(query, args...)
		if err != nil {
			return fmt.Errorf("%s: %w", label, err)
		}
		if c, _ := res.RowsAffected(); c > 0 {
			counts[label] += c
		}
		return nil
	}

	id := req.EntityID
	var steps [][]any // label, query, args... executed in order
	switch req.Kind {
	case "organisation":
		steps = [][]any{
			{"fetch URLs deleted", "DELETE FROM fetch_sources WHERE organization_id = ?", id},
			{"events detached", "UPDATE events SET organization_id = NULL WHERE organization_id = ?", id},
			{"series detached", "UPDATE event_series SET organization_id = NULL WHERE organization_id = ?", id},
			{"invite links detached", "UPDATE invite_links SET org_id = NULL WHERE org_id = ?", id},
			{"members removed", "DELETE FROM organization_members WHERE organization_id = ?", id},
			{"location links removed", "DELETE FROM location_organizations WHERE organization_id = ?", id},
		}
	case "location":
		steps = [][]any{
			{"events detached", "UPDATE events SET location_id = NULL WHERE location_id IN (SELECT id FROM locations WHERE id = ? OR parent_id = ?)", id, id},
			{"timetable entries detached", "UPDATE timetable_entries SET location_id = NULL WHERE location_id IN (SELECT id FROM locations WHERE id = ? OR parent_id = ?)", id, id},
			{"series default location detached", "UPDATE event_series SET default_location_id = NULL WHERE default_location_id IN (SELECT id FROM locations WHERE id = ? OR parent_id = ?)", id, id},
			{"rooms deleted", "DELETE FROM locations WHERE parent_id = ?", id},
		}
	case "series":
		steps = [][]any{
			{"events detached", "UPDATE events SET series_id = NULL WHERE series_id = ?", id},
		}
	case "musician":
		steps = [][]any{
			{"event links removed", "DELETE FROM event_musicians WHERE musician_id = ?", id},
			{"timetable entries detached", "UPDATE timetable_entries SET musician_id = NULL WHERE musician_id = ?", id},
			{"series detached", "UPDATE event_series SET musician_id = NULL WHERE musician_id = ?", id},
		}
	case "instructor":
		steps = [][]any{
			{"event links removed", "DELETE FROM event_instructors WHERE instructor_id = ?", id},
			{"timetable entries detached", "UPDATE timetable_entries SET instructor_id = NULL WHERE instructor_id = ?", id},
			{"series detached", "UPDATE event_series SET instructor_id = NULL WHERE instructor_id = ?", id},
		}
	}
	for _, s := range steps {
		if err := step(s[0].(string), s[1].(string), s[2:]...); err != nil {
			return adminResponse{OK: false, Error: err.Error()}
		}
	}
	// The entity itself goes last: the FKs above have already cleared the
	// references that would otherwise block it.
	if err := step(req.Kind+" deleted", "DELETE FROM "+table+" WHERE id = ?", id); err != nil {
		return adminResponse{OK: false, Error: err.Error()}
	}

	report := entityDeleteReport{Kind: req.Kind, ID: id, Confirmed: req.Confirm, Changes: sortedCounts(counts)}
	if !req.Confirm {
		report.Note = "preview only: nothing was changed; repeat with --confirm to delete"
		return adminResponse{OK: true, Data: report}
	}
	if err := tx.Commit(); err != nil {
		return adminResponse{OK: false, Error: err.Error()}
	}
	return adminResponse{OK: true, Data: report}
}

// entityDeleteTables maps the --type values to their tables.
var entityDeleteTables = map[string]string{
	"organisation": "organizations",
	"location":     "locations",
	"series":       "event_series",
	"musician":     "musicians",
	"instructor":   "instructors",
}

type entityDeleteReport struct {
	Kind      string         `json:"kind"`
	ID        int            `json:"id"`
	Confirmed bool           `json:"confirmed"`
	Changes   []entityChange `json:"changes"`
	Note      string         `json:"note,omitempty"`
}

type entityChange struct {
	What  string `json:"what"`
	Count int64  `json:"count"`
}

func sortedCounts(m map[string]int64) []entityChange {
	out := make([]entityChange, 0, len(m))
	for k, v := range m {
		out = append(out, entityChange{What: k, Count: v})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].What < out[j].What })
	return out
}
