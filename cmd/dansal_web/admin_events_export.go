package main

import (
	"encoding/csv"
	"fmt"
	"io"
	"log"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// The events CSV export (#1584) holds exactly the events the maintenance view
// shows for the same filters. Fields are separated by '|', which seldom occurs
// in event text; a '|' inside a value is replaced, so an import (#1597) can
// split on it safely. The header names are the column contract for that import.
var eventsCSVHeader = []string{
	"uid", "id", "short_code", "title", "date_from", "date_to", "start_time", "end_time",
	"venue", "address", "zip", "city", "country", "organization", "tags",
	"cancelled", "published", "canonical_url", "source_url", "booking_url",
	"tickets_total", "availability", "contact_name", "contact_email", "description",
}

// csvCell makes one value safe for the export: line breaks become spaces so a
// row stays one line, '|' becomes '/', and formula-like values are neutralised.
func csvCell(s string) string {
	s = strings.NewReplacer("\r\n", " ", "\n", " ", "\r", " ", "|", "/").Replace(s)
	return csvFormulaSafe(s)
}

// exportDate and exportClock format an API timestamp as an ISO date or time,
// keeping the offset the API sent (the instance timezone).
func exportDate(s string) string {
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		return ""
	}
	return t.Format("2006-01-02")
}

func exportClock(s string) string {
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		return ""
	}
	return t.Format("15:04")
}

// eventCSVRow is one export row. The uid matches the event's feed UID.
func eventCSVRow(e Event, orgMap map[int]string, domain string) []string {
	var loc Location
	if e.Location != nil {
		loc = *e.Location
	}
	org := ""
	if e.OrganizationID != nil {
		org = orgMap[*e.OrganizationID]
	}
	tickets := ""
	if e.TicketsTotal > 0 {
		tickets = strconv.Itoa(e.TicketsTotal)
	}
	cells := []string{
		fmt.Sprintf("event-%d@%s", e.ID, domain),
		strconv.Itoa(e.ID),
		e.ShortCode,
		e.Title,
		exportDate(e.StartTime),
		exportDate(e.EndTime),
		exportClock(e.StartTime),
		exportClock(e.EndTime),
		loc.Location,
		loc.Address,
		loc.Zipcode,
		loc.Town,
		loc.Country,
		org,
		strings.Join(e.Tags, ", "),
		strconv.FormatBool(e.IsCancelled),
		strconv.FormatBool(e.IsPublished),
		fmt.Sprintf("https://%s/events/%d", domain, e.ID),
		e.SourceURL,
		e.BookingURL,
		tickets,
		e.Availability,
		e.ContactName,
		e.ContactEmail,
		e.Description,
	}
	for i := range cells {
		cells[i] = csvCell(cells[i])
	}
	return cells
}

// writeEventsCSV writes a UTF-8 byte-order mark (so spreadsheet apps read
// umlauts correctly), the header row, then one row per event.
func writeEventsCSV(w io.Writer, events []Event, orgMap map[int]string, domain string) error {
	if _, err := io.WriteString(w, "\uFEFF"); err != nil {
		return err
	}
	cw := csv.NewWriter(w)
	cw.Comma = '|'
	if err := cw.Write(eventsCSVHeader); err != nil {
		return err
	}
	for _, e := range events {
		if err := cw.Write(eventCSVRow(e, orgMap, domain)); err != nil {
			return err
		}
	}
	cw.Flush()
	return cw.Error()
}

// adminEventsExportHandler serves GET /admin/events/export.csv: the events the
// maintenance view shows for the same filters, as CSV (#1584).
func adminEventsExportHandler(cfg *Config, client *DansalClient) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		user, ok := requireLogin(w, r)
		if !ok {
			return
		}
		f := parseMaintenanceFilter(r.URL.Query())
		token := getSessionToken(r)
		events, err := fetchMaintenanceEvents(r.Context(), client, token, f)
		if err != nil {
			http.Error(w, "could not load events", http.StatusBadGateway)
			return
		}
		orgs, _ := client.GetOrganizations(r.Context())
		orgMap := make(map[int]string, len(orgs))
		for _, o := range orgs {
			orgMap[o.ID] = o.Name
		}

		scope := "all"
		if f.active() {
			scope = "filtered"
		}
		name := fmt.Sprintf("dansal-events-%s-%s.csv", scope, time.Now().Format("2006-01-02"))
		w.Header().Set("Content-Type", "text/csv; charset=utf-8")
		w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="%s"`, name))
		w.Header().Set("Cache-Control", "no-store")
		if err := writeEventsCSV(w, events, orgMap, cfg.Domain); err != nil {
			log.Printf("events export: write failed: %v", err)
			return
		}
		log.Printf("events export: user=%d scope=%s rows=%d filters=%+v", user.ID, scope, len(events), f)
	}
}
