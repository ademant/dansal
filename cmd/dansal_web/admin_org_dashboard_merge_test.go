package main

import (
	"html/template"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestOrgDashboardMergeIconIsAdminOnly: the merge page is admin-only, so the
// org dashboard shows its merge icon to admins and not to org members.
func TestOrgDashboardMergeIconIsAdminOnly(t *testing.T) {
	const mergeHref = `href="/admin/organizations/7/merge"`
	for _, tc := range []struct {
		name    string
		isAdmin bool
		want    bool
	}{
		{"admin sees merge", true, true},
		{"member does not", false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			data := AdminOrgDashboardData{Org: Organization{ID: 7, Name: "Org"}, Slug: "org", IsMember: true, IsAdmin: tc.isAdmin}
			req := httptest.NewRequest(http.MethodGet, "/admin/organization/org", nil)
			html := renderPageHTML(t, func(ts *Templates) *template.Template { return ts.adminOrgDashboard }, req, "Org", data)
			if got := strings.Contains(html, mergeHref); got != tc.want {
				t.Errorf("merge link present = %v, want %v", got, tc.want)
			}
		})
	}
}
