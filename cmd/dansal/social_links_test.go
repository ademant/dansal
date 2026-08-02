package main

import (
	"database/sql"
	"encoding/json"
	"reflect"
	"strconv"
	"testing"

	_ "github.com/mattn/go-sqlite3"
)

func TestFilterChatLinks(t *testing.T) {
	in := []ChatLink{
		{Platform: "telegram", URL: "https://t.me/+abc"},
		{Platform: "myspace", URL: "https://myspace.com/whatever"}, // unknown platform
		{Platform: "signal", URL: "  "},                           // blank URL
		{Platform: "mailing_list", URL: "https://lists.example.org/postorius/lists/balfolk-koeln.example.org/"},
	}
	want := []ChatLink{
		{Platform: "telegram", URL: "https://t.me/+abc"},
		{Platform: "mailing_list", URL: "https://lists.example.org/postorius/lists/balfolk-koeln.example.org/"},
	}
	got := filterChatLinks(in)
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("filterChatLinks() = %+v, want %+v", got, want)
	}
}

// TestInstructorSocialFieldsPersist verifies the mastodon/instagram/facebook
// columns added for #924 round-trip through instructorCols/scanInstructor,
// the same way musicians' existing social fields already do.
func TestInstructorSocialFieldsPersist(t *testing.T) {
	old := db
	defer func() { db = old }()

	conn, err := sql.Open("sqlite3", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	db = conn

	if err := createTables(); err != nil {
		t.Fatalf("createTables: %v", err)
	}
	migrateDB()
	oldAvatars := instructorAvatars
	instructorAvatars = newAvatarSet(t.TempDir(), "/api/v1/instructor-avatars/")
	defer func() { instructorAvatars = oldAvatars }()

	inst, err := scanInstructor(db.QueryRow(
		"INSERT INTO instructors (name, mastodon, instagram, facebook) VALUES (?,?,?,?) RETURNING "+instructorCols,
		"Jane Doe", "@jane@dance.social", "janedoe", "janedoe.dance",
	))
	if err != nil {
		t.Fatalf("insert instructor: %v", err)
	}
	if inst.Mastodon != "@jane@dance.social" || inst.Instagram != "janedoe" || inst.Facebook != "janedoe.dance" {
		t.Fatalf("social fields did not round-trip: %+v", inst)
	}

	reloaded, err := scanInstructor(db.QueryRow("SELECT " + instructorCols + " FROM instructors WHERE id=" + strconv.Itoa(inst.ID)))
	if err != nil {
		t.Fatalf("reload instructor: %v", err)
	}
	if reloaded.Mastodon != inst.Mastodon || reloaded.Instagram != inst.Instagram || reloaded.Facebook != inst.Facebook {
		t.Fatalf("social fields did not survive reload: %+v", reloaded)
	}
}

// TestOrganizationChatLinksPersist verifies the chat_links JSON column added
// for #925 round-trips through orgSelectCols/scanOrg.
func TestOrganizationChatLinksPersist(t *testing.T) {
	old := db
	defer func() { db = old }()

	conn, err := sql.Open("sqlite3", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	db = conn

	if err := createTables(); err != nil {
		t.Fatalf("createTables: %v", err)
	}
	migrateDB()
	oldAvatars := orgAvatars
	orgAvatars = newAvatarSet(t.TempDir(), "/api/v1/org-avatars/")
	defer func() { orgAvatars = oldAvatars }()

	links := filterChatLinks([]ChatLink{
		{Platform: "telegram", URL: "https://t.me/+xyz"},
		{Platform: "mailing_list", URL: "https://lists.example.org/postorius/lists/balfolk-koeln.example.org/"},
	})
	chatLinksJSON, _ := json.Marshal(links)

	o, err := scanOrg(db.QueryRow(
		"INSERT INTO organizations (name, chat_links) VALUES (?,?) RETURNING "+orgSelectCols,
		"Balfolk Köln", string(chatLinksJSON),
	))
	if err != nil {
		t.Fatalf("insert organization: %v", err)
	}
	if !reflect.DeepEqual(o.ChatLinks, links) {
		t.Fatalf("ChatLinks = %+v, want %+v", o.ChatLinks, links)
	}

	reloaded, err := scanOrg(db.QueryRow("SELECT " + orgSelectCols + " FROM organizations WHERE id=" + strconv.Itoa(o.ID)))
	if err != nil {
		t.Fatalf("reload organization: %v", err)
	}
	if !reflect.DeepEqual(reloaded.ChatLinks, links) {
		t.Fatalf("ChatLinks did not survive reload: %+v, want %+v", reloaded.ChatLinks, links)
	}
}
