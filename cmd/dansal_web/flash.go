package main

import (
	"context"
	"errors"
	"net/http"
	"net/url"
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
	// ImageUploadError/ImageUploadWidget (#1285) carry a scoped notice for a
	// single image/avatar upload control that failed *after* its owning
	// entity was already saved successfully — the save itself is not in
	// error, so this rides along on the normal success redirect rather than
	// being folded into a whole-page save-error state. ImageUploadError is
	// an i18n key ("image_too_large", "image_invalid_format", or the generic
	// "admin_save_error"); ImageUploadWidget names which control failed
	// ("image", "avatar", "site_plan", …) for pages with more than one.
	ImageUploadError  string
	ImageUploadWidget string
	// PublishedUnusual (#1413) lists events just published whose date is in
	// the past or more than 2 years ahead. Carried on its own ?pubmsg= token
	// (publishFlashRedirect) and rendered by base.html for any page, since
	// the publish actions redirect back to whichever page they came from.
	PublishedUnusual []PublishedUnusualEvent
	expires          time.Time
}

// PublishedUnusualEvent is one entry of FlashMsg.PublishedUnusual; Kind is
// unusualDate's "past" or "far".
type PublishedUnusualEvent struct {
	ID    int
	Title string
	Kind  string
}

// imageUploadErrorKey classifies err (from a DansalClient image/avatar
// upload) into an i18n key, using the API's HTTP status when available
// (#1285): 413 (too large) and 415 (unsupported format) get their own
// specific message; anything else — including a network-level error with no
// apiHTTPError at all — falls back to the generic admin_save_error key.
// Shared by imageUploadErrorFlash (full-page redirect flows) and the
// location site-plan AJAX handler (JSON response flow).
func imageUploadErrorKey(err error) string {
	var ae *apiHTTPError
	if errors.As(err, &ae) {
		switch ae.StatusCode {
		case http.StatusRequestEntityTooLarge:
			return "image_too_large"
		case http.StatusUnsupportedMediaType:
			return "image_invalid_format"
		}
	}
	return "admin_save_error"
}

// imageUploadErrorFlash builds a FlashMsg reporting that the widget-named
// upload control (e.g. "image", "avatar") failed with err — see
// imageUploadErrorKey for the classification.
func imageUploadErrorFlash(widget string, err error) FlashMsg {
	return FlashMsg{ImageUploadError: imageUploadErrorKey(err), ImageUploadWidget: widget}
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
	flashRedirectParam(w, r, path, "msg", tok, msg)
}

func flashRedirectParam(w http.ResponseWriter, r *http.Request, path, param, tok string, msg FlashMsg) {
	msg.expires = time.Now().Add(flashTTL)
	flashMu.Lock()
	flashStore[tok] = msg
	flashMu.Unlock()
	sep := "?"
	if strings.Contains(path, "?") {
		sep = "&"
	}
	http.Redirect(w, r, path+sep+param+"="+tok, http.StatusSeeOther)
}

// publishFlashRedirect (#1413) redirects a publish action back to the page it
// came from (fallback when there's no usable Referer). When any of the just
// published events has an unusual date, the redirect carries a one-time
// ?pubmsg= flash that base.html turns into a warning with edit links. A
// stale pubmsg from an earlier redirect is dropped from the referer first.
func publishFlashRedirect(w http.ResponseWriter, r *http.Request, fallback string, items []PublishedUnusualEvent) {
	dest := safeReferer(r, fallback)
	if u, err := url.Parse(dest); err == nil {
		q := u.Query()
		if q.Has("pubmsg") {
			q.Del("pubmsg")
			u.RawQuery = q.Encode()
			dest = u.String()
		}
	}
	if len(items) == 0 {
		http.Redirect(w, r, dest, http.StatusSeeOther)
		return
	}
	flashRedirectParam(w, r, dest, "pubmsg", newErrorID(), FlashMsg{PublishedUnusual: items})
}

// unusualPublished fetches each event and keeps those whose date is unusual
// (see unusualDate). A failed fetch is skipped — the warning is advisory.
func unusualPublished(ctx context.Context, client *DansalClient, token string, ids []int) []PublishedUnusualEvent {
	var out []PublishedUnusualEvent
	for _, id := range ids {
		ev, err := client.GetEventAuthed(ctx, id, token)
		if err != nil {
			continue
		}
		if kind := unusualDate(ev.StartTime); kind != "" {
			out = append(out, PublishedUnusualEvent{ID: id, Title: ev.Title, Kind: kind})
		}
	}
	return out
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
