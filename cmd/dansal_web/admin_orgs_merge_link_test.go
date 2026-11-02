package main

import (
	"html/template"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestAdminOrgsListLinksToMerge: the organisations list must offer the merge
// page for each row the user can edit, not only the edit form. The merge
// page is reachable nowhere else from the list.
func TestAdminOrgsListLinksToMerge(t *testing.T) {
	data := AdminOrgsData{
		CanEdit: true,
		Stats: []OrgStats{
			{Org: Organization{ID: 7, Name: "Editable"}, Slug: "editable", CanEdit: true},
			{Org: Organization{ID: 8, Name: "Read only"}, Slug: "read-only", CanEdit: false},
		},
	}
	req := httptest.NewRequest(http.MethodGet, "/admin/organizations", nil)
	html := renderPageHTML(t, func(ts *Templates) *template.Template { return ts.adminOrgs }, req, "Organisations", data)

	if !strings.Contains(html, `href="/admin/organizations/7/merge"`) {
		t.Error("editable organisation row has no merge link")
	}
	if strings.Contains(html, `href="/admin/organizations/8/merge"`) {
		t.Error("read-only organisation row must not offer merge")
	}
}
