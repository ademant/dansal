package main

import (
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"time"
)

type MusiciansPageData struct {
	Musicians []Musician
}

type MusicianPageData struct {
	Musician    Musician
	Events      []Event
	Slug        string
	Members     []string
	Albums      []string
	HasPast     bool
	IncludePast bool
}

// musicianSearchHandler serves GET /search/musicians?name=... — proxies the
// musician autocomplete used by the public event-suggest form so the browser
// never touches /api/v1/ directly (#1068).
func musicianSearchHandler(client *DansalClient) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		q := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("name")))
		if q == "" {
			writeJSONError(w, r, http.StatusBadRequest, "name parameter required")
			return
		}
		all, err := client.GetMusicians(r.Context())
		if err != nil {
			writeJSONError(w, r, http.StatusBadGateway, "could not load musicians")
			return
		}
		out := make([]Musician, 0, 8)
		for _, m := range all {
			if strings.Contains(strings.ToLower(m.Bandname), q) {
				out = append(out, m)
				if len(out) == 8 {
					break
				}
			}
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(out)
	}
}

func musiciansHandler(cfg *Config, tmpls *Templates, client *DansalClient, i18n *I18n) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		musicians, err := client.GetMusicians(r.Context())
		if err != nil {
			http.Error(w, "could not load musicians", http.StatusBadGateway)
			return
		}
		title := i18n.T(r, "musicians_title")
		renderTemplate(w, tmpls.musicians, tmplData(r, cfg, i18n, title, MusiciansPageData{Musicians: musicians}))
	}
}

func musicianHandler(cfg *Config, tmpls *Templates, client *DansalClient, i18n *I18n) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id, err := strconv.Atoi(r.PathValue("id"))
		if err != nil {
			http.NotFound(w, r)
			return
		}
		musician, err := client.GetMusician(r.Context(), id)
		if err != nil {
			http.NotFound(w, r)
			return
		}

		allEvents, err := client.GetAllPublicEventsByMusician(r.Context(), id)
		if err != nil {
			logHTTPError(w, r, "could not load musician events", http.StatusBadGateway)
			return
		}

		upcoming, past := splitUpcomingPast(allEvents, time.Now())

		includePast := r.URL.Query().Get("include_past") == "1"
		displayEvents := upcoming
		if includePast {
			displayEvents = allEvents
		}

		var members, albums []string
		json.Unmarshal([]byte(musician.MembersJSON), &members)
		json.Unmarshal([]byte(musician.AlbumsJSON), &albums)
		title := musician.Bandname
		td := tmplData(r, cfg, i18n, title, MusicianPageData{
			Musician:    musician,
			Events:      displayEvents,
			Slug:        orgSlug(musician.Bandname),
			Members:     members,
			Albums:      albums,
			HasPast:     len(past) > 0,
			IncludePast: includePast,
		})
		desc := musician.Biography
		if desc == "" {
			desc = musician.Description
		}
		td.MetaDescription = metaDesc(desc, metaDescMaxLen)
		if musician.ImageURL != "" {
			td.OGImage = "https://" + cfg.Domain + musician.ImageURL
		}
		renderTemplate(w, tmpls.musician, td)
	}
}
