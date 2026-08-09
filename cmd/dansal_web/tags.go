package main

import (
	"fmt"
	"log"
	"net/http"
	"strings"
)

// TagCatalogGroup groups tags by category for the /tags catalog view.
type TagCatalogGroup struct {
	Category string
	Tags     []Tag
}

// TagsIndexData is the template data for GET /tags (HTML view).
type TagsIndexData struct {
	Groups []TagCatalogGroup
}

// tagsIndexHandler serves GET /tags — content-negotiated:
//   - ActivityPub: OrderedCollection of all tag collection URIs (#955)
//   - HTML: browsable tag catalog with event counts (#961)
func tagsIndexHandler(cfg *Config, tmpls *Templates, client *DansalClient, i18n *I18n) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		tags, err := client.GetTags(r.Context())
		if err != nil {
			http.Error(w, "could not load tags", http.StatusBadGateway)
			return
		}

		if isAPRequest(r) {
			items := make([]string, len(tags))
			for i, t := range tags {
				items[i] = fmt.Sprintf("https://%s/tags/%s", cfg.Domain, t.Slug)
			}
			col := map[string]any{
				"@context":     APContext,
				"type":         "OrderedCollection",
				"id":           fmt.Sprintf("https://%s/tags", cfg.Domain),
				"totalItems":   len(tags),
				"orderedItems": items,
			}
			writeJSON(w, http.StatusOK, col)
			return
		}

		// Group by category (API already orders by category, name).
		var groups []TagCatalogGroup
		for _, t := range tags {
			if len(groups) == 0 || groups[len(groups)-1].Category != t.Category {
				groups = append(groups, TagCatalogGroup{Category: t.Category})
			}
			groups[len(groups)-1].Tags = append(groups[len(groups)-1].Tags, t)
		}

		td := tmplData(r, cfg, i18n, i18n.Strings(i18n.detectLang(r)).T("tags_title"), TagsIndexData{Groups: groups})
		renderTemplate(w, tmpls.tagsIndex, td)
	}
}

// TagPageData is the template data for GET /tags/{slug}.
type TagPageData struct {
	Tag    Tag
	Events []Event
}

// tagHandler serves GET /tags/{slug} — content-negotiated like /org/{name}
// (#947): an ActivityPub OrderedCollection of Note objects for
// Accept: application/activity+json (or ld+json), an HTML listing page
// otherwise. The tag vocabulary lives in the dansal API's tags table
// (client.GetTagMap) — the single source of truth already used for
// validateTags() on the write side — so an unknown slug 404s instead of
// rendering an empty page for arbitrary input (issue #949).
func tagHandler(cfg *Config, tmpls *Templates, client *DansalClient, i18n *I18n) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		slug := r.PathValue("slug")
		tagMap, err := client.GetTagMap(r.Context())
		if err != nil {
			http.Error(w, "could not load tags", http.StatusBadGateway)
			return
		}
		tag, ok := tagMap[slug]
		if !ok {
			http.NotFound(w, r)
			return
		}

		events, err := client.GetEventsFiltered(r.Context(), (EventFilter{IsPublished: true, Tag: slug}).Values())
		if err != nil {
			logHTTPError(w, r, "could not load tag events", http.StatusBadGateway)
			return
		}
		if events == nil {
			events = []Event{}
		}

		if isAPRequest(r) {
			serveTagCollection(w, r, cfg, client, slug, tag, events)
			return
		}

		title := tag.Name
		if title == "" {
			title = slug
		}
		td := tmplData(r, cfg, i18n, title, TagPageData{Tag: tag, Events: events})
		td.MetaDescription = metaDesc(title, metaDescMaxLen)
		renderTemplate(w, tmpls.tag, td)
	}
}

// serveTagCollection renders events tagged with slug as an ActivityPub
// Collection actor (#957). The base request returns a collection actor object
// with inbox/followers/outbox links so remote servers can follow the tag;
// ?page=true embeds the event Note objects for the timeline pager. Each Note
// is attributed to its owning organization's actor when resolvable, or the
// relay actor otherwise.
func serveTagCollection(w http.ResponseWriter, r *http.Request, cfg *Config, client *DansalClient, slug string, tag Tag, events []Event) {
	base := "https://" + cfg.Domain + "/tags/" + slug

	if r.URL.Query().Get("page") != "true" {
		name := tag.Name
		if name == "" {
			name = slug
		}
		// Return an actor-like OrderedCollection so remote servers can follow
		// this tag. The inbox and followers links enable AP Follow/Undo handling.
		col := map[string]any{
			"@context":     APContext,
			"type":         "OrderedCollection",
			"id":           base,
			"name":         "#" + name,
			"summary":      fmt.Sprintf("Events tagged #%s", name),
			"url":          base,
			"totalItems":   len(events),
			"first":        base + "?page=true",
			"inbox":        base + "/inbox",
			"followers":    base + "/followers",
			"discoverable": true,
			"indexable":    true,
		}
		writeJSON(w, http.StatusOK, col)
		return
	}

	orgs, err := client.GetOrganizations(r.Context())
	if err != nil {
		log.Printf("tag collection: could not load organizations: %v", err)
	}
	slugByOrgID := make(map[int]string, len(orgs))
	for _, o := range orgs {
		slugByOrgID[o.ID] = effectiveSlug(o)
	}

	items := make([]any, 0, len(events))
	for _, e := range events {
		actorSlug := cfg.RelayActorName
		if e.OrganizationID != nil {
			if s, ok := slugByOrgID[*e.OrganizationID]; ok {
				actorSlug = s
			}
		}
		items = append(items, buildNoteFromEvent(cfg, actorSlug, e))
	}

	page := OrderedCollectionPage{
		Context:      APContext,
		Type:         "OrderedCollectionPage",
		ID:           base + "?page=true",
		PartOf:       base,
		TotalItems:   len(items),
		OrderedItems: items,
	}
	writeJSON(w, http.StatusOK, page)
}

// tagSearchHandler serves GET /api/v1/tags/search?q=... — returns the subset
// of known tags whose name or slug contains the query string (case-insensitive).
// Useful for autocomplete and external tag discovery (#960).
func tagSearchHandler(client *DansalClient) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query().Get("q")
		if q == "" {
			writeJSONError(w, r, http.StatusBadRequest, "query parameter q is required")
			return
		}

		tags, err := client.GetTags(r.Context())
		if err != nil {
			writeJSONError(w, r, http.StatusBadGateway, "could not load tags")
			return
		}

		qLower := strings.ToLower(q)
		matches := make([]Tag, 0)
		for _, t := range tags {
			if strings.Contains(strings.ToLower(t.Name), qLower) ||
				strings.Contains(strings.ToLower(t.Slug), qLower) {
				matches = append(matches, t)
			}
		}

		writeJSON(w, http.StatusOK, matches)
	}
}
