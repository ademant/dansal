package main

import (
	"database/sql"
	"encoding/json"
	"log"
	"net/http"
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
}

func loadBotStatsDB(dbPath string) ([]botStatDay, []userStatDay, error) {
	db, err := sql.Open("sqlite3", dbPath+"?mode=ro&_busy_timeout=3000")
	if err != nil {
		return nil, nil, err
	}
	defer db.Close()

	// --- bot meta ---
	rows, err := db.Query(`SELECT date, total_requests, human_count, bot_count, inbox_failures
		FROM bot_stats_meta ORDER BY date`)
	if err != nil {
		return nil, nil, err
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
		return nil, nil, err
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
		return nil, nil, err
	}
	defer urows.Close()

	var userDays []userStatDay
	for urows.Next() {
		var d userStatDay
		urows.Scan(&d.Date, &d.TotalRequests, &d.DirectCount, &d.SearchCount,
			&d.ExternalCount, &d.InternalCount, &d.ClicksPerEntry, &d.ScannerSlip)
		userDays = append(userDays, d)
	}

	result := make([]botStatDay, len(botDays))
	for i, d := range botDays {
		result[i] = *d
	}
	return result, userDays, nil
}

func buildBotChartJSON(days []botStatDay) string {
	dates := make([]string, len(days))
	series := map[string][]int{}
	for _, c := range botCategoryList {
		series[c.Key] = make([]int, len(days))
	}
	for i, d := range days {
		if len(d.Date) >= 10 {
			dates[i] = d.Date[5:] // MM-DD
		}
		for _, c := range botCategoryList {
			series[c.Key][i] = d.Categories[c.Key]
		}
	}
	b, _ := json.Marshal(map[string]any{
		"dates":      dates,
		"categories": botCategoryList,
		"series":     series,
	})
	return string(b)
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
		if len(d.Date) >= 10 {
			dates[i] = d.Date[5:]
		}
		series["direct"][i] = d.DirectCount
		series["search"][i] = d.SearchCount
		series["external"][i] = d.ExternalCount
		series["internal"][i] = d.InternalCount
	}
	b, _ := json.Marshal(map[string]any{
		"dates":      dates,
		"categories": userRefList,
		"series":     series,
	})
	return string(b)
}

func botStatsPageHandler(cfg *Config, tmpls *Templates) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		data := BotStatsPageData{}

		if cfg.BotStatsDBPath == "" {
			data.DBMissing = true
		} else {
			botDays, userDays, err := loadBotStatsDB(cfg.BotStatsDBPath)
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
			}
		}

		d := tmplData(r, cfg, "Bot & Traffic Stats", data)
		d.User = getSessionUser(r)
		renderTemplate(w, tmpls.botStats, d)
	}
}
