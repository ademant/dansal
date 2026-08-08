package main

import (
	"net/http"
	"strings"
	"sync"
)

// /event/{id} page assembly — the concurrent lookups, canManage logic, and
// title building extracted from eventHandler so the handler stays readable
// (#1031).

// eventPageData bundles the per-event data fetched concurrently to render the
// event page.
type eventPageData struct {
	org       *Organization
	slug      string
	posts     []ContactPost
	members   []OrgMember
	tagMap    map[string]Tag
	userOrgs  []Organization
	prevEvent *Event
	nextEvent *Event
}

// fetchEventWithFallback fetches event id, preferring the authed endpoint when
// a session token is present and falling back to the public one.
func fetchEventWithFallback(r *http.Request, client *DansalClient, id int) (Event, error) {
	if token := getSessionToken(r); token != "" {
		if e, err := client.GetEventAuthed(r.Context(), id, token); err == nil {
			return e, nil
		}
	}
	return client.GetEvent(r.Context(), id)
}

// loadEventPageData runs all the independent lookups needed to render the
// event page concurrently.
func loadEventPageData(r *http.Request, client *DansalClient, event Event, su *SessionUser) eventPageData {
	token := getSessionToken(r)
	needMembers := su != nil && su.Role != "admin" && event.OrganizationID != nil

	var data eventPageData
	var wg sync.WaitGroup
	wg.Add(1)
	go func() { defer wg.Done(); data.tagMap, _ = client.GetTagMap(r.Context()) }()
	if event.OrganizationID != nil {
		wg.Add(1)
		go func() {
			defer wg.Done()
			o, err := client.GetOrganization(r.Context(), *event.OrganizationID)
			if err == nil {
				data.org = &o
				data.slug = effectiveSlug(o)
			}
		}()
	}
	wg.Add(1)
	go func() {
		defer wg.Done()
		data.posts, _ = client.GetContactPosts(r.Context(), event.ID)
	}()
	if needMembers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			ms, err := client.GetOrganizationMembers(r.Context(), *event.OrganizationID, token)
			if err == nil {
				data.members = ms
			}
		}()
	}
	// Series siblings for prev/next navigation.
	if event.SeriesID != nil {
		wg.Add(1)
		go func() {
			defer wg.Done()
			siblings, err := client.GetEventsBySeries(r.Context(), *event.SeriesID)
			if err != nil {
				return
			}
			for i, e := range siblings {
				if e.ID == event.ID {
					if i > 0 {
						prev := siblings[i-1]
						data.prevEvent = &prev
					}
					if i < len(siblings)-1 {
						next := siblings[i+1]
						data.nextEvent = &next
					}
					break
				}
			}
		}()
	}
	// For logged-in users: fetch their orgs (for assign/publish flow and save-as-template).
	if su != nil {
		wg.Add(1)
		go func() {
			defer wg.Done()
			allOrgs, _ := client.GetOrganizations(r.Context())
			if su.Role == "admin" {
				data.userOrgs = allOrgs
			} else {
				orgIDs := getUserOrgIDs(r.Context(), client, su.ID, token)
				idSet := make(map[int]bool, len(orgIDs))
				for _, oid := range orgIDs {
					idSet[oid] = true
				}
				for _, o := range allOrgs {
					if idSet[o.ID] {
						data.userOrgs = append(data.userOrgs, o)
					}
				}
			}
		}()
	}
	wg.Wait()
	return data
}

// eventCanManage reports whether su may manage event's board and bookings.
func eventCanManage(su *SessionUser, event Event, members []OrgMember) bool {
	if su == nil {
		return false
	}
	if su.Role == "admin" {
		return true
	}
	if event.OrganizationID == nil {
		return false
	}
	for _, m := range members {
		if m.UserID == su.ID {
			return true
		}
	}
	return false
}

// eventPageTitle builds the "Title – Town, Date" page title.
func eventPageTitle(event Event, lang string) string {
	var suffix []string
	if event.Location != nil && event.Location.Town != "" {
		suffix = append(suffix, event.Location.Town)
	}
	if d := formatDateStr(lang, event.StartTime); d != event.StartTime {
		suffix = append(suffix, d)
	}
	if len(suffix) == 0 {
		return event.Title
	}
	return event.Title + " – " + strings.Join(suffix, ", ")
}
