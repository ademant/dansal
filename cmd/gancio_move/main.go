// One-shot migration tool: sends ActivityPub Move activity from the old
// Gancio relay (https://balfolk.jetzt/federation/u/relay) to the new
// dansal relay (https://balfolk.jetzt/org/relay) for every known follower.
//
// Usage:
//
//	go run ./cmd/gancio_move/ -key old_private.pem [-dry-run]
package main

import (
	"bytes"
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"strings"
	"time"
)

const (
	oldActorURL = "https://balfolk.jetzt/federation/u/relay"
	newActorURL = "https://balfolk.jetzt/org/relay"
	oldKeyID    = oldActorURL + "#main-key"
)

var followers = []string{
	"https://mastodon.social/users/PiojoConK",
	"https://mastodon.social/users/ademant",
	"https://events.festnoz.de/federation/u/relay",
	"https://mastodon.nl/users/VoordeMus",
	"https://digitalcourage.social/users/ademant",
	"https://talk.balfolk.social/users/ademant",
	"https://nrw.social/users/Ademant",
	"https://mastodon.social/users/idtanzhaus",
	"https://mastodon.online/users/qwandor",
	"https://mastodon.social/ap/users/115910096158266802",
	"https://mastodon.social/ap/users/116172665375859195",
	"https://mastodon.social/users/samuelFRDE",
	"https://mastodon.nl/users/sandradejong",
	"https://discover.holos.social/actor",
}

// Known shared inboxes for efficiency (avoids per-follower lookup where possible).
var knownSharedInboxes = map[string]string{
	"mastodon.social":       "https://mastodon.social/inbox",
	"nrw.social":            "https://nrw.social/inbox",
	"mastodon.nl":           "https://mastodon.nl/inbox",
	"digitalcourage.social": "https://digitalcourage.social/inbox",
	"talk.balfolk.social":   "https://talk.balfolk.social/inbox",
	"mastodon.online":       "https://mastodon.online/inbox",
}

func main() {
	keyFile := flag.String("key", "old_private.pem", "path to old Gancio relay private key (PKCS8 PEM)")
	dryRun := flag.Bool("dry-run", false, "print what would be sent without sending")
	flag.Parse()

	keyPEM, err := os.ReadFile(*keyFile)
	if err != nil {
		log.Fatalf("read key: %v", err)
	}
	privKey, err := parsePrivateKey(keyPEM)
	if err != nil {
		log.Fatalf("parse key: %v", err)
	}
	log.Printf("loaded private key for %s", oldKeyID)

	client := &http.Client{Timeout: 15 * time.Second}
	ctx := context.Background()

	// Deduplicate inboxes: send once per shared inbox, not once per follower.
	inboxTargets := map[string]bool{}
	for _, followerURL := range followers {
		inbox := resolveInbox(ctx, client, followerURL)
		if inbox == "" {
			log.Printf("WARN: could not resolve inbox for %s, skipping", followerURL)
			continue
		}
		inboxTargets[inbox] = true
	}

	move := buildMove()
	body, err := json.Marshal(move)
	if err != nil {
		log.Fatalf("marshal: %v", err)
	}
	log.Printf("Move activity:\n%s", string(body))

	for inbox := range inboxTargets {
		if *dryRun {
			log.Printf("[dry-run] would POST to %s", inbox)
			continue
		}
		if err := postSigned(ctx, client, inbox, body, privKey); err != nil {
			log.Printf("ERROR posting to %s: %v", inbox, err)
		} else {
			log.Printf("OK: posted Move to %s", inbox)
		}
		time.Sleep(500 * time.Millisecond)
	}
}

func buildMove() map[string]any {
	return map[string]any{
		"@context": []string{
			"https://www.w3.org/ns/activitystreams",
			"https://w3id.org/security/v1",
		},
		"id":     oldActorURL + "#move-" + fmt.Sprintf("%d", time.Now().Unix()),
		"type":   "Move",
		"actor":  oldActorURL,
		"object": oldActorURL,
		"target": newActorURL,
	}
}

func resolveInbox(ctx context.Context, client *http.Client, actorURL string) string {
	// Use known shared inbox if domain matches.
	for domain, inbox := range knownSharedInboxes {
		if strings.Contains(actorURL, domain) {
			return inbox
		}
	}
	// Fetch actor profile to find inbox.
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, actorURL, nil)
	if err != nil {
		return ""
	}
	req.Header.Set("Accept", "application/activity+json")
	resp, err := client.Do(req)
	if err != nil {
		return ""
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		log.Printf("fetch %s: HTTP %d", actorURL, resp.StatusCode)
		return ""
	}
	var actor struct {
		Inbox     string `json:"inbox"`
		Endpoints struct {
			SharedInbox string `json:"sharedInbox"`
		} `json:"endpoints"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&actor); err != nil {
		return ""
	}
	if actor.Endpoints.SharedInbox != "" {
		return actor.Endpoints.SharedInbox
	}
	return actor.Inbox
}

func postSigned(ctx context.Context, client *http.Client, inboxURL string, body []byte, key *rsa.PrivateKey) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, inboxURL, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/activity+json")
	req.Header.Set("Accept", "application/activity+json")

	date := time.Now().UTC().Format(http.TimeFormat)
	req.Header.Set("Date", date)

	digest := sha256.Sum256(body)
	req.Header.Set("Digest", "SHA-256="+base64.StdEncoding.EncodeToString(digest[:]))

	// Build signature string.
	sigStr := fmt.Sprintf("(request-target): post %s\nhost: %s\ndate: %s\ndigest: SHA-256=%s",
		req.URL.Path, req.URL.Host, date,
		base64.StdEncoding.EncodeToString(digest[:]))

	h := crypto.SHA256.New()
	h.Write([]byte(sigStr))
	sig, err := rsa.SignPKCS1v15(rand.Reader, key, crypto.SHA256, h.Sum(nil))
	if err != nil {
		return fmt.Errorf("sign: %w", err)
	}
	b64sig := base64.StdEncoding.EncodeToString(sig)

	req.Header.Set("Signature", fmt.Sprintf(
		`keyId="%s",algorithm="rsa-sha256",headers="(request-target) host date digest",signature="%s"`,
		oldKeyID, b64sig,
	))

	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(respBody)))
	}
	return nil
}

func parsePrivateKey(pemBytes []byte) (*rsa.PrivateKey, error) {
	block, _ := pem.Decode(pemBytes)
	if block == nil {
		return nil, fmt.Errorf("no PEM block found")
	}
	switch block.Type {
	case "PRIVATE KEY": // PKCS8
		key, err := x509.ParsePKCS8PrivateKey(block.Bytes)
		if err != nil {
			return nil, err
		}
		rk, ok := key.(*rsa.PrivateKey)
		if !ok {
			return nil, fmt.Errorf("not an RSA key")
		}
		return rk, nil
	case "RSA PRIVATE KEY": // PKCS1
		return x509.ParsePKCS1PrivateKey(block.Bytes)
	default:
		return nil, fmt.Errorf("unknown PEM type: %s", block.Type)
	}
}
