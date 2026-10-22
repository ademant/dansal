package main

import (
	"testing"
	"time"
)

func TestValidateFetchWindow(t *testing.T) {
	zero, ten, huge, neg := 0, 10, 3651, -1
	cases := []struct {
		name         string
		past, future *int
		wantErr      bool
	}{
		{"nil leaves both alone", nil, nil, false},
		{"zero is a valid window", &zero, &zero, false},
		{"ten each way", &ten, &ten, false},
		{"over the limit", &huge, nil, true},
		{"negative", nil, &neg, true},
	}
	for _, c := range cases {
		err := validateFetchWindow(c.past, c.future)
		if (err != nil) != c.wantErr {
			t.Errorf("%s: err=%v, want error=%v", c.name, err, c.wantErr)
		}
	}
}

func TestFilterEntriesToWindow(t *testing.T) {
	now := time.Date(2026, 10, 22, 12, 0, 0, 0, time.UTC)
	ident := func(r EventCreateRequest) EventCreateRequest { return r }
	req := func(start time.Time) EventCreateRequest {
		return EventCreateRequest{EventWriteRequest: EventWriteRequest{StartTime: start.Format(time.RFC3339)}}
	}
	entries := []EventCreateRequest{
		req(now.Add(-48 * time.Hour)),      // two days ago: outside a 1-day past window
		req(now.Add(-6 * time.Hour)),       // inside
		req(now.Add(30 * 24 * time.Hour)),  // inside
		req(now.Add(400 * 24 * time.Hour)), // past a 365-day future window
	}
	src := FetchSource{ID: 7, MaxPastDays: 1, MaxFutureDays: 365}
	kept := filterEntriesToWindow(src, entries, ident, now)
	if len(kept) != 2 {
		t.Fatalf("kept %d entries, want 2 (inside the window)", len(kept))
	}
}

func TestFilterEntriesToWindowDefaultsWhenUnset(t *testing.T) {
	now := time.Date(2026, 10, 22, 12, 0, 0, 0, time.UTC)
	ident := func(r EventCreateRequest) EventCreateRequest { return r }
	entries := []EventCreateRequest{{EventWriteRequest: EventWriteRequest{StartTime: now.Add(-48 * time.Hour).Format(time.RFC3339)}}}
	// Both zero means no window was set on the struct: the defaults apply, so a
	// two-day-old entry is dropped.
	if kept := filterEntriesToWindow(FetchSource{ID: 8}, entries, ident, now); len(kept) != 0 {
		t.Errorf("unset window kept %d old entries, want 0 (default 1-day past)", len(kept))
	}
}

func TestFilterEntriesToWindowKeepsUnparseableStart(t *testing.T) {
	now := time.Date(2026, 10, 22, 12, 0, 0, 0, time.UTC)
	ident := func(r EventCreateRequest) EventCreateRequest { return r }
	entries := []EventCreateRequest{{EventWriteRequest: EventWriteRequest{StartTime: "not a time"}}}
	if kept := filterEntriesToWindow(FetchSource{ID: 9, MaxPastDays: 1, MaxFutureDays: 1}, entries, ident, now); len(kept) != 1 {
		t.Errorf("unparseable start was dropped; a malformed date must never remove an entry")
	}
}
