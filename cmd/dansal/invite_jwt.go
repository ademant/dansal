package main

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/pem"
	"errors"
	"fmt"
	"log"
	"os"
	"strings"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"
)

// Invite links are signed as JWTs so that clients (e.g. the WordPress
// plugin) can validate authenticity, expiry, and audience before ever
// making a redemption request to the server — see issue #769.
//
// The signed JWT string itself is stored as invite_links.token (unchanged
// column, now holding a longer value), so the existing used_at/expires_at
// bookkeeping in loadValidInvite continues to provide one-time-use
// enforcement; JWT verification is an additional integrity/authenticity
// check layered on top, and the same public key is published so clients
// can verify independently.

var (
	inviteSigningKey   *ecdsa.PrivateKey
	inviteSigningKeyID string
	errInviteBadToken  = errors.New("invite token failed signature or claims validation")
)

// inviteJWTClaims is the payload of an invite-link JWT.
type inviteJWTClaims struct {
	jwt.RegisteredClaims
	OrgID     *int   `json:"org_id,omitempty"`
	TokenType string `json:"token_type"`
}

// loadOrGenerateInviteSigningKey loads the ECDSA P-256 key pair used to
// sign invite JWTs from config.Server.InviteSigningKeyPath, generating and
// persisting a new one on first run. Must be called once at startup before
// any invite tokens are issued or verified.
func loadOrGenerateInviteSigningKey() error {
	path := config.Server.InviteSigningKeyPath

	if data, err := os.ReadFile(path); err == nil {
		block, _ := pem.Decode(data)
		if block == nil {
			return fmt.Errorf("invite signing key %s: not a valid PEM file", path)
		}
		key, err := x509.ParseECPrivateKey(block.Bytes)
		if err != nil {
			return fmt.Errorf("invite signing key %s: %w", path, err)
		}
		inviteSigningKey = key
		inviteSigningKeyID = inviteKeyID(&key.PublicKey)
		return nil
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("invite signing key %s: %w", path, err)
	}

	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return fmt.Errorf("generating invite signing key: %w", err)
	}
	der, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		return fmt.Errorf("marshalling invite signing key: %w", err)
	}
	block := &pem.Block{Type: "EC PRIVATE KEY", Bytes: der}
	if err := os.WriteFile(path, pem.EncodeToMemory(block), 0o600); err != nil {
		return fmt.Errorf("writing invite signing key %s: %w", path, err)
	}
	log.Printf("invite: generated new signing key at %s", path)

	inviteSigningKey = key
	inviteSigningKeyID = inviteKeyID(&key.PublicKey)
	return nil
}

// inviteKeyID derives a stable, non-secret identifier for a public key so
// JWKS consumers and JWT "kid" headers can refer to it consistently.
func inviteKeyID(pub *ecdsa.PublicKey) string {
	byteLen := (pub.Curve.Params().BitSize + 7) / 8
	sum := sha256.New()
	sum.Write(pub.X.FillBytes(make([]byte, byteLen)))
	sum.Write(pub.Y.FillBytes(make([]byte, byteLen)))
	return base64.RawURLEncoding.EncodeToString(sum.Sum(nil)[:8])
}

// signInviteJWT creates a compact ES256-signed JWT for an invite link.
// tokenType is e.g. "user_invite" or "publisher_invite".
func signInviteJWT(role string, orgID *int, tokenType string, expiresAt time.Time) (string, error) {
	now := time.Now().UTC()
	claims := inviteJWTClaims{
		RegisteredClaims: jwt.RegisteredClaims{
			Issuer:    strings.TrimRight(config.Server.BaseURL, "/"),
			Subject:   "invite",
			Audience:  jwt.ClaimStrings{role},
			ExpiresAt: jwt.NewNumericDate(expiresAt),
			NotBefore: jwt.NewNumericDate(now),
			IssuedAt:  jwt.NewNumericDate(now),
			ID:        uuid.NewString(),
		},
		OrgID:     orgID,
		TokenType: tokenType,
	}
	token := jwt.NewWithClaims(jwt.SigningMethodES256, claims)
	token.Header["kid"] = inviteSigningKeyID
	return token.SignedString(inviteSigningKey)
}

// verifyInviteJWT parses and validates the signature and standard claims of
// an invite JWT, and additionally checks that its token_type matches what
// the caller expects (e.g. "publisher_invite" for the publisher redemption
// endpoint). It does not consult the database — callers still rely on
// loadValidInvite's used_at/expires_at check as the source of truth for
// one-time-use.
func verifyInviteJWT(tokenString, wantTokenType string) (*inviteJWTClaims, error) {
	claims := &inviteJWTClaims{}
	parsed, err := jwt.ParseWithClaims(tokenString, claims, func(t *jwt.Token) (any, error) {
		if _, ok := t.Method.(*jwt.SigningMethodECDSA); !ok {
			return nil, fmt.Errorf("unexpected signing method %v", t.Header["alg"])
		}
		return &inviteSigningKey.PublicKey, nil
	}, jwt.WithIssuer(strings.TrimRight(config.Server.BaseURL, "/")))
	if err != nil || !parsed.Valid {
		return nil, errInviteBadToken
	}
	if claims.TokenType != wantTokenType {
		return nil, errInviteBadToken
	}
	return claims, nil
}

// inviteJWKS returns the JSON Web Key Set for the current invite signing
// key, published at GET /.well-known/jwks.json so clients can validate
// invite JWTs without calling the server.
func inviteJWKS() map[string]any {
	pub := inviteSigningKey.PublicKey
	byteLen := (pub.Curve.Params().BitSize + 7) / 8
	xBytes := pub.X.FillBytes(make([]byte, byteLen))
	yBytes := pub.Y.FillBytes(make([]byte, byteLen))
	return map[string]any{
		"keys": []map[string]any{
			{
				"kty": "EC",
				"crv": "P-256",
				"alg": "ES256",
				"use": "sig",
				"kid": inviteSigningKeyID,
				"x":   base64.RawURLEncoding.EncodeToString(xBytes),
				"y":   base64.RawURLEncoding.EncodeToString(yBytes),
			},
		},
	}
}
