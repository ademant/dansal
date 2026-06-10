package main

import (
	"log"
	"net/http"
	"sync"
	"time"
)

type userCounter struct {
	globalCount    int
	endpointCounts map[string]int
	lastRequest    map[string]time.Time
	windowStart    time.Time
}

// throttleDelay is the maximum delay applied per request once a user exceeds
// an endpoint's soft limit within the current window.
const throttleDelay = 4 * time.Second

// UserRateLimiter tracks per-user POST request counts within a sliding 1-minute window.
//
// Two limits apply:
//   - a per-endpoint "soft" limit (defaultLimit / endpointLimits): once exceeded,
//     requests are not rejected but delayed by up to throttleDelay, so rapid
//     sequential actions (e.g. "Save & Next") slow down instead of erroring.
//   - a global hard limit (globalLimit): once exceeded, requests are rejected
//     with 429 as a last-resort abuse ceiling.
type UserRateLimiter struct {
	globalLimit    int
	defaultLimit   int
	endpointLimits map[string]int
	counters       map[int]*userCounter
	mu             sync.Mutex
}

func newUserRateLimiter(globalLimit int, endpointLimits map[string]int) *UserRateLimiter {
	if globalLimit <= 0 {
		globalLimit = 100
	}
	lim := make(map[string]int, len(endpointLimits))
	for k, v := range endpointLimits {
		lim[k] = v
	}
	return &UserRateLimiter{
		globalLimit:    globalLimit,
		defaultLimit:   15,
		endpointLimits: lim,
		counters:       make(map[int]*userCounter),
	}
}

// Check reports whether the request is allowed at all (false if the hard
// global limit is exceeded), and how long the caller should sleep before
// proceeding to enforce the soft per-endpoint limit.
func (rl *UserRateLimiter) Check(userID int, endpoint string) (allowed bool, delay time.Duration) {
	rl.mu.Lock()
	defer rl.mu.Unlock()

	now := time.Now()
	c, ok := rl.counters[userID]
	if !ok || now.Sub(c.windowStart) > time.Minute {
		c = &userCounter{
			endpointCounts: make(map[string]int),
			lastRequest:    make(map[string]time.Time),
			windowStart:    now,
		}
		rl.counters[userID] = c
	}

	if c.globalCount >= rl.globalLimit {
		return false, 0
	}
	c.globalCount++
	c.endpointCounts[endpoint]++

	limit := rl.defaultLimit
	if v, ok := rl.endpointLimits[endpoint]; ok && v > 0 {
		limit = v
	}

	if c.endpointCounts[endpoint] > limit {
		if since := now.Sub(c.lastRequest[endpoint]); since < throttleDelay {
			delay = throttleDelay - since
		}
	}
	c.lastRequest[endpoint] = now
	return true, delay
}

func (rl *UserRateLimiter) cleanup() {
	rl.mu.Lock()
	defer rl.mu.Unlock()
	cutoff := time.Now().Add(-2 * time.Minute)
	for id, c := range rl.counters {
		if c.windowStart.Before(cutoff) {
			delete(rl.counters, id)
		}
	}
}

func (rl *UserRateLimiter) startCleanup(interval time.Duration) {
	go func() {
		t := time.NewTicker(interval)
		for range t.C {
			rl.cleanup()
		}
	}()
}

// routeEndpoint maps ServeMux patterns to endpoint names used in user_rate_limits config.
var routeEndpoint = map[string]string{
	"POST /admin/registrations/{id}/approve":      "admin_registrations_approve",
	"POST /admin/registrations/{id}/reject":       "admin_registrations_reject",
	"POST /admin/users/bulk":                      "admin_users_bulk",
	"POST /admin/users/{id}/delete":               "admin_users_delete",
	"POST /admin/users/{id}/role":                 "admin_users_role",
	"POST /admin/users/{id}/org":                  "admin_users_org",
	"POST /admin/invites/new":                     "admin_invites_new",
	"POST /admin/invites/{token}/revoke":          "admin_invites_revoke",
	"POST /admin/bookings/{id}/approve":           "admin_bookings_approve",
	"POST /admin/bookings/{id}/cancel":            "admin_bookings_cancel",
	"POST /admin/bookings/{id}/delete":            "admin_bookings_delete",
	"POST /admin/events/{id}/publish":             "admin_events_publish",
	"POST /admin/events/{id}/cancel":              "admin_events_cancel",
	"POST /admin/events/{id}/delete":              "admin_events_delete",
	"POST /admin/events/{id}/image/delete":        "admin_events_image_delete",
	"POST /admin/musicians/{id}/image/delete":     "admin_musicians_image_delete",
	"POST /admin/organizations/{id}/image/delete": "admin_orgs_image_delete",
	"POST /admin/events/import":                   "admin_import_events",
	"POST /admin/events/import/confirm":           "admin_import_confirm",
	"POST /admin/events/new":                      "admin_events_create",
	"POST /admin/events/{id}/edit":                "admin_events_edit",
	"POST /admin/events/{id}/save-template":       "admin_events_save_template",
	"POST /admin/templates/{id}/delete":           "admin_templates_delete",
	"POST /admin/organizations/new":               "admin_orgs_create",
	"POST /admin/organizations/{id}/edit":         "admin_orgs_edit",
	"POST /admin/organizations/{id}/delete":       "admin_orgs_delete",
	"POST /admin/organizations/{id}/run-feeds":    "admin_orgs_run_feeds",
	"POST /admin/organizations/{id}/members":      "admin_orgs_members",
	"POST /admin/organizations/{id}/locations":    "admin_orgs_locations",
	"POST /admin/organizations/{id}/follow":       "admin_orgs_follow",
	"POST /admin/organizations/{id}/unfollow":     "admin_orgs_unfollow",
	"POST /admin/dances":                          "admin_dances_create",
	"POST /admin/dances/{id}/delete":              "admin_dances_delete",
	"POST /admin/site-config":                     "admin_site_config_save",
	"POST /admin/site-config/matrix-login":        "admin_site_config_matrix_login",
	"POST /admin/fetchurls/new":                   "admin_fetchurls_new",
	"POST /admin/fetchurls/bulk":                  "admin_fetchurls_bulk",
	"POST /admin/fetchurls/{id}/edit":             "admin_fetchurls_edit",
	"POST /admin/fetchurls/{id}/delete":           "admin_fetchurls_delete",
	"POST /admin/fetchurls/{id}/run":              "admin_fetchurls_run",
	"POST /admin/musicians/new":                   "admin_musicians_create",
	"POST /admin/musicians/{id}/edit":             "admin_musicians_edit",
	"POST /admin/musicians/{id}/delete":           "admin_musicians_delete",
	"POST /admin/locations/new":                   "admin_locations_new",
	"POST /admin/locations/bulk-assign":           "admin_locations_bulk",
	"POST /admin/locations/{id}/edit":             "admin_locations_edit",
	"POST /admin/locations/{id}/delete":           "admin_locations_delete",
}

var userRateLimiter *UserRateLimiter

func adminRateLimit(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		su := getSessionUser(r)
		if su != nil {
			endpoint := routeEndpoint[r.Pattern]
			if endpoint == "" {
				endpoint = r.Pattern
			}
			allowed, delay := userRateLimiter.Check(su.ID, endpoint)
			if !allowed {
				log.Printf("dansal-web: USER_RATE_LIMIT user=%d action=%s ip=%s", su.ID, endpoint, getClientIP(r))
				http.Error(w, "Rate limit exceeded. Please slow down.", http.StatusTooManyRequests)
				return
			}
			if delay > 0 {
				time.Sleep(delay)
			}
		}
		next(w, r)
	}
}
