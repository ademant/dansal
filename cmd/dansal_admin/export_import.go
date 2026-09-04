package main

import (
	"database/sql"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	_ "github.com/mattn/go-sqlite3"
)

// ── shared record types ────────────────────────────────────────────────────

type exportFetchSource struct {
	ID             int      `json:"id"`
	URL            string   `json:"url"`
	Type           string   `json:"type"`
	Tags           []string `json:"tags"`
	DanceIDs       []int    `json:"dance_ids,omitempty"`
	OrganizationID *int     `json:"organization_id,omitempty"`
	LastFetchedAt  string   `json:"last_fetched_at,omitempty"`
	CreatedAt      string   `json:"created_at"`
}

type exportLocation struct {
	ID              int     `json:"id"`
	Location        string  `json:"location"`
	ShortName       string  `json:"short_name,omitempty"`
	Address         string  `json:"address,omitempty"`
	Zipcode         string  `json:"zipcode,omitempty"`
	Town            string  `json:"town,omitempty"`
	Country         string  `json:"country,omitempty"`
	Latitude        float64 `json:"latitude,omitempty"`
	Longitude       float64 `json:"longitude,omitempty"`
	Internetsite    string  `json:"internetsite,omitempty"`
	OrganizationIDs []int   `json:"organization_ids,omitempty"`
	OrganizationID  *int    `json:"organization_id,omitempty"` // legacy: read-only during import
	CreatedAt       string  `json:"created_at"`
}

type exportOrganization struct {
	ID           int    `json:"id"`
	Name         string `json:"name"`
	Description  string `json:"description,omitempty"`
	ActorName    string `json:"actor_name,omitempty"`
	Website      string `json:"website,omitempty"`
	Instagram    string `json:"instagram,omitempty"`
	Mastodon     string `json:"mastodon,omitempty"`
	Facebook     string `json:"facebook,omitempty"`
	ContactEmail string `json:"contact_email,omitempty"`
	CreatedAt    string `json:"created_at"`
}

type exportEvent struct {
	UID                string   `json:"uid,omitempty"`
	Title              string   `json:"title"`
	Description        string   `json:"description,omitempty"`
	StartTime          string   `json:"start_time"`
	EndTime            string   `json:"end_time"`
	HasBall            bool     `json:"has_ball,omitempty"`
	HasWorkshop        bool     `json:"has_workshop,omitempty"`
	HasFestival        bool     `json:"has_festival,omitempty"`
	WorkshopDifficulty string   `json:"workshop_difficulty,omitempty"`
	IsCancelled        bool     `json:"is_cancelled,omitempty"`
	Tags               []string `json:"tags,omitempty"`
	IsPublished        bool     `json:"is_published"`
	URL                string   `json:"url,omitempty"`
	Source             string   `json:"source,omitempty"`
	Pricing            string   `json:"pricing,omitempty"`
	BookingURL         string   `json:"booking_url,omitempty"`
	Availability       string   `json:"availability,omitempty"`
	TicketsTotal       int      `json:"tickets_total,omitempty"`
	BookingEnabled     bool     `json:"booking_enabled,omitempty"`
	OrganizationID     *int     `json:"organization_id,omitempty"`
	LocationName       string   `json:"location_name,omitempty"`
	LocationShortName  string   `json:"location_short_name,omitempty"`
	LocationAddress    string   `json:"location_address,omitempty"`
	LocationZipcode    string   `json:"location_zipcode,omitempty"`
	LocationTown       string   `json:"location_town,omitempty"`
	LocationCountry    string   `json:"location_country,omitempty"`
	LocationLat        float64  `json:"location_lat,omitempty"`
	LocationLng        float64  `json:"location_lng,omitempty"`
	DanceNames         []string `json:"dance_names,omitempty"`
	CreatedAt          string   `json:"created_at"`
}

// ── helpers ────────────────────────────────────────────────────────────────

func openDB(path string) *sql.DB {
	db, err := sql.Open("sqlite3", path+"?_foreign_keys=ON&_journal_mode=WAL&_busy_timeout=5000")
	if err != nil {
		die("open db: %v", err)
	}
	return db
}

func epochToRFC3339(epoch int64) string {
	return time.Unix(epoch, 0).UTC().Format(time.RFC3339)
}

func parseRFC3339ToEpoch(s string) (int64, error) {
	for _, layout := range []string{time.RFC3339, "2006-01-02T15:04:05", "2006-01-02 15:04:05"} {
		if t, err := time.Parse(layout, s); err == nil {
			return t.Unix(), nil
		}
	}
	return 0, fmt.Errorf("unrecognised time format: %q", s)
}

func writeJSON(v any, path string) {
	enc := json.NewEncoder(os.Stdout)
	if path != "" {
		f, err := os.Create(path)
		if err != nil {
			die("create output: %v", err)
		}
		defer f.Close()
		enc = json.NewEncoder(f)
	}
	enc.SetIndent("", "  ")
	if err := enc.Encode(v); err != nil {
		die("encode: %v", err)
	}
}

func readJSON(path string, v any) {
	var r *os.File
	if path == "-" || path == "" {
		r = os.Stdin
	} else {
		f, err := os.Open(path)
		if err != nil {
			die("open input: %v", err)
		}
		defer f.Close()
		r = f
	}
	if err := json.NewDecoder(r).Decode(v); err != nil {
		die("decode JSON: %v", err)
	}
}

// ── export ─────────────────────────────────────────────────────────────────

func cmdExport(args []string) {
	fs := flag.NewFlagSet("export", flag.ExitOnError)
	fs.Usage = func() { fmt.Println(commandHelp["export"]) }
	table := fs.String("table", "", "table to export: fetchurl, locations, organisations, events")
	output := fs.String("output", "", "output file (default: stdout)")
	dbPath := fs.String("db", "/var/lib/dansal/calendar.db", "path to calendar.db")
	fs.Parse(args)

	if *table == "" {
		die("--table is required (fetchurl, locations, organisations, events)")
	}

	db := openDB(*dbPath)
	defer db.Close()

	switch *table {
	case "fetchurl", "fetch_sources":
		exportFetchSources(db, *output)
	case "locations":
		exportLocations(db, *output)
	case "organisations", "organizations":
		exportOrganizations(db, *output)
	case "events":
		exportEvents(db, *output)
	default:
		die("unknown table %q; choose: fetchurl, locations, organisations, events", *table)
	}
}

func exportFetchSources(db *sql.DB, output string) {
	rows, err := db.Query(`SELECT id, url, type, COALESCE(tags,'[]'),
		COALESCE((SELECT GROUP_CONCAT(dance_id) FROM fetch_source_dances WHERE fetch_source_id=id),''),
		organization_id, COALESCE(last_fetched_at,''), created_at FROM fetch_sources ORDER BY id`)
	if err != nil {
		die("query: %v", err)
	}
	defer rows.Close()

	var out []exportFetchSource
	for rows.Next() {
		var s exportFetchSource
		var tagsJSON, danceCSV, lastFetched string
		var orgID sql.NullInt64
		if err := rows.Scan(&s.ID, &s.URL, &s.Type, &tagsJSON, &danceCSV, &orgID, &lastFetched, &s.CreatedAt); err != nil {
			die("scan: %v", err)
		}
		json.Unmarshal([]byte(tagsJSON), &s.Tags)
		if danceCSV != "" {
			for p := range strings.SplitSeq(danceCSV, ",") {
				if id, err := strconv.Atoi(strings.TrimSpace(p)); err == nil {
					s.DanceIDs = append(s.DanceIDs, id)
				}
			}
		}
		if orgID.Valid {
			v := int(orgID.Int64)
			s.OrganizationID = &v
		}
		s.LastFetchedAt = lastFetched
		out = append(out, s)
	}
	writeJSON(out, output)
}

func exportLocations(db *sql.DB, output string) {
	rows, err := db.Query(`SELECT id, location, COALESCE(short_name,''), COALESCE(address,''),
		COALESCE(zipcode,''), COALESCE(town,''), COALESCE(country,''),
		COALESCE(latitude,0), COALESCE(longitude,0), COALESCE(internetsite,''),
		created_at FROM locations ORDER BY id`)
	if err != nil {
		die("query: %v", err)
	}
	defer rows.Close()

	var out []exportLocation
	for rows.Next() {
		var l exportLocation
		if err := rows.Scan(&l.ID, &l.Location, &l.ShortName, &l.Address, &l.Zipcode,
			&l.Town, &l.Country, &l.Latitude, &l.Longitude, &l.Internetsite,
			&l.CreatedAt); err != nil {
			die("scan: %v", err)
		}
		orgRows, _ := db.Query("SELECT organization_id FROM location_organizations WHERE location_id=? ORDER BY organization_id", l.ID)
		if orgRows != nil {
			for orgRows.Next() {
				var oid int
				orgRows.Scan(&oid)
				l.OrganizationIDs = append(l.OrganizationIDs, oid)
			}
			orgRows.Close()
		}
		out = append(out, l)
	}
	writeJSON(out, output)
}

func exportOrganizations(db *sql.DB, output string) {
	rows, err := db.Query(`SELECT id, name, COALESCE(description,''), COALESCE(actor_name,''),
		COALESCE(website,''), COALESCE(instagram,''), COALESCE(mastodon,''),
		COALESCE(facebook,''), COALESCE(contact_email,''), created_at FROM organizations ORDER BY id`)
	if err != nil {
		die("query: %v", err)
	}
	defer rows.Close()

	var out []exportOrganization
	for rows.Next() {
		var o exportOrganization
		if err := rows.Scan(&o.ID, &o.Name, &o.Description, &o.ActorName,
			&o.Website, &o.Instagram, &o.Mastodon, &o.Facebook, &o.ContactEmail, &o.CreatedAt); err != nil {
			die("scan: %v", err)
		}
		out = append(out, o)
	}
	writeJSON(out, output)
}

func exportEvents(db *sql.DB, output string) {
	rows, err := db.Query(`SELECT e.uid, e.title, COALESCE(e.description,''),
		e.start_time, e.end_time,
		e.has_ball, e.has_workshop, e.has_festival, e.is_cancelled,
		COALESCE(e.workshop_difficulty,''), COALESCE(e.tags,'[]'), e.is_published,
		COALESCE(e.url,''), COALESCE(e.source,''), COALESCE(e.pricing,''),
		COALESCE(e.booking_url,''), COALESCE(e.availability,''),
		COALESCE(e.tickets_total,0), COALESCE(e.booking_enabled,0),
		e.organization_id,
		COALESCE(l.location,''), COALESCE(l.short_name,''), COALESCE(l.address,''),
		COALESCE(l.zipcode,''), COALESCE(l.town,''), COALESCE(l.country,''),
		COALESCE(l.latitude,0), COALESCE(l.longitude,0),
		COALESCE((SELECT GROUP_CONCAT(d.name,',') FROM event_dances ed JOIN dances d ON d.id=ed.dance_id WHERE ed.event_id=e.id),''),
		e.created_at
		FROM events e LEFT JOIN locations l ON e.location_id=l.id
		ORDER BY e.start_time`)
	if err != nil {
		die("query: %v", err)
	}
	defer rows.Close()

	var out []exportEvent
	for rows.Next() {
		var ev exportEvent
		var uid sql.NullString
		var orgID sql.NullInt64
		var startEpoch, endEpoch int64
		var hasBall, hasWorkshop, hasFestival, isCancelled, isPublished, bookingEnabled int
		var tagsJSON, danceCSV string
		if err := rows.Scan(&uid, &ev.Title, &ev.Description,
			&startEpoch, &endEpoch,
			&hasBall, &hasWorkshop, &hasFestival, &isCancelled,
			&ev.WorkshopDifficulty, &tagsJSON, &isPublished,
			&ev.URL, &ev.Source, &ev.Pricing,
			&ev.BookingURL, &ev.Availability,
			&ev.TicketsTotal, &bookingEnabled,
			&orgID,
			&ev.LocationName, &ev.LocationShortName, &ev.LocationAddress,
			&ev.LocationZipcode, &ev.LocationTown, &ev.LocationCountry,
			&ev.LocationLat, &ev.LocationLng,
			&danceCSV, &ev.CreatedAt); err != nil {
			die("scan: %v", err)
		}
		if uid.Valid {
			ev.UID = uid.String
		}
		if orgID.Valid {
			v := int(orgID.Int64)
			ev.OrganizationID = &v
		}
		ev.StartTime = epochToRFC3339(startEpoch)
		ev.EndTime = epochToRFC3339(endEpoch)
		ev.HasBall = hasBall == 1
		ev.HasWorkshop = hasWorkshop == 1
		ev.HasFestival = hasFestival == 1
		ev.IsCancelled = isCancelled == 1
		ev.IsPublished = isPublished == 1
		ev.BookingEnabled = bookingEnabled == 1
		json.Unmarshal([]byte(tagsJSON), &ev.Tags)
		if danceCSV != "" {
			ev.DanceNames = strings.Split(danceCSV, ",")
		}
		out = append(out, ev)
	}
	writeJSON(out, output)
}

// ── import ─────────────────────────────────────────────────────────────────

func cmdImport(args []string) {
	fs := flag.NewFlagSet("import", flag.ExitOnError)
	fs.Usage = func() { fmt.Println(commandHelp["import"]) }
	table := fs.String("table", "", "table to import: fetchurl, locations, organisations, events")
	input := fs.String("input", "-", "input JSON file (default: stdin)")
	dbPath := fs.String("db", "/var/lib/dansal/calendar.db", "path to calendar.db")
	apply := fs.Bool("apply", false, "write changes (default is dry-run)")
	fs.Parse(args)

	if *table == "" {
		die("--table is required (fetchurl, locations, organisations, events)")
	}

	db := openDB(*dbPath)
	defer db.Close()

	if *apply {
		if _, lockErr := db.Exec("BEGIN IMMEDIATE"); lockErr != nil {
			fmt.Fprintln(os.Stderr, "warning: could not acquire exclusive lock on calendar.db — dansal may be running.")
			fmt.Fprintln(os.Stderr, "         Proceeding anyway; concurrent writes may conflict.")
		} else {
			db.Exec("ROLLBACK")
		}
	}

	switch *table {
	case "fetchurl", "fetch_sources":
		importFetchSources(db, *input, *apply)
	case "locations":
		importLocations(db, *input, *apply)
	case "organisations", "organizations":
		importOrganizations(db, *input, *apply)
	case "events":
		importEvents(db, *input, *apply)
	default:
		die("unknown table %q; choose: fetchurl, locations, organisations, events", *table)
	}
}

func importFetchSources(db *sql.DB, input string, apply bool) {
	var records []exportFetchSource
	readJSON(input, &records)
	fmt.Printf("importing %d fetch source(s)...\n", len(records))
	n := 0
	for _, s := range records {
		tagsJSON, _ := json.Marshal(s.Tags)
		var orgVal any
		if s.OrganizationID != nil {
			orgVal = *s.OrganizationID
		}
		fmt.Printf("  %s (%s)\n", s.URL, s.Type)
		if !apply {
			continue
		}
		var existing int
		db.QueryRow("SELECT id FROM fetch_sources WHERE url=?", s.URL).Scan(&existing)
		if existing > 0 {
			db.Exec("UPDATE fetch_sources SET type=?, tags=?, organization_id=? WHERE id=?",
				s.Type, string(tagsJSON), orgVal, existing)
		} else {
			res, err := db.Exec("INSERT INTO fetch_sources (url,type,tags,organization_id) VALUES (?,?,?,?)",
				s.URL, s.Type, string(tagsJSON), orgVal)
			if err != nil {
				fmt.Fprintf(os.Stderr, "  error: %v\n", err)
				continue
			}
			id, _ := res.LastInsertId()
			existing = int(id)
		}
		db.Exec("DELETE FROM fetch_source_dances WHERE fetch_source_id=?", existing)
		for _, dID := range s.DanceIDs {
			db.Exec("INSERT OR IGNORE INTO fetch_source_dances (fetch_source_id,dance_id) VALUES (?,?)", existing, dID)
		}
		n++
	}
	if !apply {
		fmt.Printf("dry-run: %d record(s) would be imported. Re-run with --apply to write.\n", len(records))
	} else {
		fmt.Printf("imported %d fetch source(s).\n", n)
	}
}

func importLocations(db *sql.DB, input string, apply bool) {
	var records []exportLocation
	readJSON(input, &records)
	fmt.Printf("importing %d location(s)...\n", len(records))
	n := 0
	for _, l := range records {
		fmt.Printf("  %s\n", l.Location)
		if !apply {
			continue
		}
		var existing int
		db.QueryRow("SELECT id FROM locations WHERE location=?", l.Location).Scan(&existing)
		if existing > 0 {
			db.Exec(`UPDATE locations SET short_name=?,address=?,zipcode=?,town=?,country=?,
				latitude=?,longitude=?,internetsite=? WHERE id=?`,
				nullStr(l.ShortName), nullStr(l.Address), nullStr(l.Zipcode), nullStr(l.Town),
				nullStr(l.Country), nullFloat(l.Latitude), nullFloat(l.Longitude),
				nullStr(l.Internetsite), existing)
		} else {
			res, err := db.Exec(`INSERT INTO locations (location,short_name,address,zipcode,town,country,latitude,longitude,internetsite)
				VALUES (?,?,?,?,?,?,?,?,?)`,
				l.Location, nullStr(l.ShortName), nullStr(l.Address), nullStr(l.Zipcode),
				nullStr(l.Town), nullStr(l.Country), nullFloat(l.Latitude), nullFloat(l.Longitude),
				nullStr(l.Internetsite))
			if err != nil {
				fmt.Fprintf(os.Stderr, "  error: %v\n", err)
				continue
			}
			id, _ := res.LastInsertId()
			existing = int(id)
		}
		// Assign org IDs via join table (new format).
		for _, oid := range l.OrganizationIDs {
			db.Exec("INSERT OR IGNORE INTO location_organizations (location_id, organization_id) VALUES (?,?)", existing, oid)
		}
		// Legacy single org_id field for backward-compatible imports.
		if l.OrganizationID != nil {
			db.Exec("INSERT OR IGNORE INTO location_organizations (location_id, organization_id) VALUES (?,?)", existing, *l.OrganizationID)
		}
		n++
	}
	if !apply {
		fmt.Printf("dry-run: %d record(s) would be imported. Re-run with --apply to write.\n", len(records))
	} else {
		fmt.Printf("imported %d location(s).\n", n)
	}
}

func importOrganizations(db *sql.DB, input string, apply bool) {
	var records []exportOrganization
	readJSON(input, &records)
	fmt.Printf("importing %d organisation(s)...\n", len(records))
	n := 0
	for _, o := range records {
		fmt.Printf("  %s\n", o.Name)
		if !apply {
			continue
		}
		var existing int
		db.QueryRow("SELECT id FROM organizations WHERE name=?", o.Name).Scan(&existing)
		if existing > 0 {
			db.Exec(`UPDATE organizations SET description=?,actor_name=?,website=?,instagram=?,mastodon=?,facebook=?,contact_email=? WHERE id=?`,
				nullStr(o.Description), nullStr(o.ActorName), nullStr(o.Website),
				nullStr(o.Instagram), nullStr(o.Mastodon), nullStr(o.Facebook),
				nullStr(o.ContactEmail), existing)
		} else {
			if _, err := db.Exec(`INSERT INTO organizations (name,description,actor_name,website,instagram,mastodon,facebook,contact_email)
				VALUES (?,?,?,?,?,?,?,?)`,
				o.Name, nullStr(o.Description), nullStr(o.ActorName), nullStr(o.Website),
				nullStr(o.Instagram), nullStr(o.Mastodon), nullStr(o.Facebook), nullStr(o.ContactEmail)); err != nil {
				fmt.Fprintf(os.Stderr, "  error: %v\n", err)
				continue
			}
		}
		n++
	}
	if !apply {
		fmt.Printf("dry-run: %d record(s) would be imported. Re-run with --apply to write.\n", len(records))
	} else {
		fmt.Printf("imported %d organisation(s).\n", n)
	}
}

func importEvents(db *sql.DB, input string, apply bool) {
	var records []exportEvent
	readJSON(input, &records)
	fmt.Printf("importing %d event(s)...\n", len(records))
	n := 0
	for _, ev := range records {
		startEpoch, err := parseRFC3339ToEpoch(ev.StartTime)
		if err != nil {
			fmt.Fprintf(os.Stderr, "  skip %q: bad start_time: %v\n", ev.Title, err)
			continue
		}
		endEpoch, err := parseRFC3339ToEpoch(ev.EndTime)
		if err != nil {
			endEpoch = startEpoch + 3600
		}
		fmt.Printf("  %s (%s)\n", ev.Title, ev.StartTime)
		if !apply {
			continue
		}

		// resolve or create location
		var locID sql.NullInt64
		if ev.LocationName != "" {
			var id int64
			if db.QueryRow("SELECT id FROM locations WHERE location=?", ev.LocationName).Scan(&id) == nil {
				locID = sql.NullInt64{Int64: id, Valid: true}
			} else {
				res, err := db.Exec(`INSERT INTO locations (location,short_name,address,zipcode,town,country,latitude,longitude) VALUES (?,?,?,?,?,?,?,?)`,
					ev.LocationName, nullStr(ev.LocationShortName), nullStr(ev.LocationAddress),
					nullStr(ev.LocationZipcode), nullStr(ev.LocationTown), nullStr(ev.LocationCountry),
					nullFloat(ev.LocationLat), nullFloat(ev.LocationLng))
				if err == nil {
					if id, err2 := res.LastInsertId(); err2 == nil {
						locID = sql.NullInt64{Int64: id, Valid: true}
					}
				}
			}
		}

		tagsJSON, _ := json.Marshal(ev.Tags)
		var orgVal, locVal, uidVal any
		if ev.OrganizationID != nil {
			orgVal = *ev.OrganizationID
		}
		if locID.Valid {
			locVal = locID.Int64
		}
		if ev.UID != "" {
			uidVal = ev.UID
		}

		// dedup by uid or url
		var existingID int
		if ev.UID != "" {
			db.QueryRow("SELECT id FROM events WHERE uid=?", ev.UID).Scan(&existingID)
		}
		if existingID == 0 && ev.URL != "" {
			db.QueryRow("SELECT id FROM events WHERE url=?", ev.URL).Scan(&existingID)
		}

		boolInt := func(b bool) int {
			if b {
				return 1
			}
			return 0
		}

		if existingID > 0 {
			db.Exec(`UPDATE events SET title=?,description=?,start_time=?,end_time=?,location_id=?,
				has_ball=?,has_workshop=?,has_festival=?,is_cancelled=?,workshop_difficulty=?,
				tags=?,is_published=?,url=?,source=?,pricing=?,booking_url=?,availability=?,
				tickets_total=?,booking_enabled=?,organization_id=? WHERE id=?`,
				ev.Title, nullStr(ev.Description), startEpoch, endEpoch, locVal,
				boolInt(ev.HasBall), boolInt(ev.HasWorkshop), boolInt(ev.HasFestival),
				boolInt(ev.IsCancelled), nullStr(ev.WorkshopDifficulty),
				string(tagsJSON), boolInt(ev.IsPublished), nullStr(ev.URL), nullStr(ev.Source),
				nullStr(ev.Pricing), nullStr(ev.BookingURL), nullStr(ev.Availability),
				ev.TicketsTotal, boolInt(ev.BookingEnabled), orgVal, existingID)
		} else {
			shortCode := fmt.Sprintf("%x", time.Now().UnixNano())[:8]
			res, err := db.Exec(`INSERT INTO events (uid,title,description,start_time,end_time,location_id,
				has_ball,has_workshop,has_festival,is_cancelled,workshop_difficulty,
				tags,is_published,short_code,url,source,pricing,booking_url,availability,
				tickets_total,booking_enabled,organization_id)
				VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
				uidVal, ev.Title, nullStr(ev.Description), startEpoch, endEpoch, locVal,
				boolInt(ev.HasBall), boolInt(ev.HasWorkshop), boolInt(ev.HasFestival),
				boolInt(ev.IsCancelled), nullStr(ev.WorkshopDifficulty),
				string(tagsJSON), boolInt(ev.IsPublished), shortCode, nullStr(ev.URL),
				nullStr(ev.Source), nullStr(ev.Pricing), nullStr(ev.BookingURL),
				nullStr(ev.Availability), ev.TicketsTotal, boolInt(ev.BookingEnabled), orgVal)
			if err != nil {
				fmt.Fprintf(os.Stderr, "  error: %v\n", err)
				continue
			}
			if id, err2 := res.LastInsertId(); err2 == nil {
				existingID = int(id)
			}
		}

		// link dances by name
		if existingID > 0 && len(ev.DanceNames) > 0 {
			for _, name := range ev.DanceNames {
				var dID int
				if db.QueryRow("SELECT id FROM dances WHERE name=?", name).Scan(&dID) == nil {
					db.Exec("INSERT OR IGNORE INTO event_dances (event_id,dance_id) VALUES (?,?)", existingID, dID)
				}
			}
		}
		n++
	}
	if !apply {
		fmt.Printf("dry-run: %d record(s) would be imported. Re-run with --apply to write.\n", len(records))
	} else {
		fmt.Printf("imported %d event(s).\n", n)
	}
}

// nullStr returns nil when s is empty so the column stores NULL.
func nullStr(s string) any {
	if s == "" {
		return nil
	}
	return s
}

// nullFloat returns nil when f is zero so the column stores NULL.
func nullFloat(f float64) any {
	if f == 0 {
		return nil
	}
	return f
}
