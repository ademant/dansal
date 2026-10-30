package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"sync"
	"time"

	"github.com/ademant/dansal/internal/logging"
)

// A feed run is a background job (#1709). POST /api/v1/feeds/{id}/fetch starts
// the import in a goroutine and answers 202 with the job id straight away; the
// job runs on its own context, so a cancelled HTTP request (the web's timeout)
// no longer cancels the import. Clients poll GET /api/v1/feeds/{id}/fetch/{job}.

// fetchJobRetention is how long a finished job stays readable. A restart drops
// all jobs; the fetch can simply be repeated.
const fetchJobRetention = 10 * time.Minute

// fetchJobTimeout is a hard cap on one run, so a wedged import cannot hold its
// source's slot forever.
const fetchJobTimeout = 30 * time.Minute

// fetchJob is one run of a feed source. State is "running", "ok" or "error".
type fetchJob struct {
	JobID      int             `json:"job_id"`
	SourceID   int             `json:"source_id"`
	State      string          `json:"state"`
	StartedAt  time.Time       `json:"started_at"`
	FinishedAt *time.Time      `json:"finished_at,omitempty"`
	Result     *fetchJobResult `json:"result,omitempty"`
	Error      string          `json:"error,omitempty"`
	// ErrorCode is a stable value for errors a client reacts to, such as
	// robots_not_acknowledged (the same code the synchronous endpoint returned).
	ErrorCode string `json:"error_code,omitempty"`

	callerID int
}

// fetchJobResult is what an "ok" run produced. The events and counts match the
// body the synchronous endpoint used to return.
type fetchJobResult struct {
	Events           []Event `json:"events"`
	New              int     `json:"new"`
	Updated          int     `json:"updated"`
	Unchanged        int     `json:"unchanged"`
	Failed           int     `json:"failed"`
	TimezoneFallback int     `json:"timezone_fallback,omitempty"`
	Unparsed         int     `json:"unparsed,omitempty"`
}

type fetchJobRegistry struct {
	mu      sync.Mutex
	nextID  int
	jobs    map[int]*fetchJob
	running map[int]int // source id -> id of its running job
}

var fetchJobs = &fetchJobRegistry{jobs: map[int]*fetchJob{}, running: map[int]int{}}

// start returns the source's running job when there is one, and otherwise
// starts a new job. started reports whether this call started it, so a second
// request during a run gets the existing job rather than a duplicate import.
func (reg *fetchJobRegistry) start(src FetchSource, callerID int) (job fetchJob, started bool) {
	reg.mu.Lock()
	defer reg.mu.Unlock()
	reg.pruneLocked(time.Now())
	if id, ok := reg.running[src.ID]; ok {
		logging.Debugf("fetch job: source=%d already running as id=%d", src.ID, id)
		return *reg.jobs[id], false
	}
	reg.nextID++
	j := &fetchJob{JobID: reg.nextID, SourceID: src.ID, State: "running", StartedAt: time.Now(), callerID: callerID}
	reg.jobs[j.JobID] = j
	reg.running[src.ID] = j.JobID
	logging.Debugf("fetch job: id=%d source=%d state=running", j.JobID, src.ID)
	go reg.execute(j.JobID, src)
	return *j, true
}

// get returns a copy of the job, or false when it is unknown or has expired.
func (reg *fetchJobRegistry) get(jobID int) (fetchJob, bool) {
	reg.mu.Lock()
	defer reg.mu.Unlock()
	now := time.Now()
	reg.pruneLocked(now)
	j, ok := reg.jobs[jobID]
	if !ok {
		return fetchJob{}, false
	}
	if j.State == "running" {
		logging.Debugf("fetch job: poll id=%d source=%d age_ms=%d", j.JobID, j.SourceID, now.Sub(j.StartedAt).Milliseconds())
	}
	return *j, true
}

// pruneLocked drops finished jobs older than fetchJobRetention. Callers hold mu.
func (reg *fetchJobRegistry) pruneLocked(now time.Time) {
	for id, j := range reg.jobs {
		if j.FinishedAt != nil && now.Sub(*j.FinishedAt) > fetchJobRetention {
			delete(reg.jobs, id)
		}
	}
}

// runFetchImport performs the import for a job. Tests replace it to control
// timing and outcome without touching the network.
var runFetchImport = importFromSource

// execute runs the import for one job. It is the import that used to run inside
// the request, so the source bookkeeping and log lines are the same.
func (reg *fetchJobRegistry) execute(jobID int, src FetchSource) {
	ctx, cancel := context.WithTimeout(context.Background(), fetchJobTimeout)
	defer cancel()
	started := time.Now()

	reg.mu.Lock()
	callerID := reg.jobs[jobID].callerID
	reg.mu.Unlock()

	events, counts, err := runFetchImport(ctx, src)

	// errRobotsNotAcknowledged is not a fetch failure: the feed is refused until
	// someone accepts robots.txt (#1545), so it is not recorded as one.
	state, errText, errCode := "ok", "", ""
	switch {
	case errors.Is(err, errRobotsNotAcknowledged):
		state, errText, errCode = "error", err.Error(), "robots_not_acknowledged"
	case err != nil:
		recordFetchResult(src, 0, err)
		logging.Errorf("fetchurl: source_id=%d url=%q type=%s result=error caller=%d err=%v", src.ID, src.URL, src.Type, callerID, err)
		state, errText = "error", err.Error()
	default:
		recordFetchResult(src, len(events), nil)
		logging.Infof("fetchurl: source_id=%d url=%q type=%s result=ok events=%d caller=%d", src.ID, src.URL, src.Type, len(events), callerID)
	}

	reg.mu.Lock()
	defer reg.mu.Unlock()
	j := reg.jobs[jobID]
	finished := time.Now()
	j.FinishedAt = &finished
	j.State, j.Error, j.ErrorCode = state, errText, errCode
	if state == "ok" {
		j.Result = &fetchJobResult{Events: events, New: counts.New, Updated: counts.Updated,
			Unchanged: counts.Unchanged, Failed: counts.Failed,
			TimezoneFallback: counts.TimezoneFallback, Unparsed: counts.Unparsed}
	}
	delete(reg.running, src.ID)
	logging.Debugf("fetch job: id=%d source=%d state=%s duration_ms=%d", jobID, src.ID, state, time.Since(started).Milliseconds())
}

// fetchSourceForRun checks the caller may run or inspect a feed source and
// returns it. It writes the error response and returns false when it may not.
func fetchSourceForRun(w http.ResponseWriter, r *http.Request) (FetchSource, int, bool) {
	callerID, callerRole := callerFromRequest(r)
	if callerRole == RolePublisher {
		writeError(w, "Forbidden: publishers may not trigger fetch sources", http.StatusForbidden)
		return FetchSource{}, 0, false
	}
	if callerRole != RoleAdmin && callerRole != RoleUser {
		writeError(w, "Forbidden", http.StatusForbidden)
		return FetchSource{}, 0, false
	}

	src, err := scanFetchSource(db.QueryRow(
		"SELECT "+fetchSourceCols+" FROM fetch_sources WHERE id = ?", r.PathValue("id"),
	))
	if !rowOr404(w, err, "Fetch source not found") {
		return FetchSource{}, 0, false
	}
	if callerRole != RoleAdmin {
		if !requireExistingOrgMember(w, callerID, nullIntFromPtr(src.OrganizationID)) {
			return FetchSource{}, 0, false
		}
	}
	if !validFetchType(src.Type) {
		writeError(w, "Unsupported type; use 'ical', 'rss', or 'folkdance-json'", http.StatusBadRequest)
		return FetchSource{}, 0, false
	}
	return src, callerID, true
}

// POST /api/v1/feeds/{id}/fetch
//
// Starts the run as a background job and answers 202 with the job. Poll
// GET /api/v1/feeds/{id}/fetch/{job} for the result (#1709).
func fetchURLByID(w http.ResponseWriter, r *http.Request) {
	src, callerID, ok := fetchSourceForRun(w, r)
	if !ok {
		return
	}
	job, _ := fetchJobs.start(src, callerID)
	w.Header().Set("Location", fmt.Sprintf("/api/v1/feeds/%d/fetch/%d", src.ID, job.JobID))
	writeJSONStatus(w, http.StatusAccepted, job)
}

// GET /api/v1/feeds/{id}/fetch/{job}
func getFetchJob(w http.ResponseWriter, r *http.Request) {
	src, _, ok := fetchSourceForRun(w, r)
	if !ok {
		return
	}
	jobID, err := strconv.Atoi(r.PathValue("job"))
	if err != nil {
		writeError(w, "Fetch job not found", http.StatusNotFound)
		return
	}
	job, found := fetchJobs.get(jobID)
	if !found || job.SourceID != src.ID {
		writeError(w, "Fetch job not found", http.StatusNotFound)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(job)
}
