package main

import (
	"database/sql"
	"flag"
	"fmt"

	"github.com/ademant/dansal/internal/userexport"
)

// cmdExportUser implements `dansal_admin export-user` (#1479): the GDPR
// Art. 15/20 export for a registered user, run server-side for DSARs
// against accounts that can no longer log in. Reads the DB file directly
// (like cmdExport/cmdImport), not via the admin socket, since this is a
// read-only operation rather than an audit-logged mutation.
func cmdExportUser(args []string) {
	fs := flag.NewFlagSet("export-user", flag.ExitOnError)
	fs.Usage = func() { fmt.Println(commandHelp["export-user"]) }
	id := fs.Int64("id", 0, "user id")
	output := fs.String("output", "", "output file (default: stdout)")
	dbPath := fs.String("db", "/var/lib/dansal/calendar.db", "path to calendar.db")
	fs.Parse(args)

	if *id == 0 {
		die("--id is required")
	}

	db := openDB(*dbPath)
	defer db.Close()

	data, err := userexport.Export(db, *id)
	if err != nil {
		die("export-user: %v", err)
	}
	writeJSON(data, *output)
}

// emailLinkedTables are the personal-data tables that key anonymous
// visitors by email rather than a user_id foreign key, so cmdExportUser's
// foreign-key walk can't discover them (#1480). Add a comment at any new
// table like these pointing back here, so a future email-bearing table
// doesn't get missed silently — unlike the FK walk, this list cannot
// self-maintain.
var emailLinkedTables = []struct {
	table, col, extraWhere string
}{
	{"bookings", "email", ""},
	{"contact_posts", "email", "AND user_id IS NULL"}, // logged-in posts already covered by export-user
	{"contact_requests", "sender_email", ""},
	{"events", "suggester_email", ""},
	{"pending_fetch_suggestions", "email", ""},
}

// cmdExportUserByEmail implements `dansal_admin export-user-by-email`
// (#1480): a manual-process tool for DSARs from anonymous visitors, who
// have no account to authenticate a self-service export with. The admin
// runs this after receiving a request at the instance's contact address
// (#1478) and replies with the result by hand — there is no public
// endpoint, intake table, or admin UI for this, deliberately: an
// unauthenticated email-matched lookup would let anyone read someone
// else's bookings/suggestions/board posts just by typing their address.
func cmdExportUserByEmail(args []string) {
	fs := flag.NewFlagSet("export-user-by-email", flag.ExitOnError)
	fs.Usage = func() { fmt.Println(commandHelp["export-user-by-email"]) }
	email := fs.String("email", "", "email address")
	output := fs.String("output", "", "output file (default: stdout)")
	dbPath := fs.String("db", "/var/lib/dansal/calendar.db", "path to calendar.db")
	fs.Parse(args)

	if *email == "" {
		die("--email is required")
	}

	db := openDB(*dbPath)
	defer db.Close()

	result := make(map[string]any, len(emailLinkedTables))
	for _, t := range emailLinkedTables {
		// table/col/extraWhere are from the hardcoded slice above, never
		// from user input.
		rows, err := db.Query(`SELECT * FROM "`+t.table+`" WHERE LOWER("`+t.col+`") = LOWER(?) `+t.extraWhere, *email)
		if err != nil {
			die("export-user-by-email: %s: %v", t.table, err)
		}
		data, err := scanRows(rows)
		rows.Close()
		if err != nil {
			die("export-user-by-email: %s: %v", t.table, err)
		}
		result[t.table] = data
	}
	writeJSON(result, *output)
}

// scanRows is the generic column-name-keyed row dump shared by
// cmdExportUserByEmail's hardcoded queries. (cmdExportUser's foreign-key
// walk uses the equivalent in internal/userexport instead, since that
// package is also imported by cmd/dansal's /api/v1/me/export handler.)
func scanRows(rows *sql.Rows) ([]map[string]any, error) {
	cols, err := rows.Columns()
	if err != nil {
		return nil, err
	}
	var out []map[string]any
	for rows.Next() {
		vals := make([]any, len(cols))
		ptrs := make([]any, len(cols))
		for i := range vals {
			ptrs[i] = &vals[i]
		}
		if err := rows.Scan(ptrs...); err != nil {
			return nil, err
		}
		row := make(map[string]any, len(cols))
		for i, c := range cols {
			if b, ok := vals[i].([]byte); ok {
				row[c] = string(b)
			} else {
				row[c] = vals[i]
			}
		}
		out = append(out, row)
	}
	return out, rows.Err()
}
