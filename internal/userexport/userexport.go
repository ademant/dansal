// Package userexport implements the GDPR Art. 15/20 self-service data
// export (#1479): every row across every table with a foreign key to
// users(id), for one user. Shared between cmd/dansal (the GET
// /api/v1/me/export handler) and cmd/dansal_admin (the export-user CLI
// command), which run as separate binaries and can't share an internal type
// from either's main package.
package userexport

import (
	"database/sql"
	"fmt"
)

// SensitiveColumns lists columns that must never leave the database in an
// export, keyed by table name. The foreign-key walk Export runs is
// otherwise fully generic and has no other awareness of which columns hold
// secrets rather than ordinary personal data.
var SensitiveColumns = map[string][]string{
	"users":                {"password_hash", "totp_secret", "totp_pending"},
	"tokens":               {"token"},
	"webauthn_credentials": {"public_key", "credential_id"},
	"api_keys":             {"api_key", "signing_secret_enc"},
}

// querier is satisfied by *sql.DB and *sql.Tx.
type querier interface {
	Query(query string, args ...any) (*sql.Rows, error)
}

// Export returns the caller's own account row plus every row across every
// table with a foreign key to users(id), keyed by "table" (for the account
// row) or "table.column" (one entry per referencing column — a table like
// events that references users via two different columns, created_by_id
// and changed_by_id, gets two separate entries rather than one overwriting
// the other). SensitiveColumns are redacted from every row.
//
// This mirrors cmd/dansal's deleteUserByID (dbhelpers.go), which drives
// Art. 17 erasure the same way, but keeps every discovered row instead of
// nulling/deleting it, and does not need deleteUserByID's exclusion of
// ON DELETE CASCADE/SET NULL foreign keys — export has no reason to skip
// those tables the way a manual UPDATE/DELETE pass does.
//
// Anonymous visitor data (bookings, board posts, event suggestions made
// without an account) has no user_id to key on and is out of scope here —
// see dansal_admin's export-user-by-email (#1480).
func Export(q querier, userID int64) (map[string]any, error) {
	result := make(map[string]any)

	account, err := dumpTable(q, "users", "id", userID)
	if err != nil {
		return nil, fmt.Errorf("users: %w", err)
	}
	if len(account) > 0 {
		result["account"] = account[0]
	}

	rows, err := q.Query(`SELECT m.name, f."from"
		FROM sqlite_master m, pragma_foreign_key_list(m.name) f
		WHERE m.type = 'table' AND f."table" = 'users'`)
	if err != nil {
		return nil, err
	}
	type ref struct{ table, col string }
	var refs []ref
	for rows.Next() {
		var r ref
		if err := rows.Scan(&r.table, &r.col); err != nil {
			rows.Close()
			return nil, err
		}
		refs = append(refs, r)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, err
	}
	rows.Close()

	for _, r := range refs {
		data, err := dumpTable(q, r.table, r.col, userID)
		if err != nil {
			return nil, fmt.Errorf("%s.%s: %w", r.table, r.col, err)
		}
		result[r.table+"."+r.col] = data
	}
	return result, nil
}

// dumpTable runs SELECT * FROM "table" WHERE "col" = ? and returns every
// row as a column-name-keyed map, with that table's SensitiveColumns
// omitted and []byte values converted to string for JSON output.
func dumpTable(q querier, table, col string, id int64) ([]map[string]any, error) {
	// table/col come from sqlite_master/pragma_foreign_key_list, or the
	// "users"/"id" literal above — never from user input.
	rows, err := q.Query(`SELECT * FROM "`+table+`" WHERE "`+col+`" = ?`, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	cols, err := rows.Columns()
	if err != nil {
		return nil, err
	}
	redact := make(map[string]bool, len(SensitiveColumns[table]))
	for _, c := range SensitiveColumns[table] {
		redact[c] = true
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
			if redact[c] {
				continue
			}
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
