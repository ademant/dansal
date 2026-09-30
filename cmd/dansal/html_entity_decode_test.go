package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
)

// entityReq is a small helper mirroring mediaTestRequest (owner_media_test.go)
// for the write handlers exercised in this file.
func entityReq(method, target, body string) *http.Request {
	r := httptest.NewRequest(method, target, strings.NewReader(body))
	r.Header.Set("X-User-ID", "1")
	r.Header.Set("X-User-Role", RoleAdmin)
	return r
}

// #1405: dansal never renders title/description/name/address fields as
// HTML, so a producer that HTML-entity-encodes such a field before sending
// it (e.g. WordPress's get_the_title()) must have that entity text decoded
// on write — otherwise dansal-web's own output escaping doubles it into
// visibly garbled text (confirmed live via wp-dansal#144).
func TestHTMLEntityDecodeOnWrite(t *testing.T) {
	setupDedupTestDB(t)
	db.Exec("INSERT INTO users (id, email, display_name, role) VALUES (1, 'admin@example.test', 'Admin', 'admin')")
	if orgAvatars == nil {
		orgAvatars = newAvatarSet(t.TempDir(), "/api/v1/org-avatars/")
	}
	if musicianAvatars == nil {
		musicianAvatars = newAvatarSet(t.TempDir(), "/api/v1/musician-avatars/")
	}
	if instructorAvatars == nil {
		instructorAvatars = newAvatarSet(t.TempDir(), "/api/v1/instructor-avatars/")
	}

	const (
		encodedTitle = "Tanzlernabend &#8211; Kost ar c&#8217;hoat"
		plainTitle   = "Tanzlernabend – Kost ar c’hoat"
		encodedDesc  = "Rock &amp; Roll &#8211; come as you are"
		plainDesc    = "Rock & Roll – come as you are"
	)

	t.Run("insertEvent (feed imports, POST /api/v1/events)", func(t *testing.T) {
		id, _, _, err := insertEvent(db, EventInput{
			Title:       encodedTitle,
			Description: encodedDesc,
			StartTime:   1900000000,
			EndTime:     1900003600,
			IsPublished: true,
		})
		if err != nil {
			t.Fatal(err)
		}
		var title, desc string
		db.QueryRow("SELECT title, description FROM events WHERE id=?", id).Scan(&title, &desc)
		if title != plainTitle {
			t.Errorf("title = %q, want %q", title, plainTitle)
		}
		if desc != plainDesc {
			t.Errorf("description = %q, want %q", desc, plainDesc)
		}
	})

	t.Run("updateEvent (PUT)", func(t *testing.T) {
		id, _, _, err := insertEvent(db, EventInput{Title: "Original", StartTime: 1900000000, EndTime: 1900003600, IsPublished: true})
		if err != nil {
			t.Fatal(err)
		}
		body := `{"title":"` + encodedTitle + `","description":"` + encodedDesc + `","start_time":"2030-03-01T20:00:00Z","end_time":"2030-03-01T23:00:00Z"}`
		r := entityReq("PUT", "/api/v1/events/x", body)
		r.SetPathValue("id", strconv.Itoa(id))
		w := httptest.NewRecorder()
		updateEvent(w, r)
		if w.Code != http.StatusOK {
			t.Fatalf("status = %d, body=%s", w.Code, w.Body.String())
		}
		var got Event
		if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
			t.Fatalf("decode: %v, body=%s", err, w.Body.String())
		}
		if got.Title != plainTitle {
			t.Errorf("title = %q, want %q", got.Title, plainTitle)
		}
		if got.Description != plainDesc {
			t.Errorf("description = %q, want %q", got.Description, plainDesc)
		}
	})

	t.Run("patchEvent (PATCH)", func(t *testing.T) {
		id, _, _, err := insertEvent(db, EventInput{Title: "Original", StartTime: 1900000000, EndTime: 1900003600, IsPublished: true})
		if err != nil {
			t.Fatal(err)
		}
		body := `{"title":"` + encodedTitle + `","description":"` + encodedDesc + `"}`
		r := entityReq("PATCH", "/api/v1/events/x", body)
		r.Header.Set("Content-Type", "application/merge-patch+json")
		r.SetPathValue("id", strconv.Itoa(id))
		w := httptest.NewRecorder()
		patchEvent(w, r)
		if w.Code != http.StatusOK {
			t.Fatalf("status = %d, body=%s", w.Code, w.Body.String())
		}
		var got Event
		if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
			t.Fatalf("decode: %v, body=%s", err, w.Body.String())
		}
		if got.Title != plainTitle {
			t.Errorf("title = %q, want %q", got.Title, plainTitle)
		}
		if got.Description != plainDesc {
			t.Errorf("description = %q, want %q", got.Description, plainDesc)
		}
	})

	t.Run("ensureLocation (feed imports + nested location on event write)", func(t *testing.T) {
		id, err := ensureLocation(db, EventLocationRequest{
			Location: "Salle des F&ecirc;tes",
			Address:  "1 Rue de l&#8217;&Eacute;glise",
			Town:     "Ch&acirc;teau-Gontier",
		})
		if err != nil {
			t.Fatal(err)
		}
		var loc, addr, town string
		db.QueryRow("SELECT location, address, town FROM locations WHERE id=?", id).Scan(&loc, &addr, &town)
		if loc != "Salle des Fêtes" {
			t.Errorf("location = %q, want %q", loc, "Salle des Fêtes")
		}
		if addr != "1 Rue de l’Église" {
			t.Errorf("address = %q, want %q", addr, "1 Rue de l’Église")
		}
		if town != "Château-Gontier" {
			t.Errorf("town = %q, want %q", town, "Château-Gontier")
		}
	})

	t.Run("createOrganization", func(t *testing.T) {
		w := httptest.NewRecorder()
		createOrganization(w, entityReq("POST", "/api/v1/organizations", `{"name":"Balfolk &amp; Fest-Noz Ass&#233;"}`))
		if w.Code != http.StatusCreated {
			t.Fatalf("status = %d, body=%s", w.Code, w.Body.String())
		}
		var org Organization
		if err := json.Unmarshal(w.Body.Bytes(), &org); err != nil {
			t.Fatalf("decode: %v, body=%s", err, w.Body.String())
		}
		if want := "Balfolk & Fest-Noz Assé"; org.Name != want {
			t.Errorf("name = %q, want %q", org.Name, want)
		}
	})

	t.Run("createMusician", func(t *testing.T) {
		w := httptest.NewRecorder()
		createMusician(w, entityReq("POST", "/api/v1/musicians", `{"bandname":"Kost ar c&#8217;hoat"}`))
		if w.Code != http.StatusCreated {
			t.Fatalf("status = %d, body=%s", w.Code, w.Body.String())
		}
		var created []Musician
		if err := json.Unmarshal(w.Body.Bytes(), &created); err != nil {
			t.Fatalf("decode: %v, body=%s", err, w.Body.String())
		}
		if len(created) != 1 {
			t.Fatalf("got %d musicians, want 1", len(created))
		}
		if want := "Kost ar c’hoat"; created[0].Bandname != want {
			t.Errorf("bandname = %q, want %q", created[0].Bandname, want)
		}
	})

	t.Run("createInstructor", func(t *testing.T) {
		w := httptest.NewRecorder()
		createInstructor(w, entityReq("POST", "/api/v1/instructors", `{"name":"Ma&euml;lle Le Goff"}`))
		if w.Code != http.StatusCreated {
			t.Fatalf("status = %d, body=%s", w.Code, w.Body.String())
		}
		var inst Instructor
		if err := json.Unmarshal(w.Body.Bytes(), &inst); err != nil {
			t.Fatalf("decode: %v, body=%s", err, w.Body.String())
		}
		if want := "Maëlle Le Goff"; inst.Name != want {
			t.Errorf("name = %q, want %q", inst.Name, want)
		}
	})

	seedOrg := func(t *testing.T, name string) int {
		t.Helper()
		w := httptest.NewRecorder()
		createOrganization(w, entityReq("POST", "/api/v1/organizations", `{"name":"`+name+`"}`))
		if w.Code != http.StatusCreated {
			t.Fatalf("seed org: status = %d, body=%s", w.Code, w.Body.String())
		}
		var org Organization
		json.Unmarshal(w.Body.Bytes(), &org)
		return org.ID
	}

	t.Run("updateOrganization (PUT)", func(t *testing.T) {
		id := seedOrg(t, "Original PUT target")
		w := httptest.NewRecorder()
		r := entityReq("PUT", "/api/v1/organizations/x", `{"name":"Balfolk &amp; Fest-Noz Ass&#233; PUT"}`)
		r.SetPathValue("id", strconv.Itoa(id))
		updateOrganization(w, r)
		if w.Code != http.StatusOK {
			t.Fatalf("status = %d, body=%s", w.Code, w.Body.String())
		}
		var org Organization
		json.Unmarshal(w.Body.Bytes(), &org)
		if want := "Balfolk & Fest-Noz Assé PUT"; org.Name != want {
			t.Errorf("name = %q, want %q", org.Name, want)
		}
	})

	t.Run("patchOrganization (PATCH)", func(t *testing.T) {
		id := seedOrg(t, "Original PATCH target")
		w := httptest.NewRecorder()
		r := entityReq("PATCH", "/api/v1/organizations/x", `{"name":"Balfolk &amp; Fest-Noz Ass&#233; PATCH"}`)
		r.Header.Set("Content-Type", "application/merge-patch+json")
		r.SetPathValue("id", strconv.Itoa(id))
		patchOrganization(w, r)
		if w.Code != http.StatusOK {
			t.Fatalf("status = %d, body=%s", w.Code, w.Body.String())
		}
		var org Organization
		json.Unmarshal(w.Body.Bytes(), &org)
		if want := "Balfolk & Fest-Noz Assé PATCH"; org.Name != want {
			t.Errorf("name = %q, want %q", org.Name, want)
		}
	})

	t.Run("putLocation (PUT)", func(t *testing.T) {
		id, err := ensureLocation(db, EventLocationRequest{Location: "Original"})
		if err != nil {
			t.Fatal(err)
		}
		w := httptest.NewRecorder()
		r := entityReq("PUT", "/api/v1/locations/x", `{"location":"Salle des F&ecirc;tes","address":"1 Rue de l&#8217;&Eacute;glise","town":"Ch&acirc;teau-Gontier"}`)
		r.SetPathValue("id", strconv.FormatInt(id, 10))
		putLocation(w, r)
		if w.Code != http.StatusOK {
			t.Fatalf("status = %d, body=%s", w.Code, w.Body.String())
		}
		var loc Location
		json.Unmarshal(w.Body.Bytes(), &loc)
		if want := "Salle des Fêtes"; loc.Location != want {
			t.Errorf("location = %q, want %q", loc.Location, want)
		}
		if want := "1 Rue de l’Église"; loc.Address != want {
			t.Errorf("address = %q, want %q", loc.Address, want)
		}
		if want := "Château-Gontier"; loc.Town != want {
			t.Errorf("town = %q, want %q", loc.Town, want)
		}
	})

	t.Run("patchLocation (PATCH)", func(t *testing.T) {
		id, err := ensureLocation(db, EventLocationRequest{Location: "Original"})
		if err != nil {
			t.Fatal(err)
		}
		w := httptest.NewRecorder()
		r := entityReq("PATCH", "/api/v1/locations/x", `{"location":"Salle des F&ecirc;tes","address":"1 Rue de l&#8217;&Eacute;glise","town":"Ch&acirc;teau-Gontier"}`)
		r.Header.Set("Content-Type", "application/merge-patch+json")
		r.SetPathValue("id", strconv.FormatInt(id, 10))
		patchLocation(w, r)
		if w.Code != http.StatusOK {
			t.Fatalf("status = %d, body=%s", w.Code, w.Body.String())
		}
		var loc Location
		json.Unmarshal(w.Body.Bytes(), &loc)
		if want := "Salle des Fêtes"; loc.Location != want {
			t.Errorf("location = %q, want %q", loc.Location, want)
		}
		if want := "1 Rue de l’Église"; loc.Address != want {
			t.Errorf("address = %q, want %q", loc.Address, want)
		}
		if want := "Château-Gontier"; loc.Town != want {
			t.Errorf("town = %q, want %q", loc.Town, want)
		}
	})

	t.Run("updateMusician (PUT)", func(t *testing.T) {
		w := httptest.NewRecorder()
		createMusician(w, entityReq("POST", "/api/v1/musicians", `{"bandname":"Original"}`))
		var created []Musician
		json.Unmarshal(w.Body.Bytes(), &created)
		id := created[0].ID

		w = httptest.NewRecorder()
		r := entityReq("PUT", "/api/v1/musicians/x", `{"bandname":"Kost ar c&#8217;hoat"}`)
		r.SetPathValue("id", strconv.Itoa(id))
		updateMusician(w, r)
		if w.Code != http.StatusOK {
			t.Fatalf("status = %d, body=%s", w.Code, w.Body.String())
		}
		var m Musician
		json.Unmarshal(w.Body.Bytes(), &m)
		if want := "Kost ar c’hoat"; m.Bandname != want {
			t.Errorf("bandname = %q, want %q", m.Bandname, want)
		}
	})

	t.Run("patchMusician (PATCH)", func(t *testing.T) {
		w := httptest.NewRecorder()
		createMusician(w, entityReq("POST", "/api/v1/musicians", `{"bandname":"Original"}`))
		var created []Musician
		json.Unmarshal(w.Body.Bytes(), &created)
		id := created[0].ID

		w = httptest.NewRecorder()
		r := entityReq("PATCH", "/api/v1/musicians/x", `{"bandname":"Kost ar c&#8217;hoat"}`)
		r.Header.Set("Content-Type", "application/merge-patch+json")
		r.SetPathValue("id", strconv.Itoa(id))
		patchMusician(w, r)
		if w.Code != http.StatusOK {
			t.Fatalf("status = %d, body=%s", w.Code, w.Body.String())
		}
		var m Musician
		json.Unmarshal(w.Body.Bytes(), &m)
		if want := "Kost ar c’hoat"; m.Bandname != want {
			t.Errorf("bandname = %q, want %q", m.Bandname, want)
		}
	})

	t.Run("updateInstructor (PUT)", func(t *testing.T) {
		w := httptest.NewRecorder()
		createInstructor(w, entityReq("POST", "/api/v1/instructors", `{"name":"Original"}`))
		var inst Instructor
		json.Unmarshal(w.Body.Bytes(), &inst)

		w = httptest.NewRecorder()
		r := entityReq("PUT", "/api/v1/instructors/x", `{"name":"Ma&euml;lle Le Goff"}`)
		r.SetPathValue("id", strconv.Itoa(inst.ID))
		updateInstructor(w, r)
		if w.Code != http.StatusOK {
			t.Fatalf("status = %d, body=%s", w.Code, w.Body.String())
		}
		var updated Instructor
		json.Unmarshal(w.Body.Bytes(), &updated)
		if want := "Maëlle Le Goff"; updated.Name != want {
			t.Errorf("name = %q, want %q", updated.Name, want)
		}
	})

	t.Run("patchInstructor (PATCH)", func(t *testing.T) {
		w := httptest.NewRecorder()
		createInstructor(w, entityReq("POST", "/api/v1/instructors", `{"name":"Original"}`))
		var inst Instructor
		json.Unmarshal(w.Body.Bytes(), &inst)

		w = httptest.NewRecorder()
		r := entityReq("PATCH", "/api/v1/instructors/x", `{"name":"Ma&euml;lle Le Goff"}`)
		r.Header.Set("Content-Type", "application/merge-patch+json")
		r.SetPathValue("id", strconv.Itoa(inst.ID))
		patchInstructor(w, r)
		if w.Code != http.StatusOK {
			t.Fatalf("status = %d, body=%s", w.Code, w.Body.String())
		}
		var updated Instructor
		json.Unmarshal(w.Body.Bytes(), &updated)
		if want := "Maëlle Le Goff"; updated.Name != want {
			t.Errorf("name = %q, want %q", updated.Name, want)
		}
	})
}
