package main

import (
	"database/sql"
	"encoding/json"
	"log"
	"net/http"
	"sort"
)

// botCat defines the stacking order (bottom → top) and colours for the traffic chart.
type botCat struct {
	Key   string `json:"key"`
	Label string `json:"label"`
	Color string `json:"color"`
}

var botCategoryList = []botCat{
	{"browser", "Humans", "#4caf50"},
	{"activitypub", "ActivityPub", "#1e88e5"},
	{"http_library", "HTTP Library", "#00acc1"},
	{"search_engine", "Search Engine", "#3f51b5"},
	{"generic_bot", "Generic Bot", "#78909c"},
	{"uptime_monitor", "Uptime Monitor", "#8bc34a"},
	{"ai_crawler", "AI Crawler", "#9c27b0"},
	{"spam_scanner", "Spam Scanner", "#e91e63"},
	{"seo_scanner", "SEO Scanner", "#ff9800"},
	{"empty_ua", "Scanner/Attack", "#ef5350"},
}

var userRefList = []botCat{
	{"internal", "Within-site nav", "#4caf50"},
	{"search", "Search engines", "#1e88e5"},
	{"external", "External sites", "#ff9800"},
	{"direct", "Direct/bookmark", "#90a4ae"},
}

type botStatDay struct {
	Date          string
	TotalRequests int
	HumanCount    int
	BotCount      int
	InboxFailures int
	Categories    map[string]int
}

type userStatDay struct {
	Date           string
	TotalRequests  int
	DirectCount    int
	SearchCount    int
	ExternalCount  int
	InternalCount  int
	ClicksPerEntry float64
	ScannerSlip    int
}

// searchEngineTotal holds the aggregated referral count for one search engine.
type searchEngineTotal struct {
	Engine string
	Count  int
}

// fediStatDay holds one day's fediverse activity from fedi_stats_daily.
type fediStatDay struct {
	Date                   string
	FollowersGained        int
	EventsDeliveredCreates int
	EventsDeliveredUpdates int
	InboxTotal             int
	Inbox2xx               int
	Inbox4xx               int
	Inbox5xx               int
	ActorFetches           int
	WebfingerRequests      int
	NodeinfoRequests       int
	OutboxFetches          int
	FollowersFetches       int
	EventFetches           int
}

// fediSourceInstance is one remote fediverse instance with its aggregated
// request count (summed across the loaded date range).
type fediSourceInstance struct {
	Instance string
	Count    int
}

// fediLiveSnapshot holds data read live from web.db (always current).
type fediLiveSnapshot struct {
	TotalFollowers          int
	PendingDeliveryFailures int
	FollowersByOrg          []fediOrgFollowers
	TopInstances            []fediSourceInstance // lifetime top, not just 30d
}

type fediOrgFollowers struct {
	OrgID int
	Count int
}

type BotStatsPageData struct {
	BotDays       []botStatDay
	UserDays      []userStatDay
	UserDayMap    map[string]*userStatDay
	BotChartJSON  string
	UserChartJSON string
	Latest        *botStatDay
	LatestUser    *userStatDay
	HasData       bool
	DBMissing     bool

	// Search engine referral breakdown (all-time sum from user_stats_referrers).
	SearchEngines    []searchEngineTotal
	TotalSearchCount int

	// Fediverse
	FediDays         []fediStatDay
	FediTopInstances []fediSourceInstance // aggregated over loaded fedi days
	FediLive         *fediLiveSnapshot
	FediChartJSON    string // inbox success/fail + actor fetches stacked bar
	FediFollowChart  string // daily followers gained line/bar
	FediHasData      bool
}

// openReadonlyDB opens an SQLite database read-only with a busy timeout.
// immutable=1 is intentionally not used (see #996).
func openReadonlyDB(path string) (*sql.DB, error) {
	return sql.Open("sqlite3", "file:"+path+"?mode=ro&_busy_timeout=5000")
}

func loadBotStatsDB(dbPath string) ([]botStatDay, []userStatDay, []searchEngineTotal, error) {
	db, err := openReadonlyDB(dbPath)
	if err != nil {
		return nil, nil, nil, err
	}
	defer db.Close()

	// --- bot meta ---
	rows, err := db.Query(`SELECT date, total_requests, human_count, bot_count, inbox_failures
		FROM bot_stats_meta ORDER BY date`)
	if err != nil {
		return nil, nil, nil, err
	}
	defer rows.Close()

	dayMap := map[string]*botStatDay{}
	var botDays []*botStatDay
	for rows.Next() {
		var d botStatDay
		rows.Scan(&d.Date, &d.TotalRequests, &d.HumanCount, &d.BotCount, &d.InboxFailures)
		d.Categories = map[string]int{}
		dayMap[d.Date] = &d
		botDays = append(botDays, &d)
	}

	// --- bot categories ---
	crows, err := db.Query(`SELECT date, category, count FROM bot_stats_daily ORDER BY date`)
	if err != nil {
		return nil, nil, nil, err
	}
	defer crows.Close()
	for crows.Next() {
		var date, cat string
		var count int
		crows.Scan(&date, &cat, &count)
		if r, ok := dayMap[date]; ok {
			r.Categories[cat] = count
		}
	}

	// --- user meta ---
	urows, err := db.Query(`SELECT date, total_requests, direct_count, search_count,
		external_count, internal_count, clicks_per_entry, scanner_slipthrough
		FROM user_stats_meta ORDER BY date`)
	if err != nil {
		return nil, nil, nil, err
	}
	defer urows.Close()

	var userDays []userStatDay
	for urows.Next() {
		var d userStatDay
		urows.Scan(&d.Date, &d.TotalRequests, &d.DirectCount, &d.SearchCount,
			&d.ExternalCount, &d.InternalCount, &d.ClicksPerEntry, &d.ScannerSlip)
		userDays = append(userDays, d)
	}

	// --- search engine breakdown (all-time sum; table may not exist yet) ---
	var engines []searchEngineTotal
	erows, eErr := db.Query(`
		SELECT REPLACE(source, 'search:', '') AS engine, SUM(count) AS total
		FROM user_stats_referrers
		WHERE source LIKE 'search:%'
		GROUP BY source
		ORDER BY total DESC`)
	if eErr == nil {
		defer erows.Close()
		for erows.Next() {
			var e searchEngineTotal
			erows.Scan(&e.Engine, &e.Count)
			engines = append(engines, e)
		}
	}
	// Silently ignore error if user_stats_referrers doesn't exist yet.

	result := make([]botStatDay, len(botDays))
	for i, d := range botDays {
		result[i] = *d
	}
	return result, userDays, engines, nil
}

// loadFediStatsDB reads fediverse daily stats and per-instance request counts
// from the bot-stats DB, returning up to the last 60 days.
func loadFediStatsDB(dbPath string) ([]fediStatDay, []fediSourceInstance, error) {
	db, err := openReadonlyDB(dbPath)
	if err != nil {
		return nil, nil, err
	}
	defer db.Close()

	// The fedi stats tables don't exist on a fresh bot-stats DB — not an
	// error, just no data. Check the schema explicitly instead of parsing the
	// error text.
	var exists int
	if err := db.QueryRow(
		"SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name='fedi_stats_daily'").Scan(&exists); err != nil {
		return nil, nil, err
	}
	if exists == 0 {
		return nil, nil, nil
	}

	rows, err := db.Query(`
		SELECT date,
		       followers_gained, events_delivered_creates, events_delivered_updates,
		       inbox_total, inbox_2xx, inbox_4xx, inbox_5xx,
		       actor_fetches, webfinger_requests, nodeinfo_requests,
		       outbox_fetches, followers_fetches, event_fetches
		FROM fedi_stats_daily
		ORDER BY date DESC LIMIT 60`)
	if err != nil {
		return nil, nil, err
	}
	defer rows.Close()

	var days []fediStatDay
	for rows.Next() {
		var d fediStatDay
		rows.Scan(&d.Date,
			&d.FollowersGained, &d.EventsDeliveredCreates, &d.EventsDeliveredUpdates,
			&d.InboxTotal, &d.Inbox2xx, &d.Inbox4xx, &d.Inbox5xx,
			&d.ActorFetches, &d.WebfingerRequests, &d.NodeinfoRequests,
			&d.OutboxFetches, &d.FollowersFetches, &d.EventFetches)
		days = append(days, d)
	}
	// Return oldest-first for chart rendering.
	for i, j := 0, len(days)-1; i < j; i, j = i+1, j-1 {
		days[i], days[j] = days[j], days[i]
	}

	// Aggregate instance counts across the loaded date range.
	instMap := map[string]int{}
	irows, err := db.Query(`
		SELECT instance, SUM(count) as total
		FROM fedi_source_instances
		WHERE date >= (SELECT MIN(date) FROM fedi_stats_daily)
		GROUP BY instance
		ORDER BY total DESC
		LIMIT 20`)
	if err == nil {
		defer irows.Close()
		for irows.Next() {
			var inst string
			var cnt int
			irows.Scan(&inst, &cnt)
			instMap[inst] = cnt
		}
	}
	var instances []fediSourceInstance
	for inst, cnt := range instMap {
		instances = append(instances, fediSourceInstance{Instance: inst, Count: cnt})
	}
	// Sort descending by count.
	sort.Slice(instances, func(i, j int) bool {
		return instances[i].Count > instances[j].Count
	})

	return days, instances, nil
}

// loadFediLive reads the current fediverse state directly from web.db.
func loadFediLive(webDBPath string) *fediLiveSnapshot {
	if webDBPath == "" {
		return nil
	}
	db, err := openReadonlyDB(webDBPath)
	if err != nil {
		log.Printf("fedi live: open web.db: %v", err)
		return nil
	}
	defer db.Close()

	snap := &fediLiveSnapshot{}

	// Total followers.
	if err := db.QueryRow("SELECT COUNT(*) FROM followers").Scan(&snap.TotalFollowers); err != nil {
		log.Printf("fedi live: count followers: %v", err)
	}

	// Pending delivery failures.
	if err := db.QueryRow("SELECT COUNT(*) FROM delivery_failures").Scan(&snap.PendingDeliveryFailures); err != nil {
		log.Printf("fedi live: count delivery failures: %v", err)
	}

	// Followers per org.
	orgRows, err := db.Query("SELECT org_id, COUNT(*) FROM followers GROUP BY org_id ORDER BY COUNT(*) DESC")
	if err != nil {
		log.Printf("fedi live: followers by org: %v", err)
	} else {
		defer orgRows.Close()
		for orgRows.Next() {
			var of fediOrgFollowers
			if err := orgRows.Scan(&of.OrgID, &of.Count); err != nil {
				log.Printf("fedi live: followers by org scan: %v", err)
				continue
			}
			snap.FollowersByOrg = append(snap.FollowersByOrg, of)
		}
	}

	// Top follower instances (lifetime, from followers.actor_uri).
	instRows, err := db.Query(`
		SELECT
			CASE
				WHEN instr(substr(actor_uri, instr(actor_uri,'://')+3), '/') > 0
				THEN substr(actor_uri, instr(actor_uri,'://')+3,
				            instr(substr(actor_uri, instr(actor_uri,'://')+3),'/')-1)
				ELSE substr(actor_uri, instr(actor_uri,'://')+3)
			END AS host,
			COUNT(*) as n
		FROM followers
		GROUP BY host
		ORDER BY n DESC
		LIMIT 15`)
	if err != nil {
		log.Printf("fedi live: top instances: %v", err)
	} else {
		defer instRows.Close()
		for instRows.Next() {
			var fi fediSourceInstance
			if err := instRows.Scan(&fi.Instance, &fi.Count); err != nil {
				log.Printf("fedi live: top instances scan: %v", err)
				continue
			}
			snap.TopInstances = append(snap.TopInstances, fi)
		}
	}

	return snap
}

// chartDates truncates YYYY-MM-DD labels to MM-DD for compact chart axes.
func chartDates(dates []string) []string {
	out := make([]string, len(dates))
	for i, d := range dates {
		if len(d) >= 10 {
			out[i] = d[5:]
		}
	}
	return out
}

// buildChartJSON renders the shared chart payload: date labels, the category
// legend, and one stacked series per category.
func buildChartJSON(dates []string, cats []botCat, series map[string][]int) string {
	b, _ := json.Marshal(map[string]any{
		"dates":      chartDates(dates),
		"categories": cats,
		"series":     series,
	})
	return string(b)
}

// buildFediChartJSON builds a stacked-bar chart JSON for inbox activity
// (2xx success, 4xx rejection, 5xx error) plus actor fetches as a separate series.
func buildFediChartJSON(days []fediStatDay) string {
	cats := []botCat{
		{"inbox_2xx", "Inbox accepted", "#4caf50"},
		{"inbox_4xx", "Inbox rejected", "#ff9800"},
		{"inbox_5xx", "Inbox error", "#e53935"},
		{"actor_fetches", "Actor fetches", "#1e88e5"},
		{"webfinger", "WebFinger", "#00acc1"},
	}
	dates := make([]string, len(days))
	series := map[string][]int{}
	for _, c := range cats {
		series[c.Key] = make([]int, len(days))
	}
	for i, d := range days {
		dates[i] = d.Date
		series["inbox_2xx"][i] = d.Inbox2xx
		series["inbox_4xx"][i] = d.Inbox4xx
		series["inbox_5xx"][i] = d.Inbox5xx
		series["actor_fetches"][i] = d.ActorFetches
		series["webfinger"][i] = d.WebfingerRequests
	}
	return buildChartJSON(dates, cats, series)
}

// buildFediFollowChartJSON builds a simple bar chart of daily followers gained.
func buildFediFollowChartJSON(days []fediStatDay) string {
	cats := []botCat{
		{"gained", "New followers", "#4caf50"},
		{"delivers", "Events delivered", "#1e88e5"},
	}
	dates := make([]string, len(days))
	series := map[string][]int{
		"gained":   make([]int, len(days)),
		"delivers": make([]int, len(days)),
	}
	for i, d := range days {
		dates[i] = d.Date
		series["gained"][i] = d.FollowersGained
		series["delivers"][i] = d.EventsDeliveredCreates + d.EventsDeliveredUpdates
	}
	return buildChartJSON(dates, cats, series)
}

func buildBotChartJSON(days []botStatDay) string {
	dates := make([]string, len(days))
	series := map[string][]int{}
	for _, c := range botCategoryList {
		series[c.Key] = make([]int, len(days))
	}
	for i, d := range days {
		dates[i] = d.Date
		for _, c := range botCategoryList {
			series[c.Key][i] = d.Categories[c.Key]
		}
	}
	return buildChartJSON(dates, botCategoryList, series)
}

func buildUserChartJSON(days []userStatDay) string {
	dates := make([]string, len(days))
	series := map[string][]int{
		"direct":   make([]int, len(days)),
		"search":   make([]int, len(days)),
		"external": make([]int, len(days)),
		"internal": make([]int, len(days)),
	}
	for i, d := range days {
		dates[i] = d.Date
		series["direct"][i] = d.DirectCount
		series["search"][i] = d.SearchCount
		series["external"][i] = d.ExternalCount
		series["internal"][i] = d.InternalCount
	}
	return buildChartJSON(dates, userRefList, series)
}

func botStatsPageHandler(cfg *Config, tmpls *Templates) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		data := BotStatsPageData{}

		if cfg.BotStatsDBPath == "" {
			data.DBMissing = true
		} else {
			botDays, userDays, engines, err := loadBotStatsDB(cfg.BotStatsDBPath)
			if err != nil {
				log.Printf("bot-stats db: %v", err)
				data.DBMissing = true
			} else {
				data.BotDays = botDays
				data.UserDays = userDays
				data.HasData = len(botDays) > 0
				if data.HasData {
					latest := botDays[len(botDays)-1]
					data.Latest = &latest
					data.BotChartJSON = buildBotChartJSON(botDays)
				}
				if len(userDays) > 0 {
					latest := userDays[len(userDays)-1]
					data.LatestUser = &latest
					data.UserChartJSON = buildUserChartJSON(userDays)
					data.UserDayMap = make(map[string]*userStatDay, len(userDays))
					for i := range userDays {
						data.UserDayMap[userDays[i].Date] = &userDays[i]
					}
				}
				data.SearchEngines = engines
				for _, e := range engines {
					data.TotalSearchCount += e.Count
				}

				// Fediverse historical data from the same bot-stats DB.
				fediDays, fediInst, fErr := loadFediStatsDB(cfg.BotStatsDBPath)
				if fErr != nil {
					log.Printf("fedi-stats db: %v", fErr)
				} else {
					data.FediDays = fediDays
					data.FediTopInstances = fediInst
					data.FediHasData = len(fediDays) > 0
					if data.FediHasData {
						data.FediChartJSON = buildFediChartJSON(fediDays)
						data.FediFollowChart = buildFediFollowChartJSON(fediDays)
					}
				}
			}
		}

		// Fediverse live snapshot — read from web.db regardless of bot-stats DB.
		data.FediLive = loadFediLive(cfg.WebDBPath)

		d := tmplData(r, cfg, "Bot & Traffic Stats", data)
		renderTemplate(w, tmpls.botStats, d)
	}
}
