package main

import (
	"testing"
	"time"
)

// TestBookingLongExpiryUsesConfiguredRetention covers #1440 (G3):
// bookingLongExpiry's horizon — also the age at which the hourly sweep
// deletes a confirmed booking's name/email/message — must follow
// config.Server.DataRetentionDays rather than a hardcoded 90 days, so an
// operator can tune it via YAML.
func TestBookingLongExpiryUsesConfiguredRetention(t *testing.T) {
	bookingTestDB(t)

	endTime := time.Now().Add(24 * time.Hour).Unix()
	res, err := db.Exec(
		"INSERT INTO events (title, start_time, end_time) VALUES ('Test Ball', ?, ?)",
		endTime-3600, endTime,
	)
	if err != nil {
		t.Fatal(err)
	}
	eventID, _ := res.LastInsertId()

	config.Server.DataRetentionDays = 30
	got := bookingLongExpiry(int(eventID))
	want := time.Unix(endTime, 0).UTC().Add(30 * 24 * time.Hour)
	if !got.Equal(want) {
		t.Errorf("bookingLongExpiry with DataRetentionDays=30 = %v, want %v", got, want)
	}

	config.Server.DataRetentionDays = 0 // unset: falls back to 90
	got = bookingLongExpiry(int(eventID))
	want = time.Unix(endTime, 0).UTC().Add(90 * 24 * time.Hour)
	if !got.Equal(want) {
		t.Errorf("bookingLongExpiry with DataRetentionDays=0 = %v, want %v (90-day fallback)", got, want)
	}
}
