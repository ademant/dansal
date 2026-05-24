package main

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

)

var (
	slugTranslit = strings.NewReplacer(
		"Ä", "a", "ä", "a",
		"Ö", "o", "ö", "o",
		"Ü", "u", "ü", "u",
		"ß", "ss",
		"À", "a", "à", "a", "Â", "a", "â", "a",
		"Á", "a", "á", "a", "Ã", "a", "ã", "a",
		"Å", "a", "å", "a",
		"Æ", "ae", "æ", "ae",
		"Ç", "c", "ç", "c",
		"È", "e", "è", "e", "É", "e", "é", "e",
		"Ê", "e", "ê", "e", "Ë", "e", "ë", "e",
		"Î", "i", "î", "i", "Ï", "i", "ï", "i",
		"Í", "i", "í", "i", "Ì", "i", "ì", "i",
		"Ñ", "n", "ñ", "n",
		"Ô", "o", "ô", "o", "Ó", "o", "ó", "o",
		"Ò", "o", "ò", "o", "Õ", "o", "õ", "o",
		"Ø", "o", "ø", "o",
		"Œ", "oe", "œ", "oe",
		"Ù", "u", "ù", "u", "Û", "u", "û", "u",
		"Ú", "u", "ú", "u",
		"Ý", "y", "ý", "y", "ÿ", "y",
		"'", "", "’", "", // apostrophes (e.g. Breton c'h)
	)
	slugRe      = regexp.MustCompile(`[^a-z0-9\-]`)
	slugDashRe  = regexp.MustCompile(`-{2,}`)
)

// effectiveSlug returns the AP actor slug for an org: actor_name if set, else name-derived.
func effectiveSlug(org Organization) string {
	if org.ActorName != "" {
		return org.ActorName
	}
	return orgSlug(org.Name)
}

func orgSlug(name string) string {
	s := slugTranslit.Replace(name)
	s = strings.ToLower(s)
	s = slugRe.ReplaceAllString(s, "-")
	s = slugDashRe.ReplaceAllString(s, "-")
	s = strings.Trim(s, "-")
	return s
}

func actorURL(cfg *Config, slug string) string {
	return "https://" + cfg.Domain + "/org/" + slug
}

func actorFromOrg(cfg *Config, org Organization, actor *ActorRecord) Actor {
	base := actorURL(cfg, actor.OrgSlug)
	a := Actor{
		Context:                   APContext,
		Type:                      "Organization",
		ID:                        base,
		Name:                      org.Name,
		Summary:                   org.Description,
		URL:                       base,
		PreferredUsername:         actor.OrgSlug,
		Inbox:                     base + "/inbox",
		Outbox:                    base + "/outbox",
		Followers:                 base + "/followers",
		ManuallyApprovesFollowers: false,
		Discoverable:              true,
		Indexable:                 true,
		Endpoints:                 &APEndpoints{SharedInbox: "https://" + cfg.Domain + "/inbox"},
		PublicKey: PublicKey{
			ID:              base + "#main-key",
			Owner:           base,
			PublicKeyPem:    actor.PublicKeyPEM,
			PublicKeyMultibase: actor.PublicKeyMultibase,
		},
	}
	if org.ImageURL != "" {
		a.Icon = &APDocument{Type: "Image", MediaType: "image/jpeg", URL: org.ImageURL}
	}
	return a
}

func isAPRequest(r *http.Request) bool {
	accept := r.Header.Get("Accept")
	return strings.Contains(accept, "application/activity+json") ||
		strings.Contains(accept, "application/ld+json")
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/activity+json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}

func actorsListHandler(cfg *Config, db *sql.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		slugs, err := listOrgActorSlugs(db)
		if err != nil {
			writeJSONError(w, http.StatusInternalServerError, "database error")
			return
		}
		type entry struct {
			Handle string `json:"handle"`
			URL    string `json:"url"`
		}
		result := make([]entry, 0, len(slugs))
		for _, slug := range slugs {
			result = append(result, entry{
				Handle: "@" + slug + "@" + cfg.Domain,
				URL:    actorURL(cfg, slug),
			})
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(result)
	}
}

func writeJSONError(w http.ResponseWriter, status int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(map[string]string{"error": msg})
}

func webfingerHandler(cfg *Config, db *sql.DB, client *DansalClient) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		resource := r.URL.Query().Get("resource")
		if resource == "" {
			writeJSONError(w, http.StatusBadRequest, "resource parameter required")
			return
		}
		prefix := "acct:"
		if !strings.HasPrefix(resource, prefix) {
			writeJSONError(w, http.StatusBadRequest, "only acct: resources supported")
			return
		}
		account := strings.TrimPrefix(resource, prefix)
		parts := strings.SplitN(account, "@", 2)
		if len(parts) != 2 || parts[1] != cfg.Domain {
			writeJSONError(w, http.StatusNotFound, "user not found")
			return
		}
		slug := parts[0]
		actor, err := getActorBySlug(db, slug)
		if err == sql.ErrNoRows {
			if slug == "relay" {
				writeJSONError(w, http.StatusNotFound, "user not found")
				return
			}
			orgs, err := client.GetOrganizations(r.Context())
			if err != nil {
				writeJSONError(w, http.StatusInternalServerError, "upstream error")
				return
			}
			for _, org := range orgs {
				if effectiveSlug(org) == slug {
					actor, err = ensureActor(db, org.ID, slug)
					if err != nil {
						writeJSONError(w, http.StatusInternalServerError, "actor init error")
						return
					}
					break
				}
			}
			if actor == nil {
				writeJSONError(w, http.StatusNotFound, "user not found")
				return
			}
		} else if err != nil {
			writeJSONError(w, http.StatusInternalServerError, err.Error())
			return
		}

		base := actorURL(cfg, actor.OrgSlug)
		wf := WebFinger{
			Subject: resource,
			Aliases: []string{base},
			Links: []WebFingerLink{
				{
					Rel:  "self",
					Type: "application/activity+json",
					Href: base,
				},
			},
		}
		w.Header().Set("Content-Type", "application/jrd+json")
		json.NewEncoder(w).Encode(wf)
	}
}

func nodeinfoIndexHandler(cfg *Config) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		base := "https://" + cfg.Domain
		resp := map[string]any{
			"links": []map[string]string{
				{
					"rel":  "http://nodeinfo.diaspora.software/ns/schema/2.0",
					"href": base + "/nodeinfo/2.0",
				},
				{
					"rel":  "http://nodeinfo.diaspora.software/ns/schema/2.1",
					"href": base + "/nodeinfo/2.1",
				},
			},
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
	}
}

func buildNodeInfo(cfg *Config, version string) NodeInfo {
	ni := NodeInfo{
		Version: version,
		Software: NodeInfoSoftware{
			Name:    "dansal-web",
			Version: "1.0.0",
		},
		Protocols:         []string{"activitypub"},
		OpenRegistrations: false,
	}
	if version == "2.1" {
		var meta *NodeInfoMetadata
		if cfg.NodeInfoDescription != "" || cfg.NodeInfoMaintainerName != "" || cfg.NodeInfoMaintainerEmail != "" {
			meta = &NodeInfoMetadata{NodeDescription: cfg.NodeInfoDescription}
			if cfg.NodeInfoMaintainerName != "" || cfg.NodeInfoMaintainerEmail != "" {
				meta.Maintainer = &NodeInfoMaintainer{
					Name:  cfg.NodeInfoMaintainerName,
					Email: cfg.NodeInfoMaintainerEmail,
				}
			}
		}
		ni.Metadata = meta
	}
	return ni
}

func nodeinfoHandler(cfg *Config) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ni := buildNodeInfo(cfg, "2.0")
		w.Header().Set("Content-Type", `application/json; profile="http://nodeinfo.diaspora.software/ns/schema/2.0#"`)
		json.NewEncoder(w).Encode(ni)
	}
}

func nodeinfo21Handler(cfg *Config) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ni := buildNodeInfo(cfg, "2.1")
		w.Header().Set("Content-Type", `application/json; profile="http://nodeinfo.diaspora.software/ns/schema/2.1#"`)
		json.NewEncoder(w).Encode(ni)
	}
}

func outboxHandler(cfg *Config, db *sql.DB, client *DansalClient) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		slug := r.PathValue("name")
		actor, err := getActorBySlug(db, slug)
		if err == sql.ErrNoRows {
			writeJSONError(w, http.StatusNotFound, "actor not found")
			return
		} else if err != nil {
			writeJSONError(w, http.StatusInternalServerError, err.Error())
			return
		}

		base := actorURL(cfg, slug)
		outboxURL := base + "/outbox"

		var events []Event
		if actor.OrgID == 0 {
			events, _ = client.GetEvents(r.Context(), "")
			published := events[:0]
			for _, e := range events {
				if e.IsPublished {
					published = append(published, e)
				}
			}
			events = published
		} else {
			events, _ = client.GetEventsByOrg(r.Context(), actor.OrgID)
		}

		if r.URL.Query().Get("page") != "true" {
			col := OrderedCollection{
				Context:    APContext,
				Type:       "OrderedCollection",
				ID:         outboxURL,
				TotalItems: len(events),
				First:      outboxURL + "?page=true",
			}
			writeJSON(w, http.StatusOK, col)
			return
		}

		items := make([]any, 0, len(events))
		for _, e := range events {
			items = append(items, buildCreateActivity(cfg, actor.OrgSlug, e))
		}

		page := OrderedCollectionPage{
			Context:      APContext,
			Type:         "OrderedCollectionPage",
			ID:           outboxURL + "?page=true",
			PartOf:       outboxURL,
			TotalItems:   len(items),
			OrderedItems: items,
		}
		writeJSON(w, http.StatusOK, page)
	}
}

func followersHandler(cfg *Config, db *sql.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		slug := r.PathValue("name")
		actor, err := getActorBySlug(db, slug)
		if err == sql.ErrNoRows {
			writeJSONError(w, http.StatusNotFound, "actor not found")
			return
		} else if err != nil {
			writeJSONError(w, http.StatusInternalServerError, err.Error())
			return
		}

		fs, err := listFollowers(db, actor.OrgID)
		if err != nil {
			writeJSONError(w, http.StatusInternalServerError, err.Error())
			return
		}

		uris := make([]string, len(fs))
		for i, f := range fs {
			uris[i] = f.ActorURI
		}

		base := actorURL(cfg, slug)
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

func inboxHandler(cfg *Config, db *sql.DB, client *DansalClient) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		slug := r.PathValue("name")
		actor, err := getActorBySlug(db, slug)
		if err == sql.ErrNoRows {
			writeJSONError(w, http.StatusNotFound, "actor not found")
			return
		} else if err != nil {
			writeJSONError(w, http.StatusInternalServerError, err.Error())
			return
		}

		body, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
		if err != nil {
			writeJSONError(w, http.StatusBadRequest, "read error")
			return
		}
		var raw map[string]any
		if err := json.Unmarshal(body, &raw); err != nil {
			writeJSONError(w, http.StatusBadRequest, "invalid JSON")
			return
		}
		processInboxActivity(w, r, cfg, db, client, actor, raw)
	}
}

func sharedInboxHandler(cfg *Config, db *sql.DB, client *DansalClient) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
		if err != nil {
			writeJSONError(w, http.StatusBadRequest, "read error")
			return
		}
		var raw map[string]any
		if err := json.Unmarshal(body, &raw); err != nil {
			writeJSONError(w, http.StatusBadRequest, "invalid JSON")
			return
		}
		actor := resolveSharedInboxActor(cfg, db, raw)
		if actor == nil {
			w.WriteHeader(http.StatusAccepted)
			return
		}
		processInboxActivity(w, r, cfg, db, client, actor, raw)
	}
}

// resolveSharedInboxActor determines the target local actor for an activity
// delivered to the shared inbox. Falls back to the relay actor for activities
// (like Accept) that don't name a specific local target.
func resolveSharedInboxActor(cfg *Config, db *sql.DB, raw map[string]any) *ActorRecord {
	prefix := "https://" + cfg.Domain + "/org/"
	activityType, _ := raw["type"].(string)

	var targetURL string
	switch activityType {
	case "Follow":
		targetURL, _ = raw["object"].(string)
	case "Undo":
		if obj, ok := raw["object"].(map[string]any); ok {
			switch v := obj["object"].(type) {
			case string:
				targetURL = v
			case map[string]any:
				targetURL, _ = v["id"].(string)
			}
		}
	}

	if strings.HasPrefix(targetURL, prefix) {
		slug := strings.SplitN(strings.TrimPrefix(targetURL, prefix), "/", 2)[0]
		if actor, err := getActorBySlug(db, slug); err == nil {
			return actor
		}
	}

	// Fallback: relay actor handles Accept and unroutable activities.
	actor, err := getActorBySlug(db, "relay")
	if err != nil {
		return nil
	}
	return actor
}

func processInboxActivity(w http.ResponseWriter, r *http.Request, cfg *Config, db *sql.DB, client *DansalClient, actor *ActorRecord, raw map[string]any) {
	activityType, _ := raw["type"].(string)
	actorField, _ := raw["actor"].(string)

	if actorField != "" {
		pubKeyPEM, err := fetchActorPublicKey(r.Context(), client.HTTP, actorField)
		if err != nil {
			log.Printf("inbox: fetch public key for %s: %v", actorField, err)
			writeJSONError(w, http.StatusUnauthorized, "could not fetch sender key")
			return
		}
		if err := VerifyRequest(r, pubKeyPEM); err != nil {
			log.Printf("inbox: signature verification failed for %s: %v", actorField, err)
			writeJSONError(w, http.StatusUnauthorized, "signature verification failed")
			return
		}
	}

	switch activityType {
	case "Follow":
		if actorField == "" {
			writeJSONError(w, http.StatusBadRequest, "missing actor")
			return
		}
		inboxURL, err := resolveInboxURL(r.Context(), client, actorField)
		if err != nil {
			log.Printf("inbox: resolve actor %s: %v", actorField, err)
			inboxURL = ""
		}
		if inboxURL == "" {
			writeJSONError(w, http.StatusBadRequest, "could not resolve actor inbox")
			return
		}
		if err := addFollower(db, actor.OrgID, actorField, inboxURL); err != nil {
			writeJSONError(w, http.StatusInternalServerError, err.Error())
			return
		}
		go sendAccept(cfg, actor, raw, actorField, inboxURL)
		w.WriteHeader(http.StatusAccepted)

	case "Undo":
		obj, _ := raw["object"].(map[string]any)
		if obj == nil {
			writeJSONError(w, http.StatusBadRequest, "missing object")
			return
		}
		objType, _ := obj["type"].(string)
		if objType != "Follow" {
			writeJSONError(w, http.StatusBadRequest, "only Undo{Follow} supported")
			return
		}
		undoActor := actorField
		if undoActor == "" {
			undoActor, _ = obj["actor"].(string)
		}
		if undoActor == "" {
			writeJSONError(w, http.StatusBadRequest, "missing actor")
			return
		}
		if err := removeFollower(db, actor.OrgID, undoActor); err != nil {
			writeJSONError(w, http.StatusInternalServerError, err.Error())
			return
		}
		w.WriteHeader(http.StatusAccepted)

	case "Accept":
		// Accept{Follow}: update our outbound follow state to accepted.
		var followActivityID string
		switch v := raw["object"].(type) {
		case string:
			followActivityID = v
		case map[string]any:
			followActivityID, _ = v["id"].(string)
		}
		if followActivityID != "" {
			if err := updateFollowStateByActivityID(db, followActivityID, "accepted"); err != nil {
				log.Printf("inbox Accept: update follow state for %s: %v", followActivityID, err)
			}
		}
		w.WriteHeader(http.StatusAccepted)

	case "Create", "Announce":
		eventObj := extractAPEventObject(raw)
		if eventObj == nil {
			w.WriteHeader(http.StatusAccepted)
			return
		}
		fe := apObjectToFederatedEvent(eventObj, actorField)
		if fe.APID == "" {
			w.WriteHeader(http.StatusAccepted)
			return
		}
		if err := upsertFederatedEvent(db, fe); err != nil {
			log.Printf("inbox: upsert federated event %s: %v", fe.APID, err)
		}
		w.WriteHeader(http.StatusAccepted)

	case "Update":
		obj, _ := raw["object"].(map[string]any)
		if obj == nil {
			w.WriteHeader(http.StatusAccepted)
			return
		}
		if t, _ := obj["type"].(string); t != "Event" {
			w.WriteHeader(http.StatusAccepted)
			return
		}
		fe := apObjectToFederatedEvent(obj, actorField)
		if fe.APID == "" {
			w.WriteHeader(http.StatusAccepted)
			return
		}
		if err := upsertFederatedEvent(db, fe); err != nil {
			log.Printf("inbox: update federated event %s: %v", fe.APID, err)
		}
		w.WriteHeader(http.StatusAccepted)

	case "Delete":
		var apID string
		switch v := raw["object"].(type) {
		case string:
			apID = v
		case map[string]any:
			apID, _ = v["id"].(string)
		}
		if apID != "" {
			if err := deleteFederatedEvent(db, apID); err != nil {
				log.Printf("inbox: delete federated event %s: %v", apID, err)
			}
		}
		w.WriteHeader(http.StatusAccepted)

	default:
		writeJSONError(w, http.StatusBadRequest, "unsupported activity type")
	}
}

// extractAPEventObject unwraps a Create{Event} or Announce{Create{Event}} activity
// and returns the inner Event AP object, or nil if the structure is not recognised.
func extractAPEventObject(raw map[string]any) map[string]any {
	obj, _ := raw["object"].(map[string]any)
	if obj == nil {
		return nil
	}
	switch t, _ := obj["type"].(string); t {
	case "Event":
		return obj
	case "Create":
		inner, _ := obj["object"].(map[string]any)
		if inner != nil {
			if t2, _ := inner["type"].(string); t2 == "Event" {
				return inner
			}
		}
	}
	return nil
}

func apObjectToFederatedEvent(obj map[string]any, actorID string) FederatedEvent {
	apID, _ := obj["id"].(string)
	name, _ := obj["name"].(string)
	startTime, _ := obj["startTime"].(string)
	endTime, _ := obj["endTime"].(string)
	eventURL, _ := obj["url"].(string)

	description, _ := obj["content"].(string)
	if description == "" {
		description, _ = obj["summary"].(string)
	}

	var locationName string
	if loc, ok := obj["location"].(map[string]any); ok {
		locationName, _ = loc["name"].(string)
	}

	var imageURL string
	if attachments, ok := obj["attachment"].([]any); ok {
		for _, a := range attachments {
			att, ok := a.(map[string]any)
			if !ok {
				continue
			}
			mt, _ := att["mediaType"].(string)
			if strings.HasPrefix(mt, "image/") {
				imageURL, _ = att["url"].(string)
				break
			}
		}
	}

	var tags []string
	if tagList, ok := obj["tag"].([]any); ok {
		for _, t := range tagList {
			tag, ok := t.(map[string]any)
			if !ok {
				continue
			}
			if tp, _ := tag["type"].(string); tp == "Hashtag" {
				if n, _ := tag["name"].(string); n != "" {
					tags = append(tags, strings.TrimPrefix(n, "#"))
				}
			}
		}
	}

	rawBytes, _ := json.Marshal(obj)
	return FederatedEvent{
		APID:         apID,
		ActorID:      actorID,
		Name:         name,
		StartTime:    startTime,
		EndTime:      endTime,
		URL:          eventURL,
		LocationName: locationName,
		Description:  description,
		ImageURL:     imageURL,
		Tags:         tags,
		RawJSON:      string(rawBytes),
		ReceivedAt:   time.Now().Unix(),
	}
}

// validateAPURL returns an error if rawURL is not a valid https URL with a non-empty host.
// All outbound ActivityPub fetches must use https to prevent SSRF via non-HTTP schemes.
func validateAPURL(rawURL string) error {
	u, err := url.Parse(rawURL)
	if err != nil || u.Scheme != "https" || u.Host == "" {
		return fmt.Errorf("ActivityPub URL must be https with a non-empty host: %q", rawURL)
	}
	return nil
}

func resolveInboxURL(ctx context.Context, client *DansalClient, actorURI string) (string, error) {
	if err := validateAPURL(actorURI); err != nil {
		return "", err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, actorURI, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("Accept", "application/activity+json")
	resp, err := client.HTTP.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	var actor map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&actor); err != nil {
		return "", err
	}
	inbox, _ := actor["inbox"].(string)
	return inbox, nil
}

func sendAccept(cfg *Config, actor *ActorRecord, followActivity map[string]any, followerURI, inboxURL string) {
	base := actorURL(cfg, actor.OrgSlug)
	accept := Activity{
		Context: APContext,
		Type:    "Accept",
		ID:      base + "#accept-" + strconv.FormatInt(time.Now().UnixNano(), 36),
		Actor:   base,
		Object:  followActivity,
		To:      []string{followerURI},
	}
	body, err := json.Marshal(accept)
	if err != nil {
		log.Printf("sendAccept marshal: %v", err)
		return
	}
	req, err := http.NewRequest(http.MethodPost, inboxURL, bytes.NewReader(body))
	if err != nil {
		log.Printf("sendAccept new request: %v", err)
		return
	}
	req.Header.Set("Content-Type", "application/activity+json")
	keyID := base + "#main-key"
	if err := SignRequest(req, keyID, actor.PrivateKeyPEM, body); err != nil {
		log.Printf("sendAccept sign: %v", err)
		return
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		log.Printf("sendAccept post: %v", err)
		return
	}
	resp.Body.Close()
	if resp.StatusCode >= 300 {
		log.Printf("sendAccept: remote returned %d for %s", resp.StatusCode, inboxURL)
	}
}

func buildAPEvent(cfg *Config, slug string, e Event) APEvent {
	base := actorURL(cfg, slug)
	eventID := fmt.Sprintf("https://%s/events/%d", cfg.Domain, e.ID)

	var published string
	if t, ok := parseTime(e.CreatedAt); ok {
		published = t.UTC().Format(time.RFC3339)
	}

	eventURL := e.URL
	if eventURL == "" {
		eventURL = eventID
	}

	var updated string
	if t, ok := parseTime(e.ChangedAt); ok {
		updated = t.UTC().Format(time.RFC3339)
	} else {
		updated = published
	}

	apEvent := APEvent{
		Type:         "Event",
		ID:           eventID,
		Name:         e.Title,
		Content:      e.Description,
		MediaType:    "text/html",
		StartTime:    e.StartTime,
		EndTime:      e.EndTime,
		Published:    published,
		Updated:      updated,
		AttributedTo: base,
		To:           []string{"https://www.w3.org/ns/activitystreams#Public"},
		CC:           []string{base + "/followers"},
		URL:          eventURL,
		Organizer:    map[string]string{"type": "Group", "id": base},
	}
	locationName := e.Location
	if locationName == "" {
		locationName = e.LocationTown
	}
	if locationName != "" {
		place := &APPlace{Type: "Place", Name: locationName}
		place.Latitude = e.LocationLat
		place.Longitude = e.LocationLng
		if e.LocationAddress != "" || e.LocationZipcode != "" || e.LocationTown != "" || e.LocationCountry != "" {
			place.Address = &APPostalAddress{
				Type:            "PostalAddress",
				StreetAddress:   e.LocationAddress,
				PostalCode:      e.LocationZipcode,
				AddressLocality: e.LocationTown,
				AddressCountry:  e.LocationCountry,
			}
		}
		apEvent.Location = place
	}
	for _, tag := range e.Tags {
		apEvent.Tag = append(apEvent.Tag, APHashtag{
			Type: "Hashtag",
			Name: "#" + tag,
			Href: fmt.Sprintf("https://%s/?tag=%s", cfg.Domain, tag),
		})
	}
	if e.ImageURL != "" {
		apEvent.Attachment = []APDocument{{
			Type:      "Document",
			MediaType: "image/jpeg",
			URL:       e.ImageURL,
			Name:      e.Title,
		}}
	}
	return apEvent
}

func buildCreateActivity(cfg *Config, slug string, e Event) Activity {
	base := actorURL(cfg, slug)
	eventID := fmt.Sprintf("https://%s/events/%d", cfg.Domain, e.ID)
	return Activity{
		Type:   "Create",
		ID:     eventID + "/activity",
		Actor:  base,
		Object: buildAPEvent(cfg, slug, e),
		To:     []string{"https://www.w3.org/ns/activitystreams#Public"},
		CC:     []string{base + "/followers"},
	}
}

func buildUpdateActivity(cfg *Config, slug string, e Event) Activity {
	base := actorURL(cfg, slug)
	eventID := fmt.Sprintf("https://%s/events/%d", cfg.Domain, e.ID)
	apEvent := buildAPEvent(cfg, slug, e)
	apEvent.Updated = time.Now().UTC().Format(time.RFC3339)
	return Activity{
		Type:   "Update",
		ID:     fmt.Sprintf("%s/activities/update-%d", eventID, time.Now().UnixNano()),
		Actor:  base,
		Object: apEvent,
		To:     []string{"https://www.w3.org/ns/activitystreams#Public"},
		CC:     []string{base + "/followers"},
	}
}

func buildDeleteActivity(cfg *Config, slug string, eventID int) Activity {
	base := actorURL(cfg, slug)
	apEventID := fmt.Sprintf("https://%s/events/%d", cfg.Domain, eventID)
	return Activity{
		Type:   "Delete",
		ID:     apEventID + "/activities/delete",
		Actor:  base,
		Object: apEventID,
		To:     []string{"https://www.w3.org/ns/activitystreams#Public"},
		CC:     []string{base + "/followers"},
	}
}

// resolveActorFromInput resolves a webfinger address (@user@host) or AP URL to
// the canonical AP actor URL and its inbox URL.
func resolveActorFromInput(ctx context.Context, httpClient *http.Client, input string) (apID, inboxURL string, err error) {
	input = strings.TrimSpace(input)
	if strings.HasPrefix(input, "@") {
		parts := strings.SplitN(strings.TrimPrefix(input, "@"), "@", 2)
		if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
			return "", "", fmt.Errorf("invalid webfinger address")
		}
		resource := "acct:" + parts[0] + "@" + parts[1]
		wfURL := "https://" + parts[1] + "/.well-known/webfinger?resource=" + url.QueryEscape(resource)
		if err := validateAPURL(wfURL); err != nil {
			return "", "", fmt.Errorf("invalid webfinger domain: %w", err)
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, wfURL, nil)
		if err != nil {
			return "", "", err
		}
		req.Header.Set("Accept", "application/json")
		resp, err := httpClient.Do(req)
		if err != nil {
			return "", "", err
		}
		defer resp.Body.Close()
		var wf struct {
			Links []struct {
				Rel  string `json:"rel"`
				Type string `json:"type"`
				Href string `json:"href"`
			} `json:"links"`
		}
		if err := json.NewDecoder(resp.Body).Decode(&wf); err != nil {
			return "", "", err
		}
		for _, l := range wf.Links {
			if l.Rel == "self" && l.Href != "" {
				apID = l.Href
				break
			}
		}
		if apID == "" {
			return "", "", fmt.Errorf("no self link in webfinger response")
		}
	} else if strings.HasPrefix(input, "https://") {
		apID = input
	} else {
		return "", "", fmt.Errorf("expected @user@host or https:// URL")
	}

	if err := validateAPURL(apID); err != nil {
		return "", "", fmt.Errorf("invalid actor URL: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, apID, nil)
	if err != nil {
		return "", "", err
	}
	req.Header.Set("Accept", "application/activity+json")
	resp, err := httpClient.Do(req)
	if err != nil {
		return "", "", err
	}
	defer resp.Body.Close()
	var actor map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&actor); err != nil {
		return "", "", err
	}
	inboxURL, _ = actor["inbox"].(string)
	if inboxURL == "" {
		return "", "", fmt.Errorf("no inbox in actor response")
	}
	return apID, inboxURL, nil
}

func sendFollowActivity(cfg *Config, actor *ActorRecord, followeeAPID, followeeInbox string) (followActivityID string, err error) {
	base := actorURL(cfg, actor.OrgSlug)
	followActivityID = base + "/activities/follow-" + strconv.FormatInt(time.Now().UnixNano(), 36)
	follow := Activity{
		Context: APContext,
		Type:    "Follow",
		ID:      followActivityID,
		Actor:   base,
		Object:  followeeAPID,
	}
	body, err := json.Marshal(follow)
	if err != nil {
		return "", err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, followeeInbox, bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/activity+json")
	keyID := base + "#main-key"
	if err := SignRequest(req, keyID, actor.PrivateKeyPEM, body); err != nil {
		return "", fmt.Errorf("sign: %w", err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", err
	}
	resp.Body.Close()
	if resp.StatusCode >= 300 {
		return "", fmt.Errorf("remote returned %d", resp.StatusCode)
	}
	return followActivityID, nil
}

func sendUndoFollow(cfg *Config, actor *ActorRecord, followeeAPID, followeeInbox, followActivityID string) error {
	base := actorURL(cfg, actor.OrgSlug)
	undo := Activity{
		Context: APContext,
		Type:    "Undo",
		ID:      base + "/activities/undo-" + strconv.FormatInt(time.Now().UnixNano(), 36),
		Actor:   base,
		Object: Activity{
			Type:  "Follow",
			ID:    followActivityID,
			Actor: base,
			Object: followeeAPID,
		},
	}
	body, err := json.Marshal(undo)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, followeeInbox, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/activity+json")
	keyID := base + "#main-key"
	if err := SignRequest(req, keyID, actor.PrivateKeyPEM, body); err != nil {
		return fmt.Errorf("sign: %w", err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	resp.Body.Close()
	if resp.StatusCode >= 300 {
		return fmt.Errorf("remote returned %d", resp.StatusCode)
	}
	return nil
}
