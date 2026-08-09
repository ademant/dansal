package main

// Tag ActivityPub actor support (#957): each tag acts as a first-class
// OrderedCollection actor with an inbox, followers endpoint, and outbox
// (served by the existing tagHandler). Remote servers can follow a tag to
// receive events tagged with it; Accepts are signed by the relay actor since
// individual tags do not carry their own key pairs.

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
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

		raw, ok := readInboxActivity(w, r)
		if !ok {
			return
		}

		activityType, _ := raw["type"].(string)
		actorField, _ := raw["actor"].(string)

		switch activityType {
		case "Follow":
			followID, _ := raw["id"].(string)
			handleFollowActivity(w, r, client, actorField,
				func(inboxURL string) error { return addTagFollower(db, slug, actorField, inboxURL, followID) },
				func(inboxURL string) { sendTagAccept(cfg, db, slug, raw, actorField, inboxURL) },
			)

		case "Undo":
			handleUndoActivity(w, r, raw, actorField,
				func(undoActor string) error { return removeTagFollower(db, slug, undoActor) },
			)

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

	// Sign with the relay actor's key. The key ID is the relay actor's key
	// because tags share the relay's public key (referenced in tag AP responses
	// via the relay's publicKey.id). Servers that strictly verify that the
	// signing key belongs to the Accept's actor may reject this; a per-tag key
	// pair would be needed for full compliance.
	if err := postToInbox(ctx, inboxURL, actorKeyID(cfg, relayActor.OrgSlug), relayActor.PrivateKeyPEM, body); err != nil {
		log.Printf("sendTagAccept post: %v", err)
	}
}
