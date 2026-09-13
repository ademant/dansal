package main

import (
	"html/template"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestEmbedCalendarOrgCheckboxes covers the org-checkbox filter added for
// embedCalendarHandler: checkboxes render (one per configured org) only when
// the embed was scoped to 2+ orgs, and stay absent for the single-org/no-org
// cases so existing embeds render unchanged.
func TestEmbedCalendarOrgCheckboxes(t *testing.T) {
	tmpls := loadTemplates()
	strs := loadI18n("").Strings("de")

	base := map[string]any{
		"Lang": "de", "Nonce": "x", "Events": []Event{}, "CalData": template.JS("[]"),
		"Tags": []Tag{}, "OrgNames": map[int]string{}, "From": "2026-01-01", "To": "2026-01-14",
		"SelectedTag": "", "FPLocale": "", "FPLocaleSRI": "",
		"Strings": strs, "BaseURL": "https://example.test", "SiteName": "dansal", "TileToken": "t",
	}

	render := func(t *testing.T, orgOptions []embedOrgOption) string {
		t.Helper()
		data := map[string]any{}
		for k, v := range base {
			data[k] = v
		}
		data["OrgOptions"] = orgOptions
		rec := httptest.NewRecorder()
		renderEmbed(rec, tmpls.embedCalendar, data)
		if rec.Code != 200 {
			t.Fatalf("status = %d", rec.Code)
		}
		return rec.Body.String()
	}

	t.Run("no org scoping: no checkboxes", func(t *testing.T) {
		// Checks for the actual rendered <input class="org-check" ...> markup,
		// not just the substring "org-check" — that also appears in the
		// template's always-present CSS/JS (org-check-label style rule,
		// querySelectorAll('.org-check')), which exist whether or not any
		// checkbox actually gets rendered.
		body := render(t, nil)
		if strings.Contains(body, `class="org-check"`) {
			t.Error("expected no org checkboxes when OrgOptions is empty")
		}
	})

	t.Run("2+ orgs configured: one checkbox each, checked by default", func(t *testing.T) {
		body := render(t, []embedOrgOption{{ID: 5, Name: "Balfolk Aachen"}, {ID: 9, Name: "Balfolk Köln"}})
		for _, want := range []string{`value="5"`, `value="9"`, "Balfolk Aachen", "Balfolk Köln"} {
			if !strings.Contains(body, want) {
				t.Errorf("expected body to contain %q, body: %s", want, body)
			}
		}
		if strings.Count(body, `class="org-check"`) != 2 {
			t.Errorf("expected exactly 2 org checkboxes, body: %s", body)
		}
		if strings.Count(body, `class="org-check" value="5" checked`) != 1 {
			t.Error("expected the org-5 checkbox to be checked by default")
		}
	})
}
