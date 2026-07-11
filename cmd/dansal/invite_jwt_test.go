package main

import (
	"path/filepath"
	"testing"
	"time"
)

func setupInviteJWTTest(t *testing.T) {
	t.Helper()
	prevConfig := config
	config = &Config{}
	config.Server.BaseURL = "https://dansal.example.com"
	config.Server.InviteSigningKeyPath = filepath.Join(t.TempDir(), "invite_signing_key.pem")
	t.Cleanup(func() { config = prevConfig })

	if err := loadOrGenerateInviteSigningKey(); err != nil {
		t.Fatalf("loadOrGenerateInviteSigningKey: %v", err)
	}
}

func TestInviteJWTRoundTrip(t *testing.T) {
	setupInviteJWTTest(t)

	orgID := 42
	token, err := signInviteJWT(RolePublisher, &orgID, inviteTokenType(RolePublisher), time.Now().Add(30*time.Minute))
	if err != nil {
		t.Fatalf("signInviteJWT: %v", err)
	}

	claims, err := verifyInviteJWT(token, inviteTokenType(RolePublisher))
	if err != nil {
		t.Fatalf("verifyInviteJWT: %v", err)
	}
	if claims.OrgID == nil || *claims.OrgID != orgID {
		t.Errorf("claims.OrgID = %v, want %d", claims.OrgID, orgID)
	}
}

func TestInviteJWTRejectsWrongTokenType(t *testing.T) {
	setupInviteJWTTest(t)

	orgID := 1
	token, err := signInviteJWT(RolePublisher, &orgID, inviteTokenType(RolePublisher), time.Now().Add(30*time.Minute))
	if err != nil {
		t.Fatalf("signInviteJWT: %v", err)
	}
	if _, err := verifyInviteJWT(token, inviteTokenType(RoleUser)); err == nil {
		t.Error("verifyInviteJWT should reject a token presented for the wrong token_type")
	}
}

func TestInviteJWTRejectsExpired(t *testing.T) {
	setupInviteJWTTest(t)

	orgID := 1
	token, err := signInviteJWT(RolePublisher, &orgID, inviteTokenType(RolePublisher), time.Now().Add(-time.Minute))
	if err != nil {
		t.Fatalf("signInviteJWT: %v", err)
	}
	if _, err := verifyInviteJWT(token, inviteTokenType(RolePublisher)); err == nil {
		t.Error("verifyInviteJWT should reject an expired token")
	}
}

func TestInviteJWKSContainsCurrentKey(t *testing.T) {
	setupInviteJWTTest(t)

	jwks := inviteJWKS()
	keys, ok := jwks["keys"].([]map[string]any)
	if !ok || len(keys) != 1 {
		t.Fatalf("inviteJWKS() = %v, want one key", jwks)
	}
	if keys[0]["kid"] != inviteSigningKeyID {
		t.Errorf("jwks kid = %v, want %v", keys[0]["kid"], inviteSigningKeyID)
	}
}
