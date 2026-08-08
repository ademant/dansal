package main

import (
	"context"
	"encoding/json"
	"fmt"
	"html/template"
	"io"
	"log"
	"net/http"
	"strconv"
	"strings"
)

// ── Musicians ─────────────────────────────────────────────────────────────────

type AdminMusiciansData struct {
	Musicians []Musician
}

type AdminMusicianEditData struct {
	Musician Musician
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

// musicianEntity wires the generic CRUD scaffold to the Musician client API.
var musicianEntity = adminEntity[Musician]{
	listPath:     "/admin/musicians",
	editPath:     musicianEditPath,
	listTmpl:     func(t *Templates) *template.Template { return t.adminMusicians },
	editTmpl:     func(t *Templates) *template.Template { return t.adminMusicianEdit },
	listTitleKey: "admin_musicians_title",
	listData: func(items []Musician) any {
		return AdminMusiciansData{Musicians: items}
	},
	editData: func(m Musician, isNew bool, errKey, from string) any {
		return AdminMusicianEditData{Musician: m, IsNew: isNew, ErrorKey: errKey, From: from}
	},
	listFn: func(ctx context.Context, client *DansalClient) ([]Musician, error) {
		return client.GetMusicians(ctx)
	},
	getFn: func(ctx context.Context, client *DansalClient, id int) (Musician, error) {
		return client.GetMusician(ctx, id)
	},
	createFn: func(ctx context.Context, client *DansalClient, m Musician, token string) (Musician, error) {
		return client.CreateMusician(ctx, m, token)
	},
	updateFn: func(ctx context.Context, client *DansalClient, id int, m Musician, token string) error {
		return client.UpdateMusician(ctx, id, m, token)
	},
	deleteFn: func(ctx context.Context, client *DansalClient, id int, token string) error {
		return client.DeleteMusician(ctx, id, token)
	},
	fromForm: musicianFromForm,
	afterCreate: func(cfg *Config, client *DansalClient, r *http.Request, created Musician) {
		uploadMusicianFiles(cfg, client, r, created.ID)
	},
	afterSave: func(cfg *Config, client *DansalClient, r *http.Request, id int) {
		uploadMusicianFiles(cfg, client, r, id)
	},
	needDeadline: true,
	loadErrMsg:   "could not load musicians",
	name:         "musician",
}

func musicianEditPath(id int) string {
	return "/admin/musicians/" + strconv.Itoa(id) + "/edit"
}

// uploadMusicianFiles pushes the image/avatar files from the create/save form
// and pings IndexNow for the musician page. Runs after the entity is saved so
// the backend has an ID to attach the files to.
func uploadMusicianFiles(cfg *Config, client *DansalClient, r *http.Request, id int) {
	token := getSessionToken(r)
	if file, header, ferr := r.FormFile("image"); ferr == nil {
		data, _ := io.ReadAll(file)
		file.Close()
		if uerr := client.UploadMusicianImage(r.Context(), id, data, header.Filename, token); uerr != nil {
			log.Printf("upload musician image error: %v", uerr)
		}
	}
	if file, header, ferr := r.FormFile("avatar"); ferr == nil {
		data, _ := io.ReadAll(file)
		file.Close()
		if uerr := client.UploadMusicianAvatar(r.Context(), id, data, header.Filename, token); uerr != nil {
			log.Printf("upload musician avatar error: %v", uerr)
		}
	}
	go notifyIndexNowPaths(cfg.publicBaseURL(), siteCfg.IndexNowKey(), []string{fmt.Sprintf("/musicians/%d", id)})
}

func adminMusicianImageDeleteHandler(cfg *Config, client *DansalClient) http.HandlerFunc {
	return adminSubResourceDeleteHandler(client, client.DeleteMusicianImage, "delete musician image %d: %v", musicianEditPath)
}

func adminMusicianAvatarDeleteHandler(cfg *Config, client *DansalClient) http.HandlerFunc {
	return adminSubResourceDeleteHandler(client, client.DeleteMusicianAvatar, "delete musician avatar %d: %v", musicianEditPath)
}
