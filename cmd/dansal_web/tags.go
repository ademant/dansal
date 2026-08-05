package main

import (
	"fmt"
	"net/http"
	"net/url"
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

		events, _ := client.GetEventsFiltered(r.Context(), url.Values{"tag": {slug}, "is_published": {"true"}})
		if events == nil {
			events = []Event{}
		}

		if isAPRequest(r) {
			serveTagCollection(w, r, cfg, client, slug, events)
			return
		}

		title := tag.Name
		if title == "" {
			title = slug
		}
		td := tmplData(r, cfg, i18n, title, TagPageData{Tag: tag, Events: events})
		td.MetaDescription = metaDesc(title, 155)
		renderTemplate(w, tmpls.tag, td)
	}
}

// serveTagCollection renders events tagged with slug as an ActivityPub
// OrderedCollection, paged exactly like outboxHandler (?page=true fetches
// the embedded Note objects; the base request just returns totalItems +
// first). Each Note is attributed to its owning organization's actor when
// resolvable, or the relay actor otherwise — the same fallback the relay
// actor already provides for site-wide federation aggregation.
func serveTagCollection(w http.ResponseWriter, r *http.Request, cfg *Config, client *DansalClient, slug string, events []Event) {
	base := "https://" + cfg.Domain + "/tags/" + slug

	if r.URL.Query().Get("page") != "true" {
		col := OrderedCollection{
			Context:    APContext,
			Type:       "OrderedCollection",
			ID:         base,
			TotalItems: len(events),
			First:      base + "?page=true",
		}
		writeJSON(w, http.StatusOK, col)
		return
	}

	orgs, _ := client.GetOrganizations(r.Context())
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
