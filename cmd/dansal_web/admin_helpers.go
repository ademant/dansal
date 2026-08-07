package main

import (
	"net/http"
	"time"
)

const (
	// maxMultipartSize caps admin create/save form uploads; the forms are
	// multipart because they carry image uploads.
	maxMultipartSize = 10 << 20
	// maxFeedBodySize caps fetched feed bodies in the enrich handler.
	maxFeedBodySize = 8 << 20
	// fdpMatchWindow is the ±3h start-time window for matching
	// folkdance.page events against calendar events.
	fdpMatchWindow = 3 * 3600
)

// adminWriteDeadline extends this request's write deadline for the slow AVIF
// re-encode triggered by admin image uploads (WASM-based encoder; a detailed
// photo can blow past the server's default 30s WriteTimeout). Applied
// per-request, not server-wide.
func adminWriteDeadline(w http.ResponseWriter) {
	_ = http.NewResponseController(w).SetWriteDeadline(time.Now().Add(170 * time.Second))
}

// forbidden writes a uniform 403 response. Both the HTML (http.Error
// "Forbidden") and JSON ({"error":"forbidden"}) handlers converged on this.
func forbidden(w http.ResponseWriter, r *http.Request) {
	writeJSONError(w, r, http.StatusForbidden, "forbidden")
}

// parseAdminMultipart parses r's body as multipart/form-data up to
// maxMultipartSize, falling back to ParseForm for non-multipart posts.
func parseAdminMultipart(r *http.Request) error {
	if err := r.ParseMultipartForm(maxMultipartSize); err != nil {
		return r.ParseForm()
	}
	return nil
}

// memberOrgSet returns the set of org IDs the user belongs to, or nil for
// admins (meaning "no org restriction"). Replaces the bespoke
// userOrgSet/myOrgIDs builders in the locations, orgs, users and fetchurls
// list handlers.
func memberOrgSet(r *http.Request, client *DansalClient, su *SessionUser) map[int]bool {
	if su.Role == "admin" {
		return nil
	}
	return orgIDSet(getUserOrgIDs(r.Context(), client, su.ID, getSessionToken(r)))
}

// memberOrgIDs returns the user's org memberships as a slice (empty for
// admins, who are handled separately in most call sites).
func memberOrgIDs(r *http.Request, client *DansalClient, su *SessionUser) []int {
	return getUserOrgIDs(r.Context(), client, su.ID, getSessionToken(r))
}

// filterByUserOrgs returns the subset of items whose org assignment overlaps
// the user's orgs. memberSet == nil (admin) passes everything through.
func filterByUserOrgs[T any](items []T, orgIDsOf func(T) []int, memberSet map[int]bool) []T {
	if memberSet == nil {
		return items
	}
	out := make([]T, 0, len(items))
	for _, it := range items {
		for _, oid := range orgIDsOf(it) {
			if memberSet[oid] {
				out = append(out, it)
				break
			}
		}
	}
	return out
}

// orgIDsForTemplates returns the org IDs a user's template list may include:
// every org for admins, the user's memberships otherwise. This 6-line branch
// was inlined in five handlers across admin_fetchurls.go and
// admin_templates.go.
func orgIDsForTemplates(r *http.Request, client *DansalClient, su *SessionUser) []int {
	if su.Role == "admin" {
		orgs, _ := client.GetOrganizations(r.Context())
		ids := make([]int, 0, len(orgs))
		for _, o := range orgs {
			ids = append(ids, o.ID)
		}
		return ids
	}
	return getUserOrgIDs(r.Context(), client, su.ID, getSessionToken(r))
}
