package main

import "testing"

// TestEventListSelectInheritsZipcodeParkingGeohash covers #1242:
// eventListSelect inherited address/town/country/coordinates from a parent
// location but not zipcode/parking/geohash, even though inheritLocationFields
// (the canonical Go-side mechanism) inherits all of these for a child
// location. An event held at a room with none of its own should see the
// parent building's values, same as /api/v1/locations/{id} already does.
func TestEventListSelectInheritsZipcodeParkingGeohash(t *testing.T) {
	setupDedupTestDB(t)

	res, err := db.Exec(
		`INSERT INTO locations (location, zipcode, parking, geohash) VALUES ('Schloss Colditz', '04680', 'free', 'u0abcde')`,
	)
	if err != nil {
		t.Fatalf("insert parent location: %v", err)
	}
	parentID64, _ := res.LastInsertId()
	parentID := int(parentID64)

	res, err = db.Exec(
		`INSERT INTO locations (location, parent_id) VALUES ('Kammermusiksaal', ?)`, parentID,
	)
	if err != nil {
		t.Fatalf("insert child location: %v", err)
	}
	childID64, _ := res.LastInsertId()
	childID := int(childID64)

	eventID, _, _, err := insertEvent(db, EventInput{
		Title: "Session", StartTime: 2000000000, EndTime: 2000003600, IsPublished: true, LocationID: int64(childID),
	})
	if err != nil {
		t.Fatalf("insert event: %v", err)
	}

	row := db.QueryRow(eventListSelect+" WHERE e.id=?", eventID)
	event, err := scanEventRow(row)
	if err != nil {
		t.Fatalf("scanEventRow: %v", err)
	}
	if event.Location == nil {
		t.Fatal("event.Location is nil")
	}
	if event.Location.Zipcode != "04680" {
		t.Errorf("Zipcode = %q, want inherited %q", event.Location.Zipcode, "04680")
	}
	if event.Location.Parking != "free" {
		t.Errorf("Parking = %q, want inherited %q", event.Location.Parking, "free")
	}
	if event.Location.Geohash != "u0abcde" {
		t.Errorf("Geohash = %q, want inherited %q", event.Location.Geohash, "u0abcde")
	}
}
