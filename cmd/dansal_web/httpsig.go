package main

import (
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

type pubKeyEntry struct {
	pem       string
	owner     string
	fetchedAt time.Time
}

var (
	pubKeyCache    = map[string]pubKeyEntry{}
	pubKeyCacheMu  sync.Mutex
	pubKeyCacheTTL = 10 * time.Minute
)

// fetchActorPublicKey fetches the public key PEM and owner URL for the given
// keyID. keyID may include a fragment (e.g. "https://host/actor#main-key");
// the fragment is stripped for the actual HTTP GET, but used as a cache key.
func fetchActorPublicKey(ctx context.Context, httpClient *http.Client, keyID string) (pemStr, owner string, err error) {
	if err := validateAPURL(keyID); err != nil {
		return "", "", fmt.Errorf("key ID: %w", err)
	}
	u, err := url.Parse(keyID)
	if err != nil {
		return "", "", fmt.Errorf("key ID must be an https URL with non-empty host: %q", keyID)
	}

	pubKeyCacheMu.Lock()
	if e, ok := pubKeyCache[keyID]; ok && time.Since(e.fetchedAt) < pubKeyCacheTTL {
		pubKeyCacheMu.Unlock()
		return e.pem, e.owner, nil
	}
	pubKeyCacheMu.Unlock()

	// Strip fragment for the HTTP request (fragments are client-side only).
	u.Fragment = ""
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return "", "", err
	}
	req.Header.Set("Accept", "application/activity+json")
	resp, err := httpClient.Do(req)
	if err != nil {
		return "", "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", "", fmt.Errorf("actor fetch returned HTTP %d", resp.StatusCode)
	}

	var actor struct {
		PublicKey struct {
			Owner        string `json:"owner"`
			PublicKeyPem string `json:"publicKeyPem"`
		} `json:"publicKey"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, maxRemoteJSONBody)).Decode(&actor); err != nil {
		return "", "", fmt.Errorf("decode actor: %w", err)
	}
	if actor.PublicKey.PublicKeyPem == "" {
		return "", "", fmt.Errorf("no publicKeyPem in actor response")
	}

	pubKeyCacheMu.Lock()
	pubKeyCache[keyID] = pubKeyEntry{
		pem:       actor.PublicKey.PublicKeyPem,
		owner:     actor.PublicKey.Owner,
		fetchedAt: time.Now(),
	}
	pubKeyCacheMu.Unlock()

	return actor.PublicKey.PublicKeyPem, actor.PublicKey.Owner, nil
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

const inboxDateTolerance = 5 * time.Minute

// verifyInboxRequest performs full HTTP Signature verification for an inbound
// ActivityPub POST: required-header enforcement, Digest validation, Date
// freshness, key-owner↔actor matching, and RSA signature verification.
func verifyInboxRequest(ctx context.Context, httpClient *http.Client, r *http.Request, body []byte, actorField string) error {
	sigHeader := r.Header.Get("Signature")
	if sigHeader == "" {
		return fmt.Errorf("missing Signature header")
	}
	params := parseSignatureHeader(sigHeader)

	keyID := params["keyId"]
	if keyID == "" {
		return fmt.Errorf("missing keyId in Signature header")
	}

	// Enforce required signed headers for inbox POSTs.
	headersParam := params["headers"]
	for _, required := range []string{"(request-target)", "host", "date", "digest"} {
		found := false
		for _, h := range strings.Fields(headersParam) {
			if h == required {
				found = true
				break
			}
		}
		if !found {
			return fmt.Errorf("required header %q not covered by signature", required)
		}
	}

	// Validate Digest against the raw body.
	digestHdr := r.Header.Get("Digest")
	if digestHdr == "" {
		return fmt.Errorf("missing Digest header")
	}
	bodyHash := sha256.Sum256(body)
	expectedDigest := "SHA-256=" + base64.StdEncoding.EncodeToString(bodyHash[:])
	if digestHdr != expectedDigest {
		return fmt.Errorf("Digest header mismatch")
	}

	// Enforce Date freshness.
	dateHdr := r.Header.Get("Date")
	if dateHdr == "" {
		return fmt.Errorf("missing Date header")
	}
	t, err := http.ParseTime(dateHdr)
	if err != nil {
		return fmt.Errorf("invalid Date header: %w", err)
	}
	if age := time.Since(t); age < -inboxDateTolerance || age > inboxDateTolerance {
		return fmt.Errorf("Date header outside ±5 min window: %v", t)
	}

	// Fetch key PEM and owner via the keyId from the Signature header.
	pubKeyPEM, owner, err := fetchActorPublicKey(ctx, httpClient, keyID)
	if err != nil {
		return fmt.Errorf("fetch public key %q: %w", keyID, err)
	}

	// Verify that the key's owner matches the declared activity actor.
	if actorField != "" {
		// Derive the base actor URL by stripping any key fragment.
		keyBase := keyID
		if idx := strings.Index(keyBase, "#"); idx != -1 {
			keyBase = keyBase[:idx]
		}
		effectiveOwner := owner
		if effectiveOwner == "" {
			effectiveOwner = keyBase // fallback when owner absent in actor doc
		}
		if effectiveOwner != actorField {
			return fmt.Errorf("key owner %q does not match activity actor %q", effectiveOwner, actorField)
		}
	}

	return VerifyRequest(r, pubKeyPEM)
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
