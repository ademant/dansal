package main

import (
	"database/sql"
	"encoding/json"
	"strings"
	"testing"

	_ "github.com/mattn/go-sqlite3"
)

func testCfg() *Config {
	return &Config{Domain: "example.com", RelayActorName: "relay"}
}

func intPtr(i int) *int { return &i }

func testEvent() Event {
	return Event{ID: 42, Title: "Summer Ball", OrganizationID: intPtr(7), IsPublished: true}
}

func decodeAPEvent(t *testing.T, obj any) APEvent {
	t.Helper()
	b, err := json.Marshal(obj)
	if err != nil {
		t.Fatalf("marshal Object: %v", err)
	}
	var apev APEvent
	if err := json.Unmarshal(b, &apev); err != nil {
		t.Fatalf("unmarshal APEvent: %v", err)
	}
	return apev
}

func decodeActivity(t *testing.T, obj any) Activity {
	t.Helper()
	b, err := json.Marshal(obj)
	if err != nil {
		t.Fatalf("marshal Object: %v", err)
	}
	var act Activity
	if err := json.Unmarshal(b, &act); err != nil {
		t.Fatalf("unmarshal Activity: %v", err)
	}
	return act
}

func TestBuildCreateActivity_OrgAttribution(t *testing.T) {
	cfg := testCfg()
	act := buildCreateActivity(cfg, "myorg", testEvent())

	if act.Type != "Create" {
		t.Errorf("Type = %q, want Create", act.Type)
	}
	wantActor := "https://example.com/org/myorg"
	if act.Actor != wantActor {
		t.Errorf("Actor = %q, want %q", act.Actor, wantActor)
	}

	apev := decodeAPEvent(t, act.Object)
	if apev.AttributedTo != wantActor {
		t.Errorf("AttributedTo = %q, want %q", apev.AttributedTo, wantActor)
	}
	if apev.ID != "https://example.com/events/42" {
		t.Errorf("event ID = %q, want https://example.com/events/42", apev.ID)
	}
}

func TestBuildAnnounceActivity_OrgAttributionPreserved(t *testing.T) {
	cfg := testCfg()
	act := buildAnnounceActivity(cfg, "relay", "myorg", testEvent())

	if act.Type != "Announce" {
		t.Errorf("Type = %q, want Announce", act.Type)
	}
	wantRelayBase := "https://example.com/org/relay"
	if act.Actor != wantRelayBase {
		t.Errorf("Announce Actor = %q, want %q", act.Actor, wantRelayBase)
	}
	if !strings.HasPrefix(act.ID, wantRelayBase) {
		t.Errorf("Announce ID %q should start with relay base URL", act.ID)
	}

	// Inner object must be a Create attributed to the org, not the relay.
	inner := decodeActivity(t, act.Object)
	wantOrgBase := "https://example.com/org/myorg"
	if inner.Actor != wantOrgBase {
		t.Errorf("inner Create Actor = %q, want %q", inner.Actor, wantOrgBase)
	}

	apev := decodeAPEvent(t, inner.Object)
	if apev.AttributedTo != wantOrgBase {
		t.Errorf("inner APEvent AttributedTo = %q, want %q", apev.AttributedTo, wantOrgBase)
	}
	if strings.Contains(apev.AttributedTo, "relay") {
		t.Errorf("inner APEvent AttributedTo contains 'relay', should not: %q", apev.AttributedTo)
	}
}

func TestBuildUpdateActivity_StableEventID(t *testing.T) {
	cfg := testCfg()
	create := buildCreateActivity(cfg, "myorg", testEvent())
	update := buildUpdateActivity(cfg, "myorg", testEvent())

	if update.Type != "Update" {
		t.Errorf("Type = %q, want Update", update.Type)
	}

	// The event object ID must stay the same across Create and Update.
	createObj := decodeAPEvent(t, create.Object)
	updateObj := decodeAPEvent(t, update.Object)
	if createObj.ID != updateObj.ID {
		t.Errorf("event ID changed: Create=%q Update=%q", createObj.ID, updateObj.ID)
	}

	// But the activity ID itself must differ.
	if update.ID == create.ID {
		t.Errorf("Update activity ID should differ from Create activity ID: %q", update.ID)
	}

	// Updated timestamp must be set.
	if updateObj.Updated == "" {
		t.Error("Update APEvent.Updated should be set")
	}
}

func TestBuildDeleteActivity_CanonicalEventID(t *testing.T) {
	cfg := testCfg()
	act := buildDeleteActivity(cfg, "myorg", 42)

	if act.Type != "Delete" {
		t.Errorf("Type = %q, want Delete", act.Type)
	}
	wantEventID := "https://example.com/events/42"
	obj, ok := act.Object.(string)
	if !ok {
		t.Fatalf("Delete Object is not a string, got %T", act.Object)
	}
	if obj != wantEventID {
		t.Errorf("Delete Object = %q, want %q", obj, wantEventID)
	}
}

func TestDeliveredMigration_Idempotent(t *testing.T) {
	// Simulate an old-schema DB with only the original 3 columns.
	db, err := sql.Open("sqlite3", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	db.Exec(`CREATE TABLE delivered (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		event_id INTEGER NOT NULL,
		org_id INTEGER NOT NULL,
		delivered_at DATETIME DEFAULT CURRENT_TIMESTAMP,
		UNIQUE(event_id, org_id)
	)`)
	db.Exec(`INSERT INTO delivered (event_id, org_id) VALUES (1, 10), (2, 10)`)

	// Run the idempotent ALTER TABLE migrations (as initDB does).
	db.Exec("ALTER TABLE delivered ADD COLUMN transferred_to INTEGER")
	db.Exec("ALTER TABLE delivered ADD COLUMN transferred_at DATETIME")
	db.Exec("ALTER TABLE delivered ADD COLUMN is_update INTEGER DEFAULT 0")

	// Write transfer data to the new columns.
	db.Exec(`UPDATE delivered SET transferred_to=20 WHERE event_id=1`)

	// Second startup: ALTER TABLE ADD COLUMN on an existing column is a no-op in SQLite.
	db.Exec("ALTER TABLE delivered ADD COLUMN transferred_to INTEGER")
	db.Exec("ALTER TABLE delivered ADD COLUMN transferred_at DATETIME")
	db.Exec("ALTER TABLE delivered ADD COLUMN is_update INTEGER DEFAULT 0")

	// Data must be intact.
	var transferredTo sql.NullInt64
	if err := db.QueryRow(`SELECT transferred_to FROM delivered WHERE event_id=1`).Scan(&transferredTo); err != nil {
		t.Fatalf("query after second migration: %v", err)
	}
	if !transferredTo.Valid || transferredTo.Int64 != 20 {
		t.Errorf("transferred_to = %v, want 20", transferredTo)
	}

	var count int
	db.QueryRow(`SELECT COUNT(*) FROM delivered`).Scan(&count)
	if count != 2 {
		t.Errorf("row count = %d, want 2", count)
	}
}
