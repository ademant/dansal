package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
)

// #1427: client side of the duplicate-pair review (cmd/dansal duplicates.go).

// DuplicateConflict is another event colliding with the checked one.
type DuplicateConflict struct {
	ID      int      `json:"id"`
	Title   string   `json:"title"`
	Reasons []string `json:"reasons"`
}

// DuplicateCheck mirrors the API's GET /events/{id}/duplicate-check result.
type DuplicateCheck struct {
	EventID      int                 `json:"event_id"`
	Flagged      bool                `json:"flagged"`
	PartnerID    int                 `json:"partner_id,omitempty"`
	PartnerTitle string              `json:"partner_title,omitempty"`
	Collides     bool                `json:"collides"`
	Reasons      []string            `json:"reasons"`
	Others       []DuplicateConflict `json:"other_conflicts"`
}

// errDuplicateStillColliding is returned by ResolveDuplicate("resolved")
// when the pair still collides; the accompanying DuplicateCheck says why.
var errDuplicateStillColliding = errors.New("duplicate pair still collides")

func (c *DansalClient) GetDuplicateCheck(ctx context.Context, eventID int, token string) (DuplicateCheck, error) {
	var out DuplicateCheck
	return out, c.do(ctx, http.MethodGet, fmt.Sprintf("/api/v1/events/%d/duplicate-check", eventID), token, nil, &out)
}

// ResolveDuplicate clears a flagged pair: mode "accept" unconditionally,
// "resolved" only when it no longer collides (409 → errDuplicateStillColliding
// plus the current check). Kept on c.authed for the 409 handling.
func (c *DansalClient) ResolveDuplicate(ctx context.Context, eventID int, mode, token string) (DuplicateCheck, error) {
	body, _ := json.Marshal(map[string]string{"mode": mode})
	resp, err := c.authed(ctx, http.MethodPost, fmt.Sprintf("/api/v1/events/%d/duplicate-resolve", eventID), token, body)
	if err != nil {
		return DuplicateCheck{}, err
	}
	defer resp.Body.Close()
	switch resp.StatusCode {
	case http.StatusNoContent:
		return DuplicateCheck{}, nil
	case http.StatusConflict:
		var out DuplicateCheck
		json.NewDecoder(resp.Body).Decode(&out)
		return out, errDuplicateStillColliding
	}
	return DuplicateCheck{}, apiErr(resp)
}

// PatchEventLocation moves an event to another location (venue or room).
func (c *DansalClient) PatchEventLocation(ctx context.Context, eventID, locationID int, token string) error {
	body, _ := json.Marshal(map[string]int{"location_id": locationID})
	req, err := http.NewRequestWithContext(ctx, http.MethodPatch,
		fmt.Sprintf("%s/api/v1/events/%d", c.BaseURL, eventID), bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/merge-patch+json")
	req.Header.Set("Authorization", "Bearer "+token)
	c.setInternalHeader(req)
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return apiErr(resp)
	}
	return nil
}
