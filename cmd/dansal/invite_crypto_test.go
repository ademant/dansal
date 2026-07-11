package main

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha1"
	"crypto/x509"
	"encoding/base64"
	"encoding/pem"
	"testing"
)

func genTestRSAPubkeyPEM(t *testing.T, bits int) string {
	t.Helper()
	priv, err := rsa.GenerateKey(rand.Reader, bits)
	if err != nil {
		t.Fatalf("rsa.GenerateKey: %v", err)
	}
	der, err := x509.MarshalPKIXPublicKey(&priv.PublicKey)
	if err != nil {
		t.Fatalf("MarshalPKIXPublicKey: %v", err)
	}
	return string(pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: der}))
}

func TestEncryptAPIKeyForClientRoundTrip(t *testing.T) {
	priv, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("rsa.GenerateKey: %v", err)
	}
	der, err := x509.MarshalPKIXPublicKey(&priv.PublicKey)
	if err != nil {
		t.Fatalf("MarshalPKIXPublicKey: %v", err)
	}
	pemStr := string(pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: der}))

	ciphertextB64, algorithm, err := encryptAPIKeyForClient(pemStr, "ak_secret123")
	if err != nil {
		t.Fatalf("encryptAPIKeyForClient: %v", err)
	}
	if algorithm != "RSA-OAEP-SHA1" {
		t.Errorf("algorithm = %q, want RSA-OAEP-SHA1", algorithm)
	}

	ciphertext, err := decodeAndDecrypt(t, priv, ciphertextB64)
	if err != nil {
		t.Fatalf("decrypt: %v", err)
	}
	if ciphertext != "ak_secret123" {
		t.Errorf("decrypted = %q, want ak_secret123", ciphertext)
	}
}

func decodeAndDecrypt(t *testing.T, priv *rsa.PrivateKey, ciphertextB64 string) (string, error) {
	t.Helper()
	data, err := base64.StdEncoding.DecodeString(ciphertextB64)
	if err != nil {
		return "", err
	}
	plaintext, err := rsa.DecryptOAEP(sha1.New(), rand.Reader, priv, data, nil)
	if err != nil {
		return "", err
	}
	return string(plaintext), nil
}

func TestParseClientRSAPubkeyRejectsSmallKey(t *testing.T) {
	pemStr := genTestRSAPubkeyPEM(t, 1024)
	if _, err := parseClientRSAPubkey(pemStr); err != errInvalidClientPubkey {
		t.Errorf("parseClientRSAPubkey(1024-bit) error = %v, want errInvalidClientPubkey", err)
	}
}

func TestParseClientRSAPubkeyRejectsGarbage(t *testing.T) {
	if _, err := parseClientRSAPubkey("not a pem key"); err != errInvalidClientPubkey {
		t.Errorf("parseClientRSAPubkey(garbage) error = %v, want errInvalidClientPubkey", err)
	}
}
