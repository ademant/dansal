package main

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha1"
	"crypto/x509"
	"encoding/base64"
	"encoding/pem"
	"errors"
)

// errInvalidClientPubkey is returned by encryptAPIKeyForClient when the
// caller-supplied client_pubkey (see #770) is malformed, the wrong type, or
// too small to be trusted.
var errInvalidClientPubkey = errors.New("client_pubkey must be a PEM-encoded RSA public key of at least 2048 bits")

const minClientPubkeyBits = 2048

// parseClientRSAPubkey parses a PEM-encoded SubjectPublicKeyInfo block
// (e.g. "-----BEGIN PUBLIC KEY-----...") and returns the contained RSA
// public key. Only RSA is supported for now — EC (ECIES) is deferred, see
// issue #770.
func parseClientRSAPubkey(pemStr string) (*rsa.PublicKey, error) {
	block, _ := pem.Decode([]byte(pemStr))
	if block == nil {
		return nil, errInvalidClientPubkey
	}
	pub, err := x509.ParsePKIXPublicKey(block.Bytes)
	if err != nil {
		return nil, errInvalidClientPubkey
	}
	rsaPub, ok := pub.(*rsa.PublicKey)
	if !ok {
		return nil, errInvalidClientPubkey
	}
	if rsaPub.N.BitLen() < minClientPubkeyBits {
		return nil, errInvalidClientPubkey
	}
	return rsaPub, nil
}

// encryptAPIKeyForClient encrypts apiKey with the client's RSA public key
// using RSA-OAEP-SHA1, so the API key returned by invite redemption is
// end-to-end encrypted (defense-in-depth on top of TLS — see #770). SHA-1 is
// used instead of SHA-256 because PHP's openssl_private_decrypt/
// openssl_public_encrypt hardcode SHA-1 for OAEP with no way to select a
// different hash — SHA-256 ciphertext is rejected outright by wp-dansal, the
// only current client of this feature (see #773). Returns the base64-encoded
// ciphertext and the algorithm identifier for the response, or
// errInvalidClientPubkey if pemStr doesn't parse.
func encryptAPIKeyForClient(pemStr, apiKey string) (ciphertextB64, algorithm string, err error) {
	pub, err := parseClientRSAPubkey(pemStr)
	if err != nil {
		return "", "", err
	}
	ciphertext, err := rsa.EncryptOAEP(sha1.New(), rand.Reader, pub, []byte(apiKey), nil)
	if err != nil {
		return "", "", err
	}
	return base64.StdEncoding.EncodeToString(ciphertext), "RSA-OAEP-SHA1", nil
}
