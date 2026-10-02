package main

import (
	"context"
	"encoding/json"
	"errors"
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
	Musician    Musician
	IsNew       bool
	ErrorKey    string
	ErrorDetail string // #1421
	ErrorRef    string
	From        string

	// ImageUploadError/ImageUploadWidget (#1285): a scoped notice for a
	// failed image/avatar upload — the musician itself saved fine either way.
	ImageUploadError  string
	ImageUploadWidget string
}

func musicianFromForm(r *http.Request) Musician {
	beginYear, _ := strconv.Atoi(strings.TrimSpace(r.FormValue("begin_year")))
	return Musician{
		Bandname:         strings.TrimSpace(r.FormValue("bandname")),
		ShortName:        strings.TrimSpace(r.FormValue("short_name")),
		Internetsite:     strings.TrimSpace(r.FormValue("internetsite")),
		Description:      strings.TrimSpace(r.FormValue("description")),
		MBID:             strings.TrimSpace(r.FormValue("mbid")),
		WikidataID:       strings.TrimSpace(r.FormValue("wikidata_id")),
		DiscogsID:        strings.TrimSpace(r.FormValue("discogs_id")),
		Country:          strings.TrimSpace(r.FormValue("country")),
		BeginYear:        beginYear,
		Biography:        strings.TrimSpace(r.FormValue("biography")),
		MembersJSON:      linesToJSON(r.FormValue("members")),
		AlbumsJSON:       linesToJSON(r.FormValue("albums")),
		Mastodon:         strings.TrimSpace(r.FormValue("mastodon")),
		Instagram:        strings.TrimSpace(r.FormValue("instagram")),
		Facebook:         strings.TrimSpace(r.FormValue("facebook")),
		Soundcloud:       strings.TrimSpace(r.FormValue("soundcloud")),
		Spotify:          strings.TrimSpace(r.FormValue("spotify")),
		Deezer:           strings.TrimSpace(r.FormValue("deezer")),
		Genre:            strings.TrimSpace(r.FormValue("genre")),
		Email:            strings.TrimSpace(r.FormValue("email")),
		ImageAIGenerated: r.FormValue("image_ai_generated") == "1",
		Media:            mediaLinksFromForm(r),
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
	editData: func(m Musician, isNew bool, saveErr SaveError, from string, imgFlash editFlash) any {
		return AdminMusicianEditData{
			Musician: m, IsNew: isNew, ErrorKey: saveErr.Key, ErrorDetail: saveErr.Detail, ErrorRef: saveErr.Ref, From: from,
			ImageUploadError: imgFlash.Key, ImageUploadWidget: imgFlash.Widget,
		}
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
	afterCreate: func(cfg *Config, client *DansalClient, r *http.Request, created Musician) FlashMsg {
		return uploadMusicianFiles(cfg, client, r, created.ID)
	},
	afterSave: func(cfg *Config, client *DansalClient, r *http.Request, id int) FlashMsg {
		return uploadMusicianFiles(cfg, client, r, id)
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
// the backend has an ID to attach the files to. Returns a FlashMsg (#1285)
// when either upload fails, so Create/Save can surface a scoped notice
// instead of silently discarding it; image is checked first, so if both
// somehow fail in the same request the image message wins.
func uploadMusicianFiles(cfg *Config, client *DansalClient, r *http.Request, id int) FlashMsg {
	var flash FlashMsg
	token := getSessionToken(r)
	if file, header, ferr := r.FormFile("image"); ferr == nil {
		data, _ := io.ReadAll(file)
		file.Close()
		if uerr := client.UploadMusicianImage(r.Context(), id, data, header.Filename, token); uerr != nil {
			log.Printf("upload musician image error: %v", uerr)
			flash = imageUploadErrorFlash("image", uerr)
		}
	}
	if file, header, ferr := r.FormFile("avatar"); ferr == nil {
		data, _ := io.ReadAll(file)
		file.Close()
		if uerr := client.UploadMusicianAvatar(r.Context(), id, data, header.Filename, token); uerr != nil {
			log.Printf("upload musician avatar error: %v", uerr)
			if flash.ImageUploadError == "" {
				flash = imageUploadErrorFlash("avatar", uerr)
			}
		}
	}
	if gflash := saveMusicianGallery(client, r, id, token); flash.ImageUploadError == "" {
		flash = gflash
	}
	go notifyIndexNowPaths(cfg.publicBaseURL(), siteCfg.IndexNowKey(), []string{fmt.Sprintf("/musicians/%d", id)})
	return flash
}

// saveMusicianGallery applies the form's gallery section (#1362): order,
// captions, AI flags and removals of the existing pictures in one PUT, then
// each newly picked file as its own upload. The PUT only runs when the form
// rendered the section with existing pictures (gallery_present), so a form
// without it can never wipe the gallery. Stops at the first failed upload —
// a full gallery or an oversized file would fail the rest the same way.
func saveMusicianGallery(client *DansalClient, r *http.Request, id int, token string) FlashMsg {
	if r.FormValue("gallery_present") == "1" {
		var items []GalleryItemUpdate
		for _, raw := range r.Form["gallery_id"] {
			gid, err := strconv.Atoi(raw)
			if err != nil || r.FormValue("gallery_remove_"+raw) == "1" {
				continue
			}
			items = append(items, GalleryItemUpdate{
				ID:          gid,
				Caption:     strings.TrimSpace(r.FormValue("gallery_caption_" + raw)),
				AIGenerated: r.FormValue("gallery_ai_"+raw) == "1",
			})
		}
		if err := client.UpdateMusicianGallery(r.Context(), id, items, token); err != nil {
			log.Printf("update musician gallery %d: %v", id, err)
			return imageUploadErrorFlash("gallery", err)
		}
	}
	if r.MultipartForm == nil {
		return FlashMsg{}
	}
	ai := r.FormValue("gallery_new_ai") == "1"
	for _, fh := range r.MultipartForm.File["gallery_new"] {
		f, err := fh.Open()
		if err != nil {
			continue
		}
		data, _ := io.ReadAll(f)
		f.Close()
		if len(data) == 0 {
			continue
		}
		if err := client.UploadMusicianGalleryImage(r.Context(), id, data, fh.Filename, "", ai, token); err != nil {
			log.Printf("upload musician gallery picture %d: %v", id, err)
			return galleryUploadErrorFlash(err)
		}
	}
	return FlashMsg{}
}

// galleryUploadErrorFlash is imageUploadErrorFlash plus the gallery's own
// 409 (the configured picture limit is reached).
func galleryUploadErrorFlash(err error) FlashMsg {
	var ae *apiHTTPError
	if errors.As(err, &ae) && ae.StatusCode == http.StatusConflict {
		return FlashMsg{ImageUploadError: "gallery_full", ImageUploadWidget: "gallery"}
	}
	return imageUploadErrorFlash("gallery", err)
}

func adminMusicianImageDeleteHandler(cfg *Config, client *DansalClient) http.HandlerFunc {
	return adminSubResourceDeleteHandler(client, client.DeleteMusicianImage, "delete musician image %d: %v", musicianEditPath)
}

func adminMusicianAvatarDeleteHandler(cfg *Config, client *DansalClient) http.HandlerFunc {
	return adminSubResourceDeleteHandler(client, client.DeleteMusicianAvatar, "delete musician avatar %d: %v", musicianEditPath)
}
