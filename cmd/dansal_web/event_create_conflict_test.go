package main

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
)

// TestCreateEventDuplicateURLConflict covers #1637 on the web side: a 409 from
// the create endpoint comes back as *DuplicateEventError carrying the matched
// event, not as a generic API error, so the admin form can offer "create
// anyway". Other statuses keep their usual error mapping.
func TestCreateEventDuplicateURLConflict(t *testing.T) {
	t.Run("409 carries the matched event", func(t *testing.T) {
		api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusConflict)
			w.Write([]byte(`{"error":"an event with this URL and start time already exists","existing_id":42,"existing_title":"Ball","existing_start_time":2000000000,"existing_short_code":"abc"}`))
		}))
		defer api.Close()
		c := &DansalClient{BaseURL: api.URL, HTTP: api.Client()}

		_, err := c.CreateEvent(context.Background(), EventCreateReq{Title: "Ball"}, "tok")
		var dup *DuplicateEventError
		if !errors.As(err, &dup) {
			t.Fatalf("err = %v, want *DuplicateEventError", err)
		}
		if dup.ExistingID != 42 || dup.ExistingTitle != "Ball" || dup.ExistingStartTime != 2000000000 {
			t.Fatalf("decoded = %+v", dup)
		}
	})

	t.Run("409 without visible details keeps the id empty", func(t *testing.T) {
		api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusConflict)
			w.Write([]byte(`{"error":"an event with this URL and start time already exists"}`))
		}))
		defer api.Close()
		c := &DansalClient{BaseURL: api.URL, HTTP: api.Client()}

		_, err := c.CreateEvent(context.Background(), EventCreateReq{Title: "Ball"}, "tok")
		var dup *DuplicateEventError
		if !errors.As(err, &dup) {
			t.Fatalf("err = %v, want *DuplicateEventError", err)
		}
		if dup.ExistingID != 0 || dup.ExistingTitle != "" {
			t.Fatalf("expected no details, got %+v", dup)
		}
	})

	t.Run("201 returns the created event", func(t *testing.T) {
		api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusCreated)
			w.Write([]byte(`[{"id":7,"title":"Ball"}]`))
		}))
		defer api.Close()
		c := &DansalClient{BaseURL: api.URL, HTTP: api.Client()}

		ev, err := c.CreateEvent(context.Background(), EventCreateReq{Title: "Ball"}, "tok")
		if err != nil {
			t.Fatalf("CreateEvent: %v", err)
		}
		if ev.ID != 7 {
			t.Fatalf("ev.ID = %d, want 7", ev.ID)
		}
	})
}
