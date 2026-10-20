package main

import (
	"database/sql"
	"encoding/json"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// Server-side watchlist endpoints (#1554). Writes need a form token from GET
// /watchlist/form-token and pass the shared anti-bot guard (honeypot and
// one-time token), like the other public forms. Reads need only the key.
//
//	GET  /watchlist/form-token   → {"form_token": "…"}
//	POST /watchlist/save         form-encoded: _form_token, dansal_phone2 (honeypot),
//	                             uuid, key (empty = create), ids (comma-separated)
//	                             create → {"key": "…"}; update → {"ok": true}
//	GET  /watchlist/load         header X-Watchlist-Key → {"ids": […]}

var (
	// Creating a key is the expensive write: 5 per hour per IP (#1554).
	watchlistCreateThrottle = newSubmissionThrottle(5, time.Hour)
	// Loads are cheap, but a key is 128-bit random, so the limit only slows
	// anyone trying keys.
	watchlistLoadThrottle = newSubmissionThrottle(30, time.Minute)
)

var (
	watchlistUUIDRe = regexp.MustCompile(`^[A-Za-z0-9-]{16,64}$`)
	watchlistKeyRe  = regexp.MustCompile(`^[0-9a-f]{32}$`)
)

// parseWatchlistIDs reads a comma-separated id list. An empty string is an
// empty list (clearing the server copy).
func parseWatchlistIDs(raw string) ([]int, error) {
	out := []int{}
	if strings.TrimSpace(raw) == "" {
		return out, nil
	}
	for _, part := range strings.Split(raw, ",") {
		n, err := strconv.Atoi(strings.TrimSpace(part))
		if err != nil {
			return nil, errWatchlistBadID
		}
		out = append(out, n)
	}
	return out, nil
}

// watchlistFormTokenHandler issues a one-time form token for the next save.
func watchlistFormTokenHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ip := getClientIP(r)
		if tokenThrottle.isBlocked(ip) {
			writeJSONError(w, r, http.StatusTooManyRequests, "rate limit exceeded")
			return
		}
		tokenThrottle.record(ip)
		tok := issueFormToken(ip)
		if tok == "" {
			writeJSONError(w, r, http.StatusServiceUnavailable, "unavailable")
			return
		}
		w.Header().Set("Cache-Control", "no-store")
		writeJSONResponse(w, http.StatusOK, map[string]string{"form_token": tok})
	}
}

// watchlistSaveHandler creates a server copy of a watchlist (no key) or
// replaces the ids of an existing one (with its key).
func watchlistSaveHandler(cfg *Config, db *sql.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ip := getClientIP(r)
		r.Body = http.MaxBytesReader(w, r.Body, 8192)

		switch guardFormSubmit(w, r, cfg, ip) {
		case formGuardOK:
		case formGuardHoneypot:
			// Same answer as a real save, so a bot can't tell it was caught;
			// nothing is stored.
			w.Header().Set("Cache-Control", "no-store")
			if r.FormValue("key") != "" {
				writeJSONResponse(w, http.StatusOK, map[string]bool{"ok": true})
				return
			}
			fake, err := newWatchlistKey()
			if err != nil {
				writeJSONError(w, r, http.StatusInternalServerError, "internal error")
				return
			}
			writeJSONResponse(w, http.StatusOK, map[string]string{"key": fake})
			return
		default:
			writeJSONError(w, r, http.StatusForbidden, "form expired; reload and try again")
			return
		}

		uuid := r.FormValue("uuid")
		if !watchlistUUIDRe.MatchString(uuid) {
			writeJSONError(w, r, http.StatusBadRequest, "invalid browser id")
			return
		}
		raw, err := parseWatchlistIDs(r.FormValue("ids"))
		if err != nil {
			writeJSONError(w, r, http.StatusBadRequest, err.Error())
			return
		}
		ids, err := cleanWatchlistIDs(raw)
		if err != nil {
			writeJSONError(w, r, http.StatusBadRequest, err.Error())
			return
		}
		idsJSON, err := json.Marshal(ids)
		if err != nil {
			writeJSONError(w, r, http.StatusInternalServerError, "internal error")
			return
		}
		w.Header().Set("Cache-Control", "no-store")
		uuidHash := hashWatchlistToken(uuid)

		key := r.FormValue("key")
		if key == "" {
			if watchlistCreateThrottle.isBlocked(ip) {
				writeJSONError(w, r, http.StatusTooManyRequests, "too many new keys; try again later")
				return
			}
			watchlistCreateThrottle.record(ip)
			newKey, err := newWatchlistKey()
			if err != nil {
				writeJSONError(w, r, http.StatusInternalServerError, "internal error")
				return
			}
			// One list per UUID: this replaces any earlier key for it.
			if err := saveWatchlistForUUID(db, uuidHash, hashWatchlistToken(newKey), string(idsJSON)); err != nil {
				writeJSONError(w, r, http.StatusInternalServerError, "internal error")
				return
			}
			writeJSONResponse(w, http.StatusOK, map[string]string{"key": newKey})
			return
		}

		if !watchlistKeyRe.MatchString(key) {
			writeJSONError(w, r, http.StatusNotFound, "no watchlist for this key")
			return
		}
		ok, err := updateWatchlistByKey(db, uuidHash, hashWatchlistToken(key), string(idsJSON))
		if err != nil {
			writeJSONError(w, r, http.StatusInternalServerError, "internal error")
			return
		}
		if !ok {
			writeJSONError(w, r, http.StatusNotFound, "no watchlist for this key")
			return
		}
		writeJSONResponse(w, http.StatusOK, map[string]bool{"ok": true})
	}
}

// watchlistLoadHandler returns the ids stored under a key.
func watchlistLoadHandler(db *sql.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ip := getClientIP(r)
		if watchlistLoadThrottle.isBlocked(ip) {
			writeJSONError(w, r, http.StatusTooManyRequests, "rate limit exceeded")
			return
		}
		watchlistLoadThrottle.record(ip)

		key := r.Header.Get("X-Watchlist-Key")
		if !watchlistKeyRe.MatchString(key) {
			writeJSONError(w, r, http.StatusNotFound, "no watchlist for this key")
			return
		}
		idsJSON, ok := loadWatchlistByKey(db, hashWatchlistToken(key))
		if !ok {
			writeJSONError(w, r, http.StatusNotFound, "no watchlist for this key")
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Cache-Control", "no-store")
		w.Write([]byte(`{"ids":` + idsJSON + `}`))
	}
}
