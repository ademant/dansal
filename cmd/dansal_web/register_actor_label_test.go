package main

import (
	"os"
	"strings"
	"testing"

	"gopkg.in/yaml.v2"
)

// TestRegisterOrgActorNameIsLabelledActivityPub covers #1647: the registration
// form's org name field is labelled with the admin form's ActivityPub wording,
// its hint says the value becomes the Fediverse address in every language, and
// the live handle preview is wired to the template's domain.
func TestRegisterOrgActorNameIsLabelledActivityPub(t *testing.T) {
	src, err := os.ReadFile("templates/register.html")
	if err != nil {
		t.Fatal(err)
	}
	tpl := string(src)
	for _, want := range []string{
		`{{$.Strings.T "org_actor_name"}}`,
		`data-fn="updateActorPreview"`,
		`id="actor-preview"`,
		`data-host="{{$.Domain}}"`,
	} {
		if !strings.Contains(tpl, want) {
			t.Errorf("register.html is missing %s", want)
		}
	}
	if strings.Contains(tpl, `{{$.Strings.T "register_org_actor_name"}}`) {
		t.Error("register.html still labels the field with the old Kurzname key")
	}

	var cfg struct {
		Languages map[string]struct {
			Strings map[string]string `yaml:"strings"`
		} `yaml:"languages"`
	}
	raw, err := os.ReadFile("i18n.yaml")
	if err != nil {
		t.Fatal(err)
	}
	if err := yaml.Unmarshal(raw, &cfg); err != nil {
		t.Fatal(err)
	}
	if len(cfg.Languages) != 12 {
		t.Fatalf("got %d languages, want 12", len(cfg.Languages))
	}
	for lang, section := range cfg.Languages {
		hint := section.Strings["register_org_actor_name_hint"]
		if !strings.Contains(hint, "ActivityPub") {
			t.Errorf("%s: register_org_actor_name_hint = %q, want it to name ActivityPub", lang, hint)
		}
	}
}
