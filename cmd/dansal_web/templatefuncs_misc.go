package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"html/template"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/yuin/goldmark"
)

// reURLAttr matches href="..." and src="..." produced by goldmark's renderer.
var reURLAttr = regexp.MustCompile(`(?i)(href|src)="([^"]*)"`)

// safeSchemes lists URL schemes allowed in rendered markdown output.
var safeSchemes = map[string]bool{
	"http": true, "https": true, "mailto": true, "tel": true,
}

// sanitizeMarkdownHTML strips dangerous URI schemes (javascript:, data:,
// vbscript:) from href and src attributes in goldmark-rendered HTML.
// Relative URLs (no scheme) are left untouched.
func sanitizeMarkdownHTML(s string) string {
	return reURLAttr.ReplaceAllStringFunc(s, func(m string) string {
		parts := reURLAttr.FindStringSubmatch(m)
		if parts == nil {
			return m
		}
		u, err := url.Parse(parts[2])
		if err != nil {
			return parts[1] + `="#"`
		}
		scheme := strings.ToLower(u.Scheme)
		if scheme != "" && !safeSchemes[scheme] {
			return parts[1] + `="#"`
		}
		return m
	})
}

// Misc template functions — one slice of the merged tmplFuncMap, split out of
// frontend.go (#1031). Covers everything that isn't time/date, location, tags,
// or chat.

var tmplFuncsMisc = template.FuncMap{
	"dict": func(pairs ...interface{}) (map[string]interface{}, error) {
		if len(pairs)%2 != 0 {
			return nil, fmt.Errorf("dict: odd number of arguments")
		}
		m := make(map[string]interface{}, len(pairs)/2)
		for i := 0; i < len(pairs); i += 2 {
			key, ok := pairs[i].(string)
			if !ok {
				return nil, fmt.Errorf("dict: key %v is not a string", pairs[i])
			}
			m[key] = pairs[i+1]
		}
		return m, nil
	},
	"join": func(ss []string) string {
		return strings.Join(ss, ", ")
	},
	"emailLocal":         emailLocal,
	"displayNameOrEmail": displayNameOrEmail,
	"jsStr": func(s string) template.JS {
		b, _ := json.Marshal(s)
		return template.JS(b)
	},
	"joinInts": func(ids []int) string {
		parts := make([]string, len(ids))
		for i, id := range ids {
			parts[i] = strconv.Itoa(id)
		}
		return strings.Join(parts, ",")
	},
	"eventsGeoJSON": func(events []Event) template.JS {
		geo := eventsToGeo(events)
		if geo == nil {
			return template.JS("[]")
		}
		b, _ := json.Marshal(geo)
		return template.JS(b)
	},
	// festivalCalendarJSON (#1144) feeds the /festivals year-calendar grid:
	// just id/title/date-range/href, built client-side with Intl month/weekday
	// names the same way the search-page date picker does.
	"festivalCalendarJSON": func(events []Event) template.JS {
		type calEvt struct {
			ID    int    `json:"id"`
			Title string `json:"t"`
			Start string `json:"s"`
			End   string `json:"e,omitempty"`
		}
		out := make([]calEvt, 0, len(events))
		for _, e := range events {
			out = append(out, calEvt{ID: e.ID, Title: e.Title, Start: e.StartTime, End: e.EndTime})
		}
		b, _ := json.Marshal(out)
		return template.JS(b)
	},
	// boardPostsGeoJSON (#1077, #1078) flattens geocoded ride/accommodation
	// board posts (Lat/Lon set) into a compact JSON blob for the board map —
	// posts without coordinates are silently excluded, so callers just check
	// len() to decide whether to render a map at all. EventTitle is included
	// for the aggregate /board map (#1078); empty on the per-event map since
	// the event is already given by context there.
	"boardPostsGeoJSON": func(posts []ContactPost) template.JS {
		type geoPost struct {
			ID         int     `json:"id"`
			EventID    int     `json:"event_id"`
			Type       string  `json:"type"`
			City       string  `json:"city"`
			Lat        float64 `json:"lat"`
			Lon        float64 `json:"lon"`
			Persons    int     `json:"persons"`
			Nickname   string  `json:"nickname"`
			EventTitle string  `json:"event_title,omitempty"`
		}
		out := []geoPost{}
		for _, p := range posts {
			if p.Lat == nil || p.Lon == nil {
				continue
			}
			gp := geoPost{
				ID: p.ID, EventID: p.EventID, Type: p.Type, City: p.City,
				Lat: *p.Lat, Lon: *p.Lon, Persons: p.Persons, Nickname: p.Nickname,
			}
			if p.Event != nil {
				gp.EventTitle = p.Event.Title
			}
			out = append(out, gp)
		}
		b, _ := json.Marshal(out)
		return template.JS(b)
	},
	"orgsMapJSON": func(pins []OrgMapPin) template.JS {
		if len(pins) == 0 {
			return template.JS("[]")
		}
		b, _ := json.Marshal(pins)
		return template.JS(b)
	},
	"orgMapJSON": func(orgMap map[int]Organization) template.JS {
		out := make(map[string]string, len(orgMap))
		for id, o := range orgMap {
			out[strconv.Itoa(id)] = o.Name
		}
		b, _ := json.Marshal(out)
		return template.JS(b)
	},
	"orgName": func(orgMap map[int]Organization, id *int) string {
		if id == nil {
			return ""
		}
		if o, ok := orgMap[*id]; ok {
			return o.Name
		}
		return ""
	},
	"orgSlug": orgSlug,
	"checkinColor": func(status string) string {
		switch status {
		case "approved", "checked_in":
			return "green"
		case "confirmed":
			return "amber"
		default:
			return "red"
		}
	},
	"checkinIcon": func(status string) string {
		switch status {
		case "approved", "checked_in":
			return "✓"
		case "confirmed":
			return "?"
		default:
			return "✗"
		}
	},
	"capPct": func(approved, total int) int {
		if total <= 0 {
			return 0
		}
		pct := approved * 100 / total
		if pct > 100 {
			return 100
		}
		return pct
	},
	"markdownHTML": func(s string) template.HTML {
		var buf bytes.Buffer
		if err := goldmark.Convert([]byte(s), &buf); err != nil {
			return template.HTML(template.HTMLEscapeString(s))
		}
		return template.HTML(sanitizeMarkdownHTML(buf.String()))
	},
	"jsonLines": func(s string) string {
		if s == "" {
			return ""
		}
		var arr []string
		if err := json.Unmarshal([]byte(s), &arr); err != nil {
			return s
		}
		return strings.Join(arr, "\n")
	},
	"countryList": func(events []Event) []string {
		seen := make(map[string]bool)
		var out []string
		for _, e := range events {
			if e.Location == nil || e.Location.Country == "" {
				continue
			}
			if !seen[e.Location.Country] {
				seen[e.Location.Country] = true
				out = append(out, e.Location.Country)
			}
		}
		sort.Strings(out)
		return out
	},
	"sourceDomain": func(actorID string) string {
		u, err := url.Parse(actorID)
		if err != nil || u.Host == "" {
			return actorID
		}
		return u.Host
	},
	"splitComma": func(s string) []string {
		var out []string
		for _, p := range strings.Split(s, ",") {
			p = strings.TrimSpace(p)
			if p != "" {
				out = append(out, p)
			}
		}
		return out
	},
	"hasPrefix": strings.HasPrefix,
	"lower":     strings.ToLower,
	"fmtBytes": func(b int64) string {
		const unit = 1024
		if b < unit {
			return fmt.Sprintf("%d B", b)
		}
		div, exp := int64(unit), 0
		for n := b / unit; n >= unit; n /= unit {
			div *= unit
			exp++
		}
		return fmt.Sprintf("%.1f %cB", float64(b)/float64(div), "KMGTPE"[exp])
	},
	"friendlyUA": func() func(string) string {
		type rule struct {
			name    string
			pattern *regexp.Regexp
		}
		rules := []rule{
			{"Edge", regexp.MustCompile(`Edg(?:e)?/(\d+)`)},
			{"Opera", regexp.MustCompile(`OPR/(\d+)`)},
			{"Chrome", regexp.MustCompile(`Chrome/(\d+)`)},
			{"Firefox", regexp.MustCompile(`Firefox/(\d+)`)},
			{"Safari", regexp.MustCompile(`Version/(\d+)`)},
		}
		return func(ua string) string {
			if ua == "" {
				return ""
			}
			for _, r := range rules {
				if m := r.pattern.FindStringSubmatch(ua); m != nil {
					return r.name + " " + m[1]
				}
			}
			if len(ua) > 40 {
				return ua[:40] + "…"
			}
			return ua
		}
	}(),
	"friendlyIP": func(ip string) string {
		if ip == "127.0.0.1" || ip == "::1" {
			return "localhost"
		}
		return ip
	},
	"parseUserMetadata": func(s string) map[string]string {
		var m map[string]string
		if s == "" {
			return nil
		}
		json.Unmarshal([]byte(s), &m)
		return m
	},
	"add": func(a, b int) int { return a + b },
	"json": func(v any) template.JS {
		b, _ := json.Marshal(v)
		return template.JS(b)
	},
	"townSlug": townSlug,
	// showRescheduledBadge reports whether the public "Rescheduled" badge
	// should show for an event: it must have a recorded previous_start_time
	// and its (new) start_time must be within site setting
	// rescheduled_badge_days of now (default 7) — keeps the badge relevant
	// without permanently flagging events rescheduled long ago (#927).
	"showRescheduledBadge": func(startTime, previousStartTime string) bool {
		if previousStartTime == "" {
			return false
		}
		t, ok := parseTime(startTime)
		if !ok {
			return false
		}
		days := siteCfg.RescheduledBadgeDays()
		return time.Until(t) <= time.Duration(days)*24*time.Hour
	},
	// pagerRange returns page numbers to display, using -1 as an ellipsis sentinel.
	"pagerRange": func(current, total int) []int {
		if total <= 7 {
			pages := make([]int, total)
			for i := range pages {
				pages[i] = i + 1
			}
			return pages
		}
		show := map[int]bool{1: true, total: true}
		for _, p := range []int{current - 1, current, current + 1} {
			if p >= 1 && p <= total {
				show[p] = true
			}
		}
		var sorted []int
		for p := range show {
			sorted = append(sorted, p)
		}
		sort.Ints(sorted)
		var out []int
		for i, p := range sorted {
			if i > 0 && p-sorted[i-1] > 1 {
				out = append(out, -1)
			}
			out = append(out, p)
		}
		return out
	},
}
