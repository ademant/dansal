package main

import (
	"errors"
	"net/http"
	"strings"
	"sync"
	"time"
)

// FlashMsg is a one-time message shown on the next page load, then discarded
// (#985). Fixes the "stale ?board_error= URL" bug: reopening a bookmarked or
// history-restored URL used to re-show the banner (or success message)
// forever, since it was encoded directly in the query string. Now the
// redirect carries only an opaque ?msg=<token>; the payload lives here,
// server-side, keyed by that token, and is deleted the moment it's read.
//
// A single in-memory map is fine — dansal_web runs one process per instance,
// so there's no cross-replica concern, and an unread flash silently vanishing
// on restart is an acceptable trade-off for "no cookies, no JS, no DB table."
type FlashMsg struct {
	BoardPosted       bool
	BoardTelegramURL  string
	BoardContacted    bool
	BoardContactTgURL string
	BoardError        string // i18n key, shown when BoardErrorMsg is empty
	BoardErrorMsg     string // detailed API/web message, when available (#973)
	BoardErrorID      string // error_id to quote when reporting the problem
	BookingOK         bool
	BookingError      string
	BookingErrorMsg   string
	BookingErrorID    string
	ManageUpdated     bool
	ManageDeleted     bool
	expires           time.Time
}

const flashTTL = 5 * time.Minute

var (
	flashMu    sync.Mutex
	flashStore = make(map[string]FlashMsg)
)

// flashToken returns the API's error_id when err carries one (via
// ErrorIDMiddleware on the API side), so a URL a user shares with an admin
// correlates directly with a specific API log line. Otherwise — including
// for success flashes, where err is nil — it mints a fresh local token from
// the same generator used by the web's own error responses.
func flashToken(err error) string {
	var ae *apiHTTPError
	if errors.As(err, &ae) && ae.ErrorID != "" {
		return ae.ErrorID
	}
	return newErrorID()
}

// flashRedirect stores msg under tok and redirects to path with ?msg=tok
// appended (path may already carry its own query string).
func flashRedirect(w http.ResponseWriter, r *http.Request, path, tok string, msg FlashMsg) {
	msg.expires = time.Now().Add(flashTTL)
	flashMu.Lock()
	flashStore[tok] = msg
	flashMu.Unlock()
	sep := "?"
	if strings.Contains(path, "?") {
		sep = "&"
	}
	http.Redirect(w, r, path+sep+"msg="+tok, http.StatusSeeOther)
}

// flashTake retrieves and deletes the flash for tok — a one-time read, so
// reopening the same URL later renders a clean page instead of the stale
// banner. Returns a zero FlashMsg for a missing, already-read, or expired token.
func flashTake(tok string) FlashMsg {
	if tok == "" {
		return FlashMsg{}
	}
	flashMu.Lock()
	msg, ok := flashStore[tok]
	if ok {
		delete(flashStore, tok)
	}
	flashMu.Unlock()
	if !ok || time.Now().After(msg.expires) {
		return FlashMsg{}
	}
	return msg
}

// startFlashSweeper periodically evicts stale, never-read flashes so the map
// doesn't grow unbounded from links that get abandoned, crawled, or shared
// without ever being reopened.
func startFlashSweeper() {
	go func() {
		t := time.NewTicker(time.Minute)
		for range t.C {
			now := time.Now()
			flashMu.Lock()
			for k, v := range flashStore {
				if now.After(v.expires) {
					delete(flashStore, k)
				}
			}
			flashMu.Unlock()
		}
	}()
}
