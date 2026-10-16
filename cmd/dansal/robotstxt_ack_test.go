package main

// robots.txt acknowledgement for disallowed feeds (#1545). A feed robots.txt
// disallows is refused unless someone has recorded an acknowledgement; an
// acknowledged feed is fetched on manual action only, and fetch-all never
// polls it. The importer is the only automatic client, so these tests drive
// importFromSource and adminFetchAll directly.

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
)

// robotsDisallowAllServer serves a robots.txt that disallows the given path
// prefix for everyone, and fails the test if the feed path itself is requested
// when the importer should have stopped before fetching it.
func robotsDisallowAllServer(t *testing.T, disallow string, feedFetchOK bool) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/robots.txt" {
			w.Write([]byte("User-agent: *\nDisallow: " + disallow + "\n"))
			return
		}
		if !feedFetchOK {
			t.Errorf("feed %s must not be fetched", r.URL.Path)
		}
		w.WriteHeader(http.StatusOK)
	}))
}

func TestRobotsDisallowRuleDescribesMatch(t *testing.T) {
	resetRobotsCacheForTest(t)
	withUnsafeTestClient(t)
	srv := robotsDisallowAllServer(t, "/private/", true)
	defer srv.Close()

	if rule := robotsDisallowRule(context.Background(), srv.URL+"/private/feed.ics"); rule == "" {
		t.Fatal("expected a disallow rule for /private/")
	}
	if rule := robotsDisallowRule(context.Background(), srv.URL+"/open/feed.ics"); rule != "" {
		t.Fatalf("expected no rule for an allowed path, got %q", rule)
	}
}

// An unacknowledged disallowed feed is refused with the sentinel, so the API
// can answer 409 and the UI can ask for consent. The feed itself is never requested.
func TestImportFromSourceRefusesUnacknowledgedDisallowed(t *testing.T) {
	resetRobotsCacheForTest(t)
	withUnsafeTestClient(t)
	srv := robotsDisallowAllServer(t, "/", false)
	defer srv.Close()

	_, _, err := importFromSource(context.Background(), FetchSource{Type: "ical", URL: srv.URL + "/feed.ics"})
	if !errors.Is(err, errRobotsNotAcknowledged) {
		t.Fatalf("expected errRobotsNotAcknowledged, got %v", err)
	}
}

// With a recorded acknowledgement the robots restriction no longer refuses the
// fetch. (The loopback SSRF guard may still stop the request afterwards; the
// assertion is only that robots.txt is not the reason.)
func TestImportFromSourceAcknowledgedPassesRobotsGate(t *testing.T) {
	setupDedupTestDB(t)
	resetRobotsCacheForTest(t)
	withUnsafeTestClient(t)
	srv := robotsDisallowAllServer(t, "/", true)
	defer srv.Close()

	_, _, err := importFromSource(context.Background(), FetchSource{
		Type:          "ical",
		URL:           srv.URL + "/feed.ics",
		RobotsAckAt:   "2026-10-14T10:00:00Z",
		RobotsAckRule: `disallowed for "dansal" on test (path /feed.ics)`,
	})
	if errors.Is(err, errRobotsNotAcknowledged) {
		t.Fatalf("acknowledged feed was refused by robots: %v", err)
	}
}

// fetch-all must not poll a disallowed feed even when it is acknowledged; the
// acknowledgement only allows manual fetches. An allowed feed in the same run is
// still fetched, so it records a result while the disallowed one records nothing.
func TestFetchAllSkipsRobotsDisallowedEvenIfAcknowledged(t *testing.T) {
	setupDedupTestDB(t)
	resetRobotsCacheForTest(t)
	withUnsafeTestClient(t)
	srv := robotsDisallowAllServer(t, "/private/", true)
	defer srv.Close()

	res, err := db.Exec(`INSERT INTO fetch_sources (url, type, tags, robots_ack_at, robots_ack_by, robots_ack_reason, robots_ack_rule)
		VALUES (?, 'ical', '[]', '2026-10-14T10:00:00Z', 'tester', 'ok', 'rule')`, srv.URL+"/private/feed.ics")
	if err != nil {
		t.Fatal(err)
	}
	privateID, _ := res.LastInsertId()
	res, err = db.Exec(`INSERT INTO fetch_sources (url, type, tags) VALUES (?, 'ical', '[]')`, srv.URL+"/open/feed.ics")
	if err != nil {
		t.Fatal(err)
	}
	openID, _ := res.LastInsertId()

	adminFetchAll()

	var privateResult, openResult string
	db.QueryRow("SELECT COALESCE(last_result,'') FROM fetch_sources WHERE id = ?", privateID).Scan(&privateResult)
	db.QueryRow("SELECT COALESCE(last_result,'') FROM fetch_sources WHERE id = ?", openID).Scan(&openResult)
	if privateResult != "" {
		t.Errorf("disallowed+acknowledged feed was polled by fetch-all (last_result=%q)", privateResult)
	}
	if openResult == "" {
		t.Errorf("allowed feed was not fetched by fetch-all")
	}
}

// The acknowledgement round-trips through the schema and the scan path.
func TestRecordRobotsAckPersists(t *testing.T) {
	setupDedupTestDB(t)
	res, err := db.Exec(`INSERT INTO fetch_sources (url, type, tags) VALUES ('https://calendar.google.com/x.ics', 'ical', '[]')`)
	if err != nil {
		t.Fatal(err)
	}
	id, _ := res.LastInsertId()

	src, err := scanFetchSource(db.QueryRow("SELECT "+fetchSourceCols+" FROM fetch_sources WHERE id = ?", id))
	if err != nil {
		t.Fatal(err)
	}
	if src.RobotsAckAt != "" {
		t.Fatalf("new source should not be acknowledged, got %q", src.RobotsAckAt)
	}

	recordRobotsAck(&src, "disallowed for \"dansal\" on calendar.google.com (path /x.ics)", "manual only", "admin")
	reloaded, err := scanFetchSource(db.QueryRow("SELECT "+fetchSourceCols+" FROM fetch_sources WHERE id = ?", id))
	if err != nil {
		t.Fatal(err)
	}
	if reloaded.RobotsAckAt == "" || reloaded.RobotsAckBy != "admin" || reloaded.RobotsAckReason != "manual only" ||
		reloaded.RobotsAckRule == "" {
		t.Fatalf("ack not persisted: %+v", reloaded)
	}
}
