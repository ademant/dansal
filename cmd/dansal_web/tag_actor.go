package main

// Tag ActivityPub actor support (#957): each tag acts as a first-class
// OrderedCollection actor with an inbox, followers endpoint, and outbox
// (served by the existing tagHandler). Remote servers can follow a tag to
// receive events tagged with it; Accepts are signed by the relay actor since
// individual tags do not carry their own key pairs.

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"strconv"
	"time"
)

// tagFollowersHandler serves GET /tags/{slug}/followers — an OrderedCollection
// of actor URIs that follow this tag.
func tagFollowersHandler(cfg *Config, db *sql.DB, client *DansalClient) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		slug := r.PathValue("slug")
		tagMap, err := client.GetTagMap(r.Context())
		if err != nil {
			writeJSONError(w, r, http.StatusBadGateway, "could not load tags")
			return
		}
		if _, ok := tagMap[slug]; !ok {
			writeJSONError(w, r, http.StatusNotFound, "tag not found")
			return
		}

		fs, err := listTagFollowers(db, slug)
		if err != nil {
			writeJSONError(w, r, http.StatusInternalServerError, err.Error())
			return
		}

		uris := make([]string, len(fs))
		for i, f := range fs {
			uris[i] = f.ActorURI
		}

		base := fmt.Sprintf("https://%s/tags/%s", cfg.Domain, slug)
		col := OrderedCollection{
			Context:    APContext,
			Type:       "OrderedCollection",
			ID:         base + "/followers",
			TotalItems: len(uris),
			Items:      uris,
		}
		writeJSON(w, http.StatusOK, col)
	}
}

// tagInboxHandler serves POST /tags/{slug}/inbox — accepts Follow and
// Undo{Follow} activities from remote actors. The relay actor's key signs
// Accept responses (tags share the relay's public key material).
func tagInboxHandler(cfg *Config, db *sql.DB, client *DansalClient) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		slug := r.PathValue("slug")
		tagMap, err := client.GetTagMap(r.Context())
		if err != nil {
			writeJSONError(w, r, http.StatusBadGateway, "could not load tags")
			return
		}
		if _, ok := tagMap[slug]; !ok {
			writeJSONError(w, r, http.StatusNotFound, "tag not found")
			return
		}

		body, err := io.ReadAll(io.LimitReader(r.Body, maxInboundJSONBody))
		if err != nil {
			writeJSONError(w, r, http.StatusBadRequest, "read error")
			return
		}
		var raw map[string]any
		if err := json.Unmarshal(body, &raw); err != nil {
			writeJSONError(w, r, http.StatusBadRequest, "invalid JSON")
			return
		}

		activityType, _ := raw["type"].(string)
		actorField, _ := raw["actor"].(string)

		// Signature verification is mandatory for every inbox activity: an
		// empty actor field must not be able to skip it (#999). Real
		// ActivityPub servers always sign inbox POSTs.
		if actorField == "" {
			writeJSONError(w, r, http.StatusBadRequest, "missing actor")
			return
		}
		if err := verifyInboxRequest(r.Context(), fedHTTPClient, r, body, actorField); err != nil {
			log.Printf("tag inbox: verification failed for %s: %v", actorField, err)
			writeJSONError(w, r, http.StatusUnauthorized, "signature verification failed")
			return
		}

		switch activityType {
		case "Follow":
			if actorField == "" {
				writeJSONError(w, r, http.StatusBadRequest, "missing actor")
				return
			}
			inboxURL, err := resolveInboxURL(r.Context(), client, actorField)
			if err != nil || inboxURL == "" {
				writeJSONError(w, r, http.StatusBadRequest, "could not resolve actor inbox")
				return
			}
			followID, _ := raw["id"].(string)
			if err := addTagFollower(db, slug, actorField, inboxURL, followID); err != nil {
				writeJSONError(w, r, http.StatusInternalServerError, err.Error())
				return
			}
			go sendTagAccept(cfg, db, slug, raw, actorField, inboxURL)
			w.WriteHeader(http.StatusAccepted)

		case "Undo":
			obj, _ := raw["object"].(map[string]any)
			if obj == nil {
				writeJSONError(w, r, http.StatusBadRequest, "missing object")
				return
			}
			objType, _ := obj["type"].(string)
			if objType != "Follow" {
				writeJSONError(w, r, http.StatusBadRequest, "only Undo{Follow} supported")
				return
			}
			undoActor := actorField
			if undoActor == "" {
				undoActor, _ = obj["actor"].(string)
			}
			if undoActor == "" {
				writeJSONError(w, r, http.StatusBadRequest, "missing actor")
				return
			}
			if err := removeTagFollower(db, slug, undoActor); err != nil {
				writeJSONError(w, r, http.StatusInternalServerError, err.Error())
				return
			}
			w.WriteHeader(http.StatusAccepted)

		default:
			// Other activity types (Create, Announce, etc.) are silently accepted —
			// we don't process them for tags, but returning 4xx would cause retries.
			w.WriteHeader(http.StatusAccepted)
		}
	}
}

// sendTagAccept sends an Accept{Follow} to the follower's inbox, signed by
// the relay actor. The Accept's actor field is the tag URL so the remote
// server associates the acceptance with the followed tag.
func sendTagAccept(cfg *Config, db *sql.DB, tagSlug string, followActivity map[string]any, followerURI, inboxURL string) {
	relayActor, err := getActorBySlug(db, cfg.RelayActorName)
	if err != nil {
		log.Printf("sendTagAccept: load relay actor: %v", err)
		return
	}

	tagURL := fmt.Sprintf("https://%s/tags/%s", cfg.Domain, tagSlug)
	accept := Activity{
		Context: APContext,
		Type:    "Accept",
		ID:      tagURL + "#accept-" + strconv.FormatInt(time.Now().UnixNano(), 36),
		Actor:   tagURL,
		Object:  followActivity,
		To:      []string{followerURI},
	}
	body, err := json.Marshal(accept)
	if err != nil {
		log.Printf("sendTagAccept marshal: %v", err)
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, inboxURL, bytes.NewReader(body))
	if err != nil {
		log.Printf("sendTagAccept new request: %v", err)
		return
	}
	req.Header.Set("Content-Type", "application/activity+json")

	// Sign with the relay actor's key. The key ID is the relay actor's key
	// because tags share the relay's public key (referenced in tag AP responses
	// via the relay's publicKey.id). Servers that strictly verify that the
	// signing key belongs to the Accept's actor may reject this; a per-tag key
	// pair would be needed for full compliance.
	relayBase := actorURL(cfg, relayActor.OrgSlug)
	keyID := relayBase + "#main-key"
	if err := SignRequest(req, keyID, relayActor.PrivateKeyPEM, body); err != nil {
		log.Printf("sendTagAccept sign: %v", err)
		return
	}

	resp, err := fedHTTPClient.Do(req)
	if err != nil {
		log.Printf("sendTagAccept post: %v", err)
		return
	}
	resp.Body.Close()
	if resp.StatusCode >= 300 {
		log.Printf("sendTagAccept: remote returned %d for %s", resp.StatusCode, inboxURL)
	}
}
