package main

import (
	"log"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

// UserRateLimiter tracks per-user POST request counts within a sliding 1-minute window.
// All endpoint limits are hard: once exceeded, requests are rejected with 429.
// The global limit is a final ceiling across all endpoints for a single user.
type UserRateLimiter struct {
	globalLimit    int
	defaultLimit   int
	endpointLimits map[string]int
	counters       map[int]*userCounter
	mu             sync.Mutex
}

type userCounter struct {
	globalCount    int
	endpointCounts map[string]int
	windowStart    time.Time
}

func newUserRateLimiter(globalLimit int, cfgLimits map[string]int) *UserRateLimiter {
	if globalLimit <= 0 {
		globalLimit = 100
	}
	// Conservative defaults for create/edit actions; operator can override via web.yaml user_rate_limits.
	defaults := map[string]int{
		"admin_events_create":    10,
		"admin_events_edit":      20,
		"admin_orgs_create":      10,
		"admin_orgs_edit":        20,
		"admin_musicians_create":    10,
		"admin_instructors_create": 10,
		"admin_musicians_edit":   20,
		"admin_locations_new":    10,
		"admin_locations_edit":   20,
	}
	lim := make(map[string]int, len(defaults)+len(cfgLimits))
	for k, v := range defaults {
		lim[k] = v
	}
	for k, v := range cfgLimits {
		lim[k] = v // config overrides defaults
	}
	return &UserRateLimiter{
		globalLimit:    globalLimit,
		defaultLimit:   15,
		endpointLimits: lim,
		counters:       make(map[int]*userCounter),
	}
}

// Check reports whether the request should be allowed. Returns false when the
// per-endpoint limit or the global limit for the user is exceeded.
func (rl *UserRateLimiter) Check(userID int, endpoint string) bool {
	rl.mu.Lock()
	defer rl.mu.Unlock()

	now := time.Now()
	c, ok := rl.counters[userID]
	if !ok || now.Sub(c.windowStart) > time.Minute {
		c = &userCounter{
			endpointCounts: make(map[string]int),
			windowStart:    now,
		}
		rl.counters[userID] = c
	}

	if c.globalCount >= rl.globalLimit {
		return false
	}

	limit := rl.defaultLimit
	if v, ok := rl.endpointLimits[endpoint]; ok && v > 0 {
		limit = v
	}
	if c.endpointCounts[endpoint] >= limit {
		return false
	}

	c.globalCount++
	c.endpointCounts[endpoint]++
	return true
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
	"POST /admin/musicians/{id}/image/delete":      "admin_musicians_image_delete",
	"POST /admin/musicians/{id}/avatar/delete":     "admin_musicians_image_delete",
	"POST /admin/organizations/{id}/avatar/delete": "admin_orgs_image_delete",
	"POST /admin/instructors/{id}/avatar/delete":   "admin_musicians_image_delete",
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
	"POST /admin/organizations/{id}/redeliver":    "admin_orgs_run_feeds",
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
	"POST /admin/api/musician/quick-create":       "admin_musicians_create",
	"POST /admin/api/instructor/quick-create":     "admin_instructors_create",
	"POST /admin/musicians/{id}/edit":             "admin_musicians_edit",
	"POST /admin/musicians/{id}/delete":           "admin_musicians_delete",
	"POST /admin/locations/new":                        "admin_locations_new",
	"POST /admin/locations/bulk-assign":                "admin_locations_bulk",
	"POST /admin/locations/{id}/edit":                  "admin_locations_edit",
	"POST /admin/locations/{id}/delete":                "admin_locations_delete",
	"POST /admin/locations/{id}/rooms/new":                        "admin_locations_rooms_new",
	"POST /admin/locations/{id}/rooms/{room_id}/delete":           "admin_locations_rooms_delete",
	"POST /admin/locations/{id}/rooms/{room_id}/quick-edit":       "admin_locations_rooms_quick_edit",
	"PUT /admin/events/{id}/locations/{location_id}":              "admin_events_locations_add",
	"DELETE /admin/events/{id}/locations/{location_id}":           "admin_events_locations_remove",
	"PUT /admin/events/{id}/locations/{location_id}/primary":      "admin_events_locations_primary",
}

var userRateLimiter *UserRateLimiter

// adminAllowedHost is set at startup from cfg.Domain and used by adminRateLimit
// to reject admin POST requests whose Origin/Referer header does not match.
var adminAllowedHost string

// checkAdminOrigin implements Option B of the CSRF analysis (#748): when an
// Origin or Referer header is present, reject unless its host matches
// adminAllowedHost. Both headers absent is allowed — SameSite=Lax already
// covers the primary browser CSRF vector in that case.
func checkAdminOrigin(r *http.Request) bool {
	for _, h := range []string{"Origin", "Referer"} {
		if v := r.Header.Get(h); v != "" {
			u, err := url.Parse(v)
			if err != nil || !strings.EqualFold(u.Hostname(), adminAllowedHost) {
				return false
			}
			return true
		}
	}
	return true
}

func adminRateLimit(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if adminAllowedHost != "" && !checkAdminOrigin(r) {
			log.Printf("dansal-web: CSRF_BLOCK ip=%s origin=%q referer=%q", getClientIP(r), r.Header.Get("Origin"), r.Header.Get("Referer"))
			http.Error(w, "Forbidden", http.StatusForbidden)
			return
		}
		su := getSessionUser(r)
		if su != nil {
			endpoint := routeEndpoint[r.Pattern]
			if endpoint == "" {
				endpoint = r.Pattern
			}
			if !userRateLimiter.Check(su.ID, endpoint) {
				log.Printf("dansal-web: USER_RATE_LIMIT user=%d action=%s ip=%s", su.ID, endpoint, getClientIP(r))
				http.Error(w, "Rate limit exceeded. Please slow down.", http.StatusTooManyRequests)
				return
			}
		}
		next(w, r)
	}
}
