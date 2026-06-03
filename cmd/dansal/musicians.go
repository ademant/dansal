package main

import (
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"net/http"
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

const musicianCols = `id, bandname,
	COALESCE(short_name,''), COALESCE(internetsite,''), COALESCE(description,''),
	COALESCE(mbid,''), COALESCE(wikidata_id,''), COALESCE(discogs_id,''), COALESCE(country,''), COALESCE(begin_year,0),
	COALESCE(biography,''), COALESCE(members_json,''), COALESCE(albums_json,''),
	COALESCE(mastodon,''), COALESCE(instagram,''),
	COALESCE(facebook,''), COALESCE(soundcloud,''),
	COALESCE(spotify,''), COALESCE(deezer,''), COALESCE(genre,''), created_at`

func scanMusician(row interface{ Scan(...any) error }) (Musician, error) {
	var m Musician
	err := row.Scan(&m.ID, &m.Bandname, &m.ShortName, &m.Internetsite, &m.Description,
		&m.MBID, &m.WikidataID, &m.DiscogsID, &m.Country, &m.BeginYear, &m.Biography, &m.MembersJSON, &m.AlbumsJSON,
		&m.Mastodon, &m.Instagram, &m.Facebook, &m.Soundcloud,
		&m.Spotify, &m.Deezer, &m.Genre, &m.CreatedAt)
	if err == nil {
		m.ImageURL = musicianImageURL(m.ID)
	}
	return m, err
}

// GET /api/v1/musicians - List all musicians; optional ?organization_id=N filter
func getMusicians(w http.ResponseWriter, r *http.Request) {
	var rows *sql.Rows
	var err error
	if orgIDStr := r.URL.Query().Get("organization_id"); orgIDStr != "" {
		rows, err = db.Query(
			`SELECT `+musicianCols+` FROM musicians
			 WHERE id IN (
			   SELECT DISTINCT em.musician_id FROM event_musicians em
			   JOIN events e ON e.id = em.event_id
			   WHERE e.organization_id = ? AND e.is_published = 1
			 ) ORDER BY bandname`, orgIDStr)
	} else {
		rows, err = db.Query("SELECT " + musicianCols + " FROM musicians ORDER BY bandname")
	}
	if err != nil {
		writeError(w, err.Error(), http.StatusInternalServerError)
		return
	}
	defer rows.Close()

	musicians := []Musician{}
	for rows.Next() {
		m, err := scanMusician(rows)
		if err != nil {
			writeError(w, err.Error(), http.StatusInternalServerError)
			return
		}
		musicians = append(musicians, m)
	}

	writeJSON(w, musicians)
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

	requesterRole := r.Header.Get("X-User-Role")
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
		 mastodon=?, instagram=?, facebook=?, soundcloud=?, spotify=?, deezer=?, genre=? WHERE id=?`,
		req.Bandname, req.ShortName, req.Internetsite, req.Description, req.MBID,
		req.WikidataID, req.DiscogsID, req.Country, req.BeginYear, req.Biography, req.MembersJSON, req.AlbumsJSON,
		req.Mastodon, req.Instagram, req.Facebook, req.Soundcloud,
		req.Spotify, req.Deezer, req.Genre, id,
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
