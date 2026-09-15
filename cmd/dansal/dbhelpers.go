package main

import "database/sql"

// This file collects small, single-purpose SQL helpers that used to be
// hand-rolled independently at several call sites each (#1317) — the same
// class of drift this codebase has already fixed before when spotted
// (findExistingEvent in dedup.go, DansalClient.do() in the web layer, the
// requireEventOrg/requireExistingOrgMember org-access helpers). Each takes a
// querier (like insertEvent already does) so it composes into a caller's
// existing transaction as easily as a plain *sql.DB call.
//
// Deliberately NOT touched here: the migration safety-net checks
// (pragma_table_info/sqlite_master lookups) also repeat across migration
// blocks, but CLAUDE.md documents that repetition as the intended idiom —
// each block stays self-contained and readable without tracing into a
// shared helper.

// deleteUserByID deletes one user row by id.
func deleteUserByID(exec querier, id any) error {
	_, err := exec.Exec("DELETE FROM users WHERE id=?", id)
	return err
}

// addOrgMember adds userID to orgID's membership, a no-op if already a member.
func addOrgMember(exec querier, orgID, userID any) error {
	_, err := exec.Exec("INSERT OR IGNORE INTO organization_members (organization_id, user_id) VALUES (?, ?)", orgID, userID)
	return err
}

// eventOrgID looks up one event's organization_id. Returns sql.ErrNoRows
// when the event doesn't exist, matching db.QueryRow's own contract, so
// callers keep their existing `err == sql.ErrNoRows` / `err != nil`
// branching unchanged — this only replaces the query+scan boilerplate.
func eventOrgID(exec querier, id any) (sql.NullInt64, error) {
	var orgID sql.NullInt64
	err := exec.QueryRow("SELECT organization_id FROM events WHERE id=?", id).Scan(&orgID)
	return orgID, err
}

// locationExists reports whether a location row with this id exists.
func locationExists(exec querier, id any) bool {
	var exists int
	exec.QueryRow("SELECT COUNT(*) FROM locations WHERE id=?", id).Scan(&exists)
	return exists != 0
}

// orgExists reports whether an organization row with this id exists.
func orgExists(exec querier, id any) bool {
	var exists int
	exec.QueryRow("SELECT COUNT(*) FROM organizations WHERE id=?", id).Scan(&exists)
	return exists != 0
}

// locationParentID looks up one location's parent_id (NULL for a top-level
// location). Returns the raw sql.NullInt64/error pair rather than
// interpreting sql.ErrNoRows itself, since callers disagree on how to
// report "location not found" (400 vs 404) — each keeps its own branching,
// this only replaces the query+scan boilerplate.
func locationParentID(exec querier, id any) (sql.NullInt64, error) {
	var parentID sql.NullInt64
	err := exec.QueryRow("SELECT parent_id FROM locations WHERE id=?", id).Scan(&parentID)
	return parentID, err
}

// deletePendingRegistration removes one pending_registrations row by id —
// the normal cleanup once a registration is verified, rejected, or expired.
func deletePendingRegistration(exec querier, id any) error {
	_, err := exec.Exec("DELETE FROM pending_registrations WHERE id=?", id)
	return err
}

// clearPendingRegistrationUser detaches a pending registration from the
// user row it had provisionally created, without deleting the pending
// registration itself — used on a webauthn registration failure, so the
// registration can be retried against a fresh user.
func clearPendingRegistrationUser(exec querier, id any) error {
	_, err := exec.Exec("UPDATE pending_registrations SET user_id=NULL WHERE id=?", id)
	return err
}

// insertJunctionRow inserts one row into a two-column many-to-many
// association table (event_musicians, event_instructors, event_dances,
// event_locations, location_organizations, fetch_source_dances, ...),
// ignoring the insert if the pair already exists. table/col1/col2 are never
// client input — every call site passes a hardcoded literal — matching the
// same safe pattern already used for entityTable in avatarUploadHandler.
func insertJunctionRow(exec querier, table, col1, col2 string, id1, id2 any) error {
	_, err := exec.Exec("INSERT OR IGNORE INTO "+table+" ("+col1+", "+col2+") VALUES (?, ?)", id1, id2)
	return err
}
