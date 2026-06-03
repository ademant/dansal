package main

import (
	"database/sql"
	"flag"
	"fmt"
	"os"
	"regexp"
	"strings"
	"text/tabwriter"

	_ "github.com/mattn/go-sqlite3"
)

// Patterns tried in order of specificity.
// Each captures: (street, zipcode, town) or a subset.
var addrPatterns = []struct {
	re         *regexp.Regexp
	streetIdx  int
	zipcodeIdx int
	townIdx    int
}{
	// "Name, Street Nr, 12345 Town"
	{regexp.MustCompile(`^[^,]+,\s*(.+?),\s*(\d{5})\s+(.+)$`), 1, 2, 3},
	// "Name, Street Nr, Town"  (no zipcode)
	{regexp.MustCompile(`^[^,]+,\s*(.+?\s+\d+\w*),\s*([A-ZÄÖÜ].+)$`), 1, 0, 2},
	// "Name, 12345 Town"  (no explicit street)
	{regexp.MustCompile(`^[^,]+,\s*(\d{5})\s+(.+)$`), 0, 1, 2},
}

type parsedAddr struct {
	street  string
	zipcode string
	town    string
}

func parseLocationName(name string) (parsedAddr, bool) {
	for _, p := range addrPatterns {
		m := p.re.FindStringSubmatch(name)
		if m == nil {
			continue
		}
		var a parsedAddr
		if p.streetIdx > 0 {
			a.street = strings.TrimSpace(m[p.streetIdx])
		}
		if p.zipcodeIdx > 0 {
			a.zipcode = strings.TrimSpace(m[p.zipcodeIdx])
		}
		if p.townIdx > 0 {
			a.town = strings.TrimSpace(m[p.townIdx])
		}
		return a, true
	}
	return parsedAddr{}, false
}

func cmdFillLocationFields(args []string) {
	fs := flag.NewFlagSet("fill-location-fields", flag.ExitOnError)
	fs.Usage = func() { fmt.Println(commandHelp["fill-location-fields"]) }
	dbPath := fs.String("db", "/var/lib/dansal/calendar.db", "path to calendar.db")
	apply := fs.Bool("apply", false, "write changes (default is dry-run)")
	fs.Parse(args)

	db, err := sql.Open("sqlite3", *dbPath+"?_foreign_keys=ON")
	if err != nil {
		die("open db: %v", err)
	}
	defer db.Close()

	rows, err := db.Query(`
		SELECT id, location, COALESCE(address,''), COALESCE(zipcode,''), COALESCE(town,'')
		FROM locations
		WHERE (address IS NULL OR address = '')
		   OR (zipcode IS NULL OR zipcode = '')
		   OR (town    IS NULL OR town    = '')
	`)
	if err != nil {
		die("query: %v", err)
	}
	defer rows.Close()

	type candidate struct {
		id          int
		location    string
		addr        parsedAddr
		fillAddress bool
		fillZipcode bool
		fillTown    bool
	}

	var candidates []candidate
	for rows.Next() {
		var id int
		var location, address, zipcode, town string
		if err := rows.Scan(&id, &location, &address, &zipcode, &town); err != nil {
			die("scan: %v", err)
		}
		parsed, ok := parseLocationName(location)
		if !ok {
			continue
		}
		c := candidate{id: id, location: location, addr: parsed}
		c.fillAddress = address == "" && parsed.street != ""
		c.fillZipcode = zipcode == "" && parsed.zipcode != ""
		c.fillTown = town == "" && parsed.town != ""
		if c.fillAddress || c.fillZipcode || c.fillTown {
			candidates = append(candidates, c)
		}
	}

	if len(candidates) == 0 {
		fmt.Println("no locations to update")
		return
	}

	tw := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "ID\tLOCATION\tADDRESS\tZIPCODE\tTOWN")
	for _, c := range candidates {
		addr, zip, town := "-", "-", "-"
		if c.fillAddress {
			addr = c.addr.street
		}
		if c.fillZipcode {
			zip = c.addr.zipcode
		}
		if c.fillTown {
			town = c.addr.town
		}
		fmt.Fprintf(tw, "%d\t%s\t%s\t%s\t%s\n", c.id, c.location, addr, zip, town)
	}
	tw.Flush()

	if !*apply {
		fmt.Printf("\n%d location(s) would be updated. Re-run with --apply to write changes.\n", len(candidates))
		return
	}

	updated := 0
	for _, c := range candidates {
		var sets []string
		var params []any
		if c.fillAddress {
			sets = append(sets, "address = ?")
			params = append(params, c.addr.street)
		}
		if c.fillZipcode {
			sets = append(sets, "zipcode = ?")
			params = append(params, c.addr.zipcode)
		}
		if c.fillTown {
			sets = append(sets, "town = ?")
			params = append(params, c.addr.town)
		}
		params = append(params, c.id)
		if _, err := db.Exec("UPDATE locations SET "+strings.Join(sets, ", ")+" WHERE id = ?", params...); err != nil {
			fmt.Fprintf(os.Stderr, "error updating id=%d: %v\n", c.id, err)
			continue
		}
		updated++
	}
	fmt.Printf("%d location(s) updated.\n", updated)
}
