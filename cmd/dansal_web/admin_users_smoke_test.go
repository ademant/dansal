package main

import (
	"database/sql"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestSmokeRenderAdminUsers(t *testing.T) {
	db, err := sql.Open("sqlite3", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	db.Exec(`CREATE TABLE site_settings (key TEXT PRIMARY KEY, value TEXT)`)
	siteCfg = newSiteSettingsCache(db)

	tmpls := loadTemplates()
	i18n := loadI18n("")
	cfg := &Config{Domain: "example.test"}

	req := httptest.NewRequest(http.MethodGet, "/admin/users", nil)
	req = withSessionUser(req, &SessionUser{ID: 1, Role: "admin"})

	orgs := []Organization{{ID: 1, Name: "Org One"}, {ID: 2, Name: "Org Two"}}
	orgMap := map[int]Organization{1: orgs[0], 2: orgs[1]}

	render := func(name string, data AdminUsersData) {
		t.Run(name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			renderTemplate(rec, tmpls.adminUsers, tmplData(req, cfg, i18n, "test", data))
			body, _ := io.ReadAll(rec.Body)
			if rec.Code != http.StatusOK {
				t.Fatalf("status=%d body=%s", rec.Code, body)
			}
			if strings.Contains(string(body), "template error") || strings.Contains(string(body), "runtime error") {
				t.Fatalf("template execution error, body tail: %s", body[max(0, len(body)-800):])
			}
			if !strings.Contains(string(body), "</html>") {
				t.Fatalf("truncated render (no closing </html>), body tail: %s", body[max(0, len(body)-300):])
			}
		})
	}

	render("admin-full", AdminUsersData{
		IsAdmin: true,
		Users: []UserInfo{
			{ID: 10, DisplayName: "Alice", Role: "user"},
			{ID: 11, DisplayName: "Bob", Role: "publisher", UserMetadata: `{"client_name":"MyApp","client_url":"https://example.test"}`},
			{ID: 12, Role: "user", Disabled: true},
		},
		Orgs:     orgs,
		OrgMap:   orgMap,
		UserOrgs: map[int][]int{10: {1, 2}, 11: {1}},
		MyOrgs:   orgs,
		MyOrgIDs: map[int]bool{1: true, 2: true},
		Invites: []InviteLink{
			{Token: "tok-qr", InviteType: "qr", OrgID: intPtr(1), ExpiresAt: "2026-08-01T00:00:00Z"},
			{Token: "tok-link", InviteType: "link", ExpiresAt: "2026-08-02T00:00:00Z"},
		},
		BaseURL: "https://example.test",
	})

	render("non-admin-scoped", AdminUsersData{
		IsAdmin: false,
		Users: []UserInfo{
			{ID: 10, DisplayName: "Alice", Role: "user"},
		},
		Orgs:             orgs,
		OrgMap:           orgMap,
		UserOrgs:         map[int][]int{10: {1}},
		MyOrgs:           []Organization{orgs[0]},
		MyOrgIDs:         map[int]bool{1: true},
		Invites:          nil,
		BaseURL:          "https://example.test",
		PreselectedOrgID: 1,
	})

	render("no-data", AdminUsersData{
		IsAdmin: true,
		OrgMap:  map[int]Organization{},
	})
}
