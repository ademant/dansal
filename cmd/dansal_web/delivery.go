package main

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"strconv"
	"time"
)

func startDelivery(cfg *Config, db *sql.DB, client *DansalClient, relayActor *ActorRecord) {
	ticker := time.NewTicker(time.Duration(cfg.PollSecs) * time.Second)
	lastPoll := time.Now().Add(-time.Duration(cfg.PollSecs) * time.Second)

	for range ticker.C {
		pollAndDeliver(cfg, db, client, relayActor, lastPoll)
		lastPoll = time.Now()
	}
}

const maxDeliveryAttempts = 8

// deliveryBackoff returns the number of seconds to wait before the next retry.
func deliveryBackoff(attempts int) int64 {
	switch {
	case attempts <= 1:
		return 5 * 60
	case attempts == 2:
		return 15 * 60
	case attempts == 3:
		return 60 * 60
	case attempts == 4:
		return 4 * 60 * 60
	default:
		return 24 * 60 * 60
	}
}

// retryFailedDeliveries retries pending per-follower delivery failures with backoff.
func retryFailedDeliveries(cfg *Config, db *sql.DB) {
	failures, err := pendingDeliveryFailures(db, time.Now().Unix())
	if err != nil {
		log.Printf("delivery retry: list pending: %v", err)
		return
	}
	for _, f := range failures {
		actor, err := getActorByOrgID(db, f.OrgID)
		if err != nil {
			log.Printf("delivery retry: get actor org %d: %v", f.OrgID, err)
			continue
		}
		base := actorURL(cfg, actor.OrgSlug)
		keyID := base + "#main-key"
		if err := postToInbox(f.InboxURL, keyID, actor.PrivateKeyPEM, []byte(f.ActivityJSON)); err != nil {
			newAttempts := f.Attempts + 1
			if newAttempts > maxDeliveryAttempts {
				log.Printf("delivery retry: giving up on %s after %d attempts: %v", f.InboxURL, f.Attempts, err)
				deleteDeliveryFailure(db, f.ActivityID, f.OrgID, f.InboxURL)
			} else {
				nextAttempt := time.Now().Unix() + deliveryBackoff(newAttempts)
				if dbErr := updateDeliveryFailure(db, f.ActivityID, f.OrgID, f.InboxURL, err.Error(), newAttempts, nextAttempt); dbErr != nil {
					log.Printf("delivery retry: update record: %v", dbErr)
				}
			}
		} else {
			log.Printf("delivery retry: succeeded to %s (activity %s)", f.InboxURL, f.ActivityID)
			deleteDeliveryFailure(db, f.ActivityID, f.OrgID, f.InboxURL)
		}
	}
}

func pollAndDeliver(cfg *Config, db *sql.DB, client *DansalClient, relayActor *ActorRecord, since time.Time) {
	retryFailedDeliveries(cfg, db)

	after := since.UTC().Format(time.RFC3339)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	events, err := client.GetEvents(ctx, after)
	if err != nil {
		log.Printf("delivery poll: %v", err)
		return
	}

	for _, e := range events {
		if !e.IsPublished || e.OrganizationID == nil {
			continue
		}
		orgID := *e.OrganizationID
		actor, actorErr := getActorByOrgID(db, orgID)

		// Deliver to org followers
		if actorErr == nil && !isDelivered(db, e.ID, orgID) {
			activity := buildCreateActivity(cfg, actor.OrgSlug, e)
			if err := deliverToFollowers(cfg, db, actor, activity); err != nil {
				log.Printf("delivery event %d org %d: %v", e.ID, orgID, err)
			} else {
				if err := markDelivered(db, e.ID, orgID, false); err != nil {
					log.Printf("mark delivered event %d org %d: %v", e.ID, orgID, err)
				}
			}
		}

		// Relay Announce{Create{Event}}: relay actor signs the outer activity,
		// but the inner Create preserves the org's identity and attribution.
		if relayActor != nil && actorErr == nil && !isDelivered(db, e.ID, 0) {
			activity := buildAnnounceActivity(cfg, relayActor.OrgSlug, actor.OrgSlug, e)
			if err := deliverToFollowers(cfg, db, relayActor, activity); err != nil {
				log.Printf("relay delivery event %d: %v", e.ID, err)
			} else {
				if err := markDelivered(db, e.ID, 0, false); err != nil {
					log.Printf("mark relay delivered event %d: %v", e.ID, err)
				}
			}
		}

		// Tag follower delivery (#956): deliver Create{Note} to remote actors
		// that follow any of the event's tags. Uses org_id = -1 as a sentinel
		// in the delivered table so each event is delivered at most once.
		if relayActor != nil && len(e.Tags) > 0 && !isDelivered(db, e.ID, -1) {
			if err := deliverEventToTagFollowers(cfg, db, relayActor, e); err != nil {
				log.Printf("tag delivery event %d: %v", e.ID, err)
			} else {
				if err := markDelivered(db, e.ID, -1, false); err != nil {
					log.Printf("mark tag delivered event %d: %v", e.ID, err)
				}
			}
		}
	}
}

func deliverToFollowers(cfg *Config, db *sql.DB, actor *ActorRecord, activity Activity) error {
	activity.Context = APContext

	followers, err := listFollowers(db, actor.OrgID)
	if err != nil {
		return err
	}
	if len(followers) == 0 {
		return nil
	}

	body, err := json.Marshal(activity)
	if err != nil {
		return err
	}

	base := actorURL(cfg, actor.OrgSlug)
	keyID := base + "#main-key"
	now := time.Now().Unix()

	for _, f := range followers {
		if err := postToInbox(f.InboxURL, keyID, actor.PrivateKeyPEM, body); err != nil {
			log.Printf("deliver to %s: %v", f.InboxURL, err)
			nextAttempt := now + deliveryBackoff(1)
			if dbErr := insertDeliveryFailure(db, activity.ID, actor.OrgID, f.InboxURL, string(body), err.Error(), nextAttempt); dbErr != nil {
				log.Printf("delivery: record failure for %s: %v", f.InboxURL, dbErr)
			}
		} else {
			deleteDeliveryFailure(db, activity.ID, actor.OrgID, f.InboxURL)
		}
	}
	return nil
}

// deliverEventToTagFollowers sends Create{Note} to all remote actors that
// follow any of the event's tags. Deduplicates by inbox URL so a follower
// of multiple matching tags receives only one copy. Signed by the relay actor.
// The delivered table entry (org_id = -1) is set by the caller.
func deliverEventToTagFollowers(cfg *Config, db *sql.DB, relayActor *ActorRecord, event Event) error {
	// Collect unique inbox URLs across all tags.
	seen := map[string]bool{}
	var targets []struct{ ActorURI, InboxURL string }
	for _, tag := range event.Tags {
		fs, err := listTagFollowers(db, tag)
		if err != nil {
			log.Printf("tag delivery: list followers for %s: %v", tag, err)
			continue
		}
		for _, f := range fs {
			if !seen[f.InboxURL] {
				seen[f.InboxURL] = true
				targets = append(targets, f)
			}
		}
	}
	if len(targets) == 0 {
		return nil
	}

	// Build a Create{Note} attributed to the relay actor.
	activity := buildCreateActivity(cfg, relayActor.OrgSlug, event)
	activity.Context = APContext

	body, err := json.Marshal(activity)
	if err != nil {
		return err
	}

	relayBase := actorURL(cfg, relayActor.OrgSlug)
	keyID := relayBase + "#main-key"

	for _, t := range targets {
		if err := postToInbox(t.InboxURL, keyID, relayActor.PrivateKeyPEM, body); err != nil {
			log.Printf("tag delivery event %d to %s: %v", event.ID, t.InboxURL, err)
		}
	}
	return nil
}

func deliverEventToFollowers(cfg *Config, db *sql.DB, orgID int, event Event) {
	actor, err := getActorByOrgID(db, orgID)
	if err != nil {
		return
	}
	activity := buildCreateActivity(cfg, actor.OrgSlug, event)
	deliverToFollowers(cfg, db, actor, activity)
}

// deliverRelayProfileUpdate tells existing relay followers to refresh the
// synthetic actor after its avatar or banner changes.
func deliverRelayProfileUpdate(cfg *Config, db *sql.DB) {
	actor, err := ensureRelayActor(db, cfg.RelayActorName)
	if err != nil {
		log.Printf("relay profile update: load actor: %v", err)
		return
	}
	base := actorURL(cfg, actor.OrgSlug)
	activity := Activity{
		Type:   "Update",
		ID:     base + "/activities/profile-" + strconv.FormatInt(time.Now().UnixNano(), 36),
		Actor:  base,
		Object: relayActorFromRecord(cfg, actor),
		To:     []string{"https://www.w3.org/ns/activitystreams#Public"},
		CC:     []string{base + "/followers"},
	}
	if err := deliverToFollowers(cfg, db, actor, activity); err != nil {
		log.Printf("relay profile update: %v", err)
	}
}

// buildAnnounceActivity wraps the org's Create inside a relay Announce.
// relaySlug signs the outer Announce; orgSlug is preserved in the inner
// Create so remote servers see the original org as the event creator.
func buildAnnounceActivity(cfg *Config, relaySlug, orgSlug string, e Event) Activity {
	base := actorURL(cfg, relaySlug)
	innerCreate := buildCreateActivity(cfg, orgSlug, e)
	return Activity{
		Type:   "Announce",
		ID:     base + "/activities/announce-" + strconv.FormatInt(time.Now().UnixNano(), 36),
		Actor:  base,
		Object: innerCreate,
		To:     []string{"https://www.w3.org/ns/activitystreams#Public"},
		CC:     []string{base + "/followers"},
	}
}

func deliverUpdateToFollowers(cfg *Config, db *sql.DB, client *DansalClient, eventID int) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	event, err := client.GetEvent(ctx, eventID)
	if err != nil || event.OrganizationID == nil {
		return
	}

	// Check if organization changed by looking at delivery history
	history, err := getEventDeliveryHistory(db, eventID)
	if err != nil {
		log.Printf("check event history for transfer: %v", err)
	}

	// Detect organization transfer
	var oldOrgID *int
	for _, h := range history {
		if h.TransferredTo == nil && !h.IsUpdate {
			// Found a non-update delivery - this is the original org
			if h.OrgID != *event.OrganizationID {
				oldOrgID = &h.OrgID
				break
			}
		}
	}

	if oldOrgID != nil {
		// Organization changed - handle transfer
		handleEventTransfer(cfg, db, client, eventID, *oldOrgID, *event.OrganizationID)
		return
	}

	// Normal update delivery
	actor, err := getActorByOrgID(db, *event.OrganizationID)
	if err != nil {
		return
	}
	activity := buildUpdateActivity(cfg, actor.OrgSlug, event)
	if err := deliverToFollowers(cfg, db, actor, activity); err != nil {
		log.Printf("deliver update event %d: %v", eventID, err)
	}
}

// deliverBoardOpenNote sends a single Note to the org's followers when the first
// contact board post for an event goes live. Called in a goroutine.
func deliverBoardOpenNote(cfg *Config, db *sql.DB, orgID, eventID int, eventTitle string) {
	actor, err := getActorByOrgID(db, orgID)
	if err != nil {
		return // org has no AP actor — nothing to do
	}
	activity := buildBoardOpenActivity(cfg, actor.OrgSlug, eventID, eventTitle)
	if err := deliverToFollowers(cfg, db, actor, activity); err != nil {
		log.Printf("board open note delivery event %d org %d: %v", eventID, orgID, err)
	}
}

func buildBoardOpenActivity(cfg *Config, slug string, eventID int, eventTitle string) Activity {
	base := actorURL(cfg, slug)
	eventURL := fmt.Sprintf("https://%s/events/%d", cfg.Domain, eventID)
	noteID := fmt.Sprintf("%s/activities/board-open-%d", eventURL, time.Now().UnixNano())
	note := APNote{
		Type:         "Note",
		ID:           noteID,
		AttributedTo: base,
		Content:      fmt.Sprintf("The board for <strong>%s</strong> is now open — coordinate rides, accommodation and tickets: %s", eventTitle, eventURL),
		Published:    time.Now().UTC().Format(time.RFC3339),
		To:           []string{"https://www.w3.org/ns/activitystreams#Public"},
		CC:           []string{base + "/followers"},
		URL:          eventURL,
	}
	return Activity{
		Type:   "Create",
		ID:     noteID + "/activity",
		Actor:  base,
		Object: note,
		To:     []string{"https://www.w3.org/ns/activitystreams#Public"},
		CC:     []string{base + "/followers"},
	}
}

func deliverDeleteToFollowers(cfg *Config, db *sql.DB, eventID, orgID int) {
	actor, err := getActorByOrgID(db, orgID)
	if err != nil {
		return
	}
	activity := buildDeleteActivity(cfg, actor.OrgSlug, eventID)
	if err := deliverToFollowers(cfg, db, actor, activity); err != nil {
		log.Printf("deliver delete event %d: %v", eventID, err)
	}
}

func postToInbox(inboxURL, keyID, privateKeyPEM string, body []byte) error {
	req, err := http.NewRequest(http.MethodPost, inboxURL, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/activity+json")
	if err := SignRequest(req, keyID, privateKeyPEM, body); err != nil {
		return fmt.Errorf("sign: %w", err)
	}
	resp, err := fedHTTPClient.Do(req)
	if err != nil {
		return err
	}
	resp.Body.Close()
	if resp.StatusCode >= 300 {
		return fmt.Errorf("remote returned %d", resp.StatusCode)
	}
	return nil
}

// handleEventTransfer manages the complete process of transferring an event between organizations
func handleEventTransfer(cfg *Config, db *sql.DB, client *DansalClient, eventID, oldOrgID, newOrgID int) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	// Get current event data
	event, err := client.GetEvent(ctx, eventID)
	if err != nil {
		log.Printf("event transfer: failed to get event %d: %v", eventID, err)
		return
	}

	log.Printf("transferring event %d from org %d to org %d", eventID, oldOrgID, newOrgID)

	// 1. Notify old organization's followers about the transfer
	deliverTransferNotification(cfg, db, event, oldOrgID, newOrgID)

	// 2. Deliver to new organization's followers
	deliverToNewOrganization(cfg, db, event, newOrgID)

	// 3. Update delivery tracking
	markEventTransferred(db, eventID, oldOrgID, newOrgID)
	markDelivered(db, eventID, newOrgID, false) // Mark as delivered to new org
}

// deliverTransferNotification sends Move activities to old organization's followers
func deliverTransferNotification(cfg *Config, db *sql.DB, event Event, oldOrgID, newOrgID int) {
	oldActor, err := getActorByOrgID(db, oldOrgID)
	if err != nil {
		log.Printf("transfer notification: failed to get old actor for org %d: %v", oldOrgID, err)
		return
	}

	newActor, err := getActorByOrgID(db, newOrgID)
	if err != nil {
		log.Printf("transfer notification: failed to get new actor for org %d: %v", newOrgID, err)
		return
	}

	oldEventURL := fmt.Sprintf("https://%s/events/%d", cfg.Domain, event.ID)
	newEventURL := actorURL(cfg, newActor.OrgSlug) // Use new actor's URL

	// Create Move activity
	moveActivity := Activity{
		Context: APContext,
		Type:    "Move",
		ID:      oldEventURL + "/activities/move-" + strconv.FormatInt(time.Now().UnixNano(), 36),
		Actor:   actorURL(cfg, oldActor.OrgSlug),
		Object:  oldEventURL,
		Target:  newEventURL,
		To:      []string{"https://www.w3.org/ns/activitystreams#Public"},
		CC:      []string{actorURL(cfg, oldActor.OrgSlug) + "/followers"},
	}

	body, err := json.Marshal(moveActivity)
	if err != nil {
		log.Printf("transfer notification: failed to marshal move activity: %v", err)
		return
	}

	// Send to old organization's followers
	followers, err := listFollowers(db, oldOrgID)
	if err != nil {
		log.Printf("transfer notification: failed to get followers for org %d: %v", oldOrgID, err)
		return
	}

	for _, f := range followers {
		keyID := actorURL(cfg, oldActor.OrgSlug) + "#main-key"
		if err := postToInbox(f.InboxURL, keyID, oldActor.PrivateKeyPEM, body); err != nil {
			log.Printf("deliver transfer move to %s: %v", f.InboxURL, err)
		}
	}
}

// deliverToNewOrganization delivers Create activity to new organization's followers
func deliverToNewOrganization(cfg *Config, db *sql.DB, event Event, newOrgID int) {
	actor, err := getActorByOrgID(db, newOrgID)
	if err != nil {
		log.Printf("deliver to new org: failed to get actor for org %d: %v", newOrgID, err)
		return
	}

	// Create Create activity from new organization
	activity := buildCreateActivity(cfg, actor.OrgSlug, event)

	// Deliver to new organization's followers
	if err := deliverToFollowers(cfg, db, actor, activity); err != nil {
		log.Printf("deliver transferred event to new org %d: %v", newOrgID, err)
	}
}

// deliverActorMove sends ActivityPub Move activities to inform followers about actor renaming
func deliverActorMove(cfg *Config, db *sql.DB, oldSlug, newSlug string, orgID int) error {
	actor, err := getActorByOrgID(db, orgID)
	if err != nil {
		return err
	}

	oldActorURL := "https://" + cfg.Domain + "/org/" + oldSlug
	newActorURL := "https://" + cfg.Domain + "/org/" + newSlug

	moveActivity := Activity{
		Context: APContext,
		Type:    "Move",
		ID:      newActorURL + "/activities/move-" + strconv.FormatInt(time.Now().UnixNano(), 36),
		Actor:   newActorURL,
		Object:  oldActorURL,
		Target:  newActorURL,
		To:      []string{"https://www.w3.org/ns/activitystreams#Public"},
		CC:      []string{newActorURL + "/followers"},
	}

	body, err := json.Marshal(moveActivity)
	if err != nil {
		return err
	}

	keyID := newActorURL + "#main-key"

	followers, err := listFollowers(db, orgID)
	if err != nil {
		return err
	}

	for _, f := range followers {
		if err := postToInbox(f.InboxURL, keyID, actor.PrivateKeyPEM, body); err != nil {
			log.Printf("deliver move to %s: %v", f.InboxURL, err)
		}
	}

	return nil
}
