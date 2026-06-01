package main

import (
	"encoding/xml"
	"fmt"
	"io"
	"strings"
	"time"
)

// RSS 2.0 structs
type rssFeed struct {
	XMLName xml.Name   `xml:"rss"`
	Channel rssChannel `xml:"channel"`
}

type rssChannel struct {
	Title string    `xml:"title"`
	Items []rssItem `xml:"item"`
}

type rssItem struct {
	Title       string   `xml:"title"`
	Link        string   `xml:"link"`
	GUID        string   `xml:"guid"`
	Description string   `xml:"description"`
	PubDate     string   `xml:"pubDate"`
	Categories  []string `xml:"category"`
	// common event extension elements (matched by local name across any namespace)
	EventStart string `xml:"eventStart"`
	EventEnd   string `xml:"eventEnd"`
	StartDate  string `xml:"startDate"`
	EndDate    string `xml:"endDate"`
	DCDate     string `xml:"date"`
}

// Atom structs
type atomFeed struct {
	XMLName xml.Name    `xml:"feed"`
	Entries []atomEntry `xml:"entry"`
}

type atomEntry struct {
	Title      atomText       `xml:"title"`
	Links      []atomLink     `xml:"link"`
	Summary    atomText       `xml:"summary"`
	Content    atomText       `xml:"content"`
	ID         string         `xml:"id"`
	Published  string         `xml:"published"`
	Updated    string         `xml:"updated"`
	Categories []atomCategory `xml:"category"`
	// event extensions
	EventStart string `xml:"eventStart"`
	EventEnd   string `xml:"eventEnd"`
	StartDate  string `xml:"startDate"`
	EndDate    string `xml:"endDate"`
	DCDate     string `xml:"date"`
}

type atomText struct {
	Value string `xml:",chardata"`
}

type atomLink struct {
	Href string `xml:"href,attr"`
	Rel  string `xml:"rel,attr"`
}

type atomCategory struct {
	Term string `xml:"term,attr"`
}

var rssPubDateLayouts = []string{
	time.RFC1123Z,
	time.RFC1123,
	time.RFC3339,
	"2006-01-02T15:04:05",
	"2006-01-02 15:04:05",
	"2006-01-02",
}

func parseRSSDate(s string) (time.Time, bool) {
	s = strings.TrimSpace(s)
	if s == "" {
		return time.Time{}, false
	}
	for _, layout := range rssPubDateLayouts {
		if t, err := time.Parse(layout, s); err == nil {
			return t.UTC(), true
		}
	}
	return time.Time{}, false
}

// firstSet returns the first non-empty string from the arguments.
func firstSet(ss ...string) string {
	for _, s := range ss {
		if strings.TrimSpace(s) != "" {
			return strings.TrimSpace(s)
		}
	}
	return ""
}

func rssEventDates(startStr, endStr string) (time.Time, time.Time, bool) {
	startT, ok := parseRSSDate(startStr)
	if !ok {
		return time.Time{}, time.Time{}, false
	}
	if endT, ok := parseRSSDate(endStr); ok {
		return startT, endT, true
	}
	return startT, startT.Add(2 * time.Hour), true
}

func importFromRSSSource(src FetchSource) ([]Event, bool, error) {
	resp, err := fetchClient.Get(src.URL)
	if err != nil {
		return nil, false, fmt.Errorf("fetch: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, false, fmt.Errorf("remote returned %d", resp.StatusCode)
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, false, fmt.Errorf("read body: %w", err)
	}

	db.Exec("UPDATE fetch_sources SET last_fetched_at = ? WHERE id = ?", time.Now().UTC().Unix(), src.ID)

	// Try RSS 2.0
	var rssFd rssFeed
	if xmlErr := xml.Unmarshal(body, &rssFd); xmlErr == nil && rssFd.XMLName.Local == "rss" {
		return importRSSItems(rssFd.Channel.Items, src)
	}

	// Try Atom
	var atomFd atomFeed
	if xmlErr := xml.Unmarshal(body, &atomFd); xmlErr == nil && atomFd.XMLName.Local == "feed" {
		return importAtomEntries(atomFd.Entries, src)
	}

	return nil, false, fmt.Errorf("not a valid RSS 2.0 or Atom feed")
}

func importRSSItems(items []rssItem, src FetchSource) ([]Event, bool, error) {
	td := parseTemplateData(src.TemplateData)

	tx, err := db.Begin()
	if err != nil {
		return nil, false, err
	}
	defer tx.Rollback()

	now := time.Now().UTC()
	var allEvents []Event
	allCreated := true

	for _, item := range items {
		title := strings.TrimSpace(item.Title)
		if title == "" {
			continue
		}

		startStr := firstSet(item.EventStart, item.StartDate, item.DCDate, item.PubDate)
		endStr := firstSet(item.EventEnd, item.EndDate)

		startT, endT, ok := rssEventDates(startStr, endStr)
		if !ok {
			continue
		}
		if endT.Before(now) {
			continue
		}

		uid := firstSet(item.GUID, item.Link)
		if uid == "" {
			uid = src.URL + "#" + title
		}

		tags := buildRSSTags(src.Tags, item.Categories)

		eventReq := EventCreateRequest{
			UID:           uid,
			Source:        src.URL,
			FetchSourceID: src.ID,
			EventWriteRequest: EventWriteRequest{
				Title:          title,
				Description:    item.Description,
				StartTime:      startT.Format(time.RFC3339),
				EndTime:        endT.Format(time.RFC3339),
				Tags:           tags,
				URL:            firstSet(item.Link),
				OrganizationID: src.OrganizationID,
				Dances:         src.DanceIDs,
			},
		}

		if _, err := importSingleEvent(tx, eventReq, td, src.TemplateMode, &allCreated, &allEvents); err != nil {
			return nil, false, err
		}
	}

	if err := tx.Commit(); err != nil {
		return nil, false, err
	}
	return allEvents, allCreated, nil
}

func importAtomEntries(entries []atomEntry, src FetchSource) ([]Event, bool, error) {
	td := parseTemplateData(src.TemplateData)

	tx, err := db.Begin()
	if err != nil {
		return nil, false, err
	}
	defer tx.Rollback()

	now := time.Now().UTC()
	var allEvents []Event
	allCreated := true

	for _, entry := range entries {
		title := strings.TrimSpace(entry.Title.Value)
		if title == "" {
			continue
		}

		link := ""
		for _, l := range entry.Links {
			if l.Rel == "alternate" || l.Rel == "" {
				link = l.Href
				break
			}
		}

		startStr := firstSet(entry.EventStart, entry.StartDate, entry.DCDate, entry.Published, entry.Updated)
		endStr := firstSet(entry.EventEnd, entry.EndDate)

		startT, endT, ok := rssEventDates(startStr, endStr)
		if !ok {
			continue
		}
		if endT.Before(now) {
			continue
		}

		uid := firstSet(entry.ID, link)
		if uid == "" {
			uid = src.URL + "#" + title
		}

		catTerms := make([]string, 0, len(entry.Categories))
		for _, cat := range entry.Categories {
			if t := strings.TrimSpace(cat.Term); t != "" {
				catTerms = append(catTerms, t)
			}
		}
		tags := buildRSSTags(src.Tags, catTerms)
		desc := firstSet(entry.Content.Value, entry.Summary.Value)

		eventReq := EventCreateRequest{
			UID:           uid,
			Source:        src.URL,
			FetchSourceID: src.ID,
			EventWriteRequest: EventWriteRequest{
				Title:          title,
				Description:    desc,
				StartTime:      startT.Format(time.RFC3339),
				EndTime:        endT.Format(time.RFC3339),
				Tags:           tags,
				URL:            link,
				OrganizationID: src.OrganizationID,
				Dances:         src.DanceIDs,
			},
		}

		if _, err := importSingleEvent(tx, eventReq, td, src.TemplateMode, &allCreated, &allEvents); err != nil {
			return nil, false, err
		}
	}

	if err := tx.Commit(); err != nil {
		return nil, false, err
	}
	return allEvents, allCreated, nil
}

// buildRSSTags merges source-level tags with per-item categories, deduplicating.
func buildRSSTags(srcTags, itemCats []string) []string {
	seen := make(map[string]bool)
	tags := make([]string, 0, len(srcTags)+len(itemCats))
	for _, t := range itemCats {
		t = strings.TrimSpace(t)
		if t != "" && !seen[t] {
			seen[t] = true
			tags = append(tags, t)
		}
	}
	for _, t := range srcTags {
		if !seen[t] {
			seen[t] = true
			tags = append(tags, t)
		}
	}
	return tags
}
