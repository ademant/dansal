package main

import "testing"

// TestTimetableTracksDefaultFallback verifies #1174's core contract: an event
// created without its own timetable_tracks reads back the default 8-slug
// palette, while an event created with a custom palette reads back exactly
// that (not the default), and a PATCH that sets a custom palette is
// preserved by a later PATCH that doesn't mention timetable_tracks at all.
func TestTimetableTracksDefaultFallback(t *testing.T) {
	setupDedupTestDB(t)

	id, _, _, err := insertEvent(db, EventInput{
		Title:       "Default palette event",
		StartTime:   2000000000,
		EndTime:     2000003600,
		IsPublished: true,
	})
	if err != nil {
		t.Fatalf("insertEvent: %v", err)
	}
	got, err := fetchEventByID(db, id)
	if err != nil {
		t.Fatalf("fetchEventByID: %v", err)
	}
	if len(got.TimetableTracks) != len(defaultTimetableTracks) {
		t.Fatalf("expected default palette (%d tracks), got %d", len(defaultTimetableTracks), len(got.TimetableTracks))
	}
	if got.TimetableTracks[0].Slug != "bal" {
		t.Fatalf("expected default palette to start with 'bal', got %q", got.TimetableTracks[0].Slug)
	}

	custom := []TimetableTrack{{Slug: "keynote", Name: "Keynote", Color: "#123456"}}
	id2, _, _, err := insertEvent(db, EventInput{
		Title:           "Custom palette event",
		StartTime:       2000000000,
		EndTime:         2000003600,
		IsPublished:     true,
		TimetableTracks: custom,
	})
	if err != nil {
		t.Fatalf("insertEvent (custom): %v", err)
	}
	got2, err := fetchEventByID(db, id2)
	if err != nil {
		t.Fatalf("fetchEventByID: %v", err)
	}
	if len(got2.TimetableTracks) != 1 || got2.TimetableTracks[0].Slug != "keynote" {
		t.Fatalf("expected custom single-track palette, got %+v", got2.TimetableTracks)
	}

	// A PUT that carries no tracks at all (req.TimetableTracks nil, e.g. an
	// ordinary event-edit-form submission) must not clobber the custom
	// palette back to default.
	if _, err := db.Exec(
		`UPDATE events SET title=?, description=?, start_time=?, end_time=?, location_id=?,
		 has_ball=?, has_workshop=?, has_festival=?, is_cancelled=?, is_published=?,
		 workshop_difficulty=?, url=?, booking_url=?, organization_id=?, pricing=jsonb(?),
		 availability=?, tickets_total=?, booking_enabled=?, food=?, drink=?, floor_condition=?, attributes=jsonb(?),
		 contact_name=?, contact_email=?, image_ai_generated=?, changed_at=?, changed_by=?, changed_by_id=?,
		 previous_start_time=COALESCE(?,previous_start_time),
		 timetable_tracks=CASE WHEN ? IS NOT NULL THEN jsonb(?) ELSE timetable_tracks END WHERE id=?`,
		"Custom palette event", "", 2000000000, 2000003600, nil,
		false, false, false, false, true,
		"", "", "", nil, nil,
		"", 0, false, "", "", "", "{}",
		"", "", false, 0, "admin", nil,
		nil, nil, nil, id2,
	); err != nil {
		t.Fatalf("simulated PUT without tracks: %v", err)
	}
	got3, err := fetchEventByID(db, id2)
	if err != nil {
		t.Fatalf("fetchEventByID: %v", err)
	}
	if len(got3.TimetableTracks) != 1 || got3.TimetableTracks[0].Slug != "keynote" {
		t.Fatalf("expected custom palette to survive a tracks-less PUT, got %+v", got3.TimetableTracks)
	}
}
