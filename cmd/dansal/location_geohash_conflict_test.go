package main

import (
	"encoding/json"
	"net/http/httptest"
	"strconv"
	"testing"
)

// TestCheckGeohashAvailable covers #1302: an exact geohash-7 collision
// between two top-level locations used to fail the raw
// idx_locations_geohash_toplevel UNIQUE constraint with a generic 500 (or,
// on update, corrupt the write silently depending on driver). checkGeohashAvailable
// mirrors checkOsmIDAvailable so this becomes a friendly 409+existing_id
// instead, matching the OSM-ID conflict UX.
func TestCheckGeohashAvailable(t *testing.T) {
	setupDedupTestDB(t)

	res, err := db.Exec(`INSERT INTO locations (location, geohash) VALUES ('Existing Hall', 'u0abcde')`)
	if err != nil {
		t.Fatalf("insert existing location: %v", err)
	}
	existingID64, _ := res.LastInsertId()
	existingID := int(existingID64)

	t.Run("no collision: empty geohash is always available", func(t *testing.T) {
		rec := httptest.NewRecorder()
		if ok := checkGeohashAvailable(rec, "", nil, "0"); !ok {
			t.Errorf("expected true for an empty geohash, got false (body=%s)", rec.Body.String())
		}
	})

	t.Run("no collision: a different geohash is available", func(t *testing.T) {
		rec := httptest.NewRecorder()
		if ok := checkGeohashAvailable(rec, "u0fghij", nil, "0"); !ok {
			t.Errorf("expected true for a distinct geohash, got false (body=%s)", rec.Body.String())
		}
	})

	t.Run("collision on create: same geohash, no self to exclude", func(t *testing.T) {
		rec := httptest.NewRecorder()
		ok := checkGeohashAvailable(rec, "u0abcde", nil, "0")
		if ok {
			t.Fatal("expected false on exact geohash collision")
		}
		if rec.Code != 409 {
			t.Errorf("status = %d, want 409", rec.Code)
		}
		var body struct {
			ExistingID int `json:"existing_id"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
			t.Fatalf("invalid JSON body: %v", err)
		}
		if body.ExistingID != existingID {
			t.Errorf("existing_id = %d, want %d", body.ExistingID, existingID)
		}
	})

	t.Run("no collision on update: excludes the location's own row", func(t *testing.T) {
		rec := httptest.NewRecorder()
		selfID := strconv.Itoa(existingID)
		if ok := checkGeohashAvailable(rec, "u0abcde", nil, selfID); !ok {
			t.Errorf("expected true when the only match is the location's own row, got false (body=%s)", rec.Body.String())
		}
	})

	t.Run("no collision: the new/edited location is itself a room (has a parent)", func(t *testing.T) {
		rec := httptest.NewRecorder()
		parentID := existingID
		if ok := checkGeohashAvailable(rec, "u0abcde", &parentID, "0"); !ok {
			t.Errorf("expected true for a room (parentID set) even on a colliding geohash, got false (body=%s)", rec.Body.String())
		}
	})
}
