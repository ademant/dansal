package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
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
	musiciansCache cacheEntry[[]Musician]
	locationsCache cacheEntry[[]Location]
	eventsCache    cacheEntry[[]Event]
}

// cached fetches from cache when fresh; otherwise calls fetch and stores the result.
// Concurrent cache misses may trigger multiple fetches (thundering-herd at small scale is fine).
func cached[T any](mu *sync.Mutex, entry *cacheEntry[T], ttl time.Duration, fetch func() (T, error)) (T, error) {
	mu.Lock()
	if !entry.fetchedAt.IsZero() && time.Since(entry.fetchedAt) < ttl {
		v := entry.val
		mu.Unlock()
		return v, nil
	}
	mu.Unlock()
	val, err := fetch()
	if err != nil {
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
		Error string `json:"error"`
	}
	if json.Unmarshal(b, &body) == nil && body.Error != "" {
		return fmt.Errorf("dansal API %d: %s", resp.StatusCode, body.Error)
	}
	if msg := strings.TrimSpace(string(b)); msg != "" {
		return fmt.Errorf("dansal API %d: %s", resp.StatusCode, msg)
	}
	return fmt.Errorf("dansal API: %s", resp.Status)
}

type Event struct {
	ID              int        `json:"id"`
	Title           string     `json:"title"`
	Description     string     `json:"description"`
	StartTime       string     `json:"start_time"`
	EndTime         string     `json:"end_time"`
	HasBall              bool       `json:"has_ball"`
	HasWorkshop          bool       `json:"has_workshop"`
	HasFestival          bool       `json:"has_festival"`
	WorkshopDifficulty   string     `json:"workshop_difficulty,omitempty"`
	IsCancelled          bool       `json:"is_cancelled"`
	Tags            []string   `json:"tags"`
	IsPublished     bool       `json:"is_published"`
	ShortCode       string     `json:"short_code"`
	URL             string     `json:"url,omitempty"`
	ImageURL        string     `json:"image_url,omitempty"`
	OrganizationID  *int       `json:"organization_id,omitempty"`
	LocationID      *int       `json:"location_id,omitempty"`
	Location          string     `json:"location,omitempty"`
	LocationShortName string     `json:"location_short_name,omitempty"`
	LocationAddress   string     `json:"location_address,omitempty"`
	LocationZipcode string     `json:"location_zipcode,omitempty"`
	LocationTown    string     `json:"location_town,omitempty"`
	LocationCountry string     `json:"location_country,omitempty"`
	LocationLat     *float64   `json:"location_lat,omitempty"`
	LocationLng     *float64   `json:"location_lng,omitempty"`
	BookingURL      string     `json:"booking_url,omitempty"`
	Availability    string     `json:"availability,omitempty"`
	TicketsTotal    int        `json:"tickets_total,omitempty"`
	BookingEnabled  bool       `json:"booking_enabled,omitempty"`
	Pricing         *Pricing         `json:"pricing,omitempty"`
	Musicians       []Musician       `json:"musicians,omitempty"`
	DanceNames      []string         `json:"dance_names,omitempty"`
	Timetable       []TimetableEntry `json:"timetable,omitempty"`
	CreatedAt       string           `json:"created_at"`
	SourceURL       string           `json:"source_url,omitempty"`
	ChangedAt       string           `json:"changed_at,omitempty"`
	ChangedBy       string           `json:"changed_by,omitempty"`
	FetchSourceID   int              `json:"fetch_source_id,omitempty"`
}

type Dance struct {
	ID   int    `json:"id"`
	Name string `json:"name"`
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
	ID           int    `json:"id"`
	Name         string `json:"name"`
	Description  string `json:"description"`
	ActorName    string `json:"actor_name,omitempty"`
	Website      string `json:"website,omitempty"`
	Instagram    string `json:"instagram,omitempty"`
	Mastodon     string `json:"mastodon,omitempty"`
	Facebook     string `json:"facebook,omitempty"`
	ContactEmail string `json:"contact_email,omitempty"`
	CreatedAt    string `json:"created_at"`
	ImageURL     string `json:"image_url,omitempty"`
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
	ID              int      `json:"id"`
	Location        string   `json:"location"`
	ShortName       string   `json:"short_name,omitempty"`
	Address         string   `json:"address"`
	Zipcode         string   `json:"zipcode"`
	Town            string   `json:"town"`
	Country         string   `json:"country,omitempty"`
	Latitude        *float64 `json:"latitude,omitempty"`
	Longitude       *float64 `json:"longitude,omitempty"`
	Internetsite    string   `json:"internetsite"`
	CreatedAt       string   `json:"created_at"`
	OrganizationIDs []int    `json:"organization_ids,omitempty"`
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
}

type LoginResponse struct {
	Token     string `json:"token"`
	ExpiresAt string `json:"expires_at"`
	User      struct {
		ID       int    `json:"id"`
		Username string `json:"username"`
		Role     string `json:"role"`
	} `json:"user"`
}

type UserInfo struct {
	ID               int    `json:"id"`
	Username         string `json:"username"`
	Email            string `json:"email"`
	Role             string `json:"role"`
	Description      string `json:"description"`
	Telegram         string `json:"telegram"`
	TelegramChatID   string `json:"telegram_chat_id,omitempty"`
	Matrix           string `json:"matrix"`
	Mastodon         string `json:"mastodon"`
	Website          string `json:"website"`
	EmailVerified    bool   `json:"email_verified"`
	TelegramVerified bool   `json:"telegram_verified"`
	CreatedAt        string `json:"created_at"`
}

func (c *DansalClient) get(ctx context.Context, path string, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.BaseURL+path, nil)
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
	if resp.StatusCode != http.StatusOK {
		return apiErr(resp)
	}
	return json.NewDecoder(resp.Body).Decode(out)
}

func (c *DansalClient) Login(ctx context.Context, username, password string) (*LoginResponse, error) {
	body, _ := json.Marshal(map[string]string{"username": username, "password": password})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.BaseURL+"/api/v1/login",
		bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
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
	resp, err := c.authed(ctx, http.MethodDelete, fmt.Sprintf("/api/v1/locations/%d", id), token, nil)
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

func (c *DansalClient) CreateFetchSource(ctx context.Context, rawURL, typ string, tags []string, orgID *int, token string) (int, error) {
	payload := map[string]any{
		"url":  rawURL,
		"type": typ,
		"tags": tags,
	}
	if orgID != nil {
		payload["organization_id"] = *orgID
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

func (c *DansalClient) UpdateFetchSource(ctx context.Context, id int, typ string, tags []string, danceIDs []int, orgID *int, token string) error {
	payload := map[string]any{
		"type":            typ,
		"tags":            tags,
		"dance_ids":       danceIDs,
		"organization_id": orgID,
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

func (c *DansalClient) RequestMagicLogin(ctx context.Context, identifier, channel, baseURL string) error {
	var payload map[string]string
	if strings.Contains(identifier, "@") {
		payload = map[string]string{"email": identifier}
	} else {
		payload = map[string]string{"username": identifier}
	}
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

func (c *DansalClient) UseMagicLogin(ctx context.Context, token string) (*LoginResponse, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet,
		c.BaseURL+"/api/v1/login/magic/"+token, nil)
	if err != nil {
		return nil, err
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
	Title                string      `json:"title"`
	Description          string      `json:"description,omitempty"`
	StartTime            string      `json:"start_time"`
	EndTime              string      `json:"end_time,omitempty"`
	HasBall              bool        `json:"has_ball"`
	HasWorkshop          bool        `json:"has_workshop"`
	HasFestival          bool        `json:"has_festival"`
	WorkshopDifficulty   string      `json:"workshop_difficulty,omitempty"`
	BookingURL           string      `json:"booking_url,omitempty"`
	Tags                 []string    `json:"tags,omitempty"`
	URL            string      `json:"url,omitempty"`
	OrganizationID *int        `json:"organization_id,omitempty"`
	Pricing        *Pricing    `json:"pricing,omitempty"`
	Location       EventLocReq `json:"location"`
	Dances         []int       `json:"dances,omitempty"`
}

type EventUpdateReq struct {
	Title                string      `json:"title"`
	Description          string      `json:"description,omitempty"`
	StartTime            string      `json:"start_time"`
	EndTime              string      `json:"end_time,omitempty"`
	HasBall              bool        `json:"has_ball"`
	HasWorkshop          bool        `json:"has_workshop"`
	HasFestival          bool        `json:"has_festival"`
	WorkshopDifficulty   string      `json:"workshop_difficulty,omitempty"`
	BookingURL           string      `json:"booking_url,omitempty"`
	IsCancelled          bool        `json:"is_cancelled"`
	Availability         string      `json:"availability,omitempty"`
	TicketsTotal         int         `json:"tickets_total,omitempty"`
	BookingEnabled       bool        `json:"booking_enabled,omitempty"`
	IsPublished    bool        `json:"is_published"`
	Tags           []string    `json:"tags,omitempty"`
	URL            string      `json:"url,omitempty"`
	OrganizationID *int        `json:"organization_id,omitempty"`
	Pricing        *Pricing    `json:"pricing,omitempty"`
	Location       EventLocReq `json:"location"`
	Musicians      []int       `json:"musicians,omitempty"`
	Dances         []int       `json:"dances,omitempty"`
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

func (c *DansalClient) ConsumeVerification(ctx context.Context, token string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet,
		c.BaseURL+"/api/v1/verify/"+token, nil)
	if err != nil {
		return err
	}
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		return fmt.Errorf("invalid")
	}
	if resp.StatusCode == http.StatusGone {
		return fmt.Errorf("expired")
	}
	if resp.StatusCode != http.StatusOK {
		return apiErr(resp)
	}
	return nil
}

type OrgMember struct {
	OrganizationID int    `json:"organization_id"`
	UserID         int    `json:"user_id"`
	Username       string `json:"username,omitempty"`
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
	ID               int    `json:"id"`
	EventID          int    `json:"event_id"`
	Type             string `json:"type"`
	City             string `json:"city"`
	Persons          int    `json:"persons"`
	Message          string `json:"message,omitempty"`
	Nickname         string `json:"nickname"`
	TelegramUsername string `json:"telegram_username,omitempty"`
	CreatedAt        string `json:"created_at"`
}

func (c *DansalClient) GetContactPosts(ctx context.Context, eventID int) ([]ContactPost, error) {
	var posts []ContactPost
	return posts, c.get(ctx, fmt.Sprintf("/api/v1/events/%d/contact-posts", eventID), &posts)
}

// CreateContactPost submits a board post and returns (telegramVerifyURL, error).
// telegramVerifyURL is non-empty only when the post was submitted with a Telegram username.
// baseURL is forwarded so the API can generate correct public links in emails.
func (c *DansalClient) CreateContactPost(ctx context.Context, eventID int, post map[string]any, baseURL string) (string, error) {
	body, _ := json.Marshal(post)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		c.BaseURL+fmt.Sprintf("/api/v1/events/%d/contact-posts", eventID),
		bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	if baseURL != "" {
		req.Header.Set("X-Base-URL", baseURL)
	}
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		return "", apiErr(resp)
	}
	var result struct {
		TelegramVerifyURL string `json:"telegram_verify_url"`
	}
	json.NewDecoder(resp.Body).Decode(&result)
	return result.TelegramVerifyURL, nil
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

func (c *DansalClient) DeleteContactPostByToken(ctx context.Context, token string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.BaseURL+"/api/v1/contact-posts/delete/"+token, nil)
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
func (c *DansalClient) ContactPoster(ctx context.Context, id int, email, telegram, message, baseURL string) (string, error) {
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

// ── users & invites ───────────────────────────────────────────────────────────

type InviteLink struct {
	ID        int    `json:"id"`
	Token     string `json:"token"`
	Role      string `json:"role"`
	OrgID     *int   `json:"org_id,omitempty"`
	ExpiresAt string `json:"expires_at"`
	UsedAt    string `json:"used_at,omitempty"`
	CreatedAt string `json:"created_at"`
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
	return d, json.NewDecoder(resp.Body).Decode(&d)
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

func (c *DansalClient) CreateInvite(ctx context.Context, role string, orgID *int, token string) (InviteLink, error) {
	payload := map[string]any{"role": role}
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

func (c *DansalClient) VerifyContactPost(ctx context.Context, token string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.BaseURL+"/api/v1/contact-posts/verify/"+token, nil)
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
	TelegramBotToken  string `json:"telegram_bot_token"`
	TelegramBotName   string `json:"telegram_bot_name"`
	MatrixHomeserver  string `json:"matrix_homeserver"`
	MatrixAccessToken string `json:"matrix_access_token"`
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

// DansalInfo mirrors the ServiceInfo struct from the dansal API.
type DansalInfo struct {
	Service         string `json:"service"`
	Version         string `json:"version"`
	BuildTime       string `json:"build_time"`
	TotalEvents     int    `json:"total_events"`
	PublishedEvents int    `json:"published_events"`
	UpcomingEvents  int    `json:"upcoming_events"`
	DBSizeBytes     int64  `json:"db_size_bytes"`
	ImagesSizeBytes int64  `json:"images_size_bytes"`
}

func (c *DansalClient) GetDansalInfo(ctx context.Context) (DansalInfo, error) {
	var info DansalInfo
	err := c.get(ctx, "/api/v1/info", &info)
	return info, err
}
