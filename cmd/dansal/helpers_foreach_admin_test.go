package main

import (
	"testing"
	"time"
)

// TestForEachAdminCallbackMayUseTheDatabase covers #1736: forEachAdmin used to
// keep its users result set open while the callback ran, so a callback that
// needed a second connection (sendMatrixMessage does) blocked forever once the
// pool was full. With a pool of one connection, the callback must still finish.
func TestForEachAdminCallbackMayUseTheDatabase(t *testing.T) {
	conn := setupDedupTestDB(t)
	for _, email := range []string{"a@example.test", "b@example.test"} {
		if _, err := conn.Exec(`INSERT INTO users (email, role) VALUES (?, 'admin')`, email); err != nil {
			t.Fatal(err)
		}
	}
	conn.SetMaxOpenConns(1)

	done := make(chan int)
	go func() {
		calls := 0
		forEachAdmin(func(id int, email, chatID, matrixID string, matrixVerified bool) {
			var n int
			if err := conn.QueryRow(`SELECT COUNT(*) FROM users`).Scan(&n); err != nil {
				t.Errorf("query inside callback: %v", err)
			}
			calls++
		})
		done <- calls
	}()

	select {
	case calls := <-done:
		if calls != 2 {
			t.Fatalf("callback ran %d times, want 2", calls)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("forEachAdmin blocked: the callback needed a connection the open result set held")
	}
}
