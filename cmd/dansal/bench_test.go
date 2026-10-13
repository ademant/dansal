package main

// Benchmarks for the genuinely expensive in-process hot paths that had no
// regression guard at all (#1543): feed-type detection, the 5-tier dedup
// hierarchy (findExistingEvent/previewDuplicateStatus), insertEvent, and
// feed parsing. Run with `make bench` (go test -bench=. -benchmem
// ./cmd/dansal/) — these never run under a plain `go test` (no -bench
// flag), so they add no cost to the normal test suite or CI.
//
// Pairs with #1544 (HTTP-level load testing): this layer answers "how many
// events/sec can dedup+insert sustain" without any HTTP or rate-limit noise
// in the measurement.

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	_ "github.com/mattn/go-sqlite3"
)

// ── detectFetchType ──────────────────────────────────────────────────────

// BenchmarkDetectFetchType covers the free, local-only path: every one of
// these URLs is decided by extension/keyword matching alone (fetchurl_
// folkdance.go), never reaching the network-probing branches.
func BenchmarkDetectFetchType(b *testing.B) {
	urls := []string{
		"http://example.org/feed/events.ics",
		"http://example.org/rss.xml",
		"http://example.org/feed/atom",
		"http://example.org/api/events.json",
	}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		detectFetchType(urls[i%len(urls)])
	}
}

// BenchmarkDetectFetchTypeContentTypeProbe covers the opposite case: an
// extensionless URL that only the HEAD content-type probe can resolve, the
// same shape fetchurl_detect_test.go's TestDetectFetchTypeContentTypePrecedence
// exercises for correctness. safeClient is swapped the same way that test
// does, so this never attempts a real network request.
func BenchmarkDetectFetchTypeContentTypeProbe(b *testing.B) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/calendar; charset=utf-8")
	}))
	defer srv.Close()

	old := safeClient
	safeClient = &http.Client{Timeout: 5 * time.Second}
	defer func() { safeClient = old }()

	url := srv.URL + "/calendar"
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		detectFetchType(url)
	}
}

// ── dedup: findExistingEvent / previewDuplicateStatus, per tier ─────────

// benchDedupFixture seeds exactly what one tier's lookup needs and returns
// the request to probe it with. Each returned request is looked up
// unchanged on every b.N iteration — findExistingEvent/previewDuplicateStatus
// are read-only, so repeating one fixed, matching lookup b.N times measures
// real query cost without any insert/dedup state to keep consistent across
// iterations.
type benchDedupFixture struct {
	title, url, uid string
	startTime       *int64
	locationID      int64
	fetchSourceID   int
}

func seedBenchDedupTier(b *testing.B, tier string) benchDedupFixture {
	b.Helper()
	const start = int64(1_800_000_000)
	switch tier {
	case "uid":
		db.Exec("INSERT INTO events (uid, title, start_time, end_time, is_published) VALUES (?,?,?,?,1)",
			"bench-uid-1", "Bal de Testville", start, start+3600)
		return benchDedupFixture{uid: "bench-uid-1"}
	case "url":
		db.Exec("INSERT INTO events (title, url, start_time, end_time, is_published) VALUES (?,?,?,?,1)",
			"Bal de Testville", "https://example.org/bal", start, start+3600)
		st := start + 1800
		return benchDedupFixture{url: "https://example.org/bal", startTime: &st}
	case "location_merge":
		res, _ := db.Exec("INSERT INTO locations (location) VALUES ('Salle Testville')")
		locID, _ := res.LastInsertId()
		db.Exec("INSERT INTO fetch_sources (id, url, type) VALUES (1, 'http://example.com/1.ics', 'ical')")
		db.Exec("INSERT INTO events (title, location_id, start_time, end_time, is_published, fetch_source_id) VALUES (?,?,?,?,1,1)",
			"Bal de Testville", locID, start, start+3600)
		st := start + 1800
		return benchDedupFixture{title: "Bal de Testville", locationID: locID, startTime: &st, fetchSourceID: 1}
	case "location_review":
		res, _ := db.Exec("INSERT INTO locations (location) VALUES ('Salle Testville')")
		locID, _ := res.LastInsertId()
		db.Exec("INSERT INTO events (title, location_id, start_time, end_time, is_published) VALUES (?,?,?,?,1)",
			"Atelier Bourrée", locID, start, start+3600)
		st := start + 1800
		return benchDedupFixture{title: "Bal du soir", locationID: locID, startTime: &st}
	case "title":
		db.Exec("INSERT INTO events (title, start_time, end_time, is_published) VALUES (?,?,?,1)",
			"Bal de Testville", start, start+3600)
		st := start + 1800
		return benchDedupFixture{title: "Bal de Testville", startTime: &st}
	case "fuzzy_review":
		db.Exec("INSERT INTO fetch_sources (id, url, type) VALUES (2, 'http://example.com/2.ics', 'ical')")
		db.Exec("INSERT INTO events (title, start_time, end_time, is_published, fetch_source_id) VALUES (?,?,?,1,2)",
			"Bal Folk de Printemps", start, start+3600)
		st := start + 1800
		return benchDedupFixture{title: "ABGESAGT – Bal Folk de Printemps", startTime: &st, fetchSourceID: 2}
	default:
		b.Fatalf("unknown tier %q", tier)
		return benchDedupFixture{}
	}
}

func BenchmarkFindExistingEvent(b *testing.B) {
	for _, tier := range []string{"uid", "url", "location_merge", "location_review", "title", "fuzzy_review"} {
		b.Run(tier, func(b *testing.B) {
			setupDedupTestDB(b)
			f := seedBenchDedupTier(b, tier)
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				findExistingEvent(db, f.title, f.url, f.startTime, f.locationID, f.uid, f.fetchSourceID)
			}
		})
	}
}

func BenchmarkPreviewDuplicateStatus(b *testing.B) {
	for _, tier := range []string{"uid", "url", "location_merge", "location_review", "title", "fuzzy_review"} {
		b.Run(tier, func(b *testing.B) {
			setupDedupTestDB(b)
			f := seedBenchDedupTier(b, tier)
			req := EventCreateRequest{
				EventWriteRequest: EventWriteRequest{
					Title:    f.title,
					URL:      f.url,
					Location: EventLocationRequest{Location: locationNameForBenchTier(tier)},
				},
				UID:           f.uid,
				FetchSourceID: f.fetchSourceID,
			}
			if f.startTime != nil {
				req.StartTime = time.Unix(*f.startTime, 0).UTC().Format(time.RFC3339)
			}
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				previewDuplicateStatus(req)
			}
		})
	}
}

// locationNameForBenchTier mirrors seedBenchDedupTier's own location name,
// so previewDuplicateStatus's read-only location-name lookup resolves to
// the same location_id findExistingEvent was given directly.
func locationNameForBenchTier(tier string) string {
	switch tier {
	case "location_merge", "location_review":
		return "Salle Testville"
	default:
		return ""
	}
}

// ── insertEvent: single insert vs. an auto-merge update, and a bulk batch ─

// BenchmarkInsertEvent_NewEvent measures a genuine insert: every iteration's
// title and start_time are unique, so no tier ever matches and each call
// takes the plain INSERT path — never the dedup-and-update one.
func BenchmarkInsertEvent_NewEvent(b *testing.B) {
	setupDedupTestDB(b)
	const start = int64(1_800_000_000)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		insertEvent(db, EventInput{
			Title:       fmt.Sprintf("Bench Event %d", i),
			StartTime:   start + int64(i)*7200,
			EndTime:     start + int64(i)*7200 + 3600,
			IsPublished: true,
		})
	}
}

// BenchmarkInsertEvent_TierLocationMerge measures the opposite case: every
// iteration collides with the same seeded event at the same location+time
// via tier 3's auto-merge path (same fetch source re-sending it), so
// insertEvent takes its UPDATE branch instead of INSERT every time.
func BenchmarkInsertEvent_TierLocationMerge(b *testing.B) {
	setupDedupTestDB(b)
	const start = int64(1_800_000_000)
	res, _ := db.Exec("INSERT INTO locations (location) VALUES ('Salle Testville')")
	locID, _ := res.LastInsertId()
	db.Exec("INSERT INTO fetch_sources (id, url, type) VALUES (1, 'http://example.com/1.ics', 'ical')")
	db.Exec("INSERT INTO events (title, location_id, start_time, end_time, is_published, fetch_source_id) VALUES ('Bal de Testville', ?, ?, ?, 1, 1)",
		locID, start, start+3600)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		insertEvent(db, EventInput{
			Title:         fmt.Sprintf("Bal de Testville (rewrite %d)", i),
			StartTime:     start + 1800,
			EndTime:       start + 5400,
			LocationID:    locID,
			FetchSourceID: 1,
			IsPublished:   true,
		})
	}
}

// BenchmarkInsertEvent_Bulk simulates one fetch-source import batch: 50 new
// events inserted per b.N iteration, reporting events/sec via
// b.ReportMetric so the throughput number doesn't need mental division by
// batch size.
func BenchmarkInsertEvent_Bulk(b *testing.B) {
	const batch = 50
	setupDedupTestDB(b)
	const start = int64(1_800_000_000)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		for j := 0; j < batch; j++ {
			n := i*batch + j
			insertEvent(db, EventInput{
				Title:       fmt.Sprintf("Bench Bulk Event %d", n),
				StartTime:   start + int64(n)*7200,
				EndTime:     start + int64(n)*7200 + 3600,
				IsPublished: true,
			})
		}
	}
	elapsed := b.Elapsed().Seconds()
	if elapsed > 0 {
		b.ReportMetric(float64(b.N*batch)/elapsed, "events/sec")
	}
}

// ── feed parsing ─────────────────────────────────────────────────────────

// parseBodyToRequests's own "rss" branch falls through to its default
// (plain ics.ParseCalendar) rather than real RSS/Atom parsing — that's
// parseRSSBody (fetchurl_rss.go), reached instead via importFromRSSSource
// on the recurring-fetch path. Benchmarked directly here rather than
// through parseBodyToRequests so "RSS/Atom parsing" actually measures the
// parser real feeds go through.

const benchICalBody = `BEGIN:VCALENDAR
VERSION:2.0
PRODID:-//dansal bench//EN
BEGIN:VEVENT
UID:bench-ical-1@example.org
DTSTAMP:20260101T000000Z
DTSTART:20260601T200000Z
DTEND:20260601T220000Z
SUMMARY:Bal de Testville
LOCATION:Salle des Fêtes Testville
DESCRIPTION:Un bal test avec musique live
END:VEVENT
BEGIN:VEVENT
UID:bench-ical-2@example.org
DTSTAMP:20260101T000000Z
DTSTART:20260602T190000Z
DTEND:20260602T230000Z
SUMMARY:Atelier Bourrée du Berry
LOCATION:Salle des Fêtes Testville
DESCRIPTION:Atelier de bourrée pour débutants
END:VEVENT
END:VCALENDAR
`

const benchRSSBody = `<?xml version="1.0" encoding="UTF-8"?>
<rss version="2.0">
<channel>
<title>Bench Feed</title>
<item>
<title>Bal de Testville</title>
<link>https://example.org/events/bal</link>
<guid>https://example.org/events/bal</guid>
<description>Un bal test avec musique live</description>
<eventStart>2026-06-01T20:00:00</eventStart>
<eventEnd>2026-06-01T22:00:00</eventEnd>
</item>
<item>
<title>Atelier Bourrée du Berry</title>
<link>https://example.org/events/atelier</link>
<guid>https://example.org/events/atelier</guid>
<description>Atelier de bourrée pour débutants</description>
<eventStart>2026-06-02T19:00:00</eventStart>
<eventEnd>2026-06-02T23:00:00</eventEnd>
</item>
</channel>
</rss>
`

const benchAtomBody = `<?xml version="1.0" encoding="UTF-8"?>
<feed xmlns="http://www.w3.org/2005/Atom">
<title>Bench Feed</title>
<entry>
<title>Bal de Testville</title>
<link href="https://example.org/events/bal" rel="alternate"/>
<id>https://example.org/events/bal</id>
<summary>Un bal test avec musique live</summary>
<eventStart>2026-06-01T20:00:00</eventStart>
<eventEnd>2026-06-01T22:00:00</eventEnd>
</entry>
<entry>
<title>Atelier Bourrée du Berry</title>
<link href="https://example.org/events/atelier" rel="alternate"/>
<id>https://example.org/events/atelier</id>
<summary>Atelier de bourrée pour débutants</summary>
<eventStart>2026-06-02T19:00:00</eventStart>
<eventEnd>2026-06-02T23:00:00</eventEnd>
</entry>
</feed>
`

func benchGancioJSONBody() []byte {
	events := []gancioEvent{
		{Title: "Bal de Testville", StartDatetime: 1780000000, EndDatetime: 1780007200},
		{Title: "Atelier Bourrée du Berry", StartDatetime: 1780090000, EndDatetime: 1780104000},
	}
	events[0].Place.Name = "Salle des Fêtes Testville"
	events[1].Place.Name = "Salle des Fêtes Testville"
	b, _ := json.Marshal(events)
	return b
}

func reportEventsPerSec(b *testing.B, n int) {
	b.Helper()
	elapsed := b.Elapsed().Seconds()
	if elapsed > 0 {
		b.ReportMetric(float64(b.N*n)/elapsed, "events/sec")
	}
}

func BenchmarkParseBodyToRequests_iCal(b *testing.B) {
	src := FetchSource{Type: "ical", URL: "https://example.org/feed.ics"}
	body := []byte(benchICalBody)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := parseBodyToRequests(body, src, &icalParseReport{}); err != nil {
			b.Fatal(err)
		}
	}
	reportEventsPerSec(b, 2)
}

func BenchmarkParseBodyToRequests_JSON(b *testing.B) {
	src := FetchSource{Type: "json", URL: "https://example.org/api/events"}
	body := benchGancioJSONBody()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := parseBodyToRequests(body, src, &icalParseReport{}); err != nil {
			b.Fatal(err)
		}
	}
	reportEventsPerSec(b, 2)
}

func BenchmarkParseRSSBody_RSS(b *testing.B) {
	src := FetchSource{Type: "rss", URL: "https://example.org/feed.rss"}
	body := []byte(benchRSSBody)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := parseRSSBody(body, src); err != nil {
			b.Fatal(err)
		}
	}
	reportEventsPerSec(b, 2)
}

func BenchmarkParseRSSBody_Atom(b *testing.B) {
	src := FetchSource{Type: "rss", URL: "https://example.org/feed.atom"}
	body := []byte(benchAtomBody)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := parseRSSBody(body, src); err != nil {
			b.Fatal(err)
		}
	}
	reportEventsPerSec(b, 2)
}
