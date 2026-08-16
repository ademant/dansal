package main

import "testing"

// TestMatchRecheckEntry covers the three-tier matching order used to pick
// which parsed feed entry corresponds to an existing event being rechecked
// (#1112): UID first, then URL, then — only when the source ever described
// exactly one event — that sole entry.
func TestMatchRecheckEntry(t *testing.T) {
	t.Run("matches by UID even when other entries share nothing else", func(t *testing.T) {
		reqs := []EventCreateRequest{
			{UID: "other-uid", EventWriteRequest: EventWriteRequest{URL: "https://example.com/other"}},
			{UID: "target-uid", EventWriteRequest: EventWriteRequest{URL: "https://example.com/target"}},
		}
		got, ok := matchRecheckEntry(reqs, "target-uid", "https://example.com/nomatch")
		if !ok || got.UID != "target-uid" {
			t.Fatalf("expected UID match, got %+v ok=%v", got, ok)
		}
	})

	t.Run("falls back to URL when UID is absent or unmatched", func(t *testing.T) {
		reqs := []EventCreateRequest{
			{EventWriteRequest: EventWriteRequest{URL: "https://example.com/a"}},
			{EventWriteRequest: EventWriteRequest{URL: "https://example.com/b"}},
		}
		got, ok := matchRecheckEntry(reqs, "", "https://example.com/b")
		if !ok || got.URL != "https://example.com/b" {
			t.Fatalf("expected URL match, got %+v ok=%v", got, ok)
		}
	})

	t.Run("falls back to the sole entry when neither UID nor URL matches", func(t *testing.T) {
		reqs := []EventCreateRequest{
			{EventWriteRequest: EventWriteRequest{Title: "Only event"}},
		}
		got, ok := matchRecheckEntry(reqs, "", "")
		if !ok || got.Title != "Only event" {
			t.Fatalf("expected sole-entry fallback, got %+v ok=%v", got, ok)
		}
	})

	t.Run("ambiguous: no UID/URL match and more than one entry", func(t *testing.T) {
		reqs := []EventCreateRequest{
			{EventWriteRequest: EventWriteRequest{Title: "A"}},
			{EventWriteRequest: EventWriteRequest{Title: "B"}},
		}
		_, ok := matchRecheckEntry(reqs, "", "")
		if ok {
			t.Fatalf("expected no match for ambiguous multi-entry source")
		}
	})
}
