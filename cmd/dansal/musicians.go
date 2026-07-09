package main

import (
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"
)

type Musician struct {
	ID           int    `json:"id"`
	Bandname     string `json:"bandname"`
	ShortName    string `json:"short_name,omitempty"`
	Internetsite string `json:"internetsite,omitempty"`
	Description  string `json:"description,omitempty"`
	MBID         string `json:"mbid,omitempty"`
	WikidataID   string `json:"wikidata_id,omitempty"`
	DiscogsID    string `json:"discogs_id,omitempty"`
	Country      string `json:"country,omitempty"`
	BeginYear    int    `json:"begin_year,omitempty"`
	Biography    string `json:"biography,omitempty"`
	MembersJSON  string `json:"members_json,omitempty"`
	AlbumsJSON   string `json:"albums_json,omitempty"`
	Mastodon     string `json:"mastodon,omitempty"`
	Instagram    string `json:"instagram,omitempty"`
	Facebook     string `json:"facebook,omitempty"`
	Soundcloud   string `json:"soundcloud,omitempty"`
	Spotify      string `json:"spotify,omitempty"`
	Deezer       string `json:"deezer,omitempty"`
	Genre        string `json:"genre,omitempty"`
	ImageURL     string `json:"image_url,omitempty"`
	CreatedAt    string `json:"created_at"`
	UpdatedAt    int64  `json:"updated_at,omitempty"`
	UpdatedBy    string `json:"updated_by,omitempty"`

	FutureEventCount int `json:"future_event_count,omitempty"`
	PastEventCount   int `json:"past_event_count,omitempty"`
}

type MusicianCreateRequest struct {
	Bandname     string `json:"bandname"`
	ShortName    string `json:"short_name"`
	Internetsite string `json:"internetsite"`
	Description  string `json:"description"`
	MBID         string `json:"mbid"`
	WikidataID   string `json:"wikidata_id"`
	DiscogsID    string `json:"discogs_id"`
	Country      string `json:"country"`
	BeginYear    int    `json:"begin_year"`
	Biography    string `json:"biography"`
	MembersJSON  string `json:"members_json"`
	AlbumsJSON   string `json:"albums_json"`
	Mastodon     string `json:"mastodon"`
	Instagram    string `json:"instagram"`
	Facebook     string `json:"facebook"`
	Soundcloud   string `json:"soundcloud"`
	Spotify      string `json:"spotify"`
	Deezer       string `json:"deezer"`
	Genre        string `json:"genre"`
}

// MusicianMergePatchRequest is the body accepted by PATCH
// /api/v1/musicians/{id} (Content-Type: application/merge-patch+json — RFC
// 7396). Every field is a pointer: an omitted key leaves the existing value
// unchanged; a present key sets it (an explicit "" clears a field).
type MusicianMergePatchRequest struct {
	Bandname     *string `json:"bandname,omitempty"`
	ShortName    *string `json:"short_name,omitempty"`
	Internetsite *string `json:"internetsite,omitempty"`
	Description  *string `json:"description,omitempty"`
	MBID         *string `json:"mbid,omitempty"`
	WikidataID   *string `json:"wikidata_id,omitempty"`
	DiscogsID    *string `json:"discogs_id,omitempty"`
	Country      *string `json:"country,omitempty"`
	BeginYear    *int    `json:"begin_year,omitempty"`
	Biography    *string `json:"biography,omitempty"`
	MembersJSON  *string `json:"members_json,omitempty"`
	AlbumsJSON   *string `json:"albums_json,omitempty"`
	Mastodon     *string `json:"mastodon,omitempty"`
	Instagram    *string `json:"instagram,omitempty"`
	Facebook     *string `json:"facebook,omitempty"`
	Soundcloud   *string `json:"soundcloud,omitempty"`
	Spotify      *string `json:"spotify,omitempty"`
	Deezer       *string `json:"deezer,omitempty"`
	Genre        *string `json:"genre,omitempty"`
}

const musicianCols = `id, bandname,
	COALESCE(short_name,''), COALESCE(internetsite,''), COALESCE(description,''),
	COALESCE(mbid,''), COALESCE(wikidata_id,''), COALESCE(discogs_id,''), COALESCE(country,''), COALESCE(begin_year,0),
	COALESCE(biography,''), COALESCE(members_json,''), COALESCE(albums_json,''),
	COALESCE(mastodon,''), COALESCE(instagram,''),
	COALESCE(facebook,''), COALESCE(soundcloud,''),
	COALESCE(spotify,''), COALESCE(deezer,''), COALESCE(genre,''), created_at, COALESCE(updated_at,0), COALESCE(updated_by,'')`

// scanMusician scans a musicianCols row into a Musician. Extra destination
// pointers (e.g. for appended event-count columns) can be passed via extra.
func scanMusician(row interface{ Scan(...any) error }, extra ...any) (Musician, error) {
	var m Musician
	dest := []any{&m.ID, &m.Bandname, &m.ShortName, &m.Internetsite, &m.Description,
		&m.MBID, &m.WikidataID, &m.DiscogsID, &m.Country, &m.BeginYear, &m.Biography, &m.MembersJSON, &m.AlbumsJSON,
		&m.Mastodon, &m.Instagram, &m.Facebook, &m.Soundcloud,
		&m.Spotify, &m.Deezer, &m.Genre, &m.CreatedAt, &m.UpdatedAt, &m.UpdatedBy}
	err := row.Scan(append(dest, extra...)...)
	if err == nil {
		m.ImageURL = musicianImageURL(m.ID)
	}
	return m, err
}

// GET /api/v1/musicians - List all musicians; optional filters
func getMusicians(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	withCounts := q.Get("with_event_counts") == "true"

	query := "SELECT " + musicianCols
	if withCounts {
		query += `, COALESCE(ec.future_count,0), COALESCE(ec.past_count,0)`
	}
	query += " FROM musicians"
	if withCounts {
		query += ` LEFT JOIN (
			SELECT em.musician_id,
				SUM(CASE WHEN e.start_time > strftime('%s','now') AND e.is_published=1 THEN 1 ELSE 0 END) AS future_count,
				SUM(CASE WHEN e.start_time <= strftime('%s','now') AND e.is_published=1 THEN 1 ELSE 0 END) AS past_count
			FROM event_musicians em JOIN events e ON e.id = em.event_id
			GROUP BY em.musician_id
		) ec ON ec.musician_id = musicians.id`
	}
	var args []any
	where := false

	addWhere := func(clause string, vals ...any) {
		if !where {
			query += " WHERE " + clause
			where = true
		} else {
			query += " AND " + clause
		}
		args = append(args, vals...)
	}

	if orgIDStr := q.Get("organization_id"); orgIDStr != "" {
		addWhere(`id IN (
			   SELECT DISTINCT em.musician_id FROM event_musicians em
			   JOIN events e ON e.id = em.event_id
			   WHERE e.organization_id = ? AND e.is_published = 1
			 )`, orgIDStr)
	}
	if name := q.Get("name"); name != "" {
		addWhere("LOWER(bandname) LIKE LOWER(?)", "%"+name+"%")
	}
	if mbid := q.Get("mbid"); mbid != "" {
		addWhere("mbid=?", mbid)
	}
	if wid := q.Get("wikidata_id"); wid != "" {
		addWhere("wikidata_id=?", wid)
	}
	if did := q.Get("discogs_id"); did != "" {
		addWhere("discogs_id=?", did)
	}
	if country := q.Get("country"); country != "" {
		addWhere("country=?", country)
	}
	query += " ORDER BY bandname"

	rows, err := db.Query(query, args...)
	if err != nil {
		writeError(w, err.Error(), http.StatusInternalServerError)
		return
	}
	defer rows.Close()

	musicians := []Musician{}
	for rows.Next() {
		var extra []any
		var futureCount, pastCount int
		if withCounts {
			extra = append(extra, &futureCount, &pastCount)
		}
		m, err := scanMusician(rows, extra...)
		if err != nil {
			writeError(w, err.Error(), http.StatusInternalServerError)
			return
		}
		if withCounts {
			m.FutureEventCount = futureCount
			m.PastEventCount = pastCount
		}
		musicians = append(musicians, m)
	}

	accept := r.Header.Get("Accept")
	if strings.Contains(accept, "application/atom+xml") {
		writeMusiciansAtom(w, r, musicians)
		return
	}
	writeJSON(w, musicians)
}

func writeMusiciansAtom(w http.ResponseWriter, r *http.Request, musicians []Musician) {
	host := r.Host
	entries := make([]apiFeedEntry, 0, len(musicians))
	for _, m := range musicians {
		summary := m.Description
		if len(summary) > 200 {
			summary = summary[:200]
		}
		e := apiFeedEntry{
			Title:   m.Bandname,
			ID:      "https://" + host + "/api/v1/musicians/" + strconv.Itoa(m.ID),
			Updated: atomTime(m.UpdatedAt),
			Summary: summary,
		}
		if m.Internetsite != "" {
			e.Links = append(e.Links, apiFeedLink{Rel: "alternate", Href: m.Internetsite})
		}
		if m.MBID != "" {
			e.Links = append(e.Links, apiFeedLink{Rel: "related", Href: "https://musicbrainz.org/artist/" + m.MBID})
		}
		if m.WikidataID != "" {
			e.Links = append(e.Links, apiFeedLink{Rel: "related", Href: "https://www.wikidata.org/wiki/" + m.WikidataID})
		}
		entries = append(entries, e)
	}
	writeAtom(w, apiFeed{
		XMLNS:   "http://www.w3.org/2005/Atom",
		Title:   "Musicians",
		ID:      "https://" + r.Host + "/api/v1/musicians",
		Updated: atomTime(0),
		Entries: entries,
	})
}

// GET /api/v1/musicians/{id} - Get single musician
func getMusician(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	musician, err := scanMusician(db.QueryRow("SELECT "+musicianCols+" FROM musicians WHERE id = ?", id))
	if err == sql.ErrNoRows {
		writeError(w, "Musician not found", http.StatusNotFound)
		return
	} else if err != nil {
		writeError(w, err.Error(), http.StatusInternalServerError)
		return
	}

	accept := r.Header.Get("Accept")
	if strings.Contains(accept, "application/atom+xml") {
		writeMusiciansAtom(w, r, []Musician{musician})
		return
	}
	writeJSON(w, musician)
}

// POST /api/v1/musicians - Create one or more musicians
func createMusician(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")

	callerID, callerRole := callerFromRequest(r)
	if callerRole != RoleAdmin && callerRole != RoleUser {
		writeError(w, "Forbidden", http.StatusForbidden)
		return
	}

	body, err := io.ReadAll(r.Body)
	if err != nil {
		status := http.StatusBadRequest
		if errors.As(err, new(*http.MaxBytesError)) {
			status = http.StatusRequestEntityTooLarge
		}
		writeError(w, err.Error(), status)
		return
	}

	var reqs []MusicianCreateRequest
	if json.Unmarshal(body, &reqs) != nil || len(reqs) == 0 || reqs[0].Bandname == "" {
		var single MusicianCreateRequest
		if err := json.Unmarshal(body, &single); err != nil {
			writeError(w, "Invalid request body", http.StatusBadRequest)
			return
		}
		reqs = []MusicianCreateRequest{single}
	}

	musicians := make([]Musician, 0, len(reqs))
	for _, req := range reqs {
		m, err := scanMusician(db.QueryRow(
			`INSERT INTO musicians (bandname, short_name, internetsite, description, mbid, wikidata_id, discogs_id, country, begin_year, biography, members_json, albums_json, mastodon, instagram, facebook, soundcloud, spotify, deezer, genre, created_by_id)
			 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?) RETURNING `+musicianCols,
			req.Bandname, req.ShortName, req.Internetsite, req.Description, req.MBID,
			req.WikidataID, req.DiscogsID, req.Country, req.BeginYear, req.Biography, req.MembersJSON, req.AlbumsJSON,
			req.Mastodon, req.Instagram, req.Facebook, req.Soundcloud,
			req.Spotify, req.Deezer, req.Genre, callerID,
		))
		if err != nil {
			writeError(w, err.Error(), http.StatusInternalServerError)
			return
		}
		musicians = append(musicians, m)
	}

	w.WriteHeader(http.StatusCreated)
	json.NewEncoder(w).Encode(musicians)
}

// PUT /api/v1/musicians/{id} - Update musician
func updateMusician(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")

	callerID, requesterRole := callerFromRequest(r)
	if requesterRole != RoleAdmin && requesterRole != RoleUser {
		writeError(w, "Forbidden", http.StatusForbidden)
		return
	}

	id := r.PathValue("id")

	var req MusicianCreateRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, err.Error(), http.StatusBadRequest)
		return
	}

	result, err := db.Exec(
		`UPDATE musicians SET bandname=?, short_name=?, internetsite=?, description=?, mbid=?,
		 wikidata_id=?, discogs_id=?, country=?, begin_year=?, biography=?, members_json=?, albums_json=?,
		 mastodon=?, instagram=?, facebook=?, soundcloud=?, spotify=?, deezer=?, genre=?,
		 updated_at=strftime('%s','now'), updated_by=? WHERE id=?`,
		req.Bandname, req.ShortName, req.Internetsite, req.Description, req.MBID,
		req.WikidataID, req.DiscogsID, req.Country, req.BeginYear, req.Biography, req.MembersJSON, req.AlbumsJSON,
		req.Mastodon, req.Instagram, req.Facebook, req.Soundcloud,
		req.Spotify, req.Deezer, req.Genre, resolveDisplayName(callerID), id,
	)
	if err != nil {
		writeError(w, err.Error(), http.StatusInternalServerError)
		return
	}

	rowsAffected, _ := result.RowsAffected()
	if rowsAffected == 0 {
		writeError(w, "Musician not found", http.StatusNotFound)
		return
	}

	musician, err := scanMusician(db.QueryRow("SELECT "+musicianCols+" FROM musicians WHERE id = ?", id))
	if err != nil {
		writeError(w, err.Error(), http.StatusInternalServerError)
		return
	}

	json.NewEncoder(w).Encode(musician)
}

// PATCH /api/v1/musicians/{id} - partial update (RFC 7396 JSON Merge Patch)
func patchMusician(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	if ct := r.Header.Get("Content-Type"); ct != "application/merge-patch+json" {
		writeError(w, "PATCH requires Content-Type: application/merge-patch+json", http.StatusUnsupportedMediaType)
		return
	}
	_, requesterRole := callerFromRequest(r)
	if requesterRole != RoleAdmin && requesterRole != RoleUser {
		writeError(w, "Forbidden", http.StatusForbidden)
		return
	}

	id := r.PathValue("id")
	m, err := scanMusician(db.QueryRow("SELECT "+musicianCols+" FROM musicians WHERE id = ?", id))
	if err == sql.ErrNoRows {
		writeError(w, "Musician not found", http.StatusNotFound)
		return
	} else if err != nil {
		writeError(w, err.Error(), http.StatusInternalServerError)
		return
	}

	var req MusicianMergePatchRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, err.Error(), http.StatusBadRequest)
		return
	}
	if req.Bandname != nil {
		m.Bandname = *req.Bandname
	}
	if req.ShortName != nil {
		m.ShortName = *req.ShortName
	}
	if req.Internetsite != nil {
		m.Internetsite = *req.Internetsite
	}
	if req.Description != nil {
		m.Description = *req.Description
	}
	if req.MBID != nil {
		m.MBID = *req.MBID
	}
	if req.WikidataID != nil {
		m.WikidataID = *req.WikidataID
	}
	if req.DiscogsID != nil {
		m.DiscogsID = *req.DiscogsID
	}
	if req.Country != nil {
		m.Country = *req.Country
	}
	if req.BeginYear != nil {
		m.BeginYear = *req.BeginYear
	}
	if req.Biography != nil {
		m.Biography = *req.Biography
	}
	if req.MembersJSON != nil {
		m.MembersJSON = *req.MembersJSON
	}
	if req.AlbumsJSON != nil {
		m.AlbumsJSON = *req.AlbumsJSON
	}
	if req.Mastodon != nil {
		m.Mastodon = *req.Mastodon
	}
	if req.Instagram != nil {
		m.Instagram = *req.Instagram
	}
	if req.Facebook != nil {
		m.Facebook = *req.Facebook
	}
	if req.Soundcloud != nil {
		m.Soundcloud = *req.Soundcloud
	}
	if req.Spotify != nil {
		m.Spotify = *req.Spotify
	}
	if req.Deezer != nil {
		m.Deezer = *req.Deezer
	}
	if req.Genre != nil {
		m.Genre = *req.Genre
	}

	callerID, _ := callerFromRequest(r)
	if _, err := db.Exec(
		`UPDATE musicians SET bandname=?, short_name=?, internetsite=?, description=?, mbid=?,
		 wikidata_id=?, discogs_id=?, country=?, begin_year=?, biography=?, members_json=?, albums_json=?,
		 mastodon=?, instagram=?, facebook=?, soundcloud=?, spotify=?, deezer=?, genre=?,
		 updated_at=strftime('%s','now'), updated_by=? WHERE id=?`,
		m.Bandname, m.ShortName, m.Internetsite, m.Description, m.MBID,
		m.WikidataID, m.DiscogsID, m.Country, m.BeginYear, m.Biography, m.MembersJSON, m.AlbumsJSON,
		m.Mastodon, m.Instagram, m.Facebook, m.Soundcloud,
		m.Spotify, m.Deezer, m.Genre, resolveDisplayName(callerID), id,
	); err != nil {
		writeError(w, err.Error(), http.StatusInternalServerError)
		return
	}

	musician, err := scanMusician(db.QueryRow("SELECT "+musicianCols+" FROM musicians WHERE id = ?", id))
	if err != nil {
		writeError(w, err.Error(), http.StatusInternalServerError)
		return
	}
	json.NewEncoder(w).Encode(musician)
}

// DELETE /api/v1/musicians/{id} - Delete musician
func deleteMusician(w http.ResponseWriter, r *http.Request) {
	callerID, callerRole := callerFromRequest(r)
	if callerRole != RoleAdmin && callerRole != RoleUser {
		writeError(w, "Forbidden", http.StatusForbidden)
		return
	}

	id := r.PathValue("id")

	if callerRole != RoleAdmin {
		var createdBy sql.NullInt64
		if err := db.QueryRow("SELECT created_by_id FROM musicians WHERE id = ?", id).Scan(&createdBy); err == sql.ErrNoRows {
			writeError(w, "Musician not found", http.StatusNotFound)
			return
		} else if err != nil {
			writeError(w, err.Error(), http.StatusInternalServerError)
			return
		}
		if !createdBy.Valid || int(createdBy.Int64) != callerID {
			writeError(w, "Forbidden: you can only delete musicians you created", http.StatusForbidden)
			return
		}
	}

	result, err := db.Exec("DELETE FROM musicians WHERE id = ?", id)
	if err != nil {
		writeError(w, err.Error(), http.StatusInternalServerError)
		return
	}

	rowsAffected, _ := result.RowsAffected()
	if rowsAffected == 0 {
		writeError(w, "Musician not found", http.StatusNotFound)
		return
	}

	w.WriteHeader(http.StatusNoContent)
}
