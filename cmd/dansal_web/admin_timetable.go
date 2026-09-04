package main

import (
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"strconv"
)

// TimetableRoom is a room option for the timetable editor's room picker.
// Label is pre-disambiguated (includes building name when two buildings share
// a room name, e.g. "305 — Aal2" vs "305 — Aalen"). BuildingID/BuildingName
// identify the room's actual parent location (not derived from the label,
// which is only suffixed on a name collision) so the grid can group room
// columns under a shared building super-header (#1233) regardless of
// whether disambiguation happened to kick in for this particular room.
type TimetableRoom struct {
	ID           int    `json:"id"`
	Label        string `json:"label"`
	BuildingID   int    `json:"buildingId,omitempty"`
	BuildingName string `json:"buildingName,omitempty"`
}

// TimetablePageData holds everything the admin timetable editor template needs.
type TimetablePageData struct {
	Event           Event
	Timetable       []TimetableEntry
	Rooms           []TimetableRoom // rooms across all relevant buildings, disambiguated
	Musicians       []Musician
	Instructors     []Instructor
	TopLocationID   int    // building-level location ID used for room quick-create
	TopLocationName string // short name of the primary building (for create-room label)
}

// GET /admin/events/{id}/timetable — serve the dedicated timetable editor.
func adminTimetablePageHandler(cfg *Config, tmpls *Templates, client *DansalClient, i18n *I18n) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		_, ok := requireLogin(w, r)
		if !ok {
			return
		}
		id, err := strconv.Atoi(r.PathValue("id"))
		if err != nil {
			http.Error(w, "invalid event id", http.StatusBadRequest)
			return
		}
		tok := getSessionToken(r)
		ctx := r.Context()

		var (
			event       Event
			allLocs     []Location
			musicians   []Musician
			instructors []Instructor
		)

		err = fetchParallel(
			func() error { var err error; event, err = client.GetEventAuthed(ctx, id, tok); return err },
			func() error {
				var err error
				allLocs, err = client.GetLocations(ctx)
				if err != nil {
					log.Printf("timetable: could not load locations: %v", err)
				}
				return nil
			},
			func() error {
				var err error
				musicians, err = client.GetMusicians(ctx)
				if err != nil {
					log.Printf("timetable: could not load musicians: %v", err)
				}
				return nil
			},
			func() error {
				var err error
				instructors, err = client.GetInstructors(ctx)
				if err != nil {
					log.Printf("timetable: could not load instructors: %v", err)
				}
				return nil
			},
		)
		if err != nil {
			http.Error(w, "event not found", http.StatusNotFound)
			return
		}
		// Normalize nil slices to empty ones: the API omits empty fields
		// (json:",omitempty"), and json.Marshal of a nil Go slice produces the
		// JS literal `null`, not `[]`, which crashes the template's unconditional
		// _entries.forEach(...) etc. on first load for a brand-new event (#1225).
		timetable := event.Timetable
		if timetable == nil {
			timetable = []TimetableEntry{}
		}
		if musicians == nil {
			musicians = []Musician{}
		}
		if instructors == nil {
			instructors = []Instructor{}
		}

		// Resolve building-level location IDs: primary + all extra locations.
		// If a location is itself a room (child), use the parent as the building.
		topLocID := 0 // primary building — used for the room quick-create API
		if event.LocationID != nil {
			topLocID = *event.LocationID
		}
		if event.Location != nil && event.Location.ParentID != nil {
			topLocID = *event.Location.ParentID
		}

		// Build a complete id→location map from the cached all-locations list.
		locByID := make(map[int]Location, len(allLocs))
		for _, l := range allLocs {
			locByID[l.ID] = l
		}

		// Collect the set of buildings (parent locations) whose rooms should be
		// offered: primary building, extra event locations (multi-venue), plus
		// the parent of any room already referenced by an existing timetable entry.
		buildingIDs := map[int]bool{}
		if topLocID > 0 {
			buildingIDs[topLocID] = true
		}
		for _, loc := range event.Locations {
			bID := loc.ID
			if loc.ParentID != nil {
				bID = *loc.ParentID
			}
			if bID > 0 {
				buildingIDs[bID] = true
			}
		}
		for _, e := range timetable {
			if e.LocationID == nil {
				continue
			}
			if l, ok := locByID[*e.LocationID]; ok && l.ParentID != nil {
				buildingIDs[*l.ParentID] = true
			}
		}

		// Group rooms by building and detect name conflicts so we can
		// disambiguate labels (e.g. "305 — Aal2" vs "305 — Aalen").
		type buildingGroup struct {
			id    int
			name  string
			rooms []Location
		}
		var buildings []buildingGroup
		for bid := range buildingIDs {
			bl := locByID[bid]
			name := bl.ShortName
			if name == "" {
				name = bl.Location
			}
			var children []Location
			for _, l := range allLocs {
				if l.ParentID != nil && *l.ParentID == bid {
					children = append(children, l)
				}
			}
			buildings = append(buildings, buildingGroup{id: bid, name: name, rooms: children})
		}

		roomLabel := func(l Location) string {
			if l.ShortName != "" {
				return l.ShortName
			}
			if l.Location != "" {
				return l.Location
			}
			return fmt.Sprintf("Room %d", l.ID)
		}

		// Count how many buildings share each base label.
		nameCounts := map[string]int{}
		for _, b := range buildings {
			for _, r := range b.rooms {
				nameCounts[roomLabel(r)]++
			}
		}

		var rooms []TimetableRoom
		for _, b := range buildings {
			for _, r := range b.rooms {
				lbl := roomLabel(r)
				if nameCounts[lbl] > 1 {
					lbl = lbl + " — " + b.name
				}
				rooms = append(rooms, TimetableRoom{ID: r.ID, Label: lbl, BuildingID: b.id, BuildingName: b.name})
			}
		}

		if rooms == nil {
			rooms = []TimetableRoom{}
		}

		topLocName := ""
		if bl, ok := locByID[topLocID]; ok {
			topLocName = bl.ShortName
			if topLocName == "" {
				topLocName = bl.Location
			}
		}

		title := fmt.Sprintf("Timetable — %s", event.Title)
		renderTemplate(w, tmpls.adminTimetable, tmplData(r, cfg, i18n, title, TimetablePageData{
			Event:           event,
			Timetable:       timetable,
			Rooms:           rooms,
			Musicians:       musicians,
			Instructors:     instructors,
			TopLocationID:   topLocID,
			TopLocationName: topLocName,
		}))
	}
}

// PUT /admin/events/{id}/timetable — replace whole timetable (proxy to API).
func adminTimetableSaveHandler(client *DansalClient) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		_, ok := requireLogin(w, r)
		if !ok {
			return
		}
		id, err := strconv.Atoi(r.PathValue("id"))
		if err != nil {
			http.Error(w, "invalid event id", http.StatusBadRequest)
			return
		}
		tok := getSessionToken(r)

		body, err := io.ReadAll(io.LimitReader(r.Body, maxInboundJSONBody))
		if err != nil {
			http.Error(w, "read error", http.StatusBadRequest)
			return
		}
		var entries []TimetableEntryReq
		if err := json.Unmarshal(body, &entries); err != nil {
			http.Error(w, "invalid JSON", http.StatusBadRequest)
			return
		}
		if err := client.ReplaceTimetable(r.Context(), id, entries, tok); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"ok":true}`))
	}
}

// POST /admin/events/{id}/timetable/sync-times — set event start/end from timetable entries.
// Body: {"start_time":"2025-10-18T18:00","end_time":"2025-10-20T23:00"}
func adminTimetableSyncTimesHandler(client *DansalClient) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		_, ok := requireLogin(w, r)
		if !ok {
			return
		}
		id, err := strconv.Atoi(r.PathValue("id"))
		if err != nil {
			http.Error(w, "invalid event id", http.StatusBadRequest)
			return
		}
		tok := getSessionToken(r)

		var req struct {
			StartTime string `json:"start_time"`
			EndTime   string `json:"end_time"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.StartTime == "" || req.EndTime == "" {
			http.Error(w, "invalid body: start_time and end_time required", http.StatusBadRequest)
			return
		}
		if err := client.PatchEventTimes(r.Context(), id, req.StartTime, req.EndTime, tok); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"ok":true}`))
	}
}

// PUT /admin/events/{id}/timetable/tracks — replace the event's timetable
// track palette (#1174; proxy to a merge-patch on the API).
func adminTimetableTracksSaveHandler(client *DansalClient) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		_, ok := requireLogin(w, r)
		if !ok {
			return
		}
		id, err := strconv.Atoi(r.PathValue("id"))
		if err != nil {
			http.Error(w, "invalid event id", http.StatusBadRequest)
			return
		}
		tok := getSessionToken(r)

		var tracks []TimetableTrack
		if err := json.NewDecoder(io.LimitReader(r.Body, maxInboundJSONBody)).Decode(&tracks); err != nil {
			http.Error(w, "invalid JSON", http.StatusBadRequest)
			return
		}
		if tracks == nil {
			tracks = []TimetableTrack{}
		}
		if err := client.PatchEventTimetableTracks(r.Context(), id, tracks, tok); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"ok":true}`))
	}
}

// DELETE /admin/events/{id}/timetable — delete all entries (proxy to API).
func adminTimetableDeleteHandler(client *DansalClient) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		_, ok := requireLogin(w, r)
		if !ok {
			return
		}
		id, err := strconv.Atoi(r.PathValue("id"))
		if err != nil {
			http.Error(w, "invalid event id", http.StatusBadRequest)
			return
		}
		tok := getSessionToken(r)
		if err := client.DeleteTimetable(r.Context(), id, tok); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}
}
