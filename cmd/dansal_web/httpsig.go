package main

import (
	"context"
	"crypto"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/multiformats/go-multibase"
)

type pubKeyEntry struct {
	pem       string
	fetchedAt time.Time
}

var (
	pubKeyCache    = map[string]pubKeyEntry{}
	pubKeyCacheMu  sync.Mutex
	pubKeyCacheTTL = 10 * time.Minute
)

func fetchActorPublicKey(ctx context.Context, httpClient *http.Client, actorURL string) (string, error) {
	// actorURL comes from the keyId field in an HTTP Signature header — validate
	// it is a proper https URL before making an outbound request.
	if u, err := url.Parse(actorURL); err != nil || u.Scheme != "https" || u.Host == "" {
		return "", fmt.Errorf("actor URL must be https with a non-empty host: %q", actorURL)
	}
	pubKeyCacheMu.Lock()
	if e, ok := pubKeyCache[actorURL]; ok && time.Since(e.fetchedAt) < pubKeyCacheTTL {
		pubKeyCacheMu.Unlock()
		return e.pem, nil
	}
	pubKeyCacheMu.Unlock()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, actorURL, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("Accept", "application/activity+json")
	resp, err := httpClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	var actor struct {
		PublicKey struct {
			PublicKeyPem string `json:"publicKeyPem"`
		} `json:"publicKey"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&actor); err != nil {
		return "", fmt.Errorf("decode actor: %w", err)
	}
	if actor.PublicKey.PublicKeyPem == "" {
		return "", fmt.Errorf("no publicKeyPem in actor response")
	}

	pubKeyCacheMu.Lock()
	pubKeyCache[actorURL] = pubKeyEntry{pem: actor.PublicKey.PublicKeyPem, fetchedAt: time.Now()}
	pubKeyCacheMu.Unlock()

	return actor.PublicKey.PublicKeyPem, nil
}

func generateRSAKeyPair() (publicPEM, privatePEM string, err error) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		return "", "", err
	}
	privDER := x509.MarshalPKCS1PrivateKey(key)
	privatePEM = string(pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: privDER}))

	pubDER, err := x509.MarshalPKIXPublicKey(&key.PublicKey)
	if err != nil {
		return "", "", err
	}
	publicPEM = string(pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: pubDER}))
	return publicPEM, privatePEM, nil
}

// generateEd25519KeyPair generates an Ed25519 key pair and returns them as PEM-encoded strings
func generateEd25519KeyPair() (publicPEM, privatePEM string, err error) {
	publicKey, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return "", "", fmt.Errorf("failed to generate Ed25519 key pair: %w", err)
	}

	// Encode private key as PEM
	privDER, err := x509.MarshalPKCS8PrivateKey(privateKey)
	if err != nil {
		return "", "", fmt.Errorf("failed to marshal Ed25519 private key: %w", err)
	}
	privatePEM = string(pem.EncodeToMemory(&pem.Block{
		Type:  "ED25519 PRIVATE KEY",
		Bytes: privDER,
	}))

	// Encode public key as PEM
	pubDER, err := x509.MarshalPKIXPublicKey(publicKey)
	if err != nil {
		return "", "", fmt.Errorf("failed to marshal Ed25519 public key: %w", err)
	}
	publicPEM = string(pem.EncodeToMemory(&pem.Block{
		Type:  "PUBLIC KEY",
		Bytes: pubDER,
	}))

	return publicPEM, privatePEM, nil
}

// generateEd25519KeyPairWithMultibase generates an Ed25519 key pair and returns PEM-encoded strings plus multibase-encoded public key
func generateEd25519KeyPairWithMultibase() (publicPEM, privatePEM, multibaseKey string, err error) {
	publicKey, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return "", "", "", fmt.Errorf("failed to generate Ed25519 key pair: %w", err)
	}

	// Encode private key as PEM
	privDER, err := x509.MarshalPKCS8PrivateKey(privateKey)
	if err != nil {
		return "", "", "", fmt.Errorf("failed to marshal Ed25519 private key: %w", err)
	}
	privatePEM = string(pem.EncodeToMemory(&pem.Block{
		Type:  "ED25519 PRIVATE KEY",
		Bytes: privDER,
	}))

	// Encode public key as PEM
	pubDER, err := x509.MarshalPKIXPublicKey(publicKey)
	if err != nil {
		return "", "", "", fmt.Errorf("failed to marshal Ed25519 public key: %w", err)
	}
	publicPEM = string(pem.EncodeToMemory(&pem.Block{
		Type:  "PUBLIC KEY",
		Bytes: pubDER,
	}))

	// Encode public key in multibase format (base58btc)
	// Extract raw Ed25519 public key bytes
	var rawPublicKey [ed25519.PublicKeySize]byte
	copy(rawPublicKey[:], publicKey)
	
	// Encode in multibase with base58btc encoding
	multibaseKey, err = multibase.Encode(multibase.Base58BTC, rawPublicKey[:])
	if err != nil {
		return "", "", "", fmt.Errorf("failed to encode public key in multibase: %w", err)
	}

	return publicPEM, privatePEM, multibaseKey, nil
}

func parsePrivateKey(pemStr string) (*rsa.PrivateKey, error) {
	block, _ := pem.Decode([]byte(pemStr))
	if block == nil {
		return nil, fmt.Errorf("no PEM block found")
	}
	return x509.ParsePKCS1PrivateKey(block.Bytes)
}

func parsePublicKey(pemStr string) (*rsa.PublicKey, error) {
	block, _ := pem.Decode([]byte(pemStr))
	if block == nil {
		return nil, fmt.Errorf("no PEM block found")
	}
	pub, err := x509.ParsePKIXPublicKey(block.Bytes)
	if err != nil {
		return nil, err
	}
	rsaPub, ok := pub.(*rsa.PublicKey)
	if !ok {
		return nil, fmt.Errorf("not an RSA public key")
	}
	return rsaPub, nil
}

// SignRequest signs an outgoing HTTP POST request using HTTP Signatures (rsa-sha256).
// It sets Date, Digest, and Signature headers.
func SignRequest(r *http.Request, keyID, privateKeyPEM string, body []byte) error {
	privKey, err := parsePrivateKey(privateKeyPEM)
	if err != nil {
		return fmt.Errorf("parse private key: %w", err)
	}

	date := time.Now().UTC().Format(http.TimeFormat)
	r.Header.Set("Date", date)

	digest := sha256.Sum256(body)
	digestHeader := "SHA-256=" + base64.StdEncoding.EncodeToString(digest[:])
	r.Header.Set("Digest", digestHeader)

	host := r.URL.Host
	if host == "" {
		host = r.Host
	}

	requestTarget := strings.ToLower(r.Method) + " " + r.URL.RequestURI()
	signingString := fmt.Sprintf(
		"(request-target): %s\nhost: %s\ndate: %s\ndigest: %s",
		requestTarget, host, date, digestHeader,
	)

	h := sha256.New()
	h.Write([]byte(signingString))
	hashed := h.Sum(nil)

	sig, err := rsa.SignPKCS1v15(rand.Reader, privKey, crypto.SHA256, hashed)
	if err != nil {
		return fmt.Errorf("sign: %w", err)
	}

	sigB64 := base64.StdEncoding.EncodeToString(sig)
	sigHeader := fmt.Sprintf(
		`keyId="%s",algorithm="rsa-sha256",headers="(request-target) host date digest",signature="%s"`,
		keyID, sigB64,
	)
	r.Header.Set("Signature", sigHeader)
	return nil
}

// VerifyRequest verifies an incoming HTTP request's HTTP Signature against the provided public key PEM.
func VerifyRequest(r *http.Request, pubKeyPEM string) error {
	sigHeader := r.Header.Get("Signature")
	if sigHeader == "" {
		return fmt.Errorf("missing Signature header")
	}

	params := parseSignatureHeader(sigHeader)
	sigB64, ok := params["signature"]
	if !ok {
		return fmt.Errorf("missing signature in Signature header")
	}
	headersParam, ok := params["headers"]
	if !ok {
		headersParam = "date"
	}

	sig, err := base64.StdEncoding.DecodeString(sigB64)
	if err != nil {
		return fmt.Errorf("decode signature: %w", err)
	}

	headerNames := strings.Split(headersParam, " ")
	var parts []string
	for _, h := range headerNames {
		h = strings.TrimSpace(h)
		var val string
		switch h {
		case "(request-target)":
			parts = append(parts, fmt.Sprintf("(request-target): %s %s", strings.ToLower(r.Method), r.URL.RequestURI()))
			continue
		case "host":
			// Go's HTTP server moves Host out of r.Header into r.Host.
			val = r.Host
		default:
			val = r.Header.Get(http.CanonicalHeaderKey(h))
		}
		parts = append(parts, fmt.Sprintf("%s: %s", h, val))
	}
	signingString := strings.Join(parts, "\n")

	pubKey, err := parsePublicKey(pubKeyPEM)
	if err != nil {
		return fmt.Errorf("parse public key: %w", err)
	}

	h := sha256.New()
	h.Write([]byte(signingString))
	hashed := h.Sum(nil)

	return rsa.VerifyPKCS1v15(pubKey, crypto.SHA256, hashed, sig)
}

func parseSignatureHeader(header string) map[string]string {
	result := make(map[string]string)
	for _, part := range strings.Split(header, ",") {
		part = strings.TrimSpace(part)
		idx := strings.IndexByte(part, '=')
		if idx < 0 {
			continue
		}
		key := strings.TrimSpace(part[:idx])
		val := strings.TrimSpace(part[idx+1:])
		val = strings.Trim(val, `"`)
		result[key] = val
	}
	return result
}
