package main

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"
)

// TestConcurrentImportsDoNotHitLockContention (#1561) runs several feed imports
// at once against one SQLite database, as adminFetchAll does, and fails on any
// "database is locked" error. Imports used to queue on SQLite's write lock for
// their whole transaction and give up after _busy_timeout, so most of a run
// failed.
func TestConcurrentImportsDoNotHitLockContention(t *testing.T) {
	setupDedupTestDB(t)
	const sources = 8
	const perSource = 25
	base := time.Date(2030, 1, 1, 19, 0, 0, 0, time.UTC).Unix()

	finalize := func(tx querier, req EventCreateRequest, td *templateImportData, counts *ImportCounts, allEvents *[]Event) error {
		_, err := importSingleEvent(tx, req, td, "", counts, allEvents)
		return err
	}

	var wg sync.WaitGroup
	errs := make(chan error, sources)
	for s := 0; s < sources; s++ {
		wg.Add(1)
		go func(s int) {
			defer wg.Done()
			src := FetchSource{ID: s + 1, URL: fmt.Sprintf("https://feed%d.example.test/", s)}
			reqs := make([]EventCreateRequest, perSource)
			for i := range reqs {
				start := base + int64(s*perSource+i)*7200
				reqs[i] = EventCreateRequest{
					EventWriteRequest: EventWriteRequest{
						Title:     fmt.Sprintf("Feed %d event %d", s, i),
						URL:       fmt.Sprintf("https://feed%d.example.test/e/%d", s, i),
						StartTime: time.Unix(start, 0).UTC().Format(time.RFC3339),
						EndTime:   time.Unix(start+3*3600, 0).UTC().Format(time.RFC3339),
					},
					UID: fmt.Sprintf("s%d-e%d", s, i),
				}
			}
			_, _, err := importEntries(context.Background(), src, reqs,
				func(r EventCreateRequest) EventCreateRequest { return r }, finalize)
			if err != nil {
				errs <- fmt.Errorf("source %d: %w", s, err)
			}
		}(s)
	}
	wg.Wait()
	close(errs)

	for err := range errs {
		if strings.Contains(err.Error(), "is locked") {
			t.Errorf("lock contention during concurrent import: %v", err)
		} else {
			t.Errorf("import failed: %v", err)
		}
	}

	var inserted int
	if err := db.QueryRow("SELECT COUNT(*) FROM events WHERE title LIKE 'Feed % event %'").Scan(&inserted); err != nil {
		t.Fatal(err)
	}
	if want := sources * perSource; inserted != want {
		t.Errorf("inserted %d events, want %d", inserted, want)
	}
}
