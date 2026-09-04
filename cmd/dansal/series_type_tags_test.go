package main

import "testing"

// TestApplySeriesTemplateSyncsTypeTags covers #1240: applySeriesTemplate used
// to write has_ball/has_workshop/has_festival straight from the client-supplied
// template_data JSON without reconciling them against td.Tags, so the two
// could disagree on every event a recurring series instantiates. It must go
// through syncEventTypeTags like every other event-write path.
func TestApplySeriesTemplateSyncsTypeTags(t *testing.T) {
	setupDedupTestDB(t)

	t.Run("tag without matching boolean sets the boolean", func(t *testing.T) {
		eventID, _, _, err := insertEvent(db, EventInput{
			Title: "Session Folk", StartTime: 2000000000, EndTime: 2000003600, IsPublished: true,
		})
		if err != nil {
			t.Fatalf("insert event: %v", err)
		}
		// Client set the tag but not the boolean — tags are authoritative.
		if err := applySeriesTemplate(db, eventID, seriesTemplateData{Tags: []string{"bal-folk"}}); err != nil {
			t.Fatalf("applySeriesTemplate: %v", err)
		}
		var hasBall int
		db.QueryRow("SELECT has_ball FROM events WHERE id=?", eventID).Scan(&hasBall)
		if hasBall != 1 {
			t.Errorf("has_ball = %d, want 1 (tag bal-folk was set)", hasBall)
		}
	})

	t.Run("boolean without matching tag adds the tag", func(t *testing.T) {
		eventID, _, _, err := insertEvent(db, EventInput{
			Title: "Bal Folk", StartTime: 2000000000, EndTime: 2000003600, IsPublished: true,
		})
		if err != nil {
			t.Fatalf("insert event: %v", err)
		}
		// Client set the boolean but not the tag — the tag should backfill.
		if err := applySeriesTemplate(db, eventID, seriesTemplateData{HasBall: true}); err != nil {
			t.Fatalf("applySeriesTemplate: %v", err)
		}
		var n int
		db.QueryRow("SELECT COUNT(*) FROM event_tags WHERE event_id=? AND tag='bal-folk'", eventID).Scan(&n)
		if n != 1 {
			t.Errorf("bal-folk tag not present after HasBall=true, want it backfilled")
		}
	})
}
