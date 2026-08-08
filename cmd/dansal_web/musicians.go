package main

import (
	"encoding/json"
	"net/http"
	"strconv"
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

		now := time.Now()
		var futureEvents []Event
		hasPast := false
		for _, e := range allEvents {
			t, err := time.Parse(time.RFC3339, e.EndTime)
			if err != nil || t.After(now) {
				futureEvents = append(futureEvents, e)
			} else {
				hasPast = true
			}
		}

		includePast := r.URL.Query().Get("include_past") == "1"
		displayEvents := futureEvents
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
			HasPast:     hasPast,
			IncludePast: includePast,
		})
		desc := musician.Biography
		if desc == "" {
			desc = musician.Description
		}
		td.MetaDescription = metaDesc(desc, 155)
		if musician.ImageURL != "" {
			td.OGImage = "https://" + cfg.Domain + musician.ImageURL
		}
		renderTemplate(w, tmpls.musician, td)
	}
}
