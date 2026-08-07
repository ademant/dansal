package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"math/rand"
	"mime/multipart"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"
)

// cacheEntry holds a cached value and when it was fetched.
type cacheEntry[T any] struct {
	val       T
	fetchedAt time.Time
	etag      string // ETag from the last successful response, for conditional GETs
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

	// InternalSecret, when set, is sent as X-Dansal-Internal on backend API
	// calls so dansal's rate limiter doesn't bucket all of dansal-web's
	// requests under the shared loopback IP.
	InternalSecret string

	mu             sync.Mutex
	orgsCache      cacheEntry[[]Organization]
	dancesCache    cacheEntry[[]Dance]
	tagsCache      cacheEntry[[]Tag]
	musiciansCache cacheEntry[[]Musician]
	locationsCache cacheEntry[[]Location]
	eventsCache    cacheEntry[[]Event]
	eventsTotal    int // total future published events server-side, from X-Total-Count; see EventsTotal
	infoCache      cacheEntry[DansalInfo]
}

// setInternalHeader marks req as an internal backend call so dansal's rate
// limiter exempts it (see RateLimitMiddleware/ConnLimitMiddleware in
// cmd/dansal/main.go). Not set on Login/CertLogin/UseMagicLogin, which must
// remain subject to per-visitor login rate limiting.
func (c *DansalClient) setInternalHeader(req *http.Request) {
	if c.InternalSecret != "" {
		req.Header.Set("X-Dansal-Internal", c.InternalSecret)
	}
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
	etag := entry.etag // GetEvents' conditional-GET ETag survives a cache store
	*entry = cacheEntry[T]{val: val, fetchedAt: time.Now(), etag: etag}
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

// OrgStatRecord is returned by GetOrgStats: per-org event/location/source/board-entry counts.
type OrgStatRecord struct {
	ID              int `json:"id"`
	EventCount      int `json:"event_count"`
	LocationCount   int `json:"location_count"`
	SourceCount     int `json:"source_count"`
	BoardEntryCount int `json:"board_entry_count"`
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

// RefBundle holds slow-changing reference lists needed by admin forms.
type RefBundle struct {
	Orgs        []Organization
	Locations   []Location
	Musicians   []Musician
	Dances      []Dance
	Instructors []Instructor
}

// FetchRefBundle loads all reference lists in parallel (cache hits are in-memory).
func (c *DansalClient) FetchRefBundle(ctx context.Context) RefBundle {
	var b RefBundle
	var wg sync.WaitGroup
	wg.Add(5)
	go func() { defer wg.Done(); b.Orgs, _ = c.GetOrganizations(ctx) }()
	go func() { defer wg.Done(); b.Locations, _ = c.GetLocations(ctx) }()
	go func() { defer wg.Done(); b.Musicians, _ = c.GetMusicians(ctx) }()
	go func() { defer wg.Done(); b.Dances, _ = c.GetDances(ctx) }()
	go func() { defer wg.Done(); b.Instructors, _ = c.GetInstructors(ctx) }()
	wg.Wait()
	return b
}

// apiHTTPError carries the dansal API's error message separately from its
// formatted Error() string, so callers that want to show the raw message to
// an end user (#973) don't have to parse it back out of "dansal API 400: ...".
type apiHTTPError struct {
	StatusCode int
	Message    string
	ErrorID    string
}

func (e *apiHTTPError) Error() string {
	if e.ErrorID != "" {
		return fmt.Sprintf("dansal API %d: %s (error_id: %s)", e.StatusCode, e.Message, e.ErrorID)
	}
	return fmt.Sprintf("dansal API %d: %s", e.StatusCode, e.Message)
}

// apiErrUserMessage returns the user-safe message from an apiHTTPError, or ""
// for any other error type (network errors, etc. should fall back to a
// generic message rather than leaking Go error text to the browser). Not to
// be confused with apiErrorMessage(resp), which extracts straight from a
// still-open response body.
func apiErrUserMessage(err error) string {
	var ae *apiHTTPError
	if errors.As(err, &ae) {
		return ae.Message
	}
	return ""
}

func apiErr(resp *http.Response) error {
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	var body struct {
		Error   string `json:"error"`
		ErrorID string `json:"error_id"`
	}
	if json.Unmarshal(b, &body) == nil && body.Error != "" {
		return &apiHTTPError{StatusCode: resp.StatusCode, Message: body.Error, ErrorID: body.ErrorID}
	}
	if msg := strings.TrimSpace(string(b)); msg != "" {
		return &apiHTTPError{StatusCode: resp.StatusCode, Message: msg}
	}
	return fmt.Errorf("dansal API: %s", resp.Status)
}

// Sentinel errors returned for specific HTTP statuses. Their messages keep the
// exact strings older callers compared with err.Error() (e.g. "expired"), so
// errors.Is comparisons and string checks both keep working.
var (
	errNotFound  = errors.New("not found")
	errForbidden = errors.New("forbidden")
	errExpired   = errors.New("expired")
	errInvalid   = errors.New("invalid")
)

// do runs a single request against the dansal API. It sets the internal
// header, adds the Bearer token when token != "", sends an application/json
// body when body != nil, checks the response status against okStatus (default
// 200 OK), and decodes the JSON body into out when out != nil. This collapses
// the ~90 identical authed → status-check → decode blocks that previously made
// up most of the client.
func (c *DansalClient) do(ctx context.Context, method, path, token string, body []byte, out any, okStatus ...int) error {
	var bodyReader io.Reader = http.NoBody
	if body != nil {
		bodyReader = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.BaseURL+path, bodyReader)
	if err != nil {
		return err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	c.setInternalHeader(req)
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if len(okStatus) == 0 {
		okStatus = []int{http.StatusOK}
	}
	ok := false
	for _, s := range okStatus {
		if resp.StatusCode == s {
			ok = true
			break
		}
	}
	if !ok {
		return apiErr(resp)
	}
	if out != nil {
		if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
			return err
		}
	}
	return nil
}

type Event struct {
	ID                     int              `json:"id"`
	Title                  string           `json:"title"`
	Description            string           `json:"description"`
	StartTime              string           `json:"start_time"`
	EndTime                string           `json:"end_time"`
	HasBall                bool             `json:"has_ball"`
	HasWorkshop            bool             `json:"has_workshop"`
	HasFestival            bool             `json:"has_festival"`
	WorkshopDifficulty     string           `json:"workshop_difficulty,omitempty"`
	IsCancelled            bool             `json:"is_cancelled"`
	Tags                   []string         `json:"tags"`
	IsPublished            bool             `json:"is_published"`
	ShortCode              string           `json:"short_code"`
	URL                    string           `json:"url,omitempty"`
	ImageURL               string           `json:"image_url,omitempty"`
	ImageAIGenerated       bool             `json:"image_ai_generated,omitempty"`
	OrganizationID         *int             `json:"organization_id,omitempty"`
	LocationID             *int             `json:"location_id,omitempty"`
	Location               *Location        `json:"location,omitempty"`
	Attributes             map[string]bool  `json:"attributes,omitempty"`
	FloorCondition         string           `json:"floor_condition,omitempty"`
	ContactName            string           `json:"contact_name,omitempty"`
	ContactEmail           string           `json:"contact_email,omitempty"`
	BookingURL             string           `json:"booking_url,omitempty"`
	Availability           string           `json:"availability,omitempty"`
	TicketsTotal           int              `json:"tickets_total,omitempty"`
	BookingEnabled         bool             `json:"booking_enabled,omitempty"`
	Food                   string           `json:"food,omitempty"`
	Drink                  string           `json:"drink,omitempty"`
	Pricing                *Pricing         `json:"pricing,omitempty"`
	Locations              []Location       `json:"locations,omitempty"`
	Musicians              []Musician       `json:"musicians,omitempty"`
	Instructors            []Instructor     `json:"instructors,omitempty"`
	DanceNames             []string         `json:"dance_names,omitempty"`
	Timetable              []TimetableEntry `json:"timetable,omitempty"`
	CreatedAt              string           `json:"created_at"`
	Source                 string           `json:"source,omitempty"`
	SourceURL              string           `json:"source_url,omitempty"`
	ChangedAt              string           `json:"changed_at,omitempty"`
	ChangedBy              string           `json:"changed_by,omitempty"`
	FetchSourceID          int              `json:"fetch_source_id,omitempty"`
	Editable               bool             `json:"editable,omitempty"`
	Cancelable             bool             `json:"cancelable,omitempty"`
	Deletable              bool             `json:"deletable,omitempty"`
	CreatedByID            *int             `json:"created_by_id,omitempty"`
	SeriesID               *int             `json:"series_id,omitempty"`
	NeedsDuplicateReview   bool             `json:"needs_duplicate_review,omitempty"`
	DuplicateOfID          *int             `json:"duplicate_of_id,omitempty"`
	PreviousStartTime      string           `json:"previous_start_time,omitempty"`
	SuggesterEmail         string           `json:"suggester_email,omitempty"`
	SuggesterName          string           `json:"suggester_name,omitempty"`
	PendingEditJSON        string           `json:"pending_edit_json,omitempty"`
	PendingEditSubmittedAt string           `json:"pending_edit_submitted_at,omitempty"`
	EmailVerified          bool             `json:"email_verified"`
}

type Dance struct {
	ID   int    `json:"id"`
	Name string `json:"name"`
}

type Tag struct {
	Slug       string `json:"slug"`
	Name       string `json:"name"`
	Category   string `json:"category"`
	EventCount int    `json:"event_count,omitempty"`
}

// City is a town with geo-tagged venues and upcoming events (#965).
type City struct {
	Town           string   `json:"town"`
	Slug           string   `json:"slug"`
	LocationCount  int      `json:"location_count"`
	EventCount     int      `json:"event_count"` // future published events
	Latitude       *float64 `json:"latitude,omitempty"`
	Longitude      *float64 `json:"longitude,omitempty"`
	NextEventID    int      `json:"next_event_id,omitempty"`
	NextEventTitle string   `json:"next_event_title,omitempty"`
	NextEventStart string   `json:"next_event_start,omitempty"`
}

type TimetableEntry struct {
	ID             int    `json:"id"`
	StartTime      string `json:"start_time"`
	EndTime        string `json:"end_time"`
	Title          string `json:"title"`
	Description    string `json:"description,omitempty"`
	Room           string `json:"room,omitempty"`
	EntryType      string `json:"entry_type,omitempty"`
	EntryDate      string `json:"entry_date,omitempty"`
	LocationID     *int   `json:"location_id,omitempty"`
	LocationName   string `json:"location_name,omitempty"`
	MusicianID     *int   `json:"musician_id,omitempty"`
	MusicianName   string `json:"musician_name,omitempty"`
	InstructorID   *int   `json:"instructor_id,omitempty"`
	InstructorName string `json:"instructor_name,omitempty"`
}

// TimetablePanel is one timetable entry positioned within a TimetableGrid
// column, in pixels relative to the grid's shared time axis (#887).
type TimetablePanel struct {
	Entry    TimetableEntry
	TopPx    float64
	HeightPx float64
}

// TimetableGridColumn is one room's positioned entries, for the multi-room
// calendar grid layout on /event/{id} (#886, #887; see timetableGrid in
// frontend.go). IsOther marks the shared fallback column for entries with
// neither a LocationID nor a free-text Room.
type TimetableGridColumn struct {
	Label   string
	IsOther bool
	Panels  []TimetablePanel
}

// TimetableGridMark is one hour/half-hour gridline on the shared time axis.
type TimetableGridMark struct {
	Label string
	TopPx float64
}

// TimetableGrid is the full computed layout for the multi-room timetable
// calendar grid: columns of positioned panels, sharing one time axis whose
// range only spans the timetable's own earliest start to latest end (#887).
type TimetableGrid struct {
	Columns  []TimetableGridColumn
	Marks    []TimetableGridMark
	HeightPx float64
}

type Instructor struct {
	ID        int    `json:"id"`
	Name      string `json:"name"`
	Bio       string `json:"bio,omitempty"`
	Website   string `json:"website,omitempty"`
	Email     string `json:"email,omitempty"`
	Mastodon  string `json:"mastodon,omitempty"`
	Instagram string `json:"instagram,omitempty"`
	Facebook  string `json:"facebook,omitempty"`
	AvatarURL string `json:"avatar_url,omitempty"`
	CreatedAt string `json:"created_at,omitempty"`
	UpdatedAt int64  `json:"updated_at,omitempty"`
	UpdatedBy string `json:"updated_by,omitempty"`

	FutureEventCount int `json:"future_event_count,omitempty"`
	PastEventCount   int `json:"past_event_count,omitempty"`
}

type Organization struct {
	ID             int        `json:"id"`
	Name           string     `json:"name"`
	Description    string     `json:"description"`
	ActorName      string     `json:"actor_name,omitempty"`
	Website        string     `json:"website,omitempty"`
	Instagram      string     `json:"instagram,omitempty"`
	Mastodon       string     `json:"mastodon,omitempty"`
	Facebook       string     `json:"facebook,omitempty"`
	ContactEmail   string     `json:"contact_email,omitempty"`
	ContactName    string     `json:"contact_name,omitempty"`
	WikidataID     string     `json:"wikidata_id,omitempty"`
	CreatedAt      string     `json:"created_at"`
	UpdatedAt      int64      `json:"updated_at,omitempty"`
	UpdatedBy      string     `json:"updated_by,omitempty"`
	ImageURL       string     `json:"image_url,omitempty"`
	ImageMediaType string     `json:"image_media_type,omitempty"`
	AvatarURL      string     `json:"avatar_url,omitempty"`
	NotesMd        string     `json:"notes_md,omitempty"`
	FetchSourceID  *int       `json:"fetch_source_id,omitempty"`
	ChatLinks      []ChatLink `json:"chat_links,omitempty"`
}

// ChatLink is one entry in an organization's chat_links: a community
// chat/list invite (Telegram, Signal, WhatsApp, Threema, Matrix room, or
// Mailman/Postorius mailing list) — distinct from the identity-linking
// mastodon/instagram/facebook fields (see #925).
type ChatLink struct {
	Platform string `json:"platform"`
	URL      string `json:"url"`
}

// ChatPlatformInfo is the web-side display registry entry for a chat_links
// platform: slug (stored/matched value) plus its display label.
type ChatPlatformInfo struct {
	Slug  string
	Label string
}

// chatLinkPlatformOrder is the canonical display order for organization
// chat_links platforms. Must stay in sync with validChatLinkPlatforms in
// cmd/dansal/chat_links.go — adding a platform is one entry in each, no DB
// migration needed (#925).
var chatLinkPlatformOrder = []ChatPlatformInfo{
	{Slug: "telegram", Label: "Telegram"},
	{Slug: "signal", Label: "Signal"},
	{Slug: "whatsapp", Label: "WhatsApp"},
	{Slug: "threema", Label: "Threema"},
	{Slug: "matrix", Label: "Matrix"},
	{Slug: "mailing_list", Label: "Mailing list"},
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
	Email        string `json:"email,omitempty"`
	ImageURL     string `json:"image_url,omitempty"`
	AvatarURL    string `json:"avatar_url,omitempty"`
	CreatedAt    string `json:"created_at,omitempty"`
	UpdatedAt    int64  `json:"updated_at,omitempty"`
	UpdatedBy    string `json:"updated_by,omitempty"`

	FutureEventCount int `json:"future_event_count,omitempty"`
	PastEventCount   int `json:"past_event_count,omitempty"`
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
	Geohash         string          `json:"geohash,omitempty"`
	CreatedAt       string          `json:"created_at"`
	OrganizationIDs []int           `json:"organization_ids,omitempty"`
	NotesMd         string          `json:"notes_md,omitempty"`
	Attributes      map[string]bool `json:"attributes,omitempty"`
	Parking         string          `json:"parking,omitempty"`
	FloorCondition  string          `json:"floor_condition,omitempty"`
	NoStreetShoes   bool            `json:"no_street_shoes,omitempty"`
	Aliases         []string        `json:"aliases,omitempty"`
	UpdatedAt       int64           `json:"updated_at,omitempty"`
	UpdatedBy       string          `json:"updated_by,omitempty"`

	// A room is a child Location with ParentID set, inheriting
	// address/coordinates from its parent (#687) rather than copying them.
	ParentID        *int       `json:"parent_id,omitempty"`
	ParentName      string     `json:"parent_name,omitempty"`
	ParentShortName string     `json:"parent_short_name,omitempty"`
	Children        []Location `json:"children,omitempty"`

	// Capacity/size are informal, display-only hints (#875) — not inherited
	// from a parent since they describe the room itself, not the building.
	Capacity *int `json:"capacity,omitempty"`
	SizeSqm  *int `json:"size_sqm,omitempty"`

	// A room's position (0-1 percentage) on its building's site-plan image
	// (#877). SitePlanURL is set on the building itself when it has an
	// uploaded site-plan image.
	PlanX       *float64 `json:"plan_x,omitempty"`
	PlanY       *float64 `json:"plan_y,omitempty"`
	SitePlanURL string   `json:"site_plan_url,omitempty"`

	FutureEventCount int `json:"future_event_count,omitempty"`
	PastEventCount   int `json:"past_event_count,omitempty"`
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
	KuferConfig    string   `json:"kufer_config,omitempty"`
}

// KuferConfig mirrors cmd/dansal's type of the same name — the JSON shape
// stored in fetch_sources.kufer_config for type="kufer" sources (#932).
type KuferConfig struct {
	Keywords     []string `json:"keywords"`
	SearchURL    string   `json:"search_url,omitempty"`
	SearchMethod string   `json:"search_method,omitempty"`
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
	TOTPEnabled      bool   `json:"totp_enabled"`
	UserMetadata     string `json:"user_metadata,omitempty"`
	CreatedAt        string `json:"created_at"`
}

// getWithHeader is the shared GET retry loop: 2 attempts with 50–150 ms
// jitter between them, 404 mapped to errNotFound, and headerHook (when set)
// called with the response headers on success. get and getWithTotal differ
// only in what they capture from the headers.
func (c *DansalClient) getWithHeader(ctx context.Context, path string, headerHook func(http.Header), out any) error {
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
		c.setInternalHeader(req)
		resp, err := c.HTTP.Do(req)
		if err != nil {
			lastErr = err
			continue // network/timeout error — retry
		}
		defer resp.Body.Close()
		if resp.StatusCode == http.StatusNotFound {
			return errNotFound
		}
		if resp.StatusCode != http.StatusOK {
			return apiErr(resp) // HTTP error — not retryable
		}
		if headerHook != nil {
			headerHook(resp.Header)
		}
		return json.NewDecoder(resp.Body).Decode(out)
	}
	return lastErr
}

func (c *DansalClient) get(ctx context.Context, path string, out any) error {
	return c.getWithHeader(ctx, path, nil, out)
}

// getWithTotal is get plus the server's X-Total-Count header. The count
// is captured before the body is decoded; a missing or non-numeric value
// leaves total at 0.
func (c *DansalClient) getWithTotal(ctx context.Context, path string, out any) (int, error) {
	var total int
	err := c.getWithHeader(ctx, path, func(h http.Header) {
		if v := h.Get("X-Total-Count"); v != "" {
			if n, err := strconv.Atoi(v); err == nil {
				total = n
			} else {
				log.Printf("dansal client: invalid X-Total-Count %q", v)
			}
		}
	}, out)
	return total, err
}

// getConditional makes a GET with an optional If-None-Match header.
// Returns (newETag, notModified, responseHeaders, error). When notModified is true, out is not written.
func (c *DansalClient) getConditional(ctx context.Context, path, etag string, out any) (string, bool, http.Header, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.BaseURL+path, nil)
	if err != nil {
		return etag, false, nil, err
	}
	if etag != "" {
		req.Header.Set("If-None-Match", etag)
	}
	c.setInternalHeader(req)
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return etag, false, nil, err
	}
	defer resp.Body.Close()
	newETag := resp.Header.Get("Etag")
	if newETag == "" {
		newETag = etag
	}
	if resp.StatusCode == http.StatusNotModified {
		return newETag, true, resp.Header, nil
	}
	if resp.StatusCode != http.StatusOK {
		return newETag, false, resp.Header, apiErr(resp)
	}
	return newETag, false, resp.Header, json.NewDecoder(resp.Body).Decode(out)
}

// ErrTOTPRequired is returned by Login when the credentials are valid but a
// TOTP code is required to complete authentication.
var ErrTOTPRequired = fmt.Errorf("totp_required")

func (c *DansalClient) Login(ctx context.Context, email, password, totpCode, clientIP, userAgent string) (*LoginResponse, error) {
	payload := map[string]string{"email": email, "password": password}
	if totpCode != "" {
		payload["totp_code"] = totpCode
	}
	body, _ := json.Marshal(payload)
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
		var body struct {
			Error string `json:"error"`
		}
		json.NewDecoder(resp.Body).Decode(&body)
		if body.Error == "totp_required" {
			return nil, ErrTOTPRequired
		}
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
	c.setInternalHeader(req)
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return err
	}
	resp.Body.Close()
	return nil
}

// bulkSetEvents posts a bulk event-mutation payload and invalidates the shared
// events cache on success. The API answers 204 or 200 for all three variants.
func (c *DansalClient) bulkSetEvents(ctx context.Context, path string, payload map[string]any, token string) error {
	body, _ := json.Marshal(payload)
	if err := c.do(ctx, http.MethodPost, path, token, body, nil, http.StatusNoContent, http.StatusOK); err != nil {
		return err
	}
	c.invalidateEvents()
	return nil
}

func (c *DansalClient) BulkSetEventLocation(ctx context.Context, ids []int, locationID int, token string) error {
	return c.bulkSetEvents(ctx, "/api/v1/events/bulk-set-location", map[string]any{"ids": ids, "location_id": locationID}, token)
}

func (c *DansalClient) BulkSetEventTime(ctx context.Context, ids []int, startTime, endTime, token string) error {
	return c.bulkSetEvents(ctx, "/api/v1/events/bulk-set-time", map[string]any{"ids": ids, "start_time": startTime, "end_time": endTime}, token)
}

func (c *DansalClient) BulkSetEventAttributes(ctx context.Context, payload map[string]any, token string) error {
	return c.bulkSetEvents(ctx, "/api/v1/events/bulk-set-attributes", payload, token)
}

// GetAllEvents fetches all published events including past ones; used by the sitemap.
func (c *DansalClient) GetAllEvents(ctx context.Context) ([]Event, error) {
	var events []Event
	return events, c.get(ctx, "/api/v1/events?is_published=true&include_past=true", &events)
}

// GetEventsFiltered fetches published events from /api/v1/events with arbitrary
// additional query parameters (e.g. start_time_after/before, tag, include_past).
func (c *DansalClient) GetEventsFiltered(ctx context.Context, params url.Values) ([]Event, error) {
	var events []Event
	return events, c.get(ctx, "/api/v1/events?"+params.Encode(), &events)
}

// GetEventsFilteredWithTotal is GetEventsFiltered plus the server's X-Total-Count
// header, so callers (e.g. /search) can tell whether the result was truncated by
// the limit param without guessing from len(events) alone.
func (c *DansalClient) GetEventsFilteredWithTotal(ctx context.Context, params url.Values) ([]Event, int, error) {
	var events []Event
	total, err := c.getWithTotal(ctx, "/api/v1/events?"+params.Encode(), &events)
	return events, total, err
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
	if after != "" {
		var events []Event
		if err := c.get(ctx, "/api/v1/events?is_published=true&start_time_after="+after, &events); err != nil {
			return nil, err
		}
		return events, nil
	}
	// The after=="" path goes through cached: fresh TTL is served from memory,
	// stale values are kept on transient fetch errors, and the fetch itself
	// uses a conditional GET so an unchanged server state costs no payload.
	return cached(&c.mu, &c.eventsCache, eventsTTL, func() ([]Event, error) {
		c.mu.Lock()
		cachedETag := c.eventsCache.etag
		c.mu.Unlock()

		var events []Event
		newETag, notModified, hdr, err := c.getConditional(ctx, "/api/v1/events?is_published=true", cachedETag, &events)
		if err != nil {
			return nil, err
		}
		c.mu.Lock()
		if notModified {
			// cached() stores the value back with a fresh fetchedAt below,
			// which is exactly the TTL extension we want.
			c.eventsCache.etag = newETag
			events = c.eventsCache.val
		} else {
			c.eventsCache.etag = newETag
			// ETag is a fingerprint of (count, max created_at), so an unchanged
			// ETag means the total is unchanged too; only update on a real refetch.
			if total, err := strconv.Atoi(hdr.Get("X-Total-Count")); err == nil {
				c.eventsTotal = total
			}
		}
		c.mu.Unlock()
		return events, nil
	})
}

// EventsTotal returns the total number of future published events server-side,
// as of the last GetEvents(ctx, "") call. Call GetEvents first to populate it.
func (c *DansalClient) EventsTotal() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.eventsTotal
}

// maxEventsPages caps how many 1000-event pages GetAllFutureEvents will fetch,
// so a data anomaly (e.g. a buggy importer flooding the table) can't turn one
// feed request into an unbounded fetch loop.
const maxEventsPages = 50

// GetAllFutureEvents fetches every future published event by looping through
// the API's offset pagination, deliberately bypassing GetEvents's shared,
// 100-event-capped cache (eventsCache) used by the index page, embeds, and
// the actor outbox. Used by feed handlers, where subscribers expect every
// upcoming event, not just the index page's first page.
func (c *DansalClient) GetAllFutureEvents(ctx context.Context) ([]Event, error) {
	var all []Event
	for page := 0; page < maxEventsPages; page++ {
		params := url.Values{"is_published": {"true"}, "limit": {"1000"}, "offset": {strconv.Itoa(page * 1000)}}
		batch, err := c.GetEventsFiltered(ctx, params)
		if err != nil {
			return nil, err
		}
		all = append(all, batch...)
		if len(batch) < 1000 {
			break
		}
	}
	return all, nil
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
	return c.do(ctx, http.MethodPost, fmt.Sprintf("/api/v1/events/%d/publish", id), token, nil, nil, http.StatusNoContent)
}

func (c *DansalClient) CancelEvent(ctx context.Context, id int, token string) error {
	return c.do(ctx, http.MethodPost, fmt.Sprintf("/api/v1/events/%d/cancel", id), token, nil, nil, http.StatusNoContent)
}

func (c *DansalClient) AssignEventOrg(ctx context.Context, id, orgID int, token string) error {
	body, _ := json.Marshal(map[string]int{"org_id": orgID})
	return c.do(ctx, http.MethodPost, fmt.Sprintf("/api/v1/events/%d/assign-org", id), token, body, nil, http.StatusNoContent)
}

func (c *DansalClient) GetOrganizations(ctx context.Context) ([]Organization, error) {
	return cached(&c.mu, &c.orgsCache, orgsTTL, func() ([]Organization, error) {
		var orgs []Organization
		return orgs, c.get(ctx, "/api/v1/organizations?limit=1000", &orgs)
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

// getEvents fetches /api/v1/events with the given query string. The authed
// variant (token != "") also includes events the caller may not yet publish;
// the public variant goes through the retrying get path.
func (c *DansalClient) getEvents(ctx context.Context, query, token string, out any) error {
	path := "/api/v1/events?" + query
	if token != "" {
		return c.do(ctx, http.MethodGet, path, token, nil, out)
	}
	return c.get(ctx, path, out)
}

func (c *DansalClient) GetEventsByMusician(ctx context.Context, musicianID int, token string) ([]Event, error) {
	var events []Event
	return events, c.getEvents(ctx, fmt.Sprintf("musician_id=%d", musicianID), token, &events)
}

func (c *DansalClient) GetPublicEventsByMusician(ctx context.Context, musicianID int) ([]Event, error) {
	var events []Event
	return events, c.getEvents(ctx, fmt.Sprintf("musician_id=%d", musicianID), "", &events)
}

func (c *DansalClient) GetAllPublicEventsByMusician(ctx context.Context, musicianID int) ([]Event, error) {
	var events []Event
	return events, c.getEvents(ctx, fmt.Sprintf("musician_id=%d&include_past=true", musicianID), "", &events)
}

func (c *DansalClient) GetEventsByInstructor(ctx context.Context, instructorID int, token string) ([]Event, error) {
	var events []Event
	return events, c.getEvents(ctx, fmt.Sprintf("instructor_id=%d", instructorID), token, &events)
}

func (c *DansalClient) GetPublicEventsByInstructor(ctx context.Context, instructorID int) ([]Event, error) {
	var events []Event
	return events, c.getEvents(ctx, fmt.Sprintf("instructor_id=%d", instructorID), "", &events)
}

func (c *DansalClient) GetAllPublicEventsByInstructor(ctx context.Context, instructorID int) ([]Event, error) {
	var events []Event
	return events, c.getEvents(ctx, fmt.Sprintf("instructor_id=%d&include_past=true", instructorID), "", &events)
}

func (c *DansalClient) GetMusicians(ctx context.Context) ([]Musician, error) {
	return cached(&c.mu, &c.musiciansCache, musiciansTTL, func() ([]Musician, error) {
		var ms []Musician
		return ms, c.get(ctx, "/api/v1/musicians?limit=1000&with_event_counts=true", &ms)
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
	if err := c.do(ctx, http.MethodPut, fmt.Sprintf("/api/v1/musicians/%d", id), token, body, nil); err != nil {
		return err
	}
	c.invalidateMusicians()
	return nil
}

func (c *DansalClient) DeleteMusician(ctx context.Context, id int, token string) error {
	if err := c.do(ctx, http.MethodDelete, fmt.Sprintf("/api/v1/musicians/%d", id), token, nil, nil, http.StatusNoContent); err != nil {
		return err
	}
	c.invalidateMusicians()
	return nil
}

func (c *DansalClient) GetInstructors(ctx context.Context) ([]Instructor, error) {
	var out []Instructor
	return out, c.get(ctx, "/api/v1/instructors?limit=1000&with_event_counts=true", &out)
}

func (c *DansalClient) GetInstructor(ctx context.Context, id int) (Instructor, error) {
	var out Instructor
	return out, c.get(ctx, fmt.Sprintf("/api/v1/instructors/%d", id), &out)
}

func (c *DansalClient) CreateInstructor(ctx context.Context, inst Instructor, token string) (Instructor, error) {
	body, _ := json.Marshal(inst)
	resp, err := c.authed(ctx, http.MethodPost, "/api/v1/instructors", token, body)
	if err != nil {
		return Instructor{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		return Instructor{}, apiErr(resp)
	}
	var out Instructor
	return out, json.NewDecoder(resp.Body).Decode(&out)
}

func (c *DansalClient) UpdateInstructor(ctx context.Context, id int, inst Instructor, token string) error {
	body, _ := json.Marshal(inst)
	return c.do(ctx, http.MethodPut, fmt.Sprintf("/api/v1/instructors/%d", id), token, body, nil)
}

func (c *DansalClient) DeleteInstructor(ctx context.Context, id int, token string) error {
	return c.do(ctx, http.MethodDelete, fmt.Sprintf("/api/v1/instructors/%d", id), token, nil, nil, http.StatusNoContent)
}

func (c *DansalClient) GetEventInstructors(ctx context.Context, eventID int) ([]Instructor, error) {
	var out []Instructor
	return out, c.get(ctx, fmt.Sprintf("/api/v1/events/%d/instructors", eventID), &out)
}

func (c *DansalClient) SetEventInstructors(ctx context.Context, eventID int, ids []int, token string) error {
	body, _ := json.Marshal(ids)
	return c.do(ctx, http.MethodPut, fmt.Sprintf("/api/v1/events/%d/instructors", eventID), token, body, nil)
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
	if err := c.do(ctx, http.MethodDelete, path, token, nil, nil, http.StatusNoContent); err != nil {
		return err
	}
	c.invalidateLocations()
	return nil
}

func (c *DansalClient) UploadMusicianImage(ctx context.Context, id int, data []byte, filename, token string) error {
	return c.uploadAvatar(ctx, fmt.Sprintf("/api/v1/musician-images/%d", id), data, filename, token)
}

func (c *DansalClient) UploadOrgImage(ctx context.Context, id int, data []byte, filename, token string) error {
	return c.uploadAvatar(ctx, fmt.Sprintf("/api/v1/org-images/%d", id), data, filename, token)
}

func (c *DansalClient) UploadLocationSitePlan(ctx context.Context, id int, data []byte, filename, token string) error {
	if err := c.uploadAvatar(ctx, fmt.Sprintf("/api/v1/locations/%d/site-plan", id), data, filename, token); err != nil {
		return err
	}
	c.invalidateLocations()
	return nil
}

func (c *DansalClient) DeleteLocationSitePlan(ctx context.Context, id int, token string) error {
	if err := c.do(ctx, http.MethodDelete, fmt.Sprintf("/api/v1/locations/%d/site-plan", id), token, nil, nil, http.StatusNoContent); err != nil {
		return err
	}
	c.invalidateLocations()
	return nil
}

// UpdateLocationPlanPosition saves a room's position (0-1 percentage) on its
// building's site-plan image (#877). Marshals its own minimal body rather
// than going through UpdateLocation, which marshals the full Location struct
// — most of its string fields lack omitempty, so a sparse Location{PlanX,
// PlanY} literal would still serialize other fields as "" and merge-patch
// them over the room's real values (#880).
func (c *DansalClient) UpdateLocationPlanPosition(ctx context.Context, id int, x, y float64, token string) error {
	body, _ := json.Marshal(struct {
		PlanX *float64 `json:"plan_x"`
		PlanY *float64 `json:"plan_y"`
	}{PlanX: &x, PlanY: &y})
	req, err := http.NewRequestWithContext(ctx, http.MethodPatch, fmt.Sprintf("%s/api/v1/locations/%d", c.BaseURL, id), bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/merge-patch+json")
	req.Header.Set("Authorization", "Bearer "+token)
	c.setInternalHeader(req)
	resp, err := c.HTTP.Do(req)
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
	c.setInternalHeader(req)
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

func (c *DansalClient) CreateFetchSource(ctx context.Context, rawURL, typ string, tags []string, orgID *int, templateID *int, templateMode, templateData string, kuferConfig string, token string) (int, error) {
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
	if kuferConfig != "" {
		payload["kufer_config"] = kuferConfig
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
	var sources []FetchSource
	return sources, c.do(ctx, http.MethodGet, "/api/v1/fetchurl", token, nil, &sources)
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

func (c *DansalClient) UpdateFetchSource(ctx context.Context, id int, typ string, tags []string, danceIDs []int, orgID *int, templateID *int, templateMode, templateData string, kuferConfig string, token string) error {
	payload := map[string]any{
		"type":            typ,
		"tags":            tags,
		"dance_ids":       danceIDs,
		"organization_id": orgID,
		"template_id":     templateID,
		"template_mode":   templateMode,
		"template_data":   templateData,
		"kufer_config":    kuferConfig,
	}
	body, _ := json.Marshal(payload)
	return c.do(ctx, http.MethodPatch, fmt.Sprintf("/api/v1/fetchurl/%d", id), token, body, nil)
}

func (c *DansalClient) GetLocations(ctx context.Context) ([]Location, error) {
	return cached(&c.mu, &c.locationsCache, locationsTTL, func() ([]Location, error) {
		var locs []Location
		return locs, c.get(ctx, "/api/v1/locations?limit=1000", &locs)
	})
}

// GetLocationsWithEventCounts fetches all locations with future/past event
// counts; used by the /embed/locations map.
func (c *DansalClient) GetLocationsWithEventCounts(ctx context.Context) ([]Location, error) {
	var locs []Location
	return locs, c.get(ctx, "/api/v1/locations?with_event_counts=true&limit=1000", &locs)
}

func (c *DansalClient) GetLocationEventCounts(ctx context.Context, token string) (map[int]int, error) {
	var counts map[int]int
	return counts, c.do(ctx, http.MethodGet, "/api/v1/locations/event-counts", token, nil, &counts)
}

func (c *DansalClient) GetLocation(ctx context.Context, id int) (Location, error) {
	var loc Location
	if err := c.get(ctx, fmt.Sprintf("/api/v1/locations/%d", id), &loc); err != nil {
		return Location{}, err
	}
	return loc, nil
}

// LocationConflictError is returned by CreateLocation when the API responds
// 409 because a location with the same OSM place already exists.
type LocationConflictError struct {
	ExistingID int
}

func (e *LocationConflictError) Error() string {
	return fmt.Sprintf("location already exists (id %d)", e.ExistingID)
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
	if resp.StatusCode == http.StatusConflict {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		var body struct {
			ExistingID int `json:"existing_id"`
		}
		json.Unmarshal(b, &body)
		return Location{}, &LocationConflictError{ExistingID: body.ExistingID}
	}
	if resp.StatusCode != http.StatusCreated {
		return Location{}, apiErr(resp)
	}
	var results []struct {
		Location Location `json:"location"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&results); err != nil || len(results) == 0 {
		return Location{}, fmt.Errorf("unexpected response from create location")
	}
	c.invalidateLocations()
	return results[0].Location, nil
}

func (c *DansalClient) UpdateLocation(ctx context.Context, id int, loc Location, token string) error {
	body, _ := json.Marshal(loc)
	req, err := http.NewRequestWithContext(ctx, http.MethodPatch, fmt.Sprintf("%s/api/v1/locations/%d", c.BaseURL, id), bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/merge-patch+json")
	req.Header.Set("Authorization", "Bearer "+token)
	c.setInternalHeader(req)
	resp, err := c.HTTP.Do(req)
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

// PatchLocationAttrs sends a raw JSON merge-patch to PATCH /api/v1/locations/{id}.
// body must be valid JSON; only the fields it contains are updated.
func (c *DansalClient) PatchLocationAttrs(ctx context.Context, id int, body []byte, token string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodPatch, fmt.Sprintf("%s/api/v1/locations/%d", c.BaseURL, id), bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/merge-patch+json")
	req.Header.Set("Authorization", "Bearer "+token)
	c.setInternalHeader(req)
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return apiErr(resp)
	}
	c.invalidateLocations()
	return nil
}

func (c *DansalClient) GetLocationChildren(ctx context.Context, locationID int) ([]Location, error) {
	var children []Location
	if err := c.get(ctx, fmt.Sprintf("/api/v1/locations/%d/children", locationID), &children); err != nil {
		return nil, err
	}
	return children, nil
}

func (c *DansalClient) CreateLocationChild(ctx context.Context, locationID int, name, floorCondition string, capacity, sizeSqm *int, token string) (Location, error) {
	body, _ := json.Marshal(map[string]any{"name": name, "floor_condition": floorCondition, "capacity": capacity, "size_sqm": sizeSqm})
	var child Location
	return child, c.do(ctx, http.MethodPost, fmt.Sprintf("/api/v1/locations/%d/children", locationID), token, body, &child, http.StatusCreated)
}

// DeleteLocationChild removes a child location — a room is just a location,
// so this is the same DELETE /api/v1/locations/{id} endpoint used everywhere else.
func (c *DansalClient) DeleteLocationChild(ctx context.Context, childID int, token string) error {
	return c.do(ctx, http.MethodDelete, fmt.Sprintf("/api/v1/locations/%d", childID), token, nil, nil, http.StatusNoContent)
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

type FetchRunResult struct {
	Count     int
	New       int
	Updated   int
	Unchanged int
}

func (c *DansalClient) RunFetchSource(ctx context.Context, id int, token string) (FetchRunResult, error) {
	resp, err := c.authed(ctx, http.MethodPost, fmt.Sprintf("/api/v1/fetchurl/%d/fetch", id), token, nil)
	if err != nil {
		return FetchRunResult{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusCreated {
		return FetchRunResult{}, apiErr(resp)
	}
	var body struct {
		Events    []json.RawMessage `json:"events"`
		New       int               `json:"new"`
		Updated   int               `json:"updated"`
		Unchanged int               `json:"unchanged"`
	}
	if err := c.do(ctx, http.MethodPost, fmt.Sprintf("/api/v1/fetchurl/%d/fetch", id), token, nil, &body, http.StatusOK, http.StatusCreated); err != nil {
		return FetchRunResult{}, err
	}
	return FetchRunResult{Count: len(body.Events), New: body.New, Updated: body.Updated, Unchanged: body.Unchanged}, nil
}

func (c *DansalClient) BulkDeleteFetchSources(ctx context.Context, ids []int, token string) error {
	body, _ := json.Marshal(map[string]any{"ids": ids})
	return c.do(ctx, http.MethodPost, "/api/v1/fetchurl/bulk-delete", token, body, nil, http.StatusNoContent)
}

func (c *DansalClient) BulkRunFetchSources(ctx context.Context, ids []int, token string) error {
	body, _ := json.Marshal(map[string]any{"ids": ids})
	return c.do(ctx, http.MethodPost, "/api/v1/fetchurl/bulk-fetch", token, body, nil, http.StatusOK, http.StatusCreated)
}

func (c *DansalClient) BulkAssignFetchSourceOrg(ctx context.Context, ids []int, orgID *int, token string) error {
	body, _ := json.Marshal(map[string]any{"ids": ids, "organization_id": orgID})
	return c.do(ctx, http.MethodPost, "/api/v1/fetchurl/bulk-assign-org", token, body, nil, http.StatusNoContent)
}

func (c *DansalClient) UnassignLocationOrg(ctx context.Context, locationID, orgID int, token string) error {
	body, _ := json.Marshal(map[string]int{"location_id": locationID, "organization_id": orgID})
	if err := c.do(ctx, http.MethodPost, "/api/v1/locations/unassign-org", token, body, nil, http.StatusNoContent); err != nil {
		return err
	}
	c.invalidateLocations()
	return nil
}

func (c *DansalClient) BulkAssignLocationOrg(ctx context.Context, ids []int, orgID *int, token string) error {
	body, _ := json.Marshal(map[string]any{"ids": ids, "organization_id": orgID})
	if err := c.do(ctx, http.MethodPost, "/api/v1/locations/bulk-assign-org", token, body, nil, http.StatusNoContent); err != nil {
		return err
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
	var u UserInfo
	return u, c.do(ctx, http.MethodGet, fmt.Sprintf("/api/v1/users/%d", id), token, nil, &u)
}

func (c *DansalClient) UpdateUser(ctx context.Context, id int, fields map[string]string, token string) error {
	body, _ := json.Marshal(fields)
	return c.do(ctx, http.MethodPut, fmt.Sprintf("/api/v1/users/%d", id), token, body, nil)
}

// sendVerification posts a verification request for one channel. The three
// channel methods differ only in the channel value, the accepted status, and
// whether they decode the telegram deep-link from the response.
func (c *DansalClient) sendVerification(ctx context.Context, channel string, id int, baseURL, token string, out any, okStatus ...int) error {
	body, _ := json.Marshal(map[string]string{"channel": channel, "base_url": baseURL})
	return c.do(ctx, http.MethodPost, fmt.Sprintf("/api/v1/users/%d/verify", id), token, body, out, okStatus...)
}

func (c *DansalClient) SendEmailVerification(ctx context.Context, id int, baseURL, token string) error {
	return c.sendVerification(ctx, "email", id, baseURL, token, nil, http.StatusNoContent)
}

func (c *DansalClient) SendMatrixVerification(ctx context.Context, id int, baseURL, token string) error {
	return c.sendVerification(ctx, "matrix", id, baseURL, token, nil, http.StatusNoContent)
}

func (c *DansalClient) GetTelegramVerifyLink(ctx context.Context, id int, baseURL, token string) (string, error) {
	var result struct {
		DeepLink string `json:"deep_link"`
	}
	if err := c.sendVerification(ctx, "telegram", id, baseURL, token, &result); err != nil {
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
	Musicians          []int           `json:"musicians,omitempty"`
	Instructors        []int           `json:"instructors,omitempty"`
	Dances             []int           `json:"dances,omitempty"`
	ImageAIGenerated   bool            `json:"image_ai_generated,omitempty"`
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
	Instructors        []int           `json:"instructors,omitempty"`
	Dances             []int           `json:"dances,omitempty"`
	ImageAIGenerated   bool            `json:"image_ai_generated,omitempty"`
}

type EventLocReq struct {
	Location    string   `json:"location"`
	ShortName   string   `json:"short_name,omitempty"`
	Address     string   `json:"address,omitempty"`
	Zipcode     string   `json:"zipcode,omitempty"`
	Town        string   `json:"town,omitempty"`
	Country     string   `json:"country,omitempty"`
	CountryCode string   `json:"country_code,omitempty"`
	Region      string   `json:"region,omitempty"`
	Latitude    *float64 `json:"latitude,omitempty"`
	Longitude   *float64 `json:"longitude,omitempty"`
}

type TimetableEntryReq struct {
	StartTime    string `json:"start_time"`
	EndTime      string `json:"end_time,omitempty"`
	Title        string `json:"title"`
	Description  string `json:"description,omitempty"`
	Room         string `json:"room,omitempty"`
	EntryType    string `json:"entry_type,omitempty"`
	EntryDate    string `json:"entry_date,omitempty"`
	LocationID   *int   `json:"location_id,omitempty"`
	MusicianID   *int   `json:"musician_id,omitempty"`
	InstructorID *int   `json:"instructor_id,omitempty"`
}

func (c *DansalClient) GetAdminEvents(ctx context.Context, token string, params url.Values) ([]Event, error) {
	path := "/api/v1/events"
	if len(params) > 0 {
		path += "?" + params.Encode()
	}
	var events []Event
	return events, c.do(ctx, http.MethodGet, path, token, nil, &events)
}

func (c *DansalClient) GetAdminEventsWithTotal(ctx context.Context, token string, params url.Values) ([]Event, int, error) {
	path := "/api/v1/events"
	if len(params) > 0 {
		path += "?" + params.Encode()
	}
	resp, err := c.authed(ctx, http.MethodGet, path, token, nil)
	if err != nil {
		return nil, 0, err
	}
	defer resp.Body.Close()
	total := 0
	if v := resp.Header.Get("X-Total-Count"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			total = n
		} else {
			log.Printf("dansal client: invalid X-Total-Count %q", v)
		}
	}
	var events []Event
	if err := json.NewDecoder(resp.Body).Decode(&events); err != nil {
		return nil, total, err
	}
	return events, total, nil
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

type EnrichEventReq struct {
	AddMusicianIDs []int    `json:"add_musician_ids,omitempty"`
	Pricing        *Pricing `json:"pricing,omitempty"`
}

func (c *DansalClient) EnrichEvent(ctx context.Context, eventID int, req EnrichEventReq, token string) error {
	body, _ := json.Marshal(req)
	resp, err := c.authed(ctx, http.MethodPost, fmt.Sprintf("/api/v1/events/%d/enrich", eventID), token, body)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return apiErr(resp)
	}
	c.invalidateEvents()
	return nil
}

func (c *DansalClient) DeleteEventImage(ctx context.Context, eventID int, token string) error {
	return c.deleteAvatar(ctx, fmt.Sprintf("/api/v1/images/%d", eventID), token)
}

func (c *DansalClient) DeleteMusicianImage(ctx context.Context, id int, token string) error {
	return c.deleteAvatar(ctx, fmt.Sprintf("/api/v1/musician-images/%d", id), token)
}

func (c *DansalClient) DeleteOrgImage(ctx context.Context, id int, token string) error {
	return c.deleteAvatar(ctx, fmt.Sprintf("/api/v1/org-images/%d", id), token)
}

// multipartForm builds a single-file multipart body with the form field
// "image", returning the body bytes and the Content-Type to send.
func multipartForm(filename string, data []byte) ([]byte, string, error) {
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	fw, err := mw.CreateFormFile("image", filename)
	if err != nil {
		return nil, "", err
	}
	if _, err := fw.Write(data); err != nil {
		return nil, "", err
	}
	mw.Close()
	return buf.Bytes(), mw.FormDataContentType(), nil
}

// uploadAvatar posts a single "image" file. token may be empty for manage-token
// endpoints that identify the request via the path instead.
func (c *DansalClient) uploadAvatar(ctx context.Context, path string, data []byte, filename, token string) error {
	body, contentType, err := multipartForm(filename, data)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.BaseURL+path, bytes.NewReader(body))
	if err != nil {
		return err
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	req.Header.Set("Content-Type", contentType)
	c.setInternalHeader(req)
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

func (c *DansalClient) UploadOrgAvatar(ctx context.Context, id int, data []byte, filename, token string) error {
	return c.uploadAvatar(ctx, fmt.Sprintf("/api/v1/org-avatars/%d", id), data, filename, token)
}

func (c *DansalClient) UploadMusicianAvatar(ctx context.Context, id int, data []byte, filename, token string) error {
	return c.uploadAvatar(ctx, fmt.Sprintf("/api/v1/musician-avatars/%d", id), data, filename, token)
}

func (c *DansalClient) UploadInstructorAvatar(ctx context.Context, id int, data []byte, filename, token string) error {
	return c.uploadAvatar(ctx, fmt.Sprintf("/api/v1/instructor-avatars/%d", id), data, filename, token)
}

func (c *DansalClient) deleteAvatar(ctx context.Context, path, token string) error {
	return c.do(ctx, http.MethodDelete, path, token, nil, nil, http.StatusNoContent)
}

func (c *DansalClient) DeleteOrgAvatar(ctx context.Context, id int, token string) error {
	return c.deleteAvatar(ctx, fmt.Sprintf("/api/v1/org-avatars/%d", id), token)
}

func (c *DansalClient) DeleteMusicianAvatar(ctx context.Context, id int, token string) error {
	return c.deleteAvatar(ctx, fmt.Sprintf("/api/v1/musician-avatars/%d", id), token)
}

func (c *DansalClient) DeleteInstructorAvatar(ctx context.Context, id int, token string) error {
	return c.deleteAvatar(ctx, fmt.Sprintf("/api/v1/instructor-avatars/%d", id), token)
}

func (c *DansalClient) UploadSuggestManageImage(ctx context.Context, manageToken string, data []byte, filename string) error {
	return c.uploadAvatar(ctx, fmt.Sprintf("/api/v1/events/suggest/manage/%s/image", manageToken), data, filename, "")
}

func (c *DansalClient) UploadEventImage(ctx context.Context, eventID int, data []byte, filename, token string) error {
	return c.uploadAvatar(ctx, fmt.Sprintf("/api/v1/images/%d", eventID), data, filename, token)
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

// apiErrorMessage extracts the "error" field from a dansal API error
// response body (see writeError in cmd/dansal/errors.go), falling back to
// the raw body text if it isn't the expected JSON shape.
func apiErrorMessage(resp *http.Response) string {
	b, err := io.ReadAll(io.LimitReader(resp.Body, 4096))
	if err != nil || len(b) == 0 {
		return ""
	}
	var v struct {
		Error string `json:"error"`
	}
	if json.Unmarshal(b, &v) == nil && v.Error != "" {
		return v.Error
	}
	return string(b)
}

func (c *DansalClient) ReplaceTimetable(ctx context.Context, eventID int, entries []TimetableEntryReq, token string) error {
	body, _ := json.Marshal(entries)
	resp, err := c.authed(ctx, http.MethodPut, fmt.Sprintf("/api/v1/events/%d/timetable", eventID), token, body)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		return fmt.Errorf("replace timetable: %s: %s", resp.Status, apiErrorMessage(resp))
	}
	return nil
}

func (c *DansalClient) AddTimetableEntries(ctx context.Context, eventID int, entries []TimetableEntryReq, token string) error {
	body, _ := json.Marshal(entries)
	resp, err := c.authed(ctx, http.MethodPost, fmt.Sprintf("/api/v1/events/%d/timetable", eventID), token, body)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		return fmt.Errorf("add timetable: %s: %s", resp.Status, apiErrorMessage(resp))
	}
	return nil
}

func (c *DansalClient) DeleteTimetable(ctx context.Context, eventID int, token string) error {
	resp, err := c.authed(ctx, http.MethodDelete, fmt.Sprintf("/api/v1/events/%d/timetable", eventID), token, nil)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		return fmt.Errorf("delete timetable: %s: %s", resp.Status, apiErrorMessage(resp))
	}
	return nil
}

func (c *DansalClient) AddEventExtraLocation(ctx context.Context, eventID, locationID int, token string) error {
	resp, err := c.authed(ctx, http.MethodPut, fmt.Sprintf("/api/v1/events/%d/locations/%d", eventID, locationID), token, nil)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		return fmt.Errorf("add event location: %s: %s", resp.Status, apiErrorMessage(resp))
	}
	return nil
}

func (c *DansalClient) RemoveEventExtraLocation(ctx context.Context, eventID, locationID int, token string) error {
	resp, err := c.authed(ctx, http.MethodDelete, fmt.Sprintf("/api/v1/events/%d/locations/%d", eventID, locationID), token, nil)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		return fmt.Errorf("remove event location: %s: %s", resp.Status, apiErrorMessage(resp))
	}
	return nil
}

func (c *DansalClient) SetEventExtraLocationPrimary(ctx context.Context, eventID, locationID int, token string) error {
	resp, err := c.authed(ctx, http.MethodPut, fmt.Sprintf("/api/v1/events/%d/locations/%d/primary", eventID, locationID), token, nil)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		return fmt.Errorf("set primary location: %s: %s", resp.Status, apiErrorMessage(resp))
	}
	return nil
}

func (c *DansalClient) PatchEventTimes(ctx context.Context, eventID int, startTime, endTime, token string) error {
	body, _ := json.Marshal(map[string]string{"start_time": startTime, "end_time": endTime})
	req, err := http.NewRequestWithContext(ctx, http.MethodPatch,
		fmt.Sprintf("%s/api/v1/events/%d", c.BaseURL, eventID), bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/merge-patch+json")
	req.Header.Set("Authorization", "Bearer "+token)
	c.setInternalHeader(req)
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("patch event times: %s: %s", resp.Status, apiErrorMessage(resp))
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

// MeStats holds the event counts for the authenticated user.
type MeStats struct {
	EventsCreated    int `json:"events_created"`
	EventsLastEdited int `json:"events_last_edited"`
}

// MeInfo is the response from GET /api/v1/me: full user profile plus the
// token's expiry so callers can re-issue session cookies with the right TTL.
type MeInfo struct {
	UserInfo
	TokenExpiresAt string `json:"token_expires_at,omitempty"`
}

// GetMe returns the full profile for the token owner, including TokenExpiresAt.
func (c *DansalClient) GetMe(ctx context.Context, token string) (MeInfo, error) {
	resp, err := c.authed(ctx, http.MethodGet, "/api/v1/me", token, nil)
	if err != nil {
		return MeInfo{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return MeInfo{}, apiErr(resp)
	}
	var m MeInfo
	return m, json.NewDecoder(resp.Body).Decode(&m)
}

// GetMeStats returns event creation and last-edit counts for the authenticated user.
func (c *DansalClient) GetMeStats(ctx context.Context, token string) (MeStats, error) {
	resp, err := c.authed(ctx, http.MethodGet, "/api/v1/me/stats", token, nil)
	if err != nil {
		return MeStats{}, err
	}
	defer resp.Body.Close()
	var s MeStats
	return s, json.NewDecoder(resp.Body).Decode(&s)
}

// GetOrganizationMembersBulk fetches members for multiple orgs in one request,
// returning a map of org ID → member list.
func (c *DansalClient) GetOrganizationMembersBulk(ctx context.Context, orgIDs []int, token string) (map[int][]OrgMember, error) {
	if len(orgIDs) == 0 {
		return map[int][]OrgMember{}, nil
	}
	parts := make([]string, len(orgIDs))
	for i, id := range orgIDs {
		parts[i] = strconv.Itoa(id)
	}
	resp, err := c.authed(ctx, http.MethodGet,
		"/api/v1/organizations/members?org_ids="+strings.Join(parts, ","), token, nil)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, apiErr(resp)
	}
	var result map[int][]OrgMember
	return result, json.NewDecoder(resp.Body).Decode(&result)
}

// GetUserOrganizationIDs returns the IDs of all organizations the given user belongs to.
func (c *DansalClient) GetUserOrganizationIDs(ctx context.Context, userID int, token string) ([]int, error) {
	resp, err := c.authed(ctx, http.MethodGet, fmt.Sprintf("/api/v1/users/%d/organizations", userID), token, nil)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, apiErr(resp)
	}
	var result struct {
		OrganizationIDs []int `json:"organization_ids"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, err
	}
	return result.OrganizationIDs, nil
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
	req.Header.Set("Content-Type", "application/merge-patch+json")
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
	c.setInternalHeader(req)
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

// CreatePublisherInvite creates an invite link with role=publisher for the given org.
func (c *DansalClient) CreatePublisherInvite(ctx context.Context, orgID int, token string) (InviteLink, error) {
	body, _ := json.Marshal(map[string]any{"type": "link", "role": "publisher", "org_id": orgID})
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

func (c *DansalClient) SendTelegramMessageToUser(ctx context.Context, userID int, message, token string) error {
	body, _ := json.Marshal(map[string]string{"message": message})
	resp, err := c.authed(ctx, http.MethodPost, fmt.Sprintf("/api/v1/users/%d/telegram/message", userID), token, body)
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
	Service                  string `json:"service"`
	Version                  string `json:"version"`
	BuildTime                string `json:"build_time"`
	TotalEvents              int    `json:"total_events"`
	PublishedEvents          int    `json:"published_events"`
	UpcomingEvents           int    `json:"upcoming_events"`
	TotalUsers               int    `json:"total_users"`
	BoardEntries             int    `json:"board_entries"`
	TotalOrganizations       int    `json:"total_organizations"`
	TotalLocations           int    `json:"total_locations"`
	DBSizeBytes              int64  `json:"db_size_bytes"`
	ImagesSizeBytes          int64  `json:"images_size_bytes"`
	SelfRegistrationEnabled  bool   `json:"self_registration_enabled"`
	TelegramChannelAvailable bool   `json:"telegram_channel_available"`
}

func (c *DansalClient) GetServiceInfo(ctx context.Context) (DansalInfo, error) {
	return cached(&c.mu, &c.infoCache, 10*time.Minute, func() (DansalInfo, error) {
		var info DansalInfo
		return info, c.get(ctx, "/api/v1/info", &info)
	})
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

func (c *DansalClient) DeletePublisher(ctx context.Context, publisherID int, token string) error {
	resp, err := c.authed(ctx, http.MethodDelete, fmt.Sprintf("/api/v1/publishers/%d", publisherID), token, nil)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		return apiErr(resp)
	}
	return nil
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
	Zipcode   string   `json:"zipcode,omitempty"`
	Latitude  *float64 `json:"latitude,omitempty"`
	Longitude *float64 `json:"longitude,omitempty"`
	OsmID     *int64   `json:"osm_id,omitempty"`
	OsmType   string   `json:"osm_type,omitempty"`
}

func (c *DansalClient) PreviewEvents(ctx context.Context, body io.Reader, contentType, token string) ([]PreviewEvent, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.BaseURL+"/api/v1/events/preview", body)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", contentType)
	req.Header.Set("Authorization", "Bearer "+token)
	c.setInternalHeader(req)
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
	Title              string              `json:"title"`
	Description        string              `json:"description"`
	StartTime          string              `json:"start_time"`
	EndTime            string              `json:"end_time,omitempty"`
	HasBall            bool                `json:"has_ball"`
	HasWorkshop        bool                `json:"has_workshop"`
	HasFestival        bool                `json:"has_festival"`
	WorkshopDifficulty string              `json:"workshop_difficulty,omitempty"`
	Tags               []string            `json:"tags,omitempty"`
	DanceIDs           []int               `json:"dance_ids,omitempty"`
	URL                string              `json:"url,omitempty"`
	Food               string              `json:"food,omitempty"`
	Drink              string              `json:"drink,omitempty"`
	Location           PreviewLoc          `json:"location"`
	Email              string              `json:"email"`
	SuggesterName      string              `json:"suggester_name,omitempty"`
	Phone2             string              `json:"phone2"` // honeypot
	Pricing            *Pricing            `json:"pricing,omitempty"`
	ContactName        string              `json:"contact_name,omitempty"`
	ContactEmail       string              `json:"contact_email,omitempty"`
	Musicians          []string            `json:"musicians,omitempty"`
	Instructors        []string            `json:"instructors,omitempty"`
	Timetable          []TimetableEntryReq `json:"timetable,omitempty"`
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

// GetSuggestManageEvent fetches a suggestion's current data via its standing
// manage token (#928), for pre-filling the wizard on the manage page.
func (c *DansalClient) GetSuggestManageEvent(ctx context.Context, token string) (Event, error) {
	var ev Event
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.BaseURL+"/api/v1/events/suggest/manage/"+token, nil)
	if err != nil {
		return ev, err
	}
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return ev, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return ev, apiErr(resp)
	}
	return ev, json.NewDecoder(resp.Body).Decode(&ev)
}

// PatchSuggestManageEvent submits an edit to a suggestion via its manage
// token (#928). Before publish it's applied directly; after publish only a
// safe subset auto-applies and the rest goes to pending_edit_json for review.
// Returns needsReview=true when changes were queued for admin review.
func (c *DansalClient) PatchSuggestManageEvent(ctx context.Context, token string, req SuggestEventReq) (needsReview bool, err error) {
	body, _ := json.Marshal(req)
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPatch, c.BaseURL+"/api/v1/events/suggest/manage/"+token, bytes.NewReader(body))
	if err != nil {
		return false, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	resp, err := c.HTTP.Do(httpReq)
	if err != nil {
		return false, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return false, apiErr(resp)
	}
	var result struct {
		NeedsReview bool `json:"needs_review"`
	}
	json.NewDecoder(resp.Body).Decode(&result)
	return result.NeedsReview, nil
}

// ApprovePendingEdit / RejectPendingEdit act on an event's pending_edit_json
// (#928), using the same authorization patchEvent already enforces.
func (c *DansalClient) ApprovePendingEdit(ctx context.Context, id int, token string) error {
	resp, err := c.authed(ctx, http.MethodPost, fmt.Sprintf("/api/v1/events/%d/pending-edit/approve", id), token, nil)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		return apiErr(resp)
	}
	return nil
}

func (c *DansalClient) RejectPendingEdit(ctx context.Context, id int, token string) error {
	resp, err := c.authed(ctx, http.MethodPost, fmt.Sprintf("/api/v1/events/%d/pending-edit/reject", id), token, nil)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
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
	c.setInternalHeader(req)
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

// DashboardAttention holds scoped counts of items needing review.
type DashboardAttention struct {
	PendingRegistrations    int `json:"pending_registrations"`
	PendingEventSuggestions int `json:"pending_event_suggestions"`
	PossibleDuplicates      int `json:"possible_duplicates"`
	PendingEdits            int `json:"pending_edits"`
	NotVerifiedEventCount   int `json:"not_verified_event_count"`
}

// GetDashboardAttention returns the scoped counts of items needing review for the caller.
func (c *DansalClient) GetDashboardAttention(ctx context.Context, token string) (DashboardAttention, error) {
	resp, err := c.authed(ctx, http.MethodGet, "/api/v1/dashboard/attention", token, nil)
	if err != nil {
		return DashboardAttention{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return DashboardAttention{}, nil
	}
	var a DashboardAttention
	json.NewDecoder(resp.Body).Decode(&a)
	return a, nil
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
	ID                int             `json:"id"`
	Slug              string          `json:"slug"`
	Title             string          `json:"title"`
	Description       string          `json:"description"`
	OrganizationID    *int            `json:"organization_id,omitempty"`
	MusicianID        *int            `json:"musician_id,omitempty"`
	InstructorID      *int            `json:"instructor_id,omitempty"`
	DefaultLocationID *int            `json:"default_location_id,omitempty"`
	DefaultStartTime  string          `json:"default_start_time"`
	DefaultEndTime    string          `json:"default_end_time"`
	InviteToken       string          `json:"invite_token,omitempty"`
	CreatedAt         string          `json:"created_at"`
	UpdatedAt         int64           `json:"updated_at"`
	EventCount        int             `json:"event_count,omitempty"`
	Events            []SeriesEvent   `json:"events,omitempty"`
	TemplateData      json.RawMessage `json:"template_data,omitempty"`
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

func (c *DansalClient) GetSeriesListForInstructor(ctx context.Context, instructorID int, token string) ([]EventSeries, error) {
	path := fmt.Sprintf("/api/v1/series?instructor_id=%d", instructorID)
	resp, err := c.authed(ctx, http.MethodGet, path, token, nil)
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

func (c *DansalClient) GetSeriesListForMusician(ctx context.Context, musicianID int, token string) ([]EventSeries, error) {
	path := fmt.Sprintf("/api/v1/series?musician_id=%d", musicianID)
	resp, err := c.authed(ctx, http.MethodGet, path, token, nil)
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
		c.BaseURL+"/api/v1/series-by-token/"+seriesToken, nil)
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
	path := fmt.Sprintf("/api/v1/series-by-token/%s/events/%d", seriesToken, eventID)
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

type TOTPSetupInfo struct {
	Secret string `json:"secret"`
	URI    string `json:"uri"`
}

func (c *DansalClient) TOTPSetup(ctx context.Context, token string) (TOTPSetupInfo, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.BaseURL+"/api/v1/auth/totp/setup", nil)
	if err != nil {
		return TOTPSetupInfo{}, err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	c.setInternalHeader(req)
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return TOTPSetupInfo{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return TOTPSetupInfo{}, apiErr(resp)
	}
	var info TOTPSetupInfo
	if err := json.NewDecoder(resp.Body).Decode(&info); err != nil {
		return TOTPSetupInfo{}, err
	}
	return info, nil
}

func (c *DansalClient) TOTPConfirm(ctx context.Context, token, code string) error {
	body, _ := json.Marshal(map[string]string{"code": code})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.BaseURL+"/api/v1/auth/totp/confirm", bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	c.setInternalHeader(req)
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

func (c *DansalClient) TOTPDisable(ctx context.Context, token, code string) error {
	body, _ := json.Marshal(map[string]string{"code": code})
	req, err := http.NewRequestWithContext(ctx, http.MethodDelete, c.BaseURL+"/api/v1/auth/totp", bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	c.setInternalHeader(req)
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

// GetCities returns towns with geo-tagged venues and upcoming events (#965).
func (c *DansalClient) GetCities(ctx context.Context) ([]City, error) {
	var cities []City
	return cities, c.get(ctx, "/api/v1/locations/cities", &cities)
}

// Syndication proxy helpers (#971, #953) — used by admin event handlers.

// SyndicationConfig mirrors the API's SyndicationConfig for admin UI.
type SyndicationConfig struct {
	Eventbrite       *EventbriteCfg       `json:"eventbrite,omitempty"`
	SocialDanceToday *SocialDanceTodayCfg `json:"social_dance_today,omitempty"`
}

type EventbriteCfg struct {
	Enabled     bool   `json:"enabled"`
	Token       string `json:"token"`
	OrgID       string `json:"org_id"`
	AutoPublish bool   `json:"auto_publish"`
}

type SocialDanceTodayCfg struct {
	Enabled bool   `json:"enabled"`
	APIKey  string `json:"api_key"`
	OrgSlug string `json:"org_slug"`
}

// PlatformSyncStatus is one platform's sync state from events.external_sync.
type PlatformSyncStatus struct {
	Status     string `json:"status"`
	ExternalID string `json:"external_id,omitempty"`
	URL        string `json:"url,omitempty"`
	SyncedAt   string `json:"synced_at,omitempty"`
	Error      string `json:"error,omitempty"`
}

// ExternalSync mirrors the API's ExternalSync for the admin event UI.
type ExternalSync struct {
	Eventbrite       *PlatformSyncStatus `json:"eventbrite,omitempty"`
	SocialDanceToday *PlatformSyncStatus `json:"social_dance_today,omitempty"`
}

func (c *DansalClient) GetSyndicationConfig(ctx context.Context, orgID int, token string) (*SyndicationConfig, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet,
		fmt.Sprintf("%s/api/v1/organizations/%d/syndication", c.BaseURL, orgID), nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	c.setInternalHeader(req)
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	var cfg SyndicationConfig
	return &cfg, json.NewDecoder(resp.Body).Decode(&cfg)
}

func (c *DansalClient) PutSyndicationConfig(ctx context.Context, orgID int, token string, cfg SyndicationConfig) error {
	body, _ := json.Marshal(cfg)
	req, err := http.NewRequestWithContext(ctx, http.MethodPut,
		fmt.Sprintf("%s/api/v1/organizations/%d/syndication", c.BaseURL, orgID),
		bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	c.setInternalHeader(req)
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

func (c *DansalClient) GetEventSyncStatus(ctx context.Context, eventID int, token string) (*ExternalSync, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet,
		fmt.Sprintf("%s/api/v1/events/%d/syndication", c.BaseURL, eventID), nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	c.setInternalHeader(req)
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	var s ExternalSync
	return &s, json.NewDecoder(resp.Body).Decode(&s)
}

func (c *DansalClient) SyndicateTo(ctx context.Context, eventID int, platform, token string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		fmt.Sprintf("%s/api/v1/events/%d/syndicate/%s", c.BaseURL, eventID, platform), nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	c.setInternalHeader(req)
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
