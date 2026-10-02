package main

import (
	"fmt"
	"net/http"
	"regexp"
	"testing"
)

// TestRouteTableHasNoConflicts replays every smux.Handle/HandleFunc pattern
// from the sources into a fresh ServeMux. Routes are registered inline in
// main(), so a conflicting pattern (e.g. two wildcards in the same position
// of otherwise identical paths) would otherwise only surface as a panic when
// the server starts — #1384 added owner-scoped routes such as
// /api/v1/events/{event_id}/image next to the existing /events/{id}/… ones.
func TestRouteTableHasNoConflicts(t *testing.T) {
	src := readAllSources(t)
	mux := http.NewServeMux()
	seen := 0
	for _, m := range routeReg.FindAllStringSubmatch(src, -1) {
		pattern := m[1] + " " + m[2]
		func() {
			defer func() {
				if r := recover(); r != nil {
					t.Errorf("registering %q: %v", pattern, fmt.Sprint(r))
				}
			}()
			mux.HandleFunc(pattern, func(http.ResponseWriter, *http.Request) {})
		}()
		seen++
	}
	if seen < 100 {
		t.Fatalf("only %d routes found — the source regex no longer matches main.go", seen)
	}
}

// TestDeprecatedAliasSuccessorsExist checks that every deprecatedAlias names
// a successor path that is itself registered — otherwise the Link header
// would point clients at a route that 404s (#1379–#1381, #1384).
func TestDeprecatedAliasSuccessorsExist(t *testing.T) {
	src := readAllSources(t)
	registered := map[string]bool{}
	for _, m := range routeReg.FindAllStringSubmatch(src, -1) {
		registered[m[2]] = true
	}
	aliases := regexp.MustCompile(`deprecatedAlias\("([^"]+)"`).FindAllStringSubmatch(src, -1)
	if len(aliases) < 30 {
		t.Fatalf("only %d deprecatedAlias calls found — has the helper been renamed?", len(aliases))
	}
	for _, m := range aliases {
		if !registered[m[1]] {
			t.Errorf("deprecatedAlias successor %s is not a registered route", m[1])
		}
	}
}
