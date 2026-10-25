package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"

	"github.com/ademant/dansal/internal/logging"
)

// Admin merge of two organisations (#1644). The API does the data merge; this
// file shows the per-field comparison, passes the admin's choices through,
// and records the slug redirect from the dropped organisation to the survivor.

// orgMergeWebFields are the fields the comparison shows, with their label keys.
var orgMergeWebFields = []struct{ field, labelKey string }{
	{"name", "org_name"},
	{"description", "org_description"},
	{"actor_name", "org_actor_name"},
	{"website", "org_website"},
	{"instagram", "org_instagram"},
	{"mastodon", "org_mastodon"},
	{"facebook", "org_facebook"},
	{"contact_email", "org_contact_email"},
	{"contact_name", "org_contact_name"},
	{"wikidata_id", "org_wikidata"},
	{"notes_md", "org_notes"},
	{"chat_links", "org_chat_links"},
	{"banner", "org_image"},
}

type orgMergeSummary struct {
	ID   int    `json:"id"`
	Name string `json:"name"`
}

type orgMergeFieldDTO struct {
	Field  string `json:"field"`
	Keep   string `json:"keep"`
	Merge  string `json:"merge"`
	Newer  string `json:"newer"`
	Choice string `json:"choice"`
}

// OrgMergePreview is the API's comparison of two organisations (#1644).
type OrgMergePreview struct {
	Keep   orgMergeSummary    `json:"keep"`
	Merge  orgMergeSummary    `json:"merge"`
	Fields []orgMergeFieldDTO `json:"fields"`
}

// orgMergeMissingError is returned when a conflicting field has no choice.
type orgMergeMissingError struct {
	Missing []string
}

func (e *orgMergeMissingError) Error() string {
	return "choose a value for every conflicting field: " + strings.Join(e.Missing, ", ")
}

// GetOrgMergePreview fetches the comparison for merging mergeID into keepID.
func (c *DansalClient) GetOrgMergePreview(ctx context.Context, keepID, mergeID int, token string) (OrgMergePreview, error) {
	var out OrgMergePreview
	path := fmt.Sprintf("/api/v1/organizations/%d/merge-preview?merge_id=%d", keepID, mergeID)
	if err := c.do(ctx, http.MethodGet, path, token, nil, &out); err != nil {
		return OrgMergePreview{}, err
	}
	return out, nil
}

// MergeOrganizations folds mergeID into keepID with the admin's choices.
// A 422 means some conflicting field has no choice; it is returned as
// *orgMergeMissingError with the fields still to decide.
func (c *DansalClient) MergeOrganizations(ctx context.Context, keepID, mergeID int, choices map[string]string, token string) error {
	body, _ := json.Marshal(map[string]any{"keep_id": keepID, "merge_id": mergeID, "choices": choices})
	resp, err := c.authed(ctx, http.MethodPost, "/api/v1/organizations/merge", token, body)
	if err != nil {
		return err
	}
	if resp.StatusCode == http.StatusUnprocessableEntity {
		defer resp.Body.Close()
		b, _ := io.ReadAll(io.LimitReader(resp.Body, maxAPIErrorBody))
		var out struct {
			Missing []string `json:"missing"`
		}
		json.Unmarshal(b, &out)
		return &orgMergeMissingError{Missing: out.Missing}
	}
	if err := decodeAPIResponse(resp, nil, http.StatusOK); err != nil {
		return err
	}
	c.invalidateOrgs()
	return nil
}

// orgMergeRowView is one row of the comparison page.
type orgMergeRowView struct {
	Field     string
	LabelKey  string
	Keep      string
	Merge     string
	Auto      bool   // no decision needed; Value is what the survivor gets
	Value     string // for Auto rows
	KeepSide  bool   // the keep value is chosen (green)
	MergeSide bool   // the merge value is chosen (green)
	Newer     string // "keep" or "merge": the side changed later (marked)
}

// AdminOrgMergeData feeds admin_org_merge.html.
type AdminOrgMergeData struct {
	KeepID    int
	KeepName  string
	MergeID   int
	MergeName string
	Rows      []orgMergeRowView
	Others    []Organization // chooser, when no other organisation is picked yet
	Compare   bool           // the comparison is shown
	ErrorKey  string
}

func orgMergeRowsFrom(p OrgMergePreview) []orgMergeRowView {
	labels := map[string]string{}
	for _, f := range orgMergeWebFields {
		labels[f.field] = f.labelKey
	}
	var rows []orgMergeRowView
	for _, f := range p.Fields {
		row := orgMergeRowView{Field: f.Field, LabelKey: labels[f.Field], Keep: f.Keep, Merge: f.Merge, Newer: f.Newer}
		if f.Field == "banner" {
			row.Keep = yesNoText(f.Keep)
			row.Merge = yesNoText(f.Merge)
		}
		if f.Choice != "" {
			row.Auto = true
			row.Value = f.Keep
			if f.Choice == "merge" {
				row.Value = f.Merge
			}
			if f.Field == "banner" {
				row.Value = yesNoText(row.Value)
			}
		} else {
			row.KeepSide = f.Newer == "keep"
			row.MergeSide = f.Newer == "merge"
		}
		rows = append(rows, row)
	}
	return rows
}

// yesNoText maps the API's "yes"/"no" banner marker to its i18n-free display.
func yesNoText(v string) string {
	switch v {
	case "yes":
		return "✓"
	case "no":
		return "–"
	}
	return v
}

// orgMergeBothIDs parses the organisation id from the path and the "with"/merge id.
func orgMergeBothIDs(w http.ResponseWriter, r *http.Request, other string) (int, int, bool) {
	keepID, ok := intPathValueOr404(w, r, "id")
	if !ok {
		return 0, 0, false
	}
	mergeID, err := strconv.Atoi(other)
	if err != nil || mergeID <= 0 || mergeID == keepID {
		return keepID, 0, true
	}
	return keepID, mergeID, true
}

// GET /admin/organizations/{id}/merge[?with=N]
func adminOrgMergePageHandler(cfg *Config, tmpls *Templates, client *DansalClient, i18n *I18n) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		su, ok := requireAdmin(w, r)
		if !ok {
			return
		}
		keepID, mergeID, ok := orgMergeBothIDs(w, r, r.URL.Query().Get("with"))
		if !ok {
			return
		}
		renderOrgMergePage(w, r, cfg, tmpls, client, i18n, su, keepID, mergeID, "")
	}
}

func renderOrgMergePage(w http.ResponseWriter, r *http.Request, cfg *Config, tmpls *Templates, client *DansalClient, i18n *I18n, su *SessionUser, keepID, mergeID int, errKey string) {
	data := AdminOrgMergeData{KeepID: keepID, MergeID: mergeID, ErrorKey: errKey}
	orgs, err := client.GetOrganizations(r.Context())
	if err != nil {
		logging.Errorf("org merge: list organizations: %v", err)
	}
	if mergeID == 0 {
		for _, o := range orgs {
			if o.ID != keepID {
				data.Others = append(data.Others, o)
			}
		}
		for _, o := range orgs {
			if o.ID == keepID {
				data.KeepName = o.Name
			}
		}
	} else {
		preview, err := client.GetOrgMergePreview(r.Context(), keepID, mergeID, getSessionToken(r))
		if err != nil {
			logging.Errorf("org merge preview %d into %d: %v", mergeID, keepID, err)
			http.Error(w, "organisation not found", http.StatusNotFound)
			return
		}
		data.Compare = true
		data.KeepName = preview.Keep.Name
		data.MergeName = preview.Merge.Name
		data.Rows = orgMergeRowsFrom(preview)
	}
	renderTemplate(w, tmpls.adminOrgMerge, tmplData(r, cfg, i18n, i18n.T(r, "org_merge_title"), data))
}

// POST /admin/organizations/{id}/merge
func adminOrgMergeSubmitHandler(cfg *Config, tmpls *Templates, db *sql.DB, client *DansalClient, i18n *I18n) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		su, ok := requireAdmin(w, r)
		if !ok {
			return
		}
		keepID, ok := intPathValueOr404(w, r, "id")
		if !ok {
			return
		}
		if err := r.ParseForm(); err != nil {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		mergeID, err := strconv.Atoi(r.FormValue("merge_id"))
		if err != nil || mergeID <= 0 || mergeID == keepID {
			http.Redirect(w, r, fmt.Sprintf("/admin/organizations/%d/merge", keepID), http.StatusSeeOther)
			return
		}
		choices := map[string]string{}
		for _, f := range orgMergeWebFields {
			if v := r.FormValue("choice_" + f.field); v == "keep" || v == "merge" {
				choices[f.field] = v
			}
		}
		token := getSessionToken(r)
		ctx := r.Context()

		// The dropped organisation's public slug is recorded as a redirect to the
		// survivor (#1462), so it must be read before the merge deletes it.
		oldSlug := ""
		if actor, aerr := getActorByOrgID(db, mergeID); aerr == nil {
			oldSlug = actor.OrgSlug
		} else if org, oerr := client.GetOrganizationDetail(ctx, mergeID); oerr == nil {
			oldSlug = effectiveSlug(org)
		}

		if err := client.MergeOrganizations(ctx, keepID, mergeID, choices, token); err != nil {
			var missing *orgMergeMissingError
			if errors.As(err, &missing) {
				renderOrgMergePage(w, r, cfg, tmpls, client, i18n, su, keepID, mergeID, "org_merge_missing")
				return
			}
			logging.Errorf("org merge %d into %d: %v", mergeID, keepID, err)
			renderOrgMergePage(w, r, cfg, tmpls, client, i18n, su, keepID, mergeID, "org_merge_error")
			return
		}

		if keep, kerr := client.GetOrganizationDetail(ctx, keepID); kerr == nil {
			if newSlug := effectiveSlug(keep); oldSlug != "" && newSlug != "" && oldSlug != newSlug {
				recordOrgSlugRename(db, oldSlug, newSlug)
			}
		}
		http.Redirect(w, r, fmt.Sprintf("/admin/organizations/%d/edit", keepID), http.StatusSeeOther)
	}
}
