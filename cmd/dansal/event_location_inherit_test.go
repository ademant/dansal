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

// TestEventListSelectIncludesRegionCountryCode covers the same #1242-shaped
// gap for region/country_code (found via #1282/#1284's cascading country/
// region search filter never actually narrowing results for a standalone,
// non-room location): eventListSelect coalesced l.country/town/address/
// zipcode/parking to the parent building's value but never selected
// l.region/l.country_code at all, for *either* the location's own value or
// its parent's — so every event at a top-level location came back with an
// always-empty Location.Region/CountryCode regardless of what was actually
// stored, and only room-in-a-building events accidentally got a value via
// the unrelated resolvedLocation() parent-inheritance path in getEvents.
func TestEventListSelectIncludesRegionCountryCode(t *testing.T) {
	setupDedupTestDB(t)

	t.Run("standalone location: own region/country_code come through directly", func(t *testing.T) {
		res, err := db.Exec(
			`INSERT INTO locations (location, country_code, region) VALUES ('Kulturhaus', 'DE', 'Bavaria')`,
		)
		if err != nil {
			t.Fatalf("insert location: %v", err)
		}
		locID64, _ := res.LastInsertId()

		eventID, _, _, err := insertEvent(db, EventInput{
			Title: "Standalone", StartTime: 2000000000, EndTime: 2000003600, IsPublished: true, LocationID: locID64,
		})
		if err != nil {
			t.Fatalf("insert event: %v", err)
		}

		event, err := scanEventRow(db.QueryRow(eventListSelect+" WHERE e.id=?", eventID))
		if err != nil {
			t.Fatalf("scanEventRow: %v", err)
		}
		if event.Location == nil {
			t.Fatal("event.Location is nil")
		}
		if event.Location.CountryCode != "DE" {
			t.Errorf("CountryCode = %q, want %q", event.Location.CountryCode, "DE")
		}
		if event.Location.Region != "Bavaria" {
			t.Errorf("Region = %q, want %q", event.Location.Region, "Bavaria")
		}
	})

	t.Run("room with no region/country_code of its own inherits the parent's", func(t *testing.T) {
		res, err := db.Exec(
			`INSERT INTO locations (location, country_code, region) VALUES ('Schloss Colditz', 'DE', 'Saxony')`,
		)
		if err != nil {
			t.Fatalf("insert parent location: %v", err)
		}
		parentID64, _ := res.LastInsertId()

		res, err = db.Exec(
			`INSERT INTO locations (location, parent_id) VALUES ('Kammermusiksaal', ?)`, parentID64,
		)
		if err != nil {
			t.Fatalf("insert child location: %v", err)
		}
		childID64, _ := res.LastInsertId()

		eventID, _, _, err := insertEvent(db, EventInput{
			Title: "Room", StartTime: 2000000000, EndTime: 2000003600, IsPublished: true, LocationID: childID64,
		})
		if err != nil {
			t.Fatalf("insert event: %v", err)
		}

		event, err := scanEventRow(db.QueryRow(eventListSelect+" WHERE e.id=?", eventID))
		if err != nil {
			t.Fatalf("scanEventRow: %v", err)
		}
		if event.Location == nil {
			t.Fatal("event.Location is nil")
		}
		if event.Location.CountryCode != "DE" {
			t.Errorf("CountryCode = %q, want inherited %q", event.Location.CountryCode, "DE")
		}
		if event.Location.Region != "Saxony" {
			t.Errorf("Region = %q, want inherited %q", event.Location.Region, "Saxony")
		}
	})
}
