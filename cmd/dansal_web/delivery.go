package main

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"github.com/ademant/dansal/internal/logging"
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

// #1363: reserved org_id sentinels in the `delivered` table for the
// poll-driven IndexNow pings above — distinct from #956's tag-delivery
// sentinel (org_id=-1) and the relay sentinel (org_id=0).
const (
	indexNowPublishedSentinel = -4
	indexNowCancelledSentinel = -5
)

// deliveredOrgIsReal reports whether a delivered row names a real org rather
// than a reserved sentinel: 0 (relay), -1 (tag followers), -4/-5 (IndexNow).
// Sentinel rows are never the event's original org, so they must not be read
// as a transfer (#1677).
func deliveredOrgIsReal(orgID int) bool {
	return orgID > 0
}

// findTransferOrigin returns the org that first received the event when that
// org differs from currentOrgID, i.e. the event was transferred. Sentinel rows
// are skipped.
func findTransferOrigin(history []deliveredRow, currentOrgID int) (int, bool) {
	for _, h := range history {
		if h.TransferredTo != nil || h.IsUpdate || !deliveredOrgIsReal(h.OrgID) {
			continue
		}
		if h.OrgID != currentOrgID {
			return h.OrgID, true
		}
	}
	return 0, false
}

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
		logging.Errorf("delivery retry: list pending: %v", err)
		return
	}
	for _, f := range failures {
		actor, actorErr := getActorByOrgID(db, f.OrgID)
		var postErr error
		if actorErr != nil {
			// #1440 (G3): an org whose actor row is gone (e.g. the org itself
			// was deleted — delivery_failures has no FK/cascade to actors)
			// must still count toward give-up like any other failure;
			// `continue`ing here left such a row's attempts/next_attempt_at
			// frozen forever, so it was never swept.
			logging.Errorf("delivery retry: get actor org %d: %v", f.OrgID, actorErr)
			postErr = actorErr
		} else {
			keyID := actorKeyID(cfg, actor.OrgSlug)
			postErr = postToInbox(context.Background(), f.InboxURL, keyID, actor.PrivateKeyPEM, []byte(f.ActivityJSON))
		}
		if postErr != nil {
			newAttempts := f.Attempts + 1
			if newAttempts > maxDeliveryAttempts {
				logging.Warnf("delivery retry: giving up on %s after %d attempts: %v", f.InboxURL, f.Attempts, postErr)
				deleteDeliveryFailure(db, f.ActivityID, f.OrgID, f.InboxURL)
				// #1490 (G12): a give-up here means this inbox has failed
				// maxDeliveryAttempts times in a row; count it toward
				// removing the follower rather than retaining a dead
				// actor_uri/inbox_url indefinitely.
				removed, bumpErr := bumpFollowerFailCount(db, f.OrgID, f.InboxURL, cfg.FollowerMaxFailures)
				if bumpErr != nil {
					logging.Errorf("delivery retry: bump follower fail count for %s: %v", f.InboxURL, bumpErr)
				} else if removed {
					logging.Warnf("delivery retry: removed follower %s after %d give-ups", f.InboxURL, cfg.FollowerMaxFailures)
				}
			} else {
				nextAttempt := time.Now().Unix() + deliveryBackoff(newAttempts)
				if dbErr := updateDeliveryFailure(db, f.ActivityID, f.OrgID, f.InboxURL, postErr.Error(), newAttempts, nextAttempt); dbErr != nil {
					logging.Errorf("delivery retry: update record: %v", dbErr)
				}
			}
		} else {
			logging.Debugf("delivery retry: succeeded to %s (activity %s)", f.InboxURL, f.ActivityID)
			deleteDeliveryFailure(db, f.ActivityID, f.OrgID, f.InboxURL)
			// #1490 (G12): a successful retry means this inbox is alive
			// again — don't let fail_count accumulate across unrelated
			// outages toward removal.
			if err := resetFollowerFailCount(db, f.OrgID, f.InboxURL); err != nil {
				logging.Errorf("delivery retry: reset follower fail count for %s: %v", f.InboxURL, err)
			}
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
		logging.Errorf("delivery poll: %v", err)
		return
	}

	for _, e := range events {
		// #1363: ping IndexNow for a publish/cancel regardless of which
		// client made the change. The admin UI already does this
		// synchronously at save time (admin_events.go), but a write coming
		// straight through the API (e.g. wp-dansal flipping is_published/
		// is_cancelled via PATCH) never touches dansal_web at all, so
		// nothing pings IndexNow for it. This poll already re-fetches every
		// upcoming published event each cycle, so it doubles as the catch:
		// reuse the `delivered` table's org_id-sentinel convention (see
		// #956's tag delivery, org_id=-1) to ping each transition once.
		if !isDelivered(db, e.ID, indexNowPublishedSentinel) {
			go notifyIndexNow(cfg.publicBaseURL(), siteCfg.IndexNowKey(), []int{e.ID})
			markDelivered(db, e.ID, indexNowPublishedSentinel, false)
		}
		if e.IsCancelled && !isDelivered(db, e.ID, indexNowCancelledSentinel) {
			go notifyIndexNow(cfg.publicBaseURL(), siteCfg.IndexNowKey(), []int{e.ID})
			markDelivered(db, e.ID, indexNowCancelledSentinel, false)
		}

		if e.OrganizationID == nil {
			continue
		}
		orgID := *e.OrganizationID
		actor, actorErr := getActorByOrgID(db, orgID)

		// Deliver to org followers
		if actorErr == nil && !isDelivered(db, e.ID, orgID) {
			activity := buildCreateActivity(cfg, actor.OrgSlug, e)
			if err := deliverActivityToFollowers(cfg, db, actor, activity); err != nil {
				logging.Errorf("delivery event %d org %d: %v", e.ID, orgID, err)
			} else {
				if err := markDelivered(db, e.ID, orgID, false); err != nil {
					logging.Errorf("mark delivered event %d org %d: %v", e.ID, orgID, err)
				}
			}
		}

		// Relay Announce{Create{Event}}: relay actor signs the outer activity,
		// but the inner Create preserves the org's identity and attribution.
		if relayActor != nil && actorErr == nil && !isDelivered(db, e.ID, 0) {
			activity := buildAnnounceActivity(cfg, relayActor.OrgSlug, actor.OrgSlug, e)
			if err := deliverActivityToFollowers(cfg, db, relayActor, activity); err != nil {
				logging.Errorf("relay delivery event %d: %v", e.ID, err)
			} else {
				if err := markDelivered(db, e.ID, 0, false); err != nil {
					logging.Errorf("mark relay delivered event %d: %v", e.ID, err)
				}
			}
		}

		// Tag follower delivery (#956): deliver Create{Note} to remote actors
		// that follow any of the event's tags. Uses org_id = -1 as a sentinel
		// in the delivered table so each event is delivered at most once.
		if relayActor != nil && len(e.Tags) > 0 && !isDelivered(db, e.ID, -1) {
			if err := deliverEventToTagFollowers(cfg, db, relayActor, e); err != nil {
				logging.Errorf("tag delivery event %d: %v", e.ID, err)
			} else {
				if err := markDelivered(db, e.ID, -1, false); err != nil {
					logging.Errorf("mark tag delivered event %d: %v", e.ID, err)
				}
			}
		}
	}
}

// deliverActivityToFollowers signs and POSTs activity to every org follower,
// recording per-follower failures in delivery_failures so the retry loop can
// pick them up later.
func deliverActivityToFollowers(cfg *Config, db *sql.DB, actor *ActorRecord, activity Activity) error {
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

	keyID := actorKeyID(cfg, actor.OrgSlug)
	now := time.Now().Unix()

	for _, f := range followers {
		if err := postToInbox(context.Background(), f.InboxURL, keyID, actor.PrivateKeyPEM, body); err != nil {
			logging.Errorf("deliver to %s: %v", f.InboxURL, err)
			nextAttempt := now + deliveryBackoff(1)
			if dbErr := insertDeliveryFailure(db, activity.ID, actor.OrgID, f.InboxURL, string(body), err.Error(), nextAttempt); dbErr != nil {
				logging.Errorf("delivery: record failure for %s: %v", f.InboxURL, dbErr)
			}
		} else {
			deleteDeliveryFailure(db, activity.ID, actor.OrgID, f.InboxURL)
			if dbErr := resetFollowerFailCount(db, actor.OrgID, f.InboxURL); dbErr != nil {
				logging.Errorf("delivery: reset follower fail count for %s: %v", f.InboxURL, dbErr)
			}
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
			logging.Errorf("tag delivery: list followers for %s: %v", tag, err)
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

	keyID := actorKeyID(cfg, relayActor.OrgSlug)

	for _, t := range targets {
		if err := postToInbox(context.Background(), t.InboxURL, keyID, relayActor.PrivateKeyPEM, body); err != nil {
			logging.Errorf("tag delivery event %d to %s: %v", event.ID, t.InboxURL, err)
		}
	}
	return nil
}

// deliverRelayProfileUpdate tells existing relay followers to refresh the
// synthetic actor after its avatar or banner changes.
func deliverRelayProfileUpdate(cfg *Config, db *sql.DB) {
	actor, err := ensureRelayActor(db, cfg.RelayActorName)
	if err != nil {
		logging.Errorf("relay profile update: load actor: %v", err)
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
	if err := deliverActivityToFollowers(cfg, db, actor, activity); err != nil {
		logging.Errorf("relay profile update: %v", err)
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
		logging.Errorf("check event history for transfer: %v", err)
	}

	// Organization changed - handle transfer
	if oldOrgID, ok := findTransferOrigin(history, *event.OrganizationID); ok {
		handleEventTransfer(cfg, db, client, eventID, oldOrgID, *event.OrganizationID)
		return
	}

	// Normal update delivery
	actor, err := getActorByOrgID(db, *event.OrganizationID)
	if err != nil {
		return
	}
	activity := buildUpdateActivity(cfg, actor.OrgSlug, event)
	if err := deliverActivityToFollowers(cfg, db, actor, activity); err != nil {
		logging.Errorf("deliver update event %d: %v", eventID, err)
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
	if err := deliverActivityToFollowers(cfg, db, actor, activity); err != nil {
		logging.Errorf("board open note delivery event %d org %d: %v", eventID, orgID, err)
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
	if err := deliverActivityToFollowers(cfg, db, actor, activity); err != nil {
		logging.Errorf("deliver delete event %d: %v", eventID, err)
	}
}

func postToInbox(ctx context.Context, inboxURL, keyID, privateKeyPEM string, body []byte) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, inboxURL, bytes.NewReader(body))
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
		logging.Errorf("event transfer: failed to get event %d: %v", eventID, err)
		return
	}

	logging.Infof("transferring event %d from org %d to org %d", eventID, oldOrgID, newOrgID)

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
		logging.Errorf("transfer notification: failed to get old actor for org %d: %v", oldOrgID, err)
		return
	}

	newActor, err := getActorByOrgID(db, newOrgID)
	if err != nil {
		logging.Errorf("transfer notification: failed to get new actor for org %d: %v", newOrgID, err)
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

	// deliverActivityToFollowers records per-follower failures in
	// delivery_failures so Move posts that fail get retried like any other.
	if err := deliverActivityToFollowers(cfg, db, oldActor, moveActivity); err != nil {
		logging.Errorf("transfer notification: %v", err)
	}
}

// deliverToNewOrganization delivers Create activity to new organization's followers
func deliverToNewOrganization(cfg *Config, db *sql.DB, event Event, newOrgID int) {
	actor, err := getActorByOrgID(db, newOrgID)
	if err != nil {
		logging.Errorf("deliver to new org: failed to get actor for org %d: %v", newOrgID, err)
		return
	}

	// Create Create activity from new organization
	activity := buildCreateActivity(cfg, actor.OrgSlug, event)

	// Deliver to new organization's followers
	if err := deliverActivityToFollowers(cfg, db, actor, activity); err != nil {
		logging.Errorf("deliver transferred event to new org %d: %v", newOrgID, err)
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

	// deliverActivityToFollowers records per-follower failures in
	// delivery_failures so Move posts that fail get retried like any other.
	return deliverActivityToFollowers(cfg, db, actor, moveActivity)
}
