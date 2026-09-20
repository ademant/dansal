package main

import (
	"fmt"
	"net/url"
	"strings"
)

// MediaLink is one external link (video, audio, image or other) attached to a
// musician, organization or location (#1360/#1361). It is only ever rendered
// as a plain link: nothing is embedded or hotlinked, so a visitor's browser
// never contacts the target site unless they click through.
type MediaLink struct {
	Kind  string `json:"kind"`
	Title string `json:"title"`
	URL   string `json:"url"`
}

const (
	ownerTypeMusician     = "musician"
	ownerTypeOrganization = "organization"
	ownerTypeLocation     = "location"

	maxMediaLinksPerOwner = 20
	maxMediaURLLen        = 2048
	maxMediaTitleLen      = 120
)

// mediaKinds is the closed set of kinds; the kind only picks the icon shown
// next to the link.
var mediaKinds = map[string]bool{"video": true, "audio": true, "image": true, "other": true}

// normalizeMediaLinks trims and validates a submitted list. Blank rows (no
// URL) are dropped so a half-filled form row doesn't fail the whole save;
// everything else must be an absolute https:// URL with a host and no
// embedded credentials. The returned slice keeps the submitted order, which
// becomes the stored sort_order.
func normalizeMediaLinks(in []MediaLink) ([]MediaLink, error) {
	out := make([]MediaLink, 0, len(in))
	for i, l := range in {
		rawURL := strings.TrimSpace(l.URL)
		if rawURL == "" {
			continue
		}
		if len(rawURL) > maxMediaURLLen {
			return nil, fmt.Errorf("media[%d]: url too long", i)
		}
		u, err := url.Parse(rawURL)
		if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil {
			return nil, fmt.Errorf("media[%d]: url must be an absolute https:// link", i)
		}
		kind := strings.ToLower(strings.TrimSpace(l.Kind))
		if kind == "" {
			kind = "other"
		}
		if !mediaKinds[kind] {
			return nil, fmt.Errorf("media[%d]: unknown kind %q", i, l.Kind)
		}
		title := strings.TrimSpace(l.Title)
		if len([]rune(title)) > maxMediaTitleLen {
			return nil, fmt.Errorf("media[%d]: title too long", i)
		}
		out = append(out, MediaLink{Kind: kind, Title: title, URL: rawURL})
	}
	if len(out) > maxMediaLinksPerOwner {
		return nil, fmt.Errorf("too many media links (max %d)", maxMediaLinksPerOwner)
	}
	return out, nil
}

// loadOwnerMedia returns an owner's links in display order.
func loadOwnerMedia(q querier, ownerType string, ownerID int) []MediaLink {
	rows, err := q.Query(
		`SELECT kind, title, url FROM owner_media
		 WHERE owner_type = ? AND owner_id = ? ORDER BY sort_order, id`,
		ownerType, ownerID,
	)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var links []MediaLink
	for rows.Next() {
		var l MediaLink
		if rows.Scan(&l.Kind, &l.Title, &l.URL) == nil {
			links = append(links, l)
		}
	}
	return links
}

// replaceOwnerMedia swaps an owner's whole list for links (already passed
// through normalizeMediaLinks). Order in the slice becomes sort_order.
func replaceOwnerMedia(q querier, ownerType string, ownerID int, links []MediaLink) error {
	if _, err := q.Exec("DELETE FROM owner_media WHERE owner_type = ? AND owner_id = ?", ownerType, ownerID); err != nil {
		return err
	}
	for i, l := range links {
		if _, err := q.Exec(
			`INSERT INTO owner_media (owner_type, owner_id, kind, title, url, sort_order) VALUES (?, ?, ?, ?, ?, ?)`,
			ownerType, ownerID, l.Kind, l.Title, l.URL, i,
		); err != nil {
			return err
		}
	}
	return nil
}

// deleteOwnerMedia drops an owner's links. owner_media is polymorphic (no
// foreign key), so owner delete handlers must call this explicitly.
func deleteOwnerMedia(q querier, ownerType string, ownerID int) {
	q.Exec("DELETE FROM owner_media WHERE owner_type = ? AND owner_id = ?", ownerType, ownerID)
}

// mergeOwnerMedia folds dropID's links into keepID's (keep's first, then
// drop's, skipping URLs keep already has, capped at maxMediaLinksPerOwner) and
// removes dropID's rows — used when two owners are merged so the surviving
// one doesn't lose the links the other had collected.
func mergeOwnerMedia(q querier, ownerType string, keepID, dropID int) error {
	combined := loadOwnerMedia(q, ownerType, keepID)
	seen := make(map[string]bool, len(combined))
	for _, l := range combined {
		seen[l.URL] = true
	}
	for _, l := range loadOwnerMedia(q, ownerType, dropID) {
		if !seen[l.URL] && len(combined) < maxMediaLinksPerOwner {
			combined = append(combined, l)
			seen[l.URL] = true
		}
	}
	if err := replaceOwnerMedia(q, ownerType, keepID, combined); err != nil {
		return err
	}
	deleteOwnerMedia(q, ownerType, dropID)
	return nil
}

// locationAndChildIDs returns a location's id plus those of its child rooms
// (deleting a building cascades to its rooms via parent_id, so their links
// must go with it). Call before the delete: the children are gone afterwards.
func locationAndChildIDs(q querier, locationID int) []int {
	ids := []int{locationID}
	rows, err := q.Query("SELECT id FROM locations WHERE parent_id = ?", locationID)
	if err != nil {
		return ids
	}
	defer rows.Close()
	for rows.Next() {
		var id int
		if rows.Scan(&id) == nil {
			ids = append(ids, id)
		}
	}
	return ids
}
