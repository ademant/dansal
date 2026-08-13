package main

import "testing"

// TestTagLabel verifies the bal-folk -> "Ball" override (#1094) and the
// fallback chain: i18n translation, then the DB name, then the bare slug.
func TestTagLabel(t *testing.T) {
	strs := I18nStrings{"tag_ball": "Ball"}

	if got := tagLabel(strs, "bal-folk", "Bal Folk"); got != "Ball" {
		t.Errorf("tagLabel(bal-folk) = %q, want %q (i18n override should win over DB name)", got, "Ball")
	}
	if got := tagLabel(I18nStrings{}, "bal-folk", "Bal Folk"); got != "Bal Folk" {
		t.Errorf("tagLabel(bal-folk, no translation) = %q, want %q (should fall back to DB name)", got, "Bal Folk")
	}
	if got := tagLabel(I18nStrings{}, "bal-folk", ""); got != "bal-folk" {
		t.Errorf("tagLabel(bal-folk, no translation, no DB name) = %q, want %q (should fall back to slug)", got, "bal-folk")
	}
	if got := tagLabel(strs, "festival", "Festival"); got != "Festival" {
		t.Errorf("tagLabel(festival) = %q, want %q (no tagI18nKeys entry, should use DB name)", got, "Festival")
	}
}
