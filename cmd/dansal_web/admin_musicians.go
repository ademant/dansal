package main

import (
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// ── Musicians ─────────────────────────────────────────────────────────────────

type AdminMusiciansData struct {
	Musicians []Musician
}

type AdminMusicianEditData struct {
	Musician Musician
	Events   []Event
	IsNew    bool
	ErrorKey string
	From     string
}

func musicianFromForm(r *http.Request) Musician {
	beginYear, _ := strconv.Atoi(strings.TrimSpace(r.FormValue("begin_year")))
	return Musician{
		Bandname:     strings.TrimSpace(r.FormValue("bandname")),
		ShortName:    strings.TrimSpace(r.FormValue("short_name")),
		Internetsite: strings.TrimSpace(r.FormValue("internetsite")),
		Description:  strings.TrimSpace(r.FormValue("description")),
		MBID:         strings.TrimSpace(r.FormValue("mbid")),
		WikidataID:   strings.TrimSpace(r.FormValue("wikidata_id")),
		DiscogsID:    strings.TrimSpace(r.FormValue("discogs_id")),
		Country:      strings.TrimSpace(r.FormValue("country")),
		BeginYear:    beginYear,
		Biography:    strings.TrimSpace(r.FormValue("biography")),
		MembersJSON:  linesToJSON(r.FormValue("members")),
		AlbumsJSON:   linesToJSON(r.FormValue("albums")),
		Mastodon:     strings.TrimSpace(r.FormValue("mastodon")),
		Instagram:    strings.TrimSpace(r.FormValue("instagram")),
		Facebook:     strings.TrimSpace(r.FormValue("facebook")),
		Soundcloud:   strings.TrimSpace(r.FormValue("soundcloud")),
		Spotify:      strings.TrimSpace(r.FormValue("spotify")),
		Deezer:       strings.TrimSpace(r.FormValue("deezer")),
		Genre:        strings.TrimSpace(r.FormValue("genre")),
		Email:        strings.TrimSpace(r.FormValue("email")),
	}
}

// linesToJSON converts a newline-separated text input to a JSON string array.
func linesToJSON(s string) string {
	var items []string
	for _, line := range strings.Split(s, "\n") {
		line = strings.TrimSpace(line)
		if line != "" {
			items = append(items, line)
		}
	}
	if len(items) == 0 {
		return ""
	}
	b, _ := json.Marshal(items)
	return string(b)
}

func adminMusiciansHandler(cfg *Config, tmpls *Templates, client *DansalClient, i18n *I18n) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		_, ok := requireLogin(w, r)
		if !ok {
			return
		}
		musicians, err := client.GetMusicians(r.Context())
		if err != nil {
			http.Error(w, "could not load musicians", http.StatusBadGateway)
			return
		}
		title := i18n.T(r, "admin_musicians_title")
		renderTemplate(w, tmpls.adminMusicians, tmplData(r, cfg, i18n, title, AdminMusiciansData{Musicians: musicians}))
	}
}

func adminMusicianNewPageHandler(cfg *Config, tmpls *Templates, i18n *I18n) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		_, ok := requireLogin(w, r)
		if !ok {
			return
		}
		title := i18n.T(r, "admin_new")
		renderTemplate(w, tmpls.adminMusicianEdit, tmplData(r, cfg, i18n, title, AdminMusicianEditData{IsNew: true}))
	}
}

func adminMusicianCreateHandler(cfg *Config, tmpls *Templates, client *DansalClient, i18n *I18n) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		_, ok := requireLogin(w, r)
		if !ok {
			return
		}
		if err := r.ParseMultipartForm(10 << 20); err != nil {
			if err := r.ParseForm(); err != nil {
				http.Error(w, "bad request", http.StatusBadRequest)
				return
			}
		}
		m := musicianFromForm(r)
		created, err := client.CreateMusician(r.Context(), m, getSessionToken(r))
		if err != nil {
			title := i18n.T(r, "admin_new")
			renderTemplate(w, tmpls.adminMusicianEdit, tmplData(r, cfg, i18n, title, AdminMusicianEditData{
				Musician: m, IsNew: true, ErrorKey: "admin_save_error",
			}))
			return
		}
		if file, header, ferr := r.FormFile("image"); ferr == nil {
			data, _ := io.ReadAll(file)
			file.Close()
			if uerr := client.UploadMusicianImage(r.Context(), created.ID, data, header.Filename, getSessionToken(r)); uerr != nil {
				log.Printf("upload musician image error: %v", uerr)
			}
		}
		if file, header, ferr := r.FormFile("avatar"); ferr == nil {
			data, _ := io.ReadAll(file)
			file.Close()
			if uerr := client.UploadMusicianAvatar(r.Context(), created.ID, data, header.Filename, getSessionToken(r)); uerr != nil {
				log.Printf("upload musician avatar error: %v", uerr)
			}
		}
		go notifyIndexNowPaths(cfg.publicBaseURL(), siteCfg.IndexNowKey(), []string{fmt.Sprintf("/musicians/%d", created.ID)})
		http.Redirect(w, r, "/admin/musicians", http.StatusSeeOther)
	}
}

func adminMusicianEditPageHandler(cfg *Config, tmpls *Templates, client *DansalClient, i18n *I18n) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		_, ok := requireLogin(w, r)
		if !ok {
			return
		}
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
		title := i18n.T(r, "admin_edit")
		renderTemplate(w, tmpls.adminMusicianEdit, tmplData(r, cfg, i18n, title, AdminMusicianEditData{
			Musician: musician,
			From:     safeReturnPath(r.URL.Query().Get("from")),
		}))
	}
}

func adminMusicianSaveHandler(cfg *Config, tmpls *Templates, client *DansalClient, i18n *I18n) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		_, ok := requireLogin(w, r)
		if !ok {
			return
		}
		// A photo upload triggers a slow AVIF re-encode on the backend (WASM-based
		// encoder, can take well over the server's default 30s WriteTimeout for a
		// detailed photo) — extend the deadline for this request rather than
		// raising it server-wide.
		_ = http.NewResponseController(w).SetWriteDeadline(time.Now().Add(170 * time.Second))
		id, err := strconv.Atoi(r.PathValue("id"))
		if err != nil {
			http.NotFound(w, r)
			return
		}
		if err := r.ParseMultipartForm(10 << 20); err != nil {
			if err := r.ParseForm(); err != nil {
				http.Error(w, "bad request", http.StatusBadRequest)
				return
			}
		}
		from := safeReturnPath(r.FormValue("from"))
		m := musicianFromForm(r)
		if err := client.UpdateMusician(r.Context(), id, m, getSessionToken(r)); err != nil {
			title := i18n.T(r, "admin_edit")
			renderTemplate(w, tmpls.adminMusicianEdit, tmplData(r, cfg, i18n, title, AdminMusicianEditData{
				Musician: m, ErrorKey: "admin_save_error", From: from,
			}))
			return
		}
		if file, header, ferr := r.FormFile("image"); ferr == nil {
			data, _ := io.ReadAll(file)
			file.Close()
			if uerr := client.UploadMusicianImage(r.Context(), id, data, header.Filename, getSessionToken(r)); uerr != nil {
				log.Printf("upload musician image error: %v", uerr)
			}
		}
		if file, header, ferr := r.FormFile("avatar"); ferr == nil {
			data, _ := io.ReadAll(file)
			file.Close()
			if uerr := client.UploadMusicianAvatar(r.Context(), id, data, header.Filename, getSessionToken(r)); uerr != nil {
				log.Printf("upload musician avatar error: %v", uerr)
			}
		}
		go notifyIndexNowPaths(cfg.publicBaseURL(), siteCfg.IndexNowKey(), []string{fmt.Sprintf("/musicians/%d", id)})
		target := "/admin/musicians"
		if from != "" {
			target = from
		}
		if p := safeReturnPath(target); p != "" {
			target = p
		} else {
			target = "/admin/musicians"
		}
		http.Redirect(w, r, target, http.StatusSeeOther)
	}
}

func adminMusicianDeleteHandler(cfg *Config, client *DansalClient) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		_, ok := requireLogin(w, r)
		if !ok {
			return
		}
		id, err := strconv.Atoi(r.PathValue("id"))
		if err != nil {
			http.NotFound(w, r)
			return
		}
		_ = client.DeleteMusician(r.Context(), id, getSessionToken(r))
		http.Redirect(w, r, "/admin/musicians", http.StatusSeeOther)
	}
}

func adminMusicianImageDeleteHandler(cfg *Config, client *DansalClient) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		_, ok := requireLogin(w, r)
		if !ok {
			return
		}
		id, err := strconv.Atoi(r.PathValue("id"))
		if err != nil {
			http.NotFound(w, r)
			return
		}
		_ = client.DeleteMusicianImage(r.Context(), id, getSessionToken(r))
		http.Redirect(w, r, fmt.Sprintf("/admin/musicians/%d/edit", id), http.StatusSeeOther)
	}
}

func adminMusicianAvatarDeleteHandler(cfg *Config, client *DansalClient) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		_, ok := requireLogin(w, r)
		if !ok {
			return
		}
		id, err := strconv.Atoi(r.PathValue("id"))
		if err != nil {
			http.NotFound(w, r)
			return
		}
		_ = client.DeleteMusicianAvatar(r.Context(), id, getSessionToken(r))
		http.Redirect(w, r, fmt.Sprintf("/admin/musicians/%d/edit", id), http.StatusSeeOther)
	}
}
