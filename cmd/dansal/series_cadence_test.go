package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
)

// TestEventSeriesCadenceMigration is a smoke test for the #1185 migration
// (safety-net pattern from CLAUDE.md): createTables + migrateDB, run
// migrateDB a second time to confirm idempotency, then confirm the column
// exists either way.
func TestEventSeriesCadenceMigration(t *testing.T) {
	setupDedupTestDB(t)
	migrateDB() // idempotency check

	var n int
	if err := db.QueryRow("SELECT COUNT(*) FROM pragma_table_info('event_series') WHERE name='cadence'").Scan(&n); err != nil {
		t.Fatalf("pragma_table_info: %v", err)
	}
	if n != 1 {
		t.Fatalf("event_series.cadence column missing after migrateDB (n=%d)", n)
	}
}

// adminReq builds an admin-authenticated request for the series handlers.
func adminReq(method, target string, body []byte) *http.Request {
	var r *http.Request
	if body != nil {
		r = httptest.NewRequest(method, target, bytes.NewReader(body))
		r.Header.Set("Content-Type", "application/json")
	} else {
		r = httptest.NewRequest(method, target, nil)
	}
	r.Header.Set("X-User-ID", "1")
	r.Header.Set("X-User-Role", "admin")
	return r
}

// TestSeriesCadenceCreateAndUpdate covers the create->read->update->clear
// round trip for EventSeries.Cadence (#1185): a single free-text,
// disclosure-only field, not an RRULE.
func TestSeriesCadenceCreateAndUpdate(t *testing.T) {
	setupDedupTestDB(t)
	db.Exec("INSERT INTO users (id, email, display_name, role) VALUES (1, 'admin@example.test', 'Admin', 'admin')")

	createBody, _ := json.Marshal(map[string]any{
		"title":   "Balfolk am Elisenbrunnen",
		"cadence": "every 2nd + 4th Thursday, except holidays",
	})
	req := adminReq("POST", "/api/v1/series", createBody)
	w := httptest.NewRecorder()
	createSeries(w, req)
	if w.Code != http.StatusCreated {
		t.Fatalf("create status = %d, want 201 (body=%s)", w.Code, w.Body.String())
	}
	var created EventSeries
	if err := json.Unmarshal(w.Body.Bytes(), &created); err != nil {
		t.Fatalf("decode create response: %v", err)
	}
	if created.Cadence != "every 2nd + 4th Thursday, except holidays" {
		t.Fatalf("created.Cadence = %q, want the submitted text", created.Cadence)
	}

	// GET reflects the stored cadence.
	getReq := adminReq("GET", "/api/v1/series/"+strconv.Itoa(created.ID), nil)
	getReq.SetPathValue("id", strconv.Itoa(created.ID))
	getW := httptest.NewRecorder()
	getSeriesByID(getW, getReq)
	var fetched EventSeries
	if err := json.Unmarshal(getW.Body.Bytes(), &fetched); err != nil {
		t.Fatalf("decode get response: %v", err)
	}
	if fetched.Cadence != created.Cadence {
		t.Fatalf("fetched.Cadence = %q, want %q", fetched.Cadence, created.Cadence)
	}

	// PUT with a new cadence replaces it.
	updateBody, _ := json.Marshal(map[string]any{
		"title":   "Balfolk am Elisenbrunnen",
		"cadence": "every Thursday",
	})
	putReq := adminReq("PUT", "/api/v1/series/"+strconv.Itoa(created.ID), updateBody)
	putReq.SetPathValue("id", strconv.Itoa(created.ID))
	putW := httptest.NewRecorder()
	updateSeries(putW, putReq)
	if putW.Code != http.StatusNoContent {
		t.Fatalf("update status = %d, want 204 (body=%s)", putW.Code, putW.Body.String())
	}
	var updatedRow string
	db.QueryRow("SELECT cadence FROM event_series WHERE id=?", created.ID).Scan(&updatedRow)
	if updatedRow != "every Thursday" {
		t.Fatalf("cadence after update = %q, want %q", updatedRow, "every Thursday")
	}

	// PUT omitting cadence entirely leaves it unchanged (pointer semantics —
	// only an explicit key, even "", touches the stored value).
	noCadenceBody, _ := json.Marshal(map[string]any{"title": "Balfolk am Elisenbrunnen"})
	putReq2 := adminReq("PUT", "/api/v1/series/"+strconv.Itoa(created.ID), noCadenceBody)
	putReq2.SetPathValue("id", strconv.Itoa(created.ID))
	putW2 := httptest.NewRecorder()
	updateSeries(putW2, putReq2)
	if putW2.Code != http.StatusNoContent {
		t.Fatalf("update (omitted cadence) status = %d, want 204", putW2.Code)
	}
	db.QueryRow("SELECT cadence FROM event_series WHERE id=?", created.ID).Scan(&updatedRow)
	if updatedRow != "every Thursday" {
		t.Fatalf("cadence after omitted-key update = %q, want unchanged %q", updatedRow, "every Thursday")
	}

	// PUT with an explicit empty cadence clears it.
	clearBody, _ := json.Marshal(map[string]any{"title": "Balfolk am Elisenbrunnen", "cadence": ""})
	putReq3 := adminReq("PUT", "/api/v1/series/"+strconv.Itoa(created.ID), clearBody)
	putReq3.SetPathValue("id", strconv.Itoa(created.ID))
	putW3 := httptest.NewRecorder()
	updateSeries(putW3, putReq3)
	if putW3.Code != http.StatusNoContent {
		t.Fatalf("update (clear cadence) status = %d, want 204", putW3.Code)
	}
	db.QueryRow("SELECT cadence FROM event_series WHERE id=?", created.ID).Scan(&updatedRow)
	if updatedRow != "" {
		t.Fatalf("cadence after explicit-clear update = %q, want empty", updatedRow)
	}
}

// TestEventResponseIncludesSeriesCadence verifies scanEventRow/eventListSelect
// denormalize event_series.cadence onto every event of that series (#1185),
// the same way series_image_url/series_image_ai_generated already do, so
// event.html and the org page can show it without a separate series lookup.
func TestEventResponseIncludesSeriesCadence(t *testing.T) {
	setupDedupTestDB(t)
	db.Exec("INSERT INTO users (id, email, display_name, role) VALUES (1, 'admin@example.test', 'Admin', 'admin')")

	res, err := db.Exec(`INSERT INTO event_series (slug, title, cadence) VALUES ('elisenbrunnen', 'Balfolk am Elisenbrunnen', 'every 2nd + 4th Thursday, except holidays')`)
	if err != nil {
		t.Fatalf("insert series: %v", err)
	}
	seriesID64, _ := res.LastInsertId()
	seriesID := int(seriesID64)

	eventID, _, _, err := insertEvent(db, EventInput{
		Title: "Balfolk am Elisenbrunnen", StartTime: 2000000000, EndTime: 2000003600, IsPublished: true,
	})
	if err != nil {
		t.Fatalf("insert event: %v", err)
	}
	if _, err := db.Exec("UPDATE events SET series_id=? WHERE id=?", seriesID, eventID); err != nil {
		t.Fatalf("attach event to series: %v", err)
	}

	event, err := fetchEventByID(db, eventID)
	if err != nil {
		t.Fatalf("fetchEventByID: %v", err)
	}
	if event.SeriesCadence != "every 2nd + 4th Thursday, except holidays" {
		t.Fatalf("event.SeriesCadence = %q, want the series' cadence", event.SeriesCadence)
	}

	// An event with no series carries no cadence.
	eventID2, _, _, err := insertEvent(db, EventInput{
		Title: "One-off event", StartTime: 2000010000, EndTime: 2000013600, IsPublished: true,
	})
	if err != nil {
		t.Fatalf("insert second event: %v", err)
	}
	event2, err := fetchEventByID(db, eventID2)
	if err != nil {
		t.Fatalf("fetchEventByID: %v", err)
	}
	if event2.SeriesCadence != "" {
		t.Fatalf("event2.SeriesCadence = %q, want empty for a non-series event", event2.SeriesCadence)
	}
}
