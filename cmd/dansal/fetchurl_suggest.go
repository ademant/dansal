package main

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"strings"
	"time"
)

var (
	fetchSuggestPreviewRateLimiter *RateLimiter
	fetchSuggestRateLimiter        *RateLimiter
)

func initFetchSuggestRateLimiters() {
	fetchSuggestPreviewRateLimiter = NewRateLimiter(5, 10*time.Minute)
	fetchSuggestRateLimiter = NewRateLimiter(3, 10*time.Minute)
}

// FetchSuggestLocationMapping is one entry of the submitter's own mapping of a
// unique feed location name to either an existing DB location or a manually
// filled-in new one (#1333 phase 1 — unlike admin_import.go's confirm step,
// there is no authenticated admin driving this, so the anonymous submitter
// resolves locations themselves before the suggestion is even stored).
type FetchSuggestLocationMapping struct {
	FeedName          string                `json:"feed_name"`
	MatchedLocationID *int                  `json:"matched_location_id,omitempty"`
	NewLocation       *EventLocationRequest `json:"new_location,omitempty"`
}

// FetchSuggestRequest is the body of POST /api/v1/fetchurl/suggest. Exactly
// one of OrgID (existing organization) or OrgName (proposing a new one) must
// be set; the org itself is not created until an admin/org-member approves
// the suggestion (phase 2).
type FetchSuggestRequest struct {
	Email            string                        `json:"email"`
	Phone2           string                        `json:"phone2"` // honeypot
	FeedURL          string                        `json:"feed_url"`
	FeedType         string                        `json:"feed_type,omitempty"`
	OrgID            *int                          `json:"org_id,omitempty"`
	OrgName          string                        `json:"org_name,omitempty"`
	OrgActorName     string                        `json:"org_actor_name,omitempty"`
	OrgDescription   string                        `json:"org_description,omitempty"`
	OrgWebsite       string                        `json:"org_website,omitempty"`
	OrgContactEmail  string                        `json:"org_contact_email,omitempty"`
	LocationMappings []FetchSuggestLocationMapping `json:"location_mappings,omitempty"`
}

// normalizeFeedURL resolves webcal(s):// aliases to https:// and validates the
// scheme — the same normalization previewEventsHandler/fetchURL apply before
// fetching, factored out here since both fetchSuggestPreviewHandler and
// fetchSuggestHandler need it independently (preview fetches once; submit
// re-fetches to avoid trusting the client's earlier preview result).
func normalizeFeedURL(rawURL string) (string, error) {
	lowerURL := strings.ToLower(rawURL)
	if strings.HasPrefix(lowerURL, "webcals://") {
		rawURL = "https://" + rawURL[len("webcals://"):]
	} else if strings.HasPrefix(lowerURL, "webcal://") {
		rawURL = "https://" + rawURL[len("webcal://"):]
	}
	u, err := url.Parse(rawURL)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return "", fmt.Errorf("URL must use http or https scheme")
	}
	return rawURL, nil
}

// POST /api/v1/fetchurl/suggest-preview — anonymous dry-run fetch+parse of a
// suggested feed URL, mirroring suggestPreviewHandler (events/suggest-preview)
// but requiring at least one parsed event: an empty feed can never become a
// useful fetch source, so there is no point letting it through to the
// location-mapping step (#1333).
func fetchSuggestPreviewHandler(w http.ResponseWriter, r *http.Request) {
	ip := getClientIP(r)
	if !fetchSuggestPreviewRateLimiter.Allow(ip) {
		http.Error(w, "rate limit exceeded", http.StatusTooManyRequests)
		return
	}
	w.Header().Set("Content-Type", "application/json")

	if err := r.ParseMultipartForm(10 << 20); err != nil {
		writeError(w, "invalid multipart form", http.StatusBadRequest)
		return
	}

	rawURL := strings.TrimSpace(r.FormValue("url"))
	if rawURL == "" {
		writeError(w, "url is required", http.StatusBadRequest)
		return
	}
	normURL, err := normalizeFeedURL(rawURL)
	if err != nil {
		writeError(w, err.Error(), http.StatusBadRequest)
		return
	}
	feedType := r.FormValue("type")
	if feedType == "" {
		feedType = detectFetchType(normURL)
	}
	if !validFetchType(feedType) {
		writeError(w, "unsupported feed type", http.StatusBadRequest)
		return
	}

	body, err := fetchFeedBody(r.Context(), FetchSource{Type: feedType, URL: normURL})
	if err != nil {
		writeError(w, err.Error(), http.StatusBadGateway)
		return
	}

	reqs, err := parseBodyToRequests(body, FetchSource{Type: feedType, URL: normURL})
	if err != nil {
		writeError(w, err.Error(), http.StatusUnprocessableEntity)
		return
	}
	if len(reqs) == 0 {
		writeError(w, "no events found in feed", http.StatusUnprocessableEntity)
		return
	}
	json.NewEncoder(w).Encode(reqs)
}

// POST /api/v1/fetchurl/suggest — submit an anonymous suggestion of a new
// .ics/.json feed to import, pending admin/org-member approval (#1333 phase
// 1). Unlike the preview step, this re-fetches and re-parses the feed itself
// rather than trusting the client's earlier preview result, so a stale or
// tampered submission can't bypass the "at least one event" requirement.
func fetchSuggestHandler(w http.ResponseWriter, r *http.Request) {
	ip := getClientIP(r)
	if !fetchSuggestRateLimiter.Allow(ip) {
		http.Error(w, "rate limit exceeded", http.StatusTooManyRequests)
		return
	}

	body, ok := readBodyOrError(w, r)
	if !ok {
		return
	}

	var req FetchSuggestRequest
	if err := json.Unmarshal(body, &req); err != nil {
		writeError(w, "invalid JSON", http.StatusBadRequest)
		return
	}

	// Honeypot: silently accept without saving.
	if req.Phone2 != "" {
		w.WriteHeader(http.StatusAccepted)
		return
	}

	if req.Email == "" || !isValidEmail(req.Email) {
		writeError(w, "valid email is required", http.StatusBadRequest)
		return
	}
	if looksLikeGmailDotSpam(req.Email) {
		writeError(w, "invalid email address", http.StatusUnprocessableEntity)
		return
	}

	// Mirrors suggestHandler's per-address open-suggestion cap (#1272): bound
	// how many pending feed suggestions one address can have open at once.
	var open int
	db.QueryRow(
		"SELECT COUNT(*) FROM pending_fetch_suggestions WHERE LOWER(email)=LOWER(?) AND status='pending'",
		req.Email,
	).Scan(&open)
	if open >= config.Server.MaxOpenTokensPerAddress {
		writeError(w, "Too many pending feed suggestions for this address. Please wait for existing ones to be reviewed first.", http.StatusTooManyRequests)
		return
	}

	if req.OrgID != nil {
		if strings.TrimSpace(req.OrgName) != "" {
			writeError(w, "choose either an existing organization or a new one, not both", http.StatusBadRequest)
			return
		}
		if !orgExists(db, *req.OrgID) {
			writeError(w, "organization not found", http.StatusBadRequest)
			return
		}
	} else if strings.TrimSpace(req.OrgName) == "" {
		writeError(w, "an organization is required", http.StatusBadRequest)
		return
	}

	rawURL := strings.TrimSpace(req.FeedURL)
	if rawURL == "" {
		writeError(w, "feed_url is required", http.StatusBadRequest)
		return
	}
	normURL, err := normalizeFeedURL(rawURL)
	if err != nil {
		writeError(w, err.Error(), http.StatusBadRequest)
		return
	}
	feedType := req.FeedType
	if feedType == "" {
		feedType = detectFetchType(normURL)
	}
	if !validFetchType(feedType) {
		writeError(w, "unsupported feed type", http.StatusBadRequest)
		return
	}

	feedBody, err := fetchFeedBody(r.Context(), FetchSource{Type: feedType, URL: normURL})
	if err != nil {
		writeError(w, err.Error(), http.StatusBadGateway)
		return
	}
	reqs, err := parseBodyToRequests(feedBody, FetchSource{Type: feedType, URL: normURL})
	if err != nil {
		writeError(w, err.Error(), http.StatusUnprocessableEntity)
		return
	}
	if len(reqs) == 0 {
		writeError(w, "no events found in feed", http.StatusUnprocessableEntity)
		return
	}

	locMapJSON, err := json.Marshal(req.LocationMappings)
	if err != nil {
		writeError(w, "invalid location_mappings", http.StatusBadRequest)
		return
	}

	token, err := generateToken(32)
	if err != nil {
		writeError(w, "internal error", http.StatusInternalServerError)
		return
	}

	var orgIDArg any
	if req.OrgID != nil {
		orgIDArg = *req.OrgID
	}

	if _, err := db.Exec(
		`INSERT INTO pending_fetch_suggestions
		 (token, email, feed_url, feed_type, event_count, org_id, org_name,
		  org_actor_name, org_description, org_website, org_contact_email,
		  location_mappings)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		token, req.Email, normURL, feedType, len(reqs), orgIDArg, req.OrgName,
		req.OrgActorName, req.OrgDescription, req.OrgWebsite, req.OrgContactEmail,
		string(locMapJSON),
	); err != nil {
		writeError(w, "db error: "+err.Error(), http.StatusInternalServerError)
		return
	}

	if smtpEnabled() {
		go func() {
			msg := "Thank you for suggesting a new event feed! Your submission has been received and is now waiting for review by the site's admins" +
				func() string {
					if req.OrgID != nil {
						return " and the organization's members.\n"
					}
					return ".\n"
				}()
			if _, err := SendEmail(req.Email, "Your feed suggestion", msg, false); err != nil {
				log.Printf("fetch-suggest: send email: %v", err)
			}
		}()
	}
	go notifyFetchSuggestion(normURL, req.OrgID)

	writeJSONStatus(w, http.StatusAccepted, map[string]string{"token": token})
}

// notifyFetchSuggestion alerts admins (and, when the submitter picked an
// existing organization, its members) that a new feed suggestion is waiting
// for review — the same reviewer set phase 2's approval endpoint will
// authorize (mirrors notifyReviewersPendingEdit's admin+org-member pattern).
func notifyFetchSuggestion(feedURL string, orgID *int) {
	msg := fmt.Sprintf("A new feed suggestion is waiting for review: %s", feedURL)
	seen := make(map[int]bool)
	notify := func(userID int) {
		if seen[userID] {
			return
		}
		seen[userID] = true
		var email, chatID, matrixID string
		var matrixVerified bool
		db.QueryRow(
			"SELECT COALESCE(email,''), COALESCE(telegram_chat_id,''), COALESCE(matrix,''), COALESCE(matrix_verified,0) FROM users WHERE id=?",
			userID,
		).Scan(&email, &chatID, &matrixID, &matrixVerified)
		notifyUser(chatID, matrixID, matrixVerified, email, "New feed suggestion", msg)
	}
	rows, err := db.Query(`SELECT id FROM users WHERE role = 'admin'`)
	if err == nil {
		for rows.Next() {
			var id int
			if rows.Scan(&id) == nil {
				notify(id)
			}
		}
		rows.Close()
	}
	if orgID != nil {
		rows, err := db.Query(`SELECT user_id FROM organization_members WHERE organization_id = ?`, *orgID)
		if err == nil {
			for rows.Next() {
				var id int
				if rows.Scan(&id) == nil {
					notify(id)
				}
			}
			rows.Close()
		}
	}
}
