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

func pollAndDeliver(cfg *Config, db *sql.DB, client *DansalClient, relayActor *ActorRecord, since time.Time) {
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

		// Deliver to org followers
		if !isDelivered(db, e.ID, orgID) {
			actor, err := getActorByOrgID(db, orgID)
			if err == nil {
				activity := buildCreateActivity(cfg, actor.OrgSlug, e)
				if err := deliverToFollowers(cfg, db, actor, activity); err != nil {
					log.Printf("delivery event %d org %d: %v", e.ID, orgID, err)
				} else {
					if err := markDelivered(db, e.ID, orgID); err != nil {
						log.Printf("mark delivered event %d org %d: %v", e.ID, orgID, err)
					}
				}
			}
		}

		// Additionally deliver to relay followers wrapped in Announce{Create{Event}}
		if relayActor != nil && !isDelivered(db, e.ID, 0) {
			activity := buildAnnounceActivity(cfg, relayActor.OrgSlug, e)
			if err := deliverToFollowers(cfg, db, relayActor, activity); err != nil {
				log.Printf("relay delivery event %d: %v", e.ID, err)
			} else {
				if err := markDelivered(db, e.ID, 0); err != nil {
					log.Printf("mark relay delivered event %d: %v", e.ID, err)
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

	for _, f := range followers {
		if err := postToInbox(f.InboxURL, keyID, actor.PrivateKeyPEM, body); err != nil {
			log.Printf("deliver to %s: %v", f.InboxURL, err)
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

func buildAnnounceActivity(cfg *Config, slug string, e Event) Activity {
	base := actorURL(cfg, slug)
	innerCreate := buildCreateActivity(cfg, slug, e)
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
	markDeliveredWithType(db, eventID, newOrgID, false) // Mark as delivered to new org
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
		Type:   "Move",
		ID:     oldEventURL + "/activities/move-" + strconv.FormatInt(time.Now().UnixNano(), 36),
		Actor:  actorURL(cfg, oldActor.OrgSlug),
		Object: oldEventURL,
		Target: newEventURL,
		To:     []string{"https://www.w3.org/ns/activitystreams#Public"},
		CC:     []string{actorURL(cfg, oldActor.OrgSlug) + "/followers"},
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
		Type:   "Move",
		ID:     newActorURL + "/activities/move-" + strconv.FormatInt(time.Now().UnixNano(), 36),
		Actor:  newActorURL,
		Object: oldActorURL,
		Target: newActorURL,
		To:     []string{"https://www.w3.org/ns/activitystreams#Public"},
		CC:     []string{newActorURL + "/followers"},
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
