package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math/rand"
	"mime/multipart"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

// cacheEntry holds a cached value and when it was fetched.
type cacheEntry[T any] struct {
	val       T
	fetchedAt time.Time
}

const (
	orgsTTL      = 60 * time.Second
	dancesTTL    = 5 * time.Minute
	tagsTTL      = 5 * time.Minute
	musiciansTTL = 30 * time.Second
	locationsTTL = 30 * time.Second
	eventsTTL    = 30 * time.Second
)

type DansalClient struct {
	BaseURL string
	HTTP    *http.Client

	mu             sync.Mutex
	orgsCache      cacheEntry[[]Organization]
	dancesCache    cacheEntry[[]Dance]
	tagsCache      cacheEntry[[]Tag]
	musiciansCache cacheEntry[[]Musician]
	locationsCache cacheEntry[[]Location]
	eventsCache    cacheEntry[[]Event]
}

// cached fetches from cache when fresh; otherwise calls fetch and stores the result.
// On fetch error, returns the previous stale value if one exists so a transient
// API outage does not blank pages that were working moments before.
// Concurrent cache misses may trigger multiple fetches (thundering-herd at small scale is fine).
func cached[T any](mu *sync.Mutex, entry *cacheEntry[T], ttl time.Duration, fetch func() (T, error)) (T, error) {
	mu.Lock()
	if !entry.fetchedAt.IsZero() && time.Since(entry.fetchedAt) < ttl {
		v := entry.val
		mu.Unlock()
		return v, nil
	}
	// Snapshot stale value before releasing the lock so we can fall back to it.
	hasStale := !entry.fetchedAt.IsZero()
	var stale T
	if hasStale {
		stale = entry.val
	}
	mu.Unlock()
	val, err := fetch()
	if err != nil {
		if hasStale {
			return stale, nil // serve stale rather than an error; next request retries
		}
		var zero T
		return zero, err
	}
	mu.Lock()
	*entry = cacheEntry[T]{val: val, fetchedAt: time.Now()}
	mu.Unlock()
	return val, nil
}

func (c *DansalClient) invalidateOrgs() {
	c.mu.Lock()
	c.orgsCache.fetchedAt = time.Time{}
	c.mu.Unlock()
}

func (c *DansalClient) invalidateMusicians() {
	c.mu.Lock()
	c.musiciansCache.fetchedAt = time.Time{}
	c.mu.Unlock()
}

func (c *DansalClient) invalidateDances() {
	c.mu.Lock()
	c.dancesCache.fetchedAt = time.Time{}
	c.mu.Unlock()
}

func (c *DansalClient) invalidateLocations() {
	c.mu.Lock()
	c.locationsCache.fetchedAt = time.Time{}
	c.mu.Unlock()
}

func (c *DansalClient) invalidateEvents() {
	c.mu.Lock()
	c.eventsCache.fetchedAt = time.Time{}
	c.mu.Unlock()
}

// OrgStatRecord is returned by GetOrgStats: per-org event/location/source counts.
type OrgStatRecord struct {
	ID            int `json:"id"`
	EventCount    int `json:"event_count"`
	LocationCount int `json:"location_count"`
	SourceCount   int `json:"source_count"`
}

// GetOrgStats returns a map of org ID → counts, fetched via a single aggregation query.
func (c *DansalClient) GetOrgStats(ctx context.Context) (map[int]OrgStatRecord, error) {
	var records []OrgStatRecord
	if err := c.get(ctx, "/api/v1/organizations/stats", &records); err != nil {
		return nil, err
	}
	m := make(map[int]OrgStatRecord, len(records))
	for _, r := range records {
		m[r.ID] = r
	}
	return m, nil
}

// RefBundle holds all four slow-changing reference lists needed by admin forms.
type RefBundle struct {
	Orgs      []Organization
	Locations []Location
	Musicians []Musician
	Dances    []Dance
}

// FetchRefBundle loads all four reference lists in parallel (cache hits are in-memory).
func (c *DansalClient) FetchRefBundle(ctx context.Context) RefBundle {
	var b RefBundle
	var wg sync.WaitGroup
	wg.Add(4)
	go func() { defer wg.Done(); b.Orgs, _ = c.GetOrganizations(ctx) }()
	go func() { defer wg.Done(); b.Locations, _ = c.GetLocations(ctx) }()
	go func() { defer wg.Done(); b.Musicians, _ = c.GetMusicians(ctx) }()
	go func() { defer wg.Done(); b.Dances, _ = c.GetDances(ctx) }()
	wg.Wait()
	return b
}

func apiErr(resp *http.Response) error {
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	var body struct {
		Error   string `json:"error"`
		ErrorID string `json:"error_id"`
	}
	if json.Unmarshal(b, &body) == nil && body.Error != "" {
		if body.ErrorID != "" {
			return fmt.Errorf("dansal API %d: %s (error_id: %s)", resp.StatusCode, body.Error, body.ErrorID)
		}
		return fmt.Errorf("dansal API %d: %s", resp.StatusCode, body.Error)
	}
	if msg := strings.TrimSpace(string(b)); msg != "" {
		return fmt.Errorf("dansal API %d: %s", resp.StatusCode, msg)
	}
	return fmt.Errorf("dansal API: %s", resp.Status)
}

type Event struct {
	ID                 int              `json:"id"`
	Title              string           `json:"title"`
	Description        string           `json:"description"`
	StartTime          string           `json:"start_time"`
	EndTime            string           `json:"end_time"`
	HasBall            bool             `json:"has_ball"`
	HasWorkshop        bool             `json:"has_workshop"`
	HasFestival        bool             `json:"has_festival"`
	WorkshopDifficulty string           `json:"workshop_difficulty,omitempty"`
	IsCancelled        bool             `json:"is_cancelled"`
	Tags               []string         `json:"tags"`
	IsPublished        bool             `json:"is_published"`
	ShortCode          string           `json:"short_code"`
	URL                string           `json:"url,omitempty"`
	ImageURL           string           `json:"image_url,omitempty"`
	OrganizationID     *int             `json:"organization_id,omitempty"`
	LocationID         *int             `json:"location_id,omitempty"`
	Location           *Location        `json:"location,omitempty"`
	Attributes         map[string]bool  `json:"attributes,omitempty"`
	FloorCondition     string           `json:"floor_condition,omitempty"`
	ContactName        string           `json:"contact_name,omitempty"`
	ContactEmail       string           `json:"contact_email,omitempty"`
	BookingURL         string           `json:"booking_url,omitempty"`
	Availability       string           `json:"availability,omitempty"`
	TicketsTotal       int              `json:"tickets_total,omitempty"`
	BookingEnabled     bool             `json:"booking_enabled,omitempty"`
	Food               string           `json:"food,omitempty"`
	Drink              string           `json:"drink,omitempty"`
	Pricing            *Pricing         `json:"pricing,omitempty"`
	Locations          []Location       `json:"locations,omitempty"`
	Musicians          []Musician       `json:"musicians,omitempty"`
	DanceNames         []string         `json:"dance_names,omitempty"`
	Timetable          []TimetableEntry `json:"timetable,omitempty"`
	CreatedAt          string           `json:"created_at"`
	Source             string           `json:"source,omitempty"`
	SourceURL          string           `json:"source_url,omitempty"`
	ChangedAt          string           `json:"changed_at,omitempty"`
	ChangedBy          string           `json:"changed_by,omitempty"`
	FetchSourceID      int              `json:"fetch_source_id,omitempty"`
	Editable           bool             `json:"editable,omitempty"`
	Cancelable         bool             `json:"cancelable,omitempty"`
	CreatedByID        *int             `json:"created_by_id,omitempty"`
	SeriesID           *int             `json:"series_id,omitempty"`
}

type Dance struct {
	ID   int    `json:"id"`
	Name string `json:"name"`
}

type Tag struct {
	Slug     string `json:"slug"`
	Name     string `json:"name"`
	Category string `json:"category"`
}

type TimetableEntry struct {
	ID           int    `json:"id"`
	StartTime    string `json:"start_time"`
	EndTime      string `json:"end_time"`
	Title        string `json:"title"`
	Description  string `json:"description,omitempty"`
	Room         string `json:"room,omitempty"`
	LocationID   *int   `json:"location_id,omitempty"`
	LocationName string `json:"location_name,omitempty"`
	MusicianID   *int   `json:"musician_id,omitempty"`
	MusicianName string `json:"musician_name,omitempty"`
}

type Organization struct {
	ID            int    `json:"id"`
	Name          string `json:"name"`
	Description   string `json:"description"`
	ActorName     string `json:"actor_name,omitempty"`
	Website       string `json:"website,omitempty"`
	Instagram     string `json:"instagram,omitempty"`
	Mastodon      string `json:"mastodon,omitempty"`
	Facebook      string `json:"facebook,omitempty"`
	ContactEmail  string `json:"contact_email,omitempty"`
	ContactName   string `json:"contact_name,omitempty"`
	WikidataID    string `json:"wikidata_id,omitempty"`
	CreatedAt     string `json:"created_at"`
	ImageURL      string `json:"image_url,omitempty"`
	NotesMd       string `json:"notes_md,omitempty"`
	FetchSourceID *int   `json:"fetch_source_id,omitempty"`
}

type Pricing struct {
	Type     string  `json:"type"`
	Amount   float64 `json:"amount,omitempty"`
	Currency string  `json:"currency,omitempty"`
	Prices   []Price `json:"prices,omitempty"`
}

type Price struct {
	Label  string  `json:"label"`
	Amount float64 `json:"amount"`
}

type Musician struct {
	ID           int    `json:"id"`
	Bandname     string `json:"bandname"`
	ShortName    string `json:"short_name,omitempty"`
	Internetsite string `json:"internetsite,omitempty"`
	Description  string `json:"description,omitempty"`
	MBID         string `json:"mbid,omitempty"`
	WikidataID   string `json:"wikidata_id,omitempty"`
	DiscogsID    string `json:"discogs_id,omitempty"`
	Country      string `json:"country,omitempty"`
	BeginYear    int    `json:"begin_year,omitempty"`
	Biography    string `json:"biography,omitempty"`
	MembersJSON  string `json:"members_json,omitempty"`
	AlbumsJSON   string `json:"albums_json,omitempty"`
	Mastodon     string `json:"mastodon,omitempty"`
	Instagram    string `json:"instagram,omitempty"`
	Facebook     string `json:"facebook,omitempty"`
	Soundcloud   string `json:"soundcloud,omitempty"`
	Spotify      string `json:"spotify,omitempty"`
	Deezer       string `json:"deezer,omitempty"`
	Genre        string `json:"genre,omitempty"`
	ImageURL     string `json:"image_url,omitempty"`
	CreatedAt    string `json:"created_at,omitempty"`
}

type Location struct {
	ID              int             `json:"id"`
	Location        string          `json:"location"`
	ShortName       string          `json:"short_name,omitempty"`
	Address         string          `json:"address"`
	Zipcode         string          `json:"zipcode"`
	Town            string          `json:"town"`
	Country         string          `json:"country,omitempty"`
	CountryCode     string          `json:"country_code,omitempty"`
	Region          string          `json:"region,omitempty"`
	Latitude        *float64        `json:"latitude,omitempty"`
	Longitude       *float64        `json:"longitude,omitempty"`
	Internetsite    string          `json:"internetsite"`
	OsmID           *int64          `json:"osm_id,omitempty"`
	OsmType         string          `json:"osm_type,omitempty"`
	CreatedAt       string          `json:"created_at"`
	OrganizationIDs []int           `json:"organization_ids,omitempty"`
	NotesMd         string          `json:"notes_md,omitempty"`
	Attributes      map[string]bool `json:"attributes,omitempty"`
	Parking         string          `json:"parking,omitempty"`
	FloorCondition  string          `json:"floor_condition,omitempty"`
	NoStreetShoes   bool            `json:"no_street_shoes,omitempty"`
	Aliases         []string        `json:"aliases,omitempty"`
}

type FetchSource struct {
	ID             int      `json:"id"`
	URL            string   `json:"url"`
	Type           string   `json:"type"`
	Tags           []string `json:"tags"`
	DanceIDs       []int    `json:"dance_ids,omitempty"`
	OrganizationID *int     `json:"organization_id,omitempty"`
	LastFetchedAt  string   `json:"last_fetched_at,omitempty"`
	LastResult     string   `json:"last_result,omitempty"`
	CreatedAt      string   `json:"created_at"`
	TemplateID     *int     `json:"template_id,omitempty"`
	TemplateMode   string   `json:"template_mode,omitempty"`
	TemplateData   string   `json:"template_data,omitempty"`
}

type LoginResponse struct {
	Token     string `json:"token"`
	ExpiresAt string `json:"expires_at"`
	User      struct {
		ID          int    `json:"id"`
		DisplayName string `json:"display_name"`
		Email       string `json:"email"`
		Role        string `json:"role"`
	} `json:"user"`
}

type UserInfo struct {
	ID               int    `json:"id"`
	Email            string `json:"email"`
	DisplayName      string `json:"display_name"`
	Role             string `json:"role"`
	Description      string `json:"description"`
	Telegram         string `json:"telegram"`
	TelegramChatID   string `json:"telegram_chat_id,omitempty"`
	Matrix           string `json:"matrix"`
	Mastodon         string `json:"mastodon"`
	Website          string `json:"website"`
	EmailVerified    bool   `json:"email_verified"`
	TelegramVerified bool   `json:"telegram_verified"`
	MatrixVerified   bool   `json:"matrix_verified"`
	Disabled         bool   `json:"disabled"`
	HasPassword      bool   `json:"has_password"`
	CreatedAt        string `json:"created_at"`
}

func (c *DansalClient) get(ctx context.Context, path string, out any) error {
	const maxAttempts = 2
	var lastErr error
	for attempt := 0; attempt < maxAttempts; attempt++ {
		if attempt > 0 {
			// Jitter 50–150 ms before retry to avoid thundering-herd.
			jitter := time.Duration(50+rand.Intn(100)) * time.Millisecond
			select {
			case <-time.After(jitter):
			case <-ctx.Done():
				return ctx.Err()
			}
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.BaseURL+path, nil)
		if err != nil {
			return err // request build error — not retryable
		}
		resp, err := c.HTTP.Do(req)
		if err != nil {
			lastErr = err
			continue // network/timeout error — retry
		}
		defer resp.Body.Close()
		if resp.StatusCode == http.StatusNotFound {
			return fmt.Errorf("not found")
		}
		if resp.StatusCode != http.StatusOK {
			return apiErr(resp) // HTTP error — not retryable
		}
		return json.NewDecoder(resp.Body).Decode(out)
	}
	return lastErr
}

func (c *DansalClient) Login(ctx context.Context, email, password, clientIP, userAgent string) (*LoginResponse, error) {
	body, _ := json.Marshal(map[string]string{"email": email, "password": password})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.BaseURL+"/api/v1/login",
		bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	if clientIP != "" {
		req.Header.Set("X-Forwarded-For", clientIP)
	}
	if userAgent != "" {
		req.Header.Set("User-Agent", userAgent)
	}
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
		return nil, fmt.Errorf("invalid credentials")
	}
	if resp.StatusCode != http.StatusOK {
		return nil, apiErr(resp)
	}
	var lr LoginResponse
	if err := json.NewDecoder(resp.Body).Decode(&lr); err != nil {
		return nil, err
	}
	return &lr, nil
}

func (c *DansalClient) CertLogin(ctx context.Context, email string) (*LoginResponse, error) {
	body, _ := json.Marshal(map[string]string{"email": email})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.BaseURL+"/api/v1/cert-login", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, apiErr(resp)
	}
	var lr LoginResponse
	return &lr, json.NewDecoder(resp.Body).Decode(&lr)
}

func (c *DansalClient) Logout(ctx context.Context, token string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodDelete, c.BaseURL+"/api/v1/login", nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return err
	}
	resp.Body.Close()
	return nil
}

func (c *DansalClient) BulkSetEventLocation(ctx context.Context, ids []int, locationID int, token string) error {
	body, _ := json.Marshal(map[string]any{"ids": ids, "location_id": locationID})
	resp, err := c.authed(ctx, http.MethodPost, "/api/v1/events/bulk-set-location", token, body)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent && resp.StatusCode != http.StatusOK {
		return apiErr(resp)
	}
	c.invalidateEvents()
	return nil
}

func (c *DansalClient) BulkSetEventAttributes(ctx context.Context, payload map[string]any, token string) error {
	body, _ := json.Marshal(payload)
	resp, err := c.authed(ctx, http.MethodPost, "/api/v1/events/bulk-set-attributes", token, body)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent && resp.StatusCode != http.StatusOK {
		return apiErr(resp)
	}
	c.invalidateEvents()
	return nil
}

func (c *DansalClient) GetEventsByLocation(ctx context.Context, locationID int) ([]Event, error) {
	var events []Event
	return events, c.get(ctx, fmt.Sprintf("/api/v1/events?location_id=%d", locationID), &events)
}

func (c *DansalClient) GetEventsBySeries(ctx context.Context, seriesID int) ([]Event, error) {
	var events []Event
	return events, c.get(ctx, fmt.Sprintf("/api/v1/events?series_id=%d&include_past=true&include_cancelled=true", seriesID), &events)
}

func (c *DansalClient) GetEvents(ctx context.Context, after string) ([]Event, error) {
	if after == "" {
		return cached(&c.mu, &c.eventsCache, eventsTTL, func() ([]Event, error) {
			var events []Event
			return events, c.get(ctx, "/api/v1/events?is_published=true", &events)
		})
	}
	path := "/api/v1/events?is_published=true&start_time_after=" + after
	var events []Event
	if err := c.get(ctx, path, &events); err != nil {
		return nil, err
	}
	return events, nil
}

func (c *DansalClient) GetEvent(ctx context.Context, id int) (Event, error) {
	var event Event
	if err := c.get(ctx, fmt.Sprintf("/api/v1/events/%d", id), &event); err != nil {
		return Event{}, err
	}
	return event, nil
}

func (c *DansalClient) GetEventAuthed(ctx context.Context, id int, token string) (Event, error) {
	resp, err := c.authed(ctx, http.MethodGet, fmt.Sprintf("/api/v1/events/%d", id), token, nil)
	if err != nil {
		return Event{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		return Event{}, fmt.Errorf("not found")
	}
	if resp.StatusCode != http.StatusOK {
		return Event{}, apiErr(resp)
	}
	var event Event
	return event, json.NewDecoder(resp.Body).Decode(&event)
}

func (c *DansalClient) PublishEvent(ctx context.Context, id int, token string) error {
	resp, err := c.authed(ctx, http.MethodPost, fmt.Sprintf("/api/v1/events/%d/publish", id), token, nil)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		return apiErr(resp)
	}
	return nil
}

func (c *DansalClient) CancelEvent(ctx context.Context, id int, token string) error {
	resp, err := c.authed(ctx, http.MethodPost, fmt.Sprintf("/api/v1/events/%d/cancel", id), token, nil)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		return apiErr(resp)
	}
	return nil
}

func (c *DansalClient) AssignEventOrg(ctx context.Context, id, orgID int, token string) error {
	body, _ := json.Marshal(map[string]int{"org_id": orgID})
	resp, err := c.authed(ctx, http.MethodPost, fmt.Sprintf("/api/v1/events/%d/assign-org", id), token, body)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		return apiErr(resp)
	}
	return nil
}

func (c *DansalClient) GetOrganizations(ctx context.Context) ([]Organization, error) {
	return cached(&c.mu, &c.orgsCache, orgsTTL, func() ([]Organization, error) {
		var orgs []Organization
		return orgs, c.get(ctx, "/api/v1/organizations", &orgs)
	})
}

func (c *DansalClient) GetOrganization(ctx context.Context, id int) (Organization, error) {
	orgs, err := c.GetOrganizations(ctx)
	if err == nil {
		for _, o := range orgs {
			if o.ID == id {
				return o, nil
			}
		}
	}
	var org Organization
	if err := c.get(ctx, fmt.Sprintf("/api/v1/organizations/%d", id), &org); err != nil {
		return Organization{}, err
	}
	return org, nil
}

func (c *DansalClient) GetEventsByMusician(ctx context.Context, musicianID int, token string) ([]Event, error) {
	path := fmt.Sprintf("/api/v1/events?musician_id=%d", musicianID)
	resp, err := c.authed(ctx, http.MethodGet, path, token, nil)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	var events []Event
	return events, json.NewDecoder(resp.Body).Decode(&events)
}

func (c *DansalClient) GetPublicEventsByMusician(ctx context.Context, musicianID int) ([]Event, error) {
	var events []Event
	return events, c.get(ctx, fmt.Sprintf("/api/v1/events?musician_id=%d", musicianID), &events)
}

func (c *DansalClient) GetAllPublicEventsByMusician(ctx context.Context, musicianID int) ([]Event, error) {
	var events []Event
	return events, c.get(ctx, fmt.Sprintf("/api/v1/events?musician_id=%d&include_past=true", musicianID), &events)
}

func (c *DansalClient) GetMusicians(ctx context.Context) ([]Musician, error) {
	return cached(&c.mu, &c.musiciansCache, musiciansTTL, func() ([]Musician, error) {
		var ms []Musician
		return ms, c.get(ctx, "/api/v1/musicians", &ms)
	})
}

func (c *DansalClient) GetMusician(ctx context.Context, id int) (Musician, error) {
	var m Musician
	return m, c.get(ctx, fmt.Sprintf("/api/v1/musicians/%d", id), &m)
}

func (c *DansalClient) CreateMusician(ctx context.Context, m Musician, token string) (Musician, error) {
	body, _ := json.Marshal(m)
	resp, err := c.authed(ctx, http.MethodPost, "/api/v1/musicians", token, body)
	if err != nil {
		return Musician{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		return Musician{}, apiErr(resp)
	}
	var out []Musician
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil || len(out) == 0 {
		return Musician{}, err
	}
	c.invalidateMusicians()
	return out[0], nil
}

func (c *DansalClient) UpdateMusician(ctx context.Context, id int, m Musician, token string) error {
	body, _ := json.Marshal(m)
	resp, err := c.authed(ctx, http.MethodPut, fmt.Sprintf("/api/v1/musicians/%d", id), token, body)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return apiErr(resp)
	}
	c.invalidateMusicians()
	return nil
}

func (c *DansalClient) DeleteMusician(ctx context.Context, id int, token string) error {
	resp, err := c.authed(ctx, http.MethodDelete, fmt.Sprintf("/api/v1/musicians/%d", id), token, nil)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		return apiErr(resp)
	}
	c.invalidateMusicians()
	return nil
}

func (c *DansalClient) DeleteLocation(ctx context.Context, id int, token string) error {
	return c.deleteLocation(ctx, id, 0, token)
}

func (c *DansalClient) MergeLocation(ctx context.Context, dropID, targetID int, token string) error {
	return c.deleteLocation(ctx, dropID, targetID, token)
}

func (c *DansalClient) deleteLocation(ctx context.Context, id, reassignTo int, token string) error {
	path := fmt.Sprintf("/api/v1/locations/%d", id)
	if reassignTo != 0 {
		path += fmt.Sprintf("?reassign_to=%d", reassignTo)
	}
	resp, err := c.authed(ctx, http.MethodDelete, path, token, nil)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		return apiErr(resp)
	}
	c.invalidateLocations()
	return nil
}

func (c *DansalClient) UploadMusicianImage(ctx context.Context, id int, data []byte, filename, token string) error {
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	fw, err := mw.CreateFormFile("image", filename)
	if err != nil {
		return err
	}
	if _, err := fw.Write(data); err != nil {
		return err
	}
	mw.Close()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		fmt.Sprintf("%s/api/v1/musician-images/%d", c.BaseURL, id), &buf)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return err
	}
	resp.Body.Close()
	if resp.StatusCode >= 300 {
		return fmt.Errorf("upload musician image: %s", resp.Status)
	}
	return nil
}

func (c *DansalClient) UploadOrgImage(ctx context.Context, id int, data []byte, filename, token string) error {
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	fw, err := mw.CreateFormFile("image", filename)
	if err != nil {
		return err
	}
	if _, err := fw.Write(data); err != nil {
		return err
	}
	mw.Close()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		fmt.Sprintf("%s/api/v1/org-images/%d", c.BaseURL, id), &buf)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		return apiErr(resp)
	}
	return nil
}

func (c *DansalClient) authed(ctx context.Context, method, path, token string, body []byte) (*http.Response, error) {
	var bodyReader io.Reader = http.NoBody
	if body != nil {
		bodyReader = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.BaseURL+path, bodyReader)
	if err != nil {
		return nil, err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	req.Header.Set("Authorization", "Bearer "+token)
	return c.HTTP.Do(req)
}

func (c *DansalClient) CreateOrganization(ctx context.Context, org Organization, token string) (Organization, error) {
	body, _ := json.Marshal(org)
	resp, err := c.authed(ctx, http.MethodPost, "/api/v1/organizations", token, body)
	if err != nil {
		return Organization{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusForbidden {
		return Organization{}, fmt.Errorf("forbidden")
	}
	if resp.StatusCode != http.StatusCreated {
		return Organization{}, apiErr(resp)
	}
	var out Organization
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return Organization{}, err
	}
	c.invalidateOrgs()
	return out, nil
}

func (c *DansalClient) UpdateOrganization(ctx context.Context, id int, org Organization, token string) error {
	body, _ := json.Marshal(org)
	resp, err := c.authed(ctx, http.MethodPut, fmt.Sprintf("/api/v1/organizations/%d", id), token, body)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusForbidden {
		return fmt.Errorf("forbidden")
	}
	if resp.StatusCode != http.StatusOK {
		return apiErr(resp)
	}
	c.invalidateOrgs()
	return nil
}

func (c *DansalClient) DeleteOrganization(ctx context.Context, id int, token string) error {
	resp, err := c.authed(ctx, http.MethodDelete, fmt.Sprintf("/api/v1/organizations/%d", id), token, nil)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusForbidden {
		return fmt.Errorf("forbidden")
	}
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusNoContent {
		return apiErr(resp)
	}
	c.invalidateOrgs()
	return nil
}

func (c *DansalClient) CreateFetchSource(ctx context.Context, rawURL, typ string, tags []string, orgID *int, templateID *int, templateMode, templateData string, token string) (int, error) {
	payload := map[string]any{
		"url":  rawURL,
		"type": typ,
		"tags": tags,
	}
	if orgID != nil {
		payload["organization_id"] = *orgID
	}
	if templateID != nil {
		payload["template_id"] = *templateID
		payload["template_mode"] = templateMode
		payload["template_data"] = templateData
	}
	body, _ := json.Marshal(payload)
	resp, err := c.authed(ctx, http.MethodPost, "/api/v1/fetchurl", token, body)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusForbidden {
		return 0, fmt.Errorf("forbidden")
	}
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusCreated {
		return 0, apiErr(resp)
	}
	var events []any
	json.NewDecoder(resp.Body).Decode(&events)
	return len(events), nil
}

func (c *DansalClient) GetFetchSources(ctx context.Context, token string) ([]FetchSource, error) {
	resp, err := c.authed(ctx, http.MethodGet, "/api/v1/fetchurl", token, nil)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, apiErr(resp)
	}
	var sources []FetchSource
	return sources, json.NewDecoder(resp.Body).Decode(&sources)
}

func (c *DansalClient) GetFetchSource(ctx context.Context, id int, token string) (FetchSource, error) {
	resp, err := c.authed(ctx, http.MethodGet, fmt.Sprintf("/api/v1/fetchurl/%d", id), token, nil)
	if err != nil {
		return FetchSource{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		return FetchSource{}, fmt.Errorf("not found")
	}
	if resp.StatusCode != http.StatusOK {
		return FetchSource{}, apiErr(resp)
	}
	var src FetchSource
	return src, json.NewDecoder(resp.Body).Decode(&src)
}

func (c *DansalClient) UpdateFetchSource(ctx context.Context, id int, typ string, tags []string, danceIDs []int, orgID *int, templateID *int, templateMode, templateData string, token string) error {
	payload := map[string]any{
		"type":            typ,
		"tags":            tags,
		"dance_ids":       danceIDs,
		"organization_id": orgID,
		"template_id":     templateID,
		"template_mode":   templateMode,
		"template_data":   templateData,
	}
	body, _ := json.Marshal(payload)
	resp, err := c.authed(ctx, http.MethodPatch, fmt.Sprintf("/api/v1/fetchurl/%d", id), token, body)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return apiErr(resp)
	}
	return nil
}

func (c *DansalClient) GetLocations(ctx context.Context) ([]Location, error) {
	return cached(&c.mu, &c.locationsCache, locationsTTL, func() ([]Location, error) {
		var locs []Location
		return locs, c.get(ctx, "/api/v1/locations", &locs)
	})
}

func (c *DansalClient) GetLocationEventCounts(ctx context.Context, token string) (map[int]int, error) {
	resp, err := c.authed(ctx, http.MethodGet, "/api/v1/locations/event-counts", token, nil)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	var counts map[int]int
	if err := json.NewDecoder(resp.Body).Decode(&counts); err != nil {
		return nil, err
	}
	return counts, nil
}

func (c *DansalClient) GetLocation(ctx context.Context, id int) (Location, error) {
	var loc Location
	if err := c.get(ctx, fmt.Sprintf("/api/v1/locations/%d", id), &loc); err != nil {
		return Location{}, err
	}
	return loc, nil
}

func (c *DansalClient) CreateLocation(ctx context.Context, loc Location, token string) (Location, error) {
	body, _ := json.Marshal(loc)
	resp, err := c.authed(ctx, http.MethodPost, "/api/v1/locations", token, body)
	if err != nil {
		return Location{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusForbidden {
		return Location{}, fmt.Errorf("forbidden")
	}
	if resp.StatusCode != http.StatusCreated {
		return Location{}, apiErr(resp)
	}
	var created Location
	if err := json.NewDecoder(resp.Body).Decode(&created); err != nil {
		return Location{}, err
	}
	c.invalidateLocations()
	return created, nil
}

func (c *DansalClient) UpdateLocation(ctx context.Context, id int, loc Location, token string) error {
	body, _ := json.Marshal(loc)
	resp, err := c.authed(ctx, http.MethodPatch, fmt.Sprintf("/api/v1/locations/%d", id), token, body)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusForbidden {
		return fmt.Errorf("forbidden")
	}
	if resp.StatusCode != http.StatusOK {
		return apiErr(resp)
	}
	c.invalidateLocations()
	return nil
}

func (c *DansalClient) DeleteFetchSource(ctx context.Context, id int, token string) error {
	resp, err := c.authed(ctx, http.MethodDelete, fmt.Sprintf("/api/v1/fetchurl/%d", id), token, nil)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		return fmt.Errorf("not found")
	}
	if resp.StatusCode != http.StatusNoContent {
		return apiErr(resp)
	}
	return nil
}

func (c *DansalClient) RunFetchSource(ctx context.Context, id int, token string) (int, error) {
	resp, err := c.authed(ctx, http.MethodPost, fmt.Sprintf("/api/v1/fetchurl/%d/fetch", id), token, nil)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusCreated {
		return 0, apiErr(resp)
	}
	var events []json.RawMessage
	if err := json.NewDecoder(resp.Body).Decode(&events); err != nil {
		return 0, nil
	}
	return len(events), nil
}

func (c *DansalClient) BulkDeleteFetchSources(ctx context.Context, ids []int, token string) error {
	body, _ := json.Marshal(map[string]any{"ids": ids})
	resp, err := c.authed(ctx, http.MethodPost, "/api/v1/fetchurl/bulk-delete", token, body)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		return apiErr(resp)
	}
	return nil
}

func (c *DansalClient) BulkRunFetchSources(ctx context.Context, ids []int, token string) error {
	body, _ := json.Marshal(map[string]any{"ids": ids})
	resp, err := c.authed(ctx, http.MethodPost, "/api/v1/fetchurl/bulk-fetch", token, body)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return apiErr(resp)
	}
	return nil
}

func (c *DansalClient) BulkAssignFetchSourceOrg(ctx context.Context, ids []int, orgID *int, token string) error {
	body, _ := json.Marshal(map[string]any{"ids": ids, "organization_id": orgID})
	resp, err := c.authed(ctx, http.MethodPost, "/api/v1/fetchurl/bulk-assign-org", token, body)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		return apiErr(resp)
	}
	return nil
}

func (c *DansalClient) UnassignLocationOrg(ctx context.Context, locationID, orgID int, token string) error {
	body, _ := json.Marshal(map[string]int{"location_id": locationID, "organization_id": orgID})
	resp, err := c.authed(ctx, http.MethodPost, "/api/v1/locations/unassign-org", token, body)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		return apiErr(resp)
	}
	c.invalidateLocations()
	return nil
}

func (c *DansalClient) BulkAssignLocationOrg(ctx context.Context, ids []int, orgID *int, token string) error {
	payload := map[string]any{"ids": ids, "organization_id": orgID}
	body, _ := json.Marshal(payload)
	resp, err := c.authed(ctx, http.MethodPost, "/api/v1/locations/bulk-assign-org", token, body)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		return apiErr(resp)
	}
	c.invalidateLocations()
	return nil
}

func (c *DansalClient) GetEventsByOrg(ctx context.Context, orgID int) ([]Event, error) {
	all, err := c.GetEvents(ctx, "")
	if err != nil {
		return nil, err
	}
	var events []Event
	for _, e := range all {
		if e.OrganizationID != nil && *e.OrganizationID == orgID {
			events = append(events, e)
		}
	}
	return events, nil
}

func (c *DansalClient) GetAllEventsByOrg(ctx context.Context, orgID int) ([]Event, error) {
	var events []Event
	return events, c.get(ctx, fmt.Sprintf("/api/v1/events?is_published=true&include_past=true&organization_id=%d", orgID), &events)
}

func (c *DansalClient) GetMusiciansByOrg(ctx context.Context, orgID int) ([]Musician, error) {
	var ms []Musician
	return ms, c.get(ctx, fmt.Sprintf("/api/v1/musicians?organization_id=%d", orgID), &ms)
}

func (c *DansalClient) GetUser(ctx context.Context, id int, token string) (UserInfo, error) {
	resp, err := c.authed(ctx, http.MethodGet, fmt.Sprintf("/api/v1/users/%d", id), token, nil)
	if err != nil {
		return UserInfo{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return UserInfo{}, apiErr(resp)
	}
	var u UserInfo
	return u, json.NewDecoder(resp.Body).Decode(&u)
}

func (c *DansalClient) UpdateUser(ctx context.Context, id int, fields map[string]string, token string) error {
	body, _ := json.Marshal(fields)
	resp, err := c.authed(ctx, http.MethodPut, fmt.Sprintf("/api/v1/users/%d", id), token, body)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return apiErr(resp)
	}
	return nil
}

func (c *DansalClient) SendEmailVerification(ctx context.Context, id int, baseURL, token string) error {
	body, _ := json.Marshal(map[string]string{"channel": "email", "base_url": baseURL})
	resp, err := c.authed(ctx, http.MethodPost, fmt.Sprintf("/api/v1/users/%d/verify", id), token, body)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		return apiErr(resp)
	}
	return nil
}

func (c *DansalClient) SendMatrixVerification(ctx context.Context, id int, baseURL, token string) error {
	body, _ := json.Marshal(map[string]string{"channel": "matrix", "base_url": baseURL})
	resp, err := c.authed(ctx, http.MethodPost, fmt.Sprintf("/api/v1/users/%d/verify", id), token, body)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		return apiErr(resp)
	}
	return nil
}

func (c *DansalClient) GetTelegramVerifyLink(ctx context.Context, id int, baseURL, token string) (string, error) {
	body, _ := json.Marshal(map[string]string{"channel": "telegram", "base_url": baseURL})
	resp, err := c.authed(ctx, http.MethodPost, fmt.Sprintf("/api/v1/users/%d/verify", id), token, body)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", apiErr(resp)
	}
	var result struct {
		DeepLink string `json:"deep_link"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return "", err
	}
	return result.DeepLink, nil
}

func (c *DansalClient) RequestMagicLogin(ctx context.Context, email, channel, baseURL string) error {
	payload := map[string]string{"email": email}
	if channel != "" && channel != "email" {
		payload["channel"] = channel
	}
	if baseURL != "" {
		payload["base_url"] = baseURL
	}
	body, _ := json.Marshal(payload)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.BaseURL+"/api/v1/login/magic",
		bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return err
	}
	resp.Body.Close()
	return nil
}

func (c *DansalClient) UseMagicLogin(ctx context.Context, token, clientIP, userAgent string) (*LoginResponse, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet,
		c.BaseURL+"/api/v1/login/magic/"+token, nil)
	if err != nil {
		return nil, err
	}
	if clientIP != "" {
		req.Header.Set("X-Forwarded-For", clientIP)
	}
	if userAgent != "" {
		req.Header.Set("User-Agent", userAgent)
	}
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound || resp.StatusCode == http.StatusGone {
		return nil, fmt.Errorf("invalid_or_expired")
	}
	if resp.StatusCode != http.StatusOK {
		return nil, apiErr(resp)
	}
	var lr LoginResponse
	return &lr, json.NewDecoder(resp.Body).Decode(&lr)
}

// ── event creation types ───────────────────────────────────────────────────

type EventCreateReq struct {
	Title              string          `json:"title"`
	Description        string          `json:"description,omitempty"`
	StartTime          string          `json:"start_time"`
	EndTime            string          `json:"end_time,omitempty"`
	HasBall            bool            `json:"has_ball"`
	HasWorkshop        bool            `json:"has_workshop"`
	HasFestival        bool            `json:"has_festival"`
	WorkshopDifficulty string          `json:"workshop_difficulty,omitempty"`
	BookingURL         string          `json:"booking_url,omitempty"`
	Food               string          `json:"food,omitempty"`
	Drink              string          `json:"drink,omitempty"`
	FloorCondition     string          `json:"floor_condition,omitempty"`
	Attributes         map[string]bool `json:"attributes,omitempty"`
	ContactName        string          `json:"contact_name,omitempty"`
	ContactEmail       string          `json:"contact_email,omitempty"`
	Tags               []string        `json:"tags,omitempty"`
	URL                string          `json:"url,omitempty"`
	OrganizationID     *int            `json:"organization_id,omitempty"`
	Pricing            *Pricing        `json:"pricing,omitempty"`
	LocationID         *int            `json:"location_id,omitempty"`
	Location           EventLocReq     `json:"location"`
	Dances             []int           `json:"dances,omitempty"`
}

type EventUpdateReq struct {
	Title              string          `json:"title"`
	Description        string          `json:"description,omitempty"`
	StartTime          string          `json:"start_time"`
	EndTime            string          `json:"end_time,omitempty"`
	HasBall            bool            `json:"has_ball"`
	HasWorkshop        bool            `json:"has_workshop"`
	HasFestival        bool            `json:"has_festival"`
	WorkshopDifficulty string          `json:"workshop_difficulty,omitempty"`
	BookingURL         string          `json:"booking_url,omitempty"`
	Food               string          `json:"food,omitempty"`
	Drink              string          `json:"drink,omitempty"`
	FloorCondition     string          `json:"floor_condition,omitempty"`
	Attributes         map[string]bool `json:"attributes,omitempty"`
	ContactName        string          `json:"contact_name,omitempty"`
	ContactEmail       string          `json:"contact_email,omitempty"`
	IsCancelled        bool            `json:"is_cancelled"`
	Availability       string          `json:"availability,omitempty"`
	TicketsTotal       int             `json:"tickets_total,omitempty"`
	BookingEnabled     bool            `json:"booking_enabled,omitempty"`
	IsPublished        bool            `json:"is_published"`
	Tags               []string        `json:"tags,omitempty"`
	URL                string          `json:"url,omitempty"`
	OrganizationID     *int            `json:"organization_id,omitempty"`
	Pricing            *Pricing        `json:"pricing,omitempty"`
	LocationID         *int            `json:"location_id,omitempty"`
	Location           EventLocReq     `json:"location"`
	Musicians          []int           `json:"musicians,omitempty"`
	Dances             []int           `json:"dances,omitempty"`
}

type EventLocReq struct {
	Location  string   `json:"location"`
	ShortName string   `json:"short_name,omitempty"`
	Address   string   `json:"address,omitempty"`
	Zipcode   string   `json:"zipcode,omitempty"`
	Town      string   `json:"town,omitempty"`
	Country   string   `json:"country,omitempty"`
	Latitude  *float64 `json:"latitude,omitempty"`
	Longitude *float64 `json:"longitude,omitempty"`
}

type TimetableEntryReq struct {
	StartTime   string `json:"start_time"`
	EndTime     string `json:"end_time,omitempty"`
	Title       string `json:"title"`
	Description string `json:"description,omitempty"`
	Room        string `json:"room,omitempty"`
	LocationID  *int   `json:"location_id,omitempty"`
	MusicianID  *int   `json:"musician_id,omitempty"`
}

func (c *DansalClient) GetAdminEvents(ctx context.Context, token string, params url.Values) ([]Event, error) {
	path := "/api/v1/events"
	if len(params) > 0 {
		path += "?" + params.Encode()
	}
	resp, err := c.authed(ctx, http.MethodGet, path, token, nil)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	var events []Event
	if err := json.NewDecoder(resp.Body).Decode(&events); err != nil {
		return nil, err
	}
	return events, nil
}

func (c *DansalClient) CreateEvent(ctx context.Context, req EventCreateReq, token string) (Event, error) {
	body, _ := json.Marshal(req)
	resp, err := c.authed(ctx, http.MethodPost, "/api/v1/events", token, body)
	if err != nil {
		return Event{}, err
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusCreated && resp.StatusCode != http.StatusOK {
		return Event{}, fmt.Errorf("create event: %s: %s", resp.Status, string(b))
	}
	var result []Event
	if err := json.Unmarshal(b, &result); err != nil || len(result) == 0 {
		return Event{}, fmt.Errorf("no event in response")
	}
	c.invalidateEvents()
	return result[0], nil
}

func (c *DansalClient) DeleteEvent(ctx context.Context, id int, token string) error {
	resp, err := c.authed(ctx, http.MethodDelete, fmt.Sprintf("/api/v1/events/%d", id), token, nil)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		return apiErr(resp)
	}
	c.invalidateEvents()
	return nil
}

func (c *DansalClient) DeleteEventImage(ctx context.Context, eventID int, token string) error {
	resp, err := c.authed(ctx, http.MethodDelete, fmt.Sprintf("/api/v1/images/%d", eventID), token, nil)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		return apiErr(resp)
	}
	return nil
}

func (c *DansalClient) DeleteMusicianImage(ctx context.Context, id int, token string) error {
	resp, err := c.authed(ctx, http.MethodDelete, fmt.Sprintf("/api/v1/musician-images/%d", id), token, nil)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		return apiErr(resp)
	}
	return nil
}

func (c *DansalClient) DeleteOrgImage(ctx context.Context, id int, token string) error {
	resp, err := c.authed(ctx, http.MethodDelete, fmt.Sprintf("/api/v1/org-images/%d", id), token, nil)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		return apiErr(resp)
	}
	return nil
}

func (c *DansalClient) UploadEventImage(ctx context.Context, eventID int, data []byte, filename, token string) error {
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	fw, err := mw.CreateFormFile("image", filename)
	if err != nil {
		return err
	}
	if _, err := fw.Write(data); err != nil {
		return err
	}
	mw.Close()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		fmt.Sprintf("%s/api/v1/images/%d", c.BaseURL, eventID), &buf)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return err
	}
	resp.Body.Close()
	if resp.StatusCode >= 300 {
		return fmt.Errorf("upload image: %s", resp.Status)
	}
	return nil
}

func (c *DansalClient) UpdateEvent(ctx context.Context, id int, req EventUpdateReq, token string) (Event, error) {
	body, _ := json.Marshal(req)
	resp, err := c.authed(ctx, http.MethodPut, fmt.Sprintf("/api/v1/events/%d", id), token, body)
	if err != nil {
		return Event{}, err
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return Event{}, fmt.Errorf("update event: %s: %s", resp.Status, string(b))
	}
	var event Event
	if err := json.Unmarshal(b, &event); err != nil {
		return Event{}, err
	}
	c.invalidateEvents()
	return event, nil
}

func (c *DansalClient) ReplaceTimetable(ctx context.Context, eventID int, entries []TimetableEntryReq, token string) error {
	body, _ := json.Marshal(entries)
	resp, err := c.authed(ctx, http.MethodPut, fmt.Sprintf("/api/v1/events/%d/timetable", eventID), token, body)
	if err != nil {
		return err
	}
	resp.Body.Close()
	if resp.StatusCode >= 300 {
		return fmt.Errorf("replace timetable: %s", resp.Status)
	}
	return nil
}

func (c *DansalClient) AddTimetableEntries(ctx context.Context, eventID int, entries []TimetableEntryReq, token string) error {
	body, _ := json.Marshal(entries)
	resp, err := c.authed(ctx, http.MethodPost, fmt.Sprintf("/api/v1/events/%d/timetable", eventID), token, body)
	if err != nil {
		return err
	}
	resp.Body.Close()
	if resp.StatusCode >= 300 {
		return fmt.Errorf("add timetable: %s", resp.Status)
	}
	return nil
}

func (c *DansalClient) ConsumeVerification(ctx context.Context, token string) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet,
		c.BaseURL+"/api/v1/verify/"+token, nil)
	if err != nil {
		return "", err
	}
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		return "", fmt.Errorf("invalid")
	}
	if resp.StatusCode == http.StatusGone {
		return "", fmt.Errorf("expired")
	}
	if resp.StatusCode != http.StatusOK {
		return "", apiErr(resp)
	}
	var result struct {
		Channel string `json:"channel"`
	}
	json.NewDecoder(resp.Body).Decode(&result)
	return result.Channel, nil
}

type OrgMember struct {
	OrganizationID int    `json:"organization_id"`
	UserID         int    `json:"user_id"`
	Email          string `json:"email,omitempty"`
	DisplayName    string `json:"display_name,omitempty"`
	Role           string `json:"role,omitempty"`
}

func (c *DansalClient) GetOrganizationMembers(ctx context.Context, orgID int, token string) ([]OrgMember, error) {
	resp, err := c.authed(ctx, http.MethodGet, fmt.Sprintf("/api/v1/organizations/%d/members", orgID), token, nil)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, apiErr(resp)
	}
	var members []OrgMember
	return members, json.NewDecoder(resp.Body).Decode(&members)
}

// ── contact board ────────────────────────────────────────────────────────────

type ContactPost struct {
	ID               int               `json:"id"`
	EventID          int               `json:"event_id"`
	Type             string            `json:"type"`
	City             string            `json:"city"`
	Persons          int               `json:"persons"`
	Message          string            `json:"message,omitempty"`
	Nickname         string            `json:"nickname"`
	TelegramUsername string            `json:"telegram_username,omitempty"`
	CreatedAt        string            `json:"created_at"`
	Event            *ContactPostEvent `json:"event,omitempty"`
}

type ContactPostEvent struct {
	ID        int    `json:"id"`
	Title     string `json:"title"`
	StartTime string `json:"start_time"`
	Town      string `json:"town,omitempty"`
	Country   string `json:"country,omitempty"`
}

func (c *DansalClient) GetContactPosts(ctx context.Context, eventID int) ([]ContactPost, error) {
	var posts []ContactPost
	return posts, c.get(ctx, fmt.Sprintf("/api/v1/events/%d/contact-posts", eventID), &posts)
}

func (c *DansalClient) GetAllContactPosts(ctx context.Context, params url.Values) ([]ContactPost, error) {
	path := "/api/v1/contact-posts"
	if len(params) > 0 {
		path += "?" + params.Encode()
	}
	var posts []ContactPost
	return posts, c.get(ctx, path, &posts)
}

// CreateContactPost submits a board post and returns (telegramVerifyURL, error).
// telegramVerifyURL is non-empty only when the post was submitted with a Telegram username.
// baseURL is forwarded so the API can generate correct public links in emails.
// CreateContactPost submits a board post and returns (telegramVerifyURL, firstPost, error).
// firstPost is true when this was the first live post for the event (AP notification needed).
func (c *DansalClient) CreateContactPost(ctx context.Context, eventID int, post map[string]any, baseURL, sessionToken string) (string, bool, error) {
	body, _ := json.Marshal(post)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		c.BaseURL+fmt.Sprintf("/api/v1/events/%d/contact-posts", eventID),
		bytes.NewReader(body))
	if err != nil {
		return "", false, err
	}
	req.Header.Set("Content-Type", "application/json")
	if baseURL != "" {
		req.Header.Set("X-Base-URL", baseURL)
	}
	if sessionToken != "" {
		req.Header.Set("Authorization", "Bearer "+sessionToken)
	}
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return "", false, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		return "", false, apiErr(resp)
	}
	var result struct {
		TelegramVerifyURL string `json:"telegram_verify_url"`
		FirstPost         bool   `json:"first_post"`
	}
	json.NewDecoder(resp.Body).Decode(&result)
	return result.TelegramVerifyURL, result.FirstPost, nil
}

func (c *DansalClient) DeleteContactPost(ctx context.Context, id int, token string) error {
	resp, err := c.authed(ctx, http.MethodDelete, fmt.Sprintf("/api/v1/contact-posts/%d", id), token, nil)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusForbidden {
		return fmt.Errorf("forbidden")
	}
	if resp.StatusCode != http.StatusNoContent {
		return apiErr(resp)
	}
	return nil
}

func (c *DansalClient) DeleteContactPostByManageToken(ctx context.Context, token string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodDelete, c.BaseURL+"/api/v1/contact-posts/token/"+token, nil)
	if err != nil {
		return err
	}
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		return fmt.Errorf("not found")
	}
	if resp.StatusCode == http.StatusGone {
		return fmt.Errorf("expired")
	}
	if resp.StatusCode != http.StatusNoContent {
		return apiErr(resp)
	}
	return nil
}

// ContactManageResult holds the state of a contact post fetched by manage token.
type ContactManageResult struct {
	ID            int    `json:"id"`
	EventID       int    `json:"event_id"`
	Type          string `json:"type"`
	City          string `json:"city"`
	Persons       int    `json:"persons"`
	Message       string `json:"message"`
	Nickname      string `json:"nickname"`
	EmailVerified bool   `json:"email_verified"`
	ExpiresAt     string `json:"expires_at"`
	Expired       bool   `json:"expired"`
	JustVerified  bool   `json:"just_verified"`
	FirstPost     bool   `json:"first_post"`
}

func (c *DansalClient) GetContactPostByToken(ctx context.Context, token string) (ContactManageResult, error) {
	var result ContactManageResult
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.BaseURL+"/api/v1/contact-posts/manage/"+token, nil)
	if err != nil {
		return result, err
	}
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return result, err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		return result, fmt.Errorf("not found")
	}
	if resp.StatusCode != http.StatusOK {
		return result, apiErr(resp)
	}
	return result, json.NewDecoder(resp.Body).Decode(&result)
}

func (c *DansalClient) UpdateContactPost(ctx context.Context, id int, token string, fields map[string]any) error {
	body, _ := json.Marshal(fields)
	req, err := http.NewRequestWithContext(ctx, http.MethodPatch,
		fmt.Sprintf("%s/api/v1/contact-posts/%d?token=%s", c.BaseURL, id, token),
		bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		return apiErr(resp)
	}
	return nil
}

// ── bookings ─────────────────────────────────────────────────────────────────

type Booking struct {
	ID        int    `json:"id"`
	EventID   int    `json:"event_id"`
	Name      string `json:"name"`
	Email     string `json:"email,omitempty"`
	Persons   int    `json:"persons"`
	Message   string `json:"message,omitempty"`
	Status    string `json:"status"`
	QRToken   string `json:"qr_token,omitempty"`
	CreatedAt string `json:"created_at"`
}

func (c *DansalClient) GetBookings(ctx context.Context, eventID int, token string) ([]Booking, error) {
	resp, err := c.authed(ctx, http.MethodGet, fmt.Sprintf("/api/v1/events/%d/bookings", eventID), token, nil)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusForbidden {
		return nil, fmt.Errorf("forbidden")
	}
	if resp.StatusCode != http.StatusOK {
		return nil, apiErr(resp)
	}
	var out []Booking
	return out, json.NewDecoder(resp.Body).Decode(&out)
}

func (c *DansalClient) UpdateBookingStatus(ctx context.Context, bookingID int, status, token string) error {
	body, _ := json.Marshal(map[string]string{"status": status})
	resp, err := c.authed(ctx, http.MethodPatch, fmt.Sprintf("/api/v1/bookings/%d/status", bookingID), token, body)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusForbidden {
		return fmt.Errorf("forbidden")
	}
	if resp.StatusCode != http.StatusNoContent {
		return apiErr(resp)
	}
	return nil
}

func (c *DansalClient) DeleteBooking(ctx context.Context, bookingID int, token string) error {
	resp, err := c.authed(ctx, http.MethodDelete, fmt.Sprintf("/api/v1/bookings/%d", bookingID), token, nil)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusForbidden {
		return fmt.Errorf("forbidden")
	}
	if resp.StatusCode != http.StatusNoContent {
		return apiErr(resp)
	}
	return nil
}

func (c *DansalClient) CheckinBooking(ctx context.Context, qrToken, authToken string) (Booking, error) {
	resp, err := c.authed(ctx, http.MethodGet, "/api/v1/bookings/checkin/"+qrToken, authToken, nil)
	if err != nil {
		return Booking{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusForbidden {
		return Booking{}, fmt.Errorf("forbidden")
	}
	if resp.StatusCode == http.StatusNotFound {
		return Booking{}, fmt.Errorf("not found")
	}
	if resp.StatusCode != http.StatusOK {
		return Booking{}, apiErr(resp)
	}
	var b Booking
	return b, json.NewDecoder(resp.Body).Decode(&b)
}

func (c *DansalClient) CreateBooking(ctx context.Context, eventID int, fields map[string]any, baseURL string) error {
	body, _ := json.Marshal(fields)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		c.BaseURL+fmt.Sprintf("/api/v1/events/%d/bookings", eventID),
		bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	if baseURL != "" {
		req.Header.Set("X-Base-URL", baseURL)
	}
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusForbidden {
		return fmt.Errorf("booking_disabled")
	}
	if resp.StatusCode != http.StatusCreated {
		return apiErr(resp)
	}
	return nil
}

type BookingVerifyResult struct {
	QRToken    string `json:"qr_token"`
	CheckinURL string `json:"checkin_url"`
}

func (c *DansalClient) VerifyBooking(ctx context.Context, token string) (BookingVerifyResult, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet,
		c.BaseURL+"/api/v1/bookings/verify/"+token, nil)
	if err != nil {
		return BookingVerifyResult{}, err
	}
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return BookingVerifyResult{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		return BookingVerifyResult{}, fmt.Errorf("invalid")
	}
	if resp.StatusCode == http.StatusGone {
		return BookingVerifyResult{}, fmt.Errorf("expired")
	}
	if resp.StatusCode != http.StatusOK {
		return BookingVerifyResult{}, apiErr(resp)
	}
	var result BookingVerifyResult
	return result, json.NewDecoder(resp.Body).Decode(&result)
}

// ContactPoster creates a pending contact request and returns (telegramVerifyURL, error).
func (c *DansalClient) ContactPoster(ctx context.Context, id int, email, telegram, message, baseURL, sessionToken string) (string, error) {
	body, _ := json.Marshal(map[string]string{"email": email, "telegram": telegram, "message": message})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		c.BaseURL+fmt.Sprintf("/api/v1/contact-posts/%d/contact", id),
		bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	if baseURL != "" {
		req.Header.Set("X-Base-URL", baseURL)
	}
	if sessionToken != "" {
		req.Header.Set("Authorization", "Bearer "+sessionToken)
	}
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusAccepted && resp.StatusCode != http.StatusNoContent {
		return "", apiErr(resp)
	}
	var result map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&result); err == nil {
		if tgURL, ok := result["telegram_verify_url"].(string); ok {
			return tgURL, nil
		}
	}
	return "", nil
}

// InviteInfo holds the public fields returned by GET /api/v1/invites/{token}.
type InviteInfo struct {
	Role        string `json:"role"`
	Expired     bool   `json:"expired"`
	PresetEmail string `json:"preset_email"`
}

func (c *DansalClient) GetInviteInfo(ctx context.Context, token string) (InviteInfo, error) {
	var info InviteInfo
	return info, c.get(ctx, "/api/v1/invites/"+token, &info)
}

// ── users & invites ───────────────────────────────────────────────────────────

type InviteLink struct {
	ID         int    `json:"id"`
	Token      string `json:"token"`
	Role       string `json:"role"`
	InviteType string `json:"type,omitempty"`
	OrgID      *int   `json:"org_id,omitempty"`
	ExpiresAt  string `json:"expires_at"`
	UsedAt     string `json:"used_at,omitempty"`
	CreatedAt  string `json:"created_at"`
}

func (c *DansalClient) GetAllUsers(ctx context.Context, token string) ([]UserInfo, error) {
	resp, err := c.authed(ctx, http.MethodGet, "/api/v1/users", token, nil)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, apiErr(resp)
	}
	var users []UserInfo
	return users, json.NewDecoder(resp.Body).Decode(&users)
}

func (c *DansalClient) GenerateMagicLink(ctx context.Context, userID int, sessionToken, baseURL string) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		fmt.Sprintf("%s/api/v1/users/%d/magic-link", c.BaseURL, userID), nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("Authorization", "Bearer "+sessionToken)
	if baseURL != "" {
		req.Header.Set("X-Base-URL", baseURL)
	}
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", apiErr(resp)
	}
	var result struct {
		URL string `json:"url"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return "", err
	}
	return result.URL, nil
}

type SessionInfo struct {
	ID          int    `json:"id"`
	UserAgent   string `json:"user_agent"`
	IP          string `json:"ip"`
	Fingerprint bool   `json:"fingerprint"`
	CreatedAt   string `json:"created_at"`
	LastSeenAt  string `json:"last_seen_at"`
	ExpiresAt   string `json:"expires_at"`
	Current     bool   `json:"current"`
}

func (c *DansalClient) GetSessions(ctx context.Context, token string) ([]SessionInfo, error) {
	resp, err := c.authed(ctx, http.MethodGet, "/api/v1/sessions", token, nil)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, apiErr(resp)
	}
	var sessions []SessionInfo
	return sessions, json.NewDecoder(resp.Body).Decode(&sessions)
}

func (c *DansalClient) RevokeSession(ctx context.Context, sessionID int, token string) error {
	resp, err := c.authed(ctx, http.MethodDelete, fmt.Sprintf("/api/v1/sessions/%d", sessionID), token, nil)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		return apiErr(resp)
	}
	return nil
}

func (c *DansalClient) DeleteUser(ctx context.Context, id int, token string) error {
	resp, err := c.authed(ctx, http.MethodDelete, fmt.Sprintf("/api/v1/users/%d", id), token, nil)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		return apiErr(resp)
	}
	return nil
}

func (c *DansalClient) SetUserDisabled(ctx context.Context, id int, disabled bool, token string) error {
	body, _ := json.Marshal(map[string]any{"disabled": disabled})
	resp, err := c.authed(ctx, http.MethodPut, fmt.Sprintf("/api/v1/users/%d", id), token, body)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return apiErr(resp)
	}
	return nil
}

func (c *DansalClient) SetUserPassword(ctx context.Context, id int, password, token string) error {
	body, _ := json.Marshal(map[string]string{"password": password})
	resp, err := c.authed(ctx, http.MethodPost, fmt.Sprintf("/api/v1/users/%d/password", id), token, body)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		return apiErr(resp)
	}
	return nil
}

func (c *DansalClient) DeleteOwnAccount(ctx context.Context, token string) error {
	resp, err := c.authed(ctx, http.MethodDelete, "/api/v1/users/me", token, nil)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		return apiErr(resp)
	}
	return nil
}

func (c *DansalClient) CreateUserDirect(ctx context.Context, email, password, role, token string) (UserInfo, error) {
	body, _ := json.Marshal(map[string]string{
		"email":    email,
		"password": password,
		"role":     role,
	})
	resp, err := c.authed(ctx, http.MethodPost, "/api/v1/users", token, body)
	if err != nil {
		return UserInfo{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		return UserInfo{}, apiErr(resp)
	}
	var u UserInfo
	return u, json.NewDecoder(resp.Body).Decode(&u)
}

func (c *DansalClient) AddOrgMember(ctx context.Context, orgID, userID int, token string) error {
	body, _ := json.Marshal(map[string]int{"user_id": userID})
	resp, err := c.authed(ctx, http.MethodPost, fmt.Sprintf("/api/v1/organizations/%d/members", orgID), token, body)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		return apiErr(resp)
	}
	return nil
}

func (c *DansalClient) RemoveOrgMember(ctx context.Context, orgID, userID int, token string) error {
	resp, err := c.authed(ctx, http.MethodDelete, fmt.Sprintf("/api/v1/organizations/%d/members/%d", orgID, userID), token, nil)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		return apiErr(resp)
	}
	return nil
}

func (c *DansalClient) GetDances(ctx context.Context) ([]Dance, error) {
	return cached(&c.mu, &c.dancesCache, dancesTTL, func() ([]Dance, error) {
		var dances []Dance
		return dances, c.get(ctx, "/api/v1/dances", &dances)
	})
}

func (c *DansalClient) GetTags(ctx context.Context) ([]Tag, error) {
	return cached(&c.mu, &c.tagsCache, tagsTTL, func() ([]Tag, error) {
		var tags []Tag
		return tags, c.get(ctx, "/api/v1/tags", &tags)
	})
}

func (c *DansalClient) GetTagMap(ctx context.Context) (map[string]Tag, error) {
	tags, err := c.GetTags(ctx)
	if err != nil {
		return nil, err
	}
	m := make(map[string]Tag, len(tags))
	for _, t := range tags {
		m[t.Slug] = t
	}
	return m, nil
}

func (c *DansalClient) CreateDance(ctx context.Context, name, token string) (Dance, error) {
	body, _ := json.Marshal(map[string]string{"name": name})
	resp, err := c.authed(ctx, http.MethodPost, "/api/v1/dances", token, body)
	if err != nil {
		return Dance{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		return Dance{}, apiErr(resp)
	}
	var d Dance
	err = json.NewDecoder(resp.Body).Decode(&d)
	if err == nil {
		c.invalidateDances()
	}
	return d, err
}

func (c *DansalClient) DeleteDance(ctx context.Context, id int, token string) error {
	resp, err := c.authed(ctx, http.MethodDelete, fmt.Sprintf("/api/v1/dances/%d", id), token, nil)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		return apiErr(resp)
	}
	c.invalidateDances()
	return nil
}

func (c *DansalClient) ListInvites(ctx context.Context, token string) ([]InviteLink, error) {
	resp, err := c.authed(ctx, http.MethodGet, "/api/v1/invites", token, nil)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, apiErr(resp)
	}
	var links []InviteLink
	return links, json.NewDecoder(resp.Body).Decode(&links)
}

func (c *DansalClient) CreateInvite(ctx context.Context, inviteType string, orgID *int, token string) (InviteLink, error) {
	payload := map[string]any{"type": inviteType}
	if orgID != nil {
		payload["org_id"] = *orgID
	}
	body, _ := json.Marshal(payload)
	resp, err := c.authed(ctx, http.MethodPost, "/api/v1/invites", token, body)
	if err != nil {
		return InviteLink{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		return InviteLink{}, apiErr(resp)
	}
	var link InviteLink
	return link, json.NewDecoder(resp.Body).Decode(&link)
}

func (c *DansalClient) RevokeInvite(ctx context.Context, inviteToken, authToken string) error {
	resp, err := c.authed(ctx, http.MethodDelete, "/api/v1/invites/"+inviteToken, authToken, nil)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		return apiErr(resp)
	}
	return nil
}

// UseInvitePassword calls POST /api/v1/invites/{token} to create an account via password track.
func (c *DansalClient) UseInvitePassword(ctx context.Context, token, email, displayName, password string) error {
	payload := map[string]string{
		"email":        email,
		"display_name": displayName,
		"password":     password,
	}
	body, _ := json.Marshal(payload)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.BaseURL+"/api/v1/invites/"+token, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		return apiErr(resp)
	}
	return nil
}

func (c *DansalClient) VerifyContactRequest(ctx context.Context, token string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.BaseURL+"/api/v1/contact-requests/verify/"+token, nil)
	if err != nil {
		return err
	}
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return apiErr(resp)
	}
	return nil
}

type AdminConfig struct {
	TelegramBotToken      string `json:"telegram_bot_token"`
	TelegramBotName       string `json:"telegram_bot_name"`
	MatrixHomeserver      string `json:"matrix_homeserver"`
	MatrixAccessToken     string `json:"matrix_access_token"`
	HeartbeatIntervalMins int    `json:"heartbeat_interval_mins"`
}

func (c *DansalClient) GetAdminConfig(ctx context.Context, token string) (AdminConfig, error) {
	resp, err := c.authed(ctx, http.MethodGet, "/api/v1/admin/config", token, nil)
	if err != nil {
		return AdminConfig{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return AdminConfig{}, apiErr(resp)
	}
	var ac AdminConfig
	return ac, json.NewDecoder(resp.Body).Decode(&ac)
}

func (c *DansalClient) PatchAdminConfig(ctx context.Context, token string, ac AdminConfig) error {
	body, _ := json.Marshal(ac)
	resp, err := c.authed(ctx, http.MethodPatch, "/api/v1/admin/config", token, body)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		return apiErr(resp)
	}
	return nil
}

// MatrixLogin exchanges username+password for a token and stores it server-side.
// Returns a human-readable error string on failure, empty string on success.
func (c *DansalClient) MatrixLogin(ctx context.Context, token, homeserver, username, password string) error {
	body, _ := json.Marshal(map[string]string{
		"homeserver": homeserver,
		"username":   username,
		"password":   password,
	})
	resp, err := c.authed(ctx, http.MethodPost, "/api/v1/admin/matrix-login", token, body)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return apiErr(resp)
	}
	return nil
}

// DansalInfo mirrors the ServiceInfo struct from the dansal API.
type DansalInfo struct {
	Service                 string `json:"service"`
	Version                 string `json:"version"`
	BuildTime               string `json:"build_time"`
	TotalEvents             int    `json:"total_events"`
	PublishedEvents         int    `json:"published_events"`
	UpcomingEvents          int    `json:"upcoming_events"`
	DBSizeBytes             int64  `json:"db_size_bytes"`
	ImagesSizeBytes         int64  `json:"images_size_bytes"`
	SelfRegistrationEnabled bool   `json:"self_registration_enabled"`
}

type PendingRegistration struct {
	ID                  int    `json:"id"`
	Email               string `json:"email"`
	Description         string `json:"description,omitempty"`
	RegType             string `json:"reg_type"`
	OrgID               *int   `json:"org_id,omitempty"`
	OrgName             string `json:"org_name,omitempty"`
	OrgDescription      string `json:"org_description,omitempty"`
	OrgWebsite          string `json:"org_website,omitempty"`
	OrgContactEmail     string `json:"org_contact_email,omitempty"`
	VerificationChannel string `json:"verification_channel"`
	Telegram            string `json:"telegram,omitempty"`
	TelegramChatID      string `json:"telegram_chat_id,omitempty"`
	Verified            bool   `json:"verified"`
	CreatedAt           string `json:"created_at"`
	ExpiresAt           string `json:"expires_at"`
}

type RegisterReq struct {
	Email           string `json:"email"`
	Description     string `json:"description,omitempty"`
	RegType         string `json:"reg_type"`
	OrgID           *int   `json:"org_id,omitempty"`
	OrgName         string `json:"org_name,omitempty"`
	OrgActorName    string `json:"org_actor_name,omitempty"`
	OrgDescription  string `json:"org_description,omitempty"`
	OrgWebsite      string `json:"org_website,omitempty"`
	OrgContactEmail string `json:"org_contact_email,omitempty"`
	Channel         string `json:"channel"`
	Telegram        string `json:"telegram,omitempty"`
	Phone2          string `json:"phone2,omitempty"`
}

func (c *DansalClient) GetDansalInfo(ctx context.Context) (DansalInfo, error) {
	var info DansalInfo
	err := c.get(ctx, "/api/v1/info", &info)
	return info, err
}

type APIKey struct {
	ID        int    `json:"id"`
	UserID    int    `json:"user_id"`
	Name      string `json:"name"`
	Key       string `json:"key,omitempty"`
	ExpiresAt string `json:"expires_at,omitempty"`
	CreatedAt string `json:"created_at"`
}

func (c *DansalClient) ListPasskeys(ctx context.Context, token string) ([]PasskeyInfo, error) {
	resp, err := c.authed(ctx, http.MethodGet, "/api/v1/user/webauthn/credentials", token, nil)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, apiErr(resp)
	}
	var items []PasskeyInfo
	return items, json.NewDecoder(resp.Body).Decode(&items)
}

func (c *DansalClient) ListAPIKeys(ctx context.Context, token string) ([]APIKey, error) {
	resp, err := c.authed(ctx, http.MethodGet, "/api/v1/apikeys", token, nil)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, apiErr(resp)
	}
	var keys []APIKey
	return keys, json.NewDecoder(resp.Body).Decode(&keys)
}

func (c *DansalClient) CreateAPIKey(ctx context.Context, token, name, expiresAt string) (*APIKey, error) {
	payload := map[string]any{"name": name}
	if expiresAt != "" {
		payload["expires_at"] = expiresAt
	}
	body, _ := json.Marshal(payload)
	resp, err := c.authed(ctx, http.MethodPost, "/api/v1/apikeys", token, body)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		return nil, apiErr(resp)
	}
	var key APIKey
	return &key, json.NewDecoder(resp.Body).Decode(&key)
}

func (c *DansalClient) DeleteAPIKey(ctx context.Context, token string, id int) error {
	resp, err := c.authed(ctx, http.MethodDelete, fmt.Sprintf("/api/v1/apikeys/%d", id), token, nil)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		return apiErr(resp)
	}
	return nil
}

type PublisherCreated struct {
	UserID int    `json:"user_id"`
	Name   string `json:"name"`
	KeyID  int    `json:"key_id"`
	APIKey string `json:"api_key"`
	OrgID  *int   `json:"org_id,omitempty"`
}

func (c *DansalClient) CreatePublisher(ctx context.Context, name string, orgID *int, token string) (*PublisherCreated, error) {
	payload := map[string]any{}
	if name != "" {
		payload["name"] = name
	}
	if orgID != nil {
		payload["org_id"] = *orgID
	}
	body, _ := json.Marshal(payload)
	resp, err := c.authed(ctx, http.MethodPost, "/api/v1/publishers", token, body)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		return nil, apiErr(resp)
	}
	var pub PublisherCreated
	return &pub, json.NewDecoder(resp.Body).Decode(&pub)
}

func (c *DansalClient) RegeneratePublisherKey(ctx context.Context, publisherID int, token string) (string, int, error) {
	resp, err := c.authed(ctx, http.MethodPost, fmt.Sprintf("/api/v1/publishers/%d/regenerate-key", publisherID), token, nil)
	if err != nil {
		return "", 0, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", 0, apiErr(resp)
	}
	var r struct {
		KeyID  int    `json:"key_id"`
		APIKey string `json:"api_key"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&r); err != nil {
		return "", 0, err
	}
	return r.APIKey, r.KeyID, nil
}

func (c *DansalClient) ChangePassword(ctx context.Context, oldPassword, newPassword, token string) error {
	body, _ := json.Marshal(map[string]string{"old_password": oldPassword, "new_password": newPassword})
	resp, err := c.authed(ctx, http.MethodPost, "/api/v1/user/password", token, body)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNoContent {
		return nil
	}
	return apiErr(resp)
}

// PreviewEvent mirrors the EventCreateRequest JSON returned by POST /api/v1/events/preview.
type PreviewEvent struct {
	Title          string     `json:"title"`
	Description    string     `json:"description,omitempty"`
	StartTime      string     `json:"start_time"`
	EndTime        string     `json:"end_time,omitempty"`
	HasBall        bool       `json:"has_ball"`
	HasWorkshop    bool       `json:"has_workshop"`
	HasFestival    bool       `json:"has_festival"`
	IsCancelled    bool       `json:"is_cancelled"`
	Tags           []string   `json:"tags,omitempty"`
	URL            string     `json:"url,omitempty"`
	Location       PreviewLoc `json:"location"`
	OrganizationID *int       `json:"organization_id,omitempty"`
	Pricing        *Pricing   `json:"pricing,omitempty"`
	Status         string     `json:"duplicate_status,omitempty"`
}

type PreviewLoc struct {
	Location  string   `json:"location"`
	Town      string   `json:"town,omitempty"`
	Country   string   `json:"country,omitempty"`
	Address   string   `json:"address,omitempty"`
	Latitude  *float64 `json:"latitude,omitempty"`
	Longitude *float64 `json:"longitude,omitempty"`
}

func (c *DansalClient) PreviewEvents(ctx context.Context, body io.Reader, contentType, token string) ([]PreviewEvent, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.BaseURL+"/api/v1/events/preview", body)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", contentType)
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, apiErr(resp)
	}
	var events []PreviewEvent
	if err := json.NewDecoder(resp.Body).Decode(&events); err != nil {
		return nil, err
	}
	return events, nil
}

func (c *DansalClient) CreateEventBatch(ctx context.Context, events []json.RawMessage, token string) ([]Event, error) {
	body, _ := json.Marshal(events)
	resp, err := c.authed(ctx, http.MethodPost, "/api/v1/events", token, body)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusCreated && resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("create events: %s: %s", resp.Status, string(b))
	}
	var result []Event
	json.Unmarshal(b, &result)
	c.invalidateEvents()
	return result, nil
}

// SuggestEventReq is the JSON body for POST /api/v1/events/suggest.
type SuggestEventReq struct {
	Title              string     `json:"title"`
	Description        string     `json:"description"`
	StartTime          string     `json:"start_time"`
	EndTime            string     `json:"end_time,omitempty"`
	HasBall            bool       `json:"has_ball"`
	HasWorkshop        bool       `json:"has_workshop"`
	HasFestival        bool       `json:"has_festival"`
	WorkshopDifficulty string     `json:"workshop_difficulty,omitempty"`
	Tags               []string   `json:"tags,omitempty"`
	DanceIDs           []int      `json:"dance_ids,omitempty"`
	URL                string     `json:"url,omitempty"`
	Food               string     `json:"food,omitempty"`
	Drink              string     `json:"drink,omitempty"`
	Location           PreviewLoc `json:"location"`
	Email              string     `json:"email"`
	Phone2             string     `json:"phone2"` // honeypot
}

// SuggestEventPreview calls POST /api/v1/events/suggest-preview with a multipart body.
func (c *DansalClient) SuggestEventPreview(ctx context.Context, body io.Reader, contentType string) ([]PreviewEvent, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.BaseURL+"/api/v1/events/suggest-preview", body)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", contentType)
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, apiErr(resp)
	}
	var events []PreviewEvent
	if err := json.NewDecoder(resp.Body).Decode(&events); err != nil {
		return nil, err
	}
	return events, nil
}

// SuggestEvent calls POST /api/v1/events/suggest.
func (c *DansalClient) SuggestEvent(ctx context.Context, req SuggestEventReq, baseURL string) error {
	body, _ := json.Marshal(req)
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, c.BaseURL+"/api/v1/events/suggest", bytes.NewReader(body))
	if err != nil {
		return err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	if baseURL != "" {
		httpReq.Header.Set("X-Base-URL", baseURL)
	}
	resp, err := c.HTTP.Do(httpReq)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusAccepted {
		return apiErr(resp)
	}
	return nil
}

// VerifySuggestion calls GET /api/v1/events/suggest/verify/{token}.
func (c *DansalClient) VerifySuggestion(ctx context.Context, token string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.BaseURL+"/api/v1/events/suggest/verify/"+token, nil)
	if err != nil {
		return err
	}
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return apiErr(resp)
	}
	return nil
}

// Register calls POST /api/v1/register.
// baseURL is the public frontend URL (e.g. https://example.com), passed as X-Base-URL so the
// API can build a correct email verification link pointing to the frontend.
// Returns the raw JSON response (may contain telegram_token).
func (c *DansalClient) Register(ctx context.Context, req RegisterReq, baseURL string) (map[string]string, error) {
	body, _ := json.Marshal(req)
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, c.BaseURL+"/api/v1/register", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	if baseURL != "" {
		httpReq.Header.Set("X-Base-URL", baseURL)
	}
	resp, err := c.HTTP.Do(httpReq)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusAccepted {
		return nil, apiErr(resp)
	}
	var result map[string]string
	json.NewDecoder(resp.Body).Decode(&result)
	return result, nil
}

// VerifyRegistrationEmail calls GET /api/v1/register/verify/email/{token}.
func (c *DansalClient) VerifyRegistrationEmail(ctx context.Context, token string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.BaseURL+"/api/v1/register/verify/email/"+token, nil)
	if err != nil {
		return err
	}
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return apiErr(resp)
	}
	return nil
}

// ListPendingRegistrations calls GET /api/v1/pending-registrations.
func (c *DansalClient) ListPendingRegistrations(ctx context.Context, token string) ([]PendingRegistration, error) {
	resp, err := c.authed(ctx, http.MethodGet, "/api/v1/pending-registrations", token, nil)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	var regs []PendingRegistration
	if err := json.NewDecoder(resp.Body).Decode(&regs); err != nil {
		return nil, err
	}
	return regs, nil
}

// PendingInvite represents an unused invite link with preset_email (awaiting account setup).
type PendingInvite struct {
	ID          int    `json:"id"`
	Token       string `json:"token"`
	Role        string `json:"role"`
	OrgID       *int   `json:"org_id,omitempty"`
	ExpiresAt   string `json:"expires_at"`
	CreatedAt   string `json:"created_at"`
	PresetEmail string `json:"preset_email"`
}

// ListPendingInvites calls GET /api/v1/pending-invites.
func (c *DansalClient) ListPendingInvites(ctx context.Context, token string) ([]PendingInvite, error) {
	resp, err := c.authed(ctx, http.MethodGet, "/api/v1/pending-invites", token, nil)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	var invites []PendingInvite
	if err := json.NewDecoder(resp.Body).Decode(&invites); err != nil {
		return nil, err
	}
	return invites, nil
}

// ResendInvite generates a fresh invite for an existing pending-invite (unused invite with preset_email).
func (c *DansalClient) ResendInvite(ctx context.Context, token string, inviteID int, baseURL string) error {
	path := fmt.Sprintf("/api/v1/pending-invites/%d/resend", inviteID)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.BaseURL+path, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	if baseURL != "" {
		req.Header.Set("X-Base-URL", baseURL)
	}
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusNoContent {
		return apiErr(resp)
	}
	return nil
}

// ApproveRegistration calls POST /api/v1/pending-registrations/{id}/approve.
func (c *DansalClient) ApproveRegistration(ctx context.Context, token string, id int, role string) error {
	body, _ := json.Marshal(map[string]string{"role": role})
	path := fmt.Sprintf("/api/v1/pending-registrations/%d/approve", id)
	resp, err := c.authed(ctx, http.MethodPost, path, token, body)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		return apiErr(resp)
	}
	return nil
}

// RejectRegistration calls DELETE /api/v1/pending-registrations/{id}.
// reason is required by the API when the registration is verified.
func (c *DansalClient) RejectRegistration(ctx context.Context, token string, id int, reason string) error {
	body, _ := json.Marshal(map[string]string{"reason": reason})
	path := fmt.Sprintf("/api/v1/pending-registrations/%d", id)
	resp, err := c.authed(ctx, http.MethodDelete, path, token, body)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		return apiErr(resp)
	}
	return nil
}

// GetPendingRegCount returns the scoped count of verified, unactioned pending registrations.
func (c *DansalClient) GetPendingRegCount(ctx context.Context, token string) (int, error) {
	resp, err := c.authed(ctx, http.MethodGet, "/api/v1/pending-registrations/count", token, nil)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return 0, nil
	}
	var r struct {
		Count int `json:"count"`
	}
	json.NewDecoder(resp.Body).Decode(&r)
	return r.Count, nil
}

type PendingRegStatus struct {
	ID         int    `json:"id"`
	Verified   bool   `json:"verified"`
	Approved   bool   `json:"approved"`
	Expired    bool   `json:"expired"`
	HasPasskey bool   `json:"has_passkey"`
	InviteURL  string `json:"invite_url,omitempty"`
}

func (c *DansalClient) GetRegistrationStatus(ctx context.Context, id int, token string) (*PendingRegStatus, error) {
	url := fmt.Sprintf("%s/api/v1/register/status/%d", c.BaseURL, id)
	if token != "" {
		url += "?token=" + token
	}
	resp, err := c.HTTP.Get(url)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		return nil, nil
	}
	if resp.StatusCode != http.StatusOK {
		return nil, apiErr(resp)
	}
	var s PendingRegStatus
	return &s, json.NewDecoder(resp.Body).Decode(&s)
}

func (c *DansalClient) ResendRegistration(ctx context.Context, token string) error {
	resp, err := c.HTTP.Post(fmt.Sprintf("%s/api/v1/register/resend/%s", c.BaseURL, token), "application/json", nil)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		return apiErr(resp)
	}
	return nil
}

func (c *DansalClient) CancelRegistration(ctx context.Context, token string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodDelete, fmt.Sprintf("%s/api/v1/register/%s", c.BaseURL, token), nil)
	if err != nil {
		return err
	}
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		return apiErr(resp)
	}
	return nil
}

// ── Event Series ──────────────────────────────────────────────────────────────

type SeriesEvent struct {
	ID           int    `json:"id"`
	Title        string `json:"title"`
	Description  string `json:"description"`
	StartTime    string `json:"start_time"`
	EndTime      string `json:"end_time"`
	LocationID   *int   `json:"location_id,omitempty"`
	LocationName string `json:"location_name,omitempty"`
	IsCancelled  bool   `json:"is_cancelled"`
	IsPublished  bool   `json:"is_published"`
}

type EventSeries struct {
	ID                int           `json:"id"`
	Slug              string        `json:"slug"`
	Title             string        `json:"title"`
	Description       string        `json:"description"`
	OrganizationID    *int          `json:"organization_id,omitempty"`
	DefaultLocationID *int          `json:"default_location_id,omitempty"`
	DefaultStartTime  string        `json:"default_start_time"`
	DefaultEndTime    string        `json:"default_end_time"`
	InviteToken       string        `json:"invite_token,omitempty"`
	CreatedAt         string        `json:"created_at"`
	UpdatedAt         int64         `json:"updated_at"`
	EventCount        int           `json:"event_count,omitempty"`
	Events            []SeriesEvent `json:"events,omitempty"`
}

func (c *DansalClient) GetSeriesList(ctx context.Context, token string) ([]EventSeries, error) {
	resp, err := c.authed(ctx, http.MethodGet, "/api/v1/series", token, nil)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, apiErr(resp)
	}
	var out []EventSeries
	return out, json.NewDecoder(resp.Body).Decode(&out)
}

func (c *DansalClient) GetSeriesByID(ctx context.Context, id int, token string) (EventSeries, error) {
	resp, err := c.authed(ctx, http.MethodGet, fmt.Sprintf("/api/v1/series/%d", id), token, nil)
	if err != nil {
		return EventSeries{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		return EventSeries{}, fmt.Errorf("not found")
	}
	if resp.StatusCode != http.StatusOK {
		return EventSeries{}, apiErr(resp)
	}
	var out EventSeries
	return out, json.NewDecoder(resp.Body).Decode(&out)
}

func (c *DansalClient) CreateSeries(ctx context.Context, body map[string]any, token string) (EventSeries, error) {
	b, _ := json.Marshal(body)
	resp, err := c.authed(ctx, http.MethodPost, "/api/v1/series", token, b)
	if err != nil {
		return EventSeries{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		return EventSeries{}, apiErr(resp)
	}
	var out EventSeries
	return out, json.NewDecoder(resp.Body).Decode(&out)
}

func (c *DansalClient) UpdateSeries(ctx context.Context, id int, body map[string]any, token string) error {
	b, _ := json.Marshal(body)
	resp, err := c.authed(ctx, http.MethodPut, fmt.Sprintf("/api/v1/series/%d", id), token, b)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		return apiErr(resp)
	}
	return nil
}

func (c *DansalClient) DeleteSeries(ctx context.Context, id int, token string) error {
	resp, err := c.authed(ctx, http.MethodDelete, fmt.Sprintf("/api/v1/series/%d", id), token, nil)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		return apiErr(resp)
	}
	return nil
}

func (c *DansalClient) AddSeriesDate(ctx context.Context, id int, body map[string]any, token string) error {
	b, _ := json.Marshal(body)
	resp, err := c.authed(ctx, http.MethodPost, fmt.Sprintf("/api/v1/series/%d/add-date", id), token, b)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		return apiErr(resp)
	}
	return nil
}

func (c *DansalClient) RegenerateSeriesToken(ctx context.Context, id int, token string) (string, error) {
	resp, err := c.authed(ctx, http.MethodPost, fmt.Sprintf("/api/v1/series/%d/token/regenerate", id), token, nil)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", apiErr(resp)
	}
	var result map[string]string
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return "", err
	}
	return result["invite_token"], nil
}

func (c *DansalClient) RevokeSeriesToken(ctx context.Context, id int, token string) error {
	resp, err := c.authed(ctx, http.MethodPost, fmt.Sprintf("/api/v1/series/%d/token/revoke", id), token, nil)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		return apiErr(resp)
	}
	return nil
}

func (c *DansalClient) UpdateSeriesDescriptions(ctx context.Context, seriesID int, updates []map[string]any, token string) error {
	b, _ := json.Marshal(map[string]any{"updates": updates})
	resp, err := c.authed(ctx, http.MethodPost, fmt.Sprintf("/api/v1/series/%d/descriptions", seriesID), token, b)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		return apiErr(resp)
	}
	return nil
}

func (c *DansalClient) RemoveEventFromSeries(ctx context.Context, eventID int, token string) error {
	resp, err := c.authed(ctx, http.MethodPost, fmt.Sprintf("/api/v1/events/%d/remove-from-series", eventID), token, nil)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		return apiErr(resp)
	}
	return nil
}

func (c *DansalClient) AssignEventsToSeries(ctx context.Context, seriesID int, eventIDs []int, token string) error {
	b, _ := json.Marshal(map[string]any{"ids": eventIDs})
	resp, err := c.authed(ctx, http.MethodPost, fmt.Sprintf("/api/v1/series/%d/assign-events", seriesID), token, b)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		return apiErr(resp)
	}
	return nil
}

func (c *DansalClient) GetSeriesByInviteToken(ctx context.Context, seriesToken string) (EventSeries, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet,
		c.BaseURL+"/api/v1/series/token/"+seriesToken, nil)
	if err != nil {
		return EventSeries{}, err
	}
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return EventSeries{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		return EventSeries{}, fmt.Errorf("not found")
	}
	if resp.StatusCode != http.StatusOK {
		return EventSeries{}, apiErr(resp)
	}
	var out EventSeries
	return out, json.NewDecoder(resp.Body).Decode(&out)
}

func (c *DansalClient) PatchSeriesEventDescription(ctx context.Context, seriesToken string, eventID int, description string) error {
	b, _ := json.Marshal(map[string]string{"description": description})
	path := fmt.Sprintf("/api/v1/series/token/%s/events/%d", seriesToken, eventID)
	req, err := http.NewRequestWithContext(ctx, http.MethodPatch, c.BaseURL+path, bytes.NewReader(b))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		return apiErr(resp)
	}
	return nil
}
