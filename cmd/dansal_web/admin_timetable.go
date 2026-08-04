package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"sync"
)

// TimetablePageData holds everything the admin timetable editor template needs.
type TimetablePageData struct {
	Event         Event
	Timetable     []TimetableEntry
	Rooms         []Location // child locations of the event's venue (building)
	Musicians     []Musician
	Instructors   []Instructor
	TopLocationID int // building-level location ID used for room quick-create
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
			eventErr    error
			timetable   []TimetableEntry
			rooms       []Location
			musicians   []Musician
			instructors []Instructor
			wg          sync.WaitGroup
		)

		wg.Add(1)
		go func() {
			defer wg.Done()
			event, eventErr = client.GetEventAuthed(ctx, id, tok)
		}()
		wg.Add(1)
		go func() {
			defer wg.Done()
			musicians, _ = client.GetMusicians(ctx)
		}()
		wg.Add(1)
		go func() {
			defer wg.Done()
			instructors, _ = client.GetInstructors(ctx)
		}()
		wg.Wait()

		if eventErr != nil {
			http.Error(w, "event not found", http.StatusNotFound)
			return
		}
		timetable = event.Timetable

		// Resolve the building-level location ID: if the event's location is
		// itself a room (child), use the parent so we fetch sibling rooms.
		topLocID := 0
		if event.LocationID != nil {
			topLocID = *event.LocationID
		}
		if event.Location != nil && event.Location.ParentID != nil {
			topLocID = *event.Location.ParentID
		}
		if topLocID > 0 {
			rooms, _ = client.GetLocationChildren(ctx, topLocID)
		}

		title := fmt.Sprintf("Timetable — %s", event.Title)
		renderTemplate(w, tmpls.adminTimetable, tmplData(r, cfg, i18n, title, TimetablePageData{
			Event:         event,
			Timetable:     timetable,
			Rooms:         rooms,
			Musicians:     musicians,
			Instructors:   instructors,
			TopLocationID: topLocID,
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

		body, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
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
