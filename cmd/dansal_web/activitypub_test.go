package main

import (
	"database/sql"
	"testing"
	"time"

	_ "github.com/mattn/go-sqlite3"
)

func TestActivityPubEnhancements(t *testing.T) {
	// Create in-memory database for testing
	db, err := sql.Open("sqlite3", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	// Create tables
	_, err = db.Exec(`
		CREATE TABLE actors (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			org_id INTEGER,
			org_slug TEXT UNIQUE,
			public_key_pem TEXT,
			private_key_pem TEXT,
			public_key_ed25519_pem TEXT,
			private_key_ed25519_pem TEXT,
			public_key_multibase TEXT
		);
		
		CREATE TABLE federated_events (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			ap_id TEXT UNIQUE,
			actor_id TEXT,
			name TEXT,
			start_time TEXT,
			end_time TEXT,
			url TEXT,
			location_name TEXT,
			description TEXT,
			image_url TEXT,
			tags TEXT,
			raw_json TEXT,
			received_at INTEGER
		);
		
		CREATE TABLE rsvps (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			ap_id TEXT UNIQUE,
			event_ap_id TEXT,
			actor_id TEXT,
			rsvp_type TEXT,
			status TEXT,
			created_at DATETIME,
			updated_at DATETIME
		);
		
		CREATE TABLE interactions (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			ap_id TEXT UNIQUE,
			target_type TEXT,
			target_id TEXT,
			actor_id TEXT,
			interaction_type TEXT,
			content TEXT,
			created_at DATETIME
		);
		
		CREATE TABLE location_actors (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			slug TEXT UNIQUE,
			name TEXT,
			description TEXT,
			address TEXT,
			latitude REAL,
			longitude REAL,
			public_key_pem TEXT,
			private_key_pem TEXT,
			public_key_ed25519_pem TEXT,
			private_key_ed25519_pem TEXT,
			public_key_multibase TEXT,
			created_at DATETIME
		);
		
		CREATE TABLE musician_actors (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			slug TEXT UNIQUE,
			name TEXT,
			musicbrainz_id TEXT,
			description TEXT,
			image_url TEXT,
			public_key_pem TEXT,
			private_key_pem TEXT,
			public_key_ed25519_pem TEXT,
			private_key_ed25519_pem TEXT,
			public_key_multibase TEXT,
			created_at DATETIME
		);
		
		CREATE TABLE webfinger_aliases (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			alias TEXT UNIQUE,
			target_type TEXT,
			target_id INTEGER,
			created_at DATETIME
		);
		
		CREATE TABLE event_updates (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			event_ap_id TEXT,
			update_type TEXT,
			update_ap_id TEXT,
			updated_at DATETIME
		);
	`)
	if err != nil {
		t.Fatal(err)
	}

	// Test data
	cfg := &Config{
		Domain: "example.com",
	}
	_ = cfg // Use cfg to avoid unused variable error

	// Test RSVP creation
	rsvp := RSVP{
		APID:      "https://example.com/rsvps/123",
		EventAPID: "https://example.com/events/1",
		ActorID:   "https://example.com/users/1",
		RSVPType:  "Yes",
		Status:    "pending",
		CreatedAt: time.Now(),
		UpdatedAt: time.Now(),
	}

	err = createRSVP(db, rsvp)
	if err != nil {
		t.Errorf("Failed to create RSVP: %v", err)
	}

	// Test RSVP retrieval
	retrievedRSVP, err := getRSVP(db, rsvp.APID)
	if err != nil {
		t.Errorf("Failed to retrieve RSVP: %v", err)
	}
	if retrievedRSVP.RSVPType != "Yes" {
		t.Errorf("RSVP type mismatch: got %s, want Yes", retrievedRSVP.RSVPType)
	}

	// Test interaction creation
	interaction := Interaction{
		APID:           "https://example.com/interactions/123",
		TargetType:     "Event",
		TargetID:       "https://example.com/events/1",
		ActorID:        "https://example.com/users/1",
		InteractionType: "Like",
		Content:        "",
		CreatedAt:      time.Now(),
	}

	err = createInteraction(db, interaction)
	if err != nil {
		t.Errorf("Failed to create interaction: %v", err)
	}

	// Test interaction retrieval
	interactions, err := listInteractionsByTarget(db, interaction.TargetID)
	if err != nil {
		t.Errorf("Failed to list interactions: %v", err)
	}
	if len(interactions) != 1 {
		t.Errorf("Expected 1 interaction, got %d", len(interactions))
	}

	// Test location actor creation
	locationActor := LocationActor{
		Slug:         "test-location",
		Name:         "Test Location",
		Description:  "A test location",
		Address:      "123 Test St",
		PublicKeyPEM: "test-key",
		PrivateKeyPEM: "test-private-key",
	}

	err = createLocationActor(db, locationActor)
	if err != nil {
		t.Errorf("Failed to create location actor: %v", err)
	}

	// Test location actor retrieval
	retrievedLocation, err := getLocationActorBySlug(db, "test-location")
	if err != nil {
		t.Errorf("Failed to retrieve location actor: %v", err)
	}
	if retrievedLocation.Name != "Test Location" {
		t.Errorf("Location name mismatch: got %s, want Test Location", retrievedLocation.Name)
	}

	// Test musician actor creation
	musicianActor := MusicianActor{
		Slug:          "test-musician",
		Name:          "Test Musician",
		MusicBrainzID: "12345678-1234-1234-1234-123456789012",
		Description:   "A test musician",
		PublicKeyPEM:  "test-key",
		PrivateKeyPEM: "test-private-key",
	}

	err = createMusicianActor(db, musicianActor)
	if err != nil {
		t.Errorf("Failed to create musician actor: %v", err)
	}

	// Test musician actor retrieval
	retrievedMusician, err := getMusicianActorBySlug(db, "test-musician")
	if err != nil {
		t.Errorf("Failed to retrieve musician actor: %v", err)
	}
	if retrievedMusician.Name != "Test Musician" {
		t.Errorf("Musician name mismatch: got %s, want Test Musician", retrievedMusician.Name)
	}

	// Test WebFinger alias creation
	alias := WebFingerAlias{
		Alias:      "test-alias",
		TargetType: "organization",
		TargetID:   1,
		CreatedAt:  time.Now(),
	}

	err = createWebFingerAlias(db, alias)
	if err != nil {
		t.Errorf("Failed to create WebFinger alias: %v", err)
	}

	// Test WebFinger alias retrieval
	retrievedAlias, err := getWebFingerAlias(db, "test-alias")
	if err != nil {
		t.Errorf("Failed to retrieve WebFinger alias: %v", err)
	}
	if retrievedAlias.Alias != "test-alias" {
		t.Errorf("Alias mismatch: got %s, want test-alias", retrievedAlias.Alias)
	}

	// Test event update creation
	update := EventUpdate{
		EventAPID: "https://example.com/events/1",
		UpdateType: "Update",
		UpdateAPID: "https://example.com/updates/123",
		UpdatedAt: time.Now(),
	}

	err = createEventUpdate(db, update)
	if err != nil {
		t.Errorf("Failed to create event update: %v", err)
	}

	// Test event update retrieval
	updates, err := listEventUpdates(db, update.EventAPID)
	if err != nil {
		t.Errorf("Failed to list event updates: %v", err)
	}
	if len(updates) != 1 {
		t.Errorf("Expected 1 update, got %d", len(updates))
	}

	t.Log("All ActivityPub enhancement tests passed!")
}

func TestAPEventBuilding(t *testing.T) {
	cfg := &Config{
		Domain: "example.com",
	}

	e := Event{
		ID:          1,
		Title:       "Test Event",
		Description: "A test event",
		StartTime:   "2023-01-01T12:00:00Z",
		EndTime:     "2023-01-01T14:00:00Z",
		URL:         "https://example.com/events/1",
		Location:    "Test Location",
		ImageURL:    "https://example.com/image.jpg",
		Tags:        []string{"test", "event"},
	}

	apEvent := buildAPEvent(cfg, "test-org", e)

	// Test basic fields
	_ = cfg // Use the cfg variable to avoid unused variable error
	if apEvent.Name != "Test Event" {
		t.Errorf("Event name mismatch: got %s, want Test Event", apEvent.Name)
	}

	if apEvent.StartTime != "2023-01-01T12:00:00Z" {
		t.Errorf("Start time mismatch: got %s, want 2023-01-01T12:00:00Z", apEvent.StartTime)
	}

	// Test duration calculation
	if apEvent.Duration != "PT2H0M" {
		t.Errorf("Duration mismatch: got %s, want PT2H0M", apEvent.Duration)
	}

	// Test categories
	if len(apEvent.Category) != 2 {
		t.Errorf("Expected 2 categories, got %d", len(apEvent.Category))
	}

	// Test icon
	if apEvent.Icon == nil {
		t.Error("Expected event icon to be set")
	} else if apEvent.Icon.URL != "https://example.com/image.jpg" {
		t.Errorf("Icon URL mismatch: got %s, want https://example.com/image.jpg", apEvent.Icon.URL)
	}

	t.Log("AP event building test passed!")
}