package main

import (
	"context"
	"testing"
	"time"
)

// stubFetchImport swaps runFetchImport for the test and restores it afterwards.
func stubFetchImport(t *testing.T, fn func(ctx context.Context, src FetchSource) ([]Event, ImportCounts, error)) {
	t.Helper()
	orig := runFetchImport
	runFetchImport = fn
	t.Cleanup(func() { runFetchImport = orig })
}

// waitFetchJob polls the registry until the job leaves the running state.
func waitFetchJob(t *testing.T, reg *fetchJobRegistry, id int) fetchJob {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		j, ok := reg.get(id)
		if !ok {
			t.Fatalf("job %d not found", id)
		}
		if j.State != "running" {
			return j
		}
		if time.Now().After(deadline) {
			t.Fatalf("job %d still running after 5s", id)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func newTestFetchJobRegistry() *fetchJobRegistry {
	return &fetchJobRegistry{jobs: map[int]*fetchJob{}, running: map[int]int{}}
}

// TestFetchJobDedupesRunningSource covers #1709: a second request for a source
// while its run is still going gets the running job, not a second import, and
// a new run can start once the first has finished.
func TestFetchJobDedupesRunningSource(t *testing.T) {
	setupDedupTestDB(t)
	release := make(chan struct{})
	calls := 0
	stubFetchImport(t, func(ctx context.Context, src FetchSource) ([]Event, ImportCounts, error) {
		calls++
		<-release
		return []Event{{ID: 7}}, ImportCounts{New: 1}, nil
	})

	reg := newTestFetchJobRegistry()
	src := FetchSource{ID: 3, URL: "https://feed.example.test/", Type: "ical"}
	first, started := reg.start(src, 1)
	if !started {
		t.Fatal("first start should begin a job")
	}
	second, started := reg.start(src, 2)
	if started || second.JobID != first.JobID {
		t.Fatalf("second start = job %d started=%v, want running job %d", second.JobID, started, first.JobID)
	}

	close(release)
	done := waitFetchJob(t, reg, first.JobID)
	if done.State != "ok" || done.Result == nil || done.Result.New != 1 || len(done.Result.Events) != 1 {
		t.Fatalf("finished job = %+v, want ok with one new event", done)
	}
	if calls != 1 {
		t.Errorf("import ran %d times, want 1", calls)
	}

	third, started := reg.start(src, 1)
	if !started || third.JobID == first.JobID {
		t.Fatalf("start after finish = job %d started=%v, want a new job", third.JobID, started)
	}
	waitFetchJob(t, reg, third.JobID)
}

// TestFetchJobRobotsIsNotAFailure covers #1709 with #1545: a feed refused for
// robots.txt reports the robots error code.
func TestFetchJobRobotsIsNotAFailure(t *testing.T) {
	setupDedupTestDB(t)
	stubFetchImport(t, func(ctx context.Context, src FetchSource) ([]Event, ImportCounts, error) {
		return nil, ImportCounts{}, errRobotsNotAcknowledged
	})
	reg := newTestFetchJobRegistry()
	job, _ := reg.start(FetchSource{ID: 4, URL: "https://feed.example.test/", Type: "ical"}, 1)
	done := waitFetchJob(t, reg, job.JobID)
	if done.State != "error" || done.ErrorCode != "robots_not_acknowledged" {
		t.Fatalf("robots job = state %q code %q, want error with robots_not_acknowledged", done.State, done.ErrorCode)
	}
}

// TestFetchJobExpiresAfterRetention covers #1709: finished jobs are kept for a
// while, then dropped.
func TestFetchJobExpiresAfterRetention(t *testing.T) {
	reg := newTestFetchJobRegistry()
	finished := time.Now().Add(-fetchJobRetention - time.Minute)
	reg.jobs[9] = &fetchJob{JobID: 9, SourceID: 1, State: "ok", FinishedAt: &finished}
	if _, ok := reg.get(9); ok {
		t.Fatal("job finished longer ago than the retention window should be dropped")
	}
}
