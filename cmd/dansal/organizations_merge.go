package main

import (
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/ademant/dansal/internal/logging"
)

// Admin merge of two organisations (#1644). The survivor (keep) stays; the
// other (merge) is folded into it and deleted. Fields are compared per field:
// an empty side takes the other side's value, identical values are kept, and
// two different non-empty values need an explicit choice ("keep" or "merge").
// The banner image follows the same rule. Newer means the later updated_at,
// falling back to created_at.

// orgMergeTextFields are the organisation columns compared in a merge.
var orgMergeTextFields = []string{
	"name", "description", "actor_name", "website", "instagram", "mastodon",
	"facebook", "contact_email", "contact_name", "wikidata_id", "notes_md", "chat_links",
}

// orgMergeRefTables lists every table with a foreign key to organizations(id),
// which a merge must repoint or clear. TestOrgMergeCoversEveryOrgForeignKey
// fails if the schema gains one that is not listed here.
var orgMergeRefTables = []struct {
	table, column string
	// unique marks junction tables whose key includes the organisation, so
	// repointing can collide with an existing row (handled with OR IGNORE).
	unique bool
}{
	{"events", "organization_id", false},
	{"event_series", "organization_id", false},
	{"fetch_sources", "organization_id", false},
	{"invite_links", "org_id", false},
	{"oidc_providers", "org_id", false},
	{"pending_fetch_suggestions", "org_id", false},
	{"pending_registrations", "org_id", false},
	{"location_organizations", "organization_id", true},
	{"organization_members", "organization_id", true},
}

type orgMergeSide struct {
	ID         int
	Name       string
	Fields     map[string]string
	Updated    int64 // updated_at, 0 if never set
	CreatedAt  int64 // created_at as unix time
	HasBanner  bool
	bannerPath string
}

// newerKey orders the two sides by recency: updated_at when set, else created_at.
func (s orgMergeSide) newerKey() int64 {
	if s.Updated > 0 {
		return s.Updated
	}
	return s.CreatedAt
}

func loadOrgMergeSide(q querier, id int) (orgMergeSide, error) {
	var s orgMergeSide
	s.Fields = make(map[string]string)
	vals := make([]string, len(orgMergeTextFields))
	ptrs := make([]any, 0, len(orgMergeTextFields)+3)
	ptrs = append(ptrs, &s.ID)
	for i := range vals {
		ptrs = append(ptrs, &vals[i])
	}
	ptrs = append(ptrs, &s.Updated, &s.CreatedAt)
	cols := "id, " + strings.Join(coalesceCols(orgMergeTextFields), ", ") + ", COALESCE(updated_at,0), COALESCE(strftime('%s', created_at),0)"
	if err := q.QueryRow("SELECT "+cols+" FROM organizations WHERE id = ?", id).Scan(ptrs...); err != nil {
		return orgMergeSide{}, err
	}
	for i, f := range orgMergeTextFields {
		s.Fields[f] = vals[i]
	}
	s.Name = s.Fields["name"]
	if path, _, found := imagePathForID(orgImagesDir, strconv.Itoa(id)); found {
		s.HasBanner = true
		s.bannerPath = path
	}
	return s, nil
}

func coalesceCols(fields []string) []string {
	out := make([]string, len(fields))
	for i, f := range fields {
		out[i] = "COALESCE(" + f + ", '')"
	}
	return out
}

// orgMergeField is one row of the comparison the admin sees.
type orgMergeField struct {
	Field  string `json:"field"`
	Keep   string `json:"keep"`
	Merge  string `json:"merge"`
	Newer  string `json:"newer"`  // "keep" or "merge": the side with the later change
	Choice string `json:"choice"` // "" when no choice is needed; otherwise "keep" or "merge"
}

// orgMergeConflicts returns the fields where both sides hold different
// non-empty values (the banner counts when both sides have one), so a choice
// is needed.
func orgMergeConflicts(keep, merge orgMergeSide) []string {
	var out []string
	for _, f := range orgMergeTextFields {
		k, m := keep.Fields[f], merge.Fields[f]
		if k != "" && m != "" && k != m {
			out = append(out, f)
		}
	}
	if keep.HasBanner && merge.HasBanner {
		out = append(out, "banner")
	}
	return out
}

func orgMergeFieldsFor(keep, merge orgMergeSide) []orgMergeField {
	newer := "keep"
	if merge.newerKey() > keep.newerKey() {
		newer = "merge"
	}
	conflicts := map[string]bool{}
	for _, f := range orgMergeConflicts(keep, merge) {
		conflicts[f] = true
	}
	var out []orgMergeField
	for _, f := range orgMergeTextFields {
		k, m := keep.Fields[f], merge.Fields[f]
		if k == m {
			continue
		}
		fd := orgMergeField{Field: f, Keep: k, Merge: m, Newer: newer}
		if conflicts[f] {
			fd.Choice = "" // filled from the request
		} else {
			fd.Choice = autoChoice(k, m)
		}
		out = append(out, fd)
	}
	if keep.HasBanner != merge.HasBanner || conflicts["banner"] {
		fd := orgMergeField{Field: "banner", Keep: yesNo(keep.HasBanner), Merge: yesNo(merge.HasBanner), Newer: newer}
		if !conflicts["banner"] {
			fd.Choice = autoChoice(boolStr(keep.HasBanner), boolStr(merge.HasBanner))
		}
		out = append(out, fd)
	}
	return out
}

// autoChoice is the side taken without asking when one value is empty.
func autoChoice(keep, merge string) string {
	if keep == "" && merge != "" {
		return "merge"
	}
	return "keep"
}

func yesNo(b bool) string {
	if b {
		return "yes"
	}
	return "no"
}

func boolStr(b bool) string {
	if b {
		return "x"
	}
	return ""
}

type orgMergeSummary struct {
	ID   int    `json:"id"`
	Name string `json:"name"`
}

// GET /api/v1/organizations/{id}/merge-preview?merge_id=N (admin)
func getOrgMergePreview(w http.ResponseWriter, r *http.Request) {
	_, role := callerFromRequest(r)
	if !requireRole(w, role, RoleAdmin) {
		return
	}
	keepID, mergeID, ok := orgMergeIDs(w, r.PathValue("id"), r.URL.Query().Get("merge_id"))
	if !ok {
		return
	}
	keep, err := loadOrgMergeSide(db, keepID)
	if err != nil {
		writeError(w, "organization not found", http.StatusNotFound)
		return
	}
	merge, err := loadOrgMergeSide(db, mergeID)
	if err != nil {
		writeError(w, "organization not found", http.StatusNotFound)
		return
	}
	writeJSON(w, map[string]any{
		"keep":   orgMergeSummary{ID: keep.ID, Name: keep.Name},
		"merge":  orgMergeSummary{ID: merge.ID, Name: merge.Name},
		"fields": orgMergeFieldsFor(keep, merge),
	})
}

// POST /api/v1/organizations/merge (admin)
// Body: {"keep_id": N, "merge_id": M, "choices": {"<field>": "keep"|"merge"}}.
// Every conflicting field needs a choice; a missing one returns 422 with the
// list of fields still to decide.
func mergeOrganizations(w http.ResponseWriter, r *http.Request) {
	_, role := callerFromRequest(r)
	if !requireRole(w, role, RoleAdmin) {
		return
	}
	var req struct {
		KeepID  int               `json:"keep_id"`
		MergeID int               `json:"merge_id"`
		Choices map[string]string `json:"choices"`
	}
	if !decodeJSONBody(w, r, &req) {
		return
	}
	keepID, mergeID, ok := orgMergeIDs(w, strconv.Itoa(req.KeepID), strconv.Itoa(req.MergeID))
	if !ok {
		return
	}
	if err := checkOrgMergeChoices(req.Choices); err != nil {
		writeError(w, err.Error(), http.StatusBadRequest)
		return
	}

	tx, err := db.Begin()
	if err != nil {
		writeInternalError(w, err)
		return
	}
	defer tx.Rollback()

	keep, err := loadOrgMergeSide(tx, keepID)
	if err != nil {
		writeError(w, "organization not found", http.StatusNotFound)
		return
	}
	merge, err := loadOrgMergeSide(tx, mergeID)
	if err != nil {
		writeError(w, "organization not found", http.StatusNotFound)
		return
	}

	var missing []string
	for _, f := range orgMergeConflicts(keep, merge) {
		if req.Choices[f] == "" {
			missing = append(missing, f)
		}
	}
	if len(missing) > 0 {
		sort.Strings(missing)
		writeJSONStatus(w, http.StatusUnprocessableEntity, map[string]any{
			"error":   "choose a value for every conflicting field",
			"missing": missing,
		})
		return
	}

	// Work out the value the survivor ends with for every field.
	final := make(map[string]string, len(orgMergeTextFields))
	for _, f := range orgMergeTextFields {
		k, m := keep.Fields[f], merge.Fields[f]
		switch {
		case k == m:
			final[f] = k
		case k == "" || m == "":
			final[f] = autoChoiceValue(k, m)
		default:
			if req.Choices[f] == "merge" {
				final[f] = m
			} else {
				final[f] = k
			}
		}
	}
	// Banner: a file is taken from the merge side only when the chosen side
	// says so (conflict) or the survivor has none.
	takeBanner := false
	if keep.HasBanner && merge.HasBanner {
		takeBanner = req.Choices["banner"] == "merge"
	} else if merge.HasBanner && !keep.HasBanner {
		takeBanner = true
	}

	for _, t := range orgMergeRefTables {
		if t.unique {
			if _, err := tx.Exec(fmt.Sprintf("UPDATE OR IGNORE %s SET %s = ? WHERE %s = ?", t.table, t.column, t.column), keepID, mergeID); err != nil {
				writeInternalError(w, err)
				return
			}
			if _, err := tx.Exec(fmt.Sprintf("DELETE FROM %s WHERE %s = ?", t.table, t.column), mergeID); err != nil {
				writeInternalError(w, err)
				return
			}
			continue
		}
		if _, err := tx.Exec(fmt.Sprintf("UPDATE %s SET %s = ? WHERE %s = ?", t.table, t.column, t.column), keepID, mergeID); err != nil {
			writeInternalError(w, err)
			return
		}
	}
	if err := mergeOwnerMedia(tx, ownerTypeOrganization, keepID, mergeID); err != nil {
		writeInternalError(w, err)
		return
	}

	// Delete the dropped row first: name is unique, so the survivor cannot
	// take the dropped row's name while it still exists.
	if _, err := tx.Exec("DELETE FROM organizations WHERE id = ?", mergeID); err != nil {
		writeInternalError(w, err)
		return
	}
	if _, err := tx.Exec(`UPDATE organizations SET name=?, description=?, actor_name=?, website=?, instagram=?, mastodon=?, facebook=?, contact_email=?, contact_name=?, wikidata_id=?, notes_md=?, chat_links=?, updated_at=strftime('%s','now') WHERE id=?`,
		final["name"], final["description"], final["actor_name"], final["website"], final["instagram"], final["mastodon"],
		final["facebook"], final["contact_email"], final["contact_name"], final["wikidata_id"], final["notes_md"], final["chat_links"], keepID); err != nil {
		writeError(w, "merged name or actor name is already used by another organisation", http.StatusConflict)
		return
	}
	if err := tx.Commit(); err != nil {
		writeInternalError(w, err)
		return
	}

	// Files follow the committed rows; a failure here leaves the merged data in
	// place and is logged for the admin to fix by hand.
	if takeBanner {
		if err := replaceOrgBanner(keep.ID, merge.bannerPath); err != nil {
			logging.Errorf("org merge %d into %d: banner copy: %v", mergeID, keepID, err)
		}
	}
	if merge.HasBanner {
		if err := os.Remove(merge.bannerPath); err != nil {
			logging.Errorf("org merge %d: remove banner: %v", mergeID, err)
		}
		orgImgCache.remove(mergeID)
	}
	writeJSON(w, map[string]any{"id": keepID})
}

// autoChoiceValue returns the non-empty side (or the empty one when both are empty).
func autoChoiceValue(keep, merge string) string {
	if keep == "" {
		return merge
	}
	return keep
}

// checkOrgMergeChoices rejects choices for unknown fields or values other than keep/merge.
func checkOrgMergeChoices(choices map[string]string) error {
	allowed := map[string]bool{"banner": true}
	for _, f := range orgMergeTextFields {
		allowed[f] = true
	}
	for f, v := range choices {
		if !allowed[f] {
			return fmt.Errorf("unknown field %q", f)
		}
		if v != "keep" && v != "merge" {
			return fmt.Errorf("choice for %q must be keep or merge", f)
		}
	}
	return nil
}

// orgMergeIDs parses and checks the two ids of a merge request.
func orgMergeIDs(w http.ResponseWriter, keepStr, mergeStr string) (int, int, bool) {
	keepID, err1 := strconv.Atoi(keepStr)
	mergeID, err2 := strconv.Atoi(mergeStr)
	if err1 != nil || err2 != nil || keepID <= 0 || mergeID <= 0 {
		writeError(w, "keep and merge organisation ids are required", http.StatusBadRequest)
		return 0, 0, false
	}
	if keepID == mergeID {
		writeError(w, "keep and merge must be different organisations", http.StatusBadRequest)
		return 0, 0, false
	}
	return keepID, mergeID, true
}

// replaceOrgBanner copies the banner file at src to the survivor's banner
// path, replacing any banner the survivor already has.
func replaceOrgBanner(keepID int, src string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	ext := strings.ToLower(filepath.Ext(src))
	if old, _, found := imagePathForID(orgImagesDir, strconv.Itoa(keepID)); found {
		if err := os.Remove(old); err != nil {
			return err
		}
	}
	dst := filepath.Join(orgImagesDir, strconv.Itoa(keepID)+ext)
	out, err := os.Create(dst)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	if err := out.Close(); err != nil {
		return err
	}
	mime := "image/avif"
	if ext == ".jpeg" || ext == ".jpg" {
		mime = "image/jpeg"
	}
	orgImgCache.add(keepID, mime)
	return nil
}
