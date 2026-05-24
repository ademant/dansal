package main

import (
	"encoding/json"
	"testing"

	"github.com/multiformats/go-multibase"
)

func TestActorDocumentStructure(t *testing.T) {
	// Test that our actor generation produces valid JSON with both key formats
	actor := Actor{
		Context: "https://www.w3.org/ns/activitystreams",
		Type: "Person",
		ID: "https://example.com/actor",
		PublicKey: PublicKey{
			ID:              "https://example.com/actor#main-key",
			Owner:           "https://example.com/actor",
			PublicKeyPem:    "-----BEGIN PUBLIC KEY-----\nMFkwEwYHKoZIzj0CAQYIKoZIzj0DAQcDQgAEXY...",
			PublicKeyMultibase: "z6MkqRYqQiSgvZQdnBytw86U75kYv3ZtPJmzP8C2WY41UvN3uQ3FxQKKcmcwFxYV48p",
		},
	}

	// Marshal to JSON
	jsonData, err := json.MarshalIndent(actor, "", "  ")
	if err != nil {
		t.Fatalf("Failed to marshal actor to JSON: %v", err)
	}

	// Verify it contains both key formats
	jsonStr := string(jsonData)
	if !contains(jsonStr, "publicKeyPem") {
		t.Error("JSON missing publicKeyPem field")
	}
	if !contains(jsonStr, "publicKeyMultibase") {
		t.Error("JSON missing publicKeyMultibase field")
	}

	// Verify the multibase format is valid
	if actor.PublicKey.PublicKeyMultibase != "" {
		_, _, err := multibase.Decode(actor.PublicKey.PublicKeyMultibase)
		if err != nil {
			t.Errorf("Invalid multibase encoding: %v", err)
		}
	}

	t.Logf("Actor JSON:\n%s", jsonStr)
}

func contains(s, substr string) bool {
	return len(s) >= len(substr) && (s == substr || len(s) > len(substr) && (s[:len(substr)] == substr || s[len(s)-len(substr):] == substr || containsSubstring(s, substr)))
}

func containsSubstring(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}
