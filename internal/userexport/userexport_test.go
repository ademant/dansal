package userexport

import (
	"database/sql"
	"testing"

	_ "github.com/mattn/go-sqlite3"
)

// TestExport mirrors cmd/dansal's TestDeleteUserByIDKeepsContentAndClearsReferences
// (delete_user_test.go) — same schema shape, same foreign-key discovery
// mechanism, opposite direction (read instead of null/delete). The
// "widgets" table stands in for any future table referencing users(id):
// Export must pick it up with no code change, exactly like deleteUserByID
// does for deletion.
func TestExport(t *testing.T) {
	conn, err := sql.Open("sqlite3", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	conn.SetMaxOpenConns(1)
	t.Cleanup(func() { conn.Close() })

	schema := `
		CREATE TABLE users (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			email TEXT,
			password_hash TEXT NOT NULL DEFAULT ''
		);
		CREATE TABLE events (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			title TEXT NOT NULL,
			created_by_id INTEGER REFERENCES users(id),
			changed_by_id INTEGER REFERENCES users(id)
		);
		CREATE TABLE api_keys (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			user_id INTEGER NOT NULL,
			name TEXT NOT NULL,
			api_key TEXT NOT NULL,
			FOREIGN KEY (user_id) REFERENCES users(id) ON DELETE CASCADE
		);
		CREATE TABLE widgets (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			user_id INTEGER NOT NULL REFERENCES users(id),
			label TEXT NOT NULL
		);
	`
	if _, err := conn.Exec(schema); err != nil {
		t.Fatalf("schema: %v", err)
	}

	res, err := conn.Exec("INSERT INTO users (email, password_hash) VALUES ('a@example.test', 'secret-hash')")
	if err != nil {
		t.Fatalf("insert user: %v", err)
	}
	uid, _ := res.LastInsertId()
	if _, err := conn.Exec(`INSERT INTO events (title, created_by_id, changed_by_id) VALUES ('E', ?, ?)`, uid, uid); err != nil {
		t.Fatalf("insert event: %v", err)
	}
	if _, err := conn.Exec(`INSERT INTO api_keys (user_id, name, api_key) VALUES (?, 'my key', 'sk-raw-value')`, uid); err != nil {
		t.Fatalf("insert api key: %v", err)
	}
	if _, err := conn.Exec(`INSERT INTO widgets (user_id, label) VALUES (?, 'W')`, uid); err != nil {
		t.Fatalf("insert widget: %v", err)
	}

	result, err := Export(conn, uid)
	if err != nil {
		t.Fatalf("Export: %v", err)
	}

	account, ok := result["account"].(map[string]any)
	if !ok {
		t.Fatalf("missing account row: %#v", result)
	}
	if account["email"] != "a@example.test" {
		t.Errorf("account email = %v, want a@example.test", account["email"])
	}
	if _, present := account["password_hash"]; present {
		t.Error("password_hash must be redacted from the account row")
	}

	for _, key := range []string{"events.created_by_id", "events.changed_by_id"} {
		rows, ok := result[key].([]map[string]any)
		if !ok || len(rows) != 1 || rows[0]["title"] != "E" {
			t.Errorf("%s = %#v, want one row titled E", key, result[key])
		}
	}

	apiKeyRows, ok := result["api_keys.user_id"].([]map[string]any)
	if !ok || len(apiKeyRows) != 1 {
		t.Fatalf("api_keys.user_id = %#v, want one row", result["api_keys.user_id"])
	}
	if apiKeyRows[0]["name"] != "my key" {
		t.Errorf("api key name = %v, want 'my key'", apiKeyRows[0]["name"])
	}
	if _, present := apiKeyRows[0]["api_key"]; present {
		t.Error("api_key value must be redacted")
	}

	widgetRows, ok := result["widgets.user_id"].([]map[string]any)
	if !ok || len(widgetRows) != 1 || widgetRows[0]["label"] != "W" {
		t.Errorf("widgets.user_id (undeclared-table discovery) = %#v, want one row labelled W", result["widgets.user_id"])
	}
}
