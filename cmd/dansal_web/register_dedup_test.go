package main

import (
	"database/sql"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/ademant/dansal/internal/orgmatch"
)

// TestRegisterDedupOfferRender covers #1648: the registration form offers the
// matching organisations, with a button to join each and one to continue with
// a new organisation, and the buttons submit the registration form.
func TestRegisterDedupOfferRender(t *testing.T) {
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
	req := httptest.NewRequest(http.MethodPost, "/register", nil)

	render := func(data RegisterPageData) string {
		rec := httptest.NewRecorder()
		renderTemplate(rec, tmpls.register, tmplData(req, cfg, i18n, "Register", data))
		body, _ := io.ReadAll(rec.Body)
		html := string(body)
		if strings.Contains(html, "template error") {
			t.Fatalf("template execution error: %s", html[max(0, len(html)-300):])
		}
		return html
	}

	offered := render(RegisterPageData{
		PrefillRegType: "new_org",
		FormToken:      "tok",
		DedupOffers:    []DedupOfferView{{ID: 7, Name: "Erlanger Tanzhaus", Strong: true}},
	})
	for _, want := range []string{
		`id="register-form"`,
		`name="dedup_action" value="join:7"`,
		`name="dedup_action" value="create"`,
		`form="register-form"`,
		"Erlanger Tanzhaus",
	} {
		if !strings.Contains(offered, want) {
			t.Errorf("offer page missing %s", want)
		}
	}

	plain := render(RegisterPageData{PrefillRegType: "new_org", FormToken: "tok"})
	if strings.Contains(plain, `name="dedup_action"`) {
		t.Error("no offer expected when nothing matched")
	}
}

// TestOrgMatchConvertsOrganizations checks the handler's conversion keeps the
// fields the matcher reads.
func TestOrgMatchConvertsOrganizations(t *testing.T) {
	got := toOrgMatchOrgs([]Organization{{ID: 3, Name: "A", ActorName: "a_act", Website: "https://a.example"}})
	want := orgmatch.Org{ID: 3, Name: "A", ActorName: "a_act", Website: "https://a.example"}
	if len(got) != 1 || got[0] != want {
		t.Fatalf("got %+v, want %+v", got, want)
	}
}
