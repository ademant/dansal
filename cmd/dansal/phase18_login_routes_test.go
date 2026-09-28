package main

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// registerLoginRoutes mirrors the login route registration in main.go. The
// real registration lives inside a large setupRoutes function that needs
// config and a live DB, so the two login patterns are repeated here to keep
// this test focused on the routing decision rather than the whole server.
func registerLoginRoutes(mux *http.ServeMux) {
	mux.HandleFunc("POST /api/v1/login", login)
	mux.HandleFunc("DELETE /api/v1/login", logout)
}

// GET /api/v1/login is not registered: login() creates a session and returns a
// token, so it must not be reachable by a method intermediaries treat as safe.
func TestLoginRejectsGETWith405(t *testing.T) {
	mux := http.NewServeMux()
	registerLoginRoutes(mux)

	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/login", nil))

	if rec.Code != http.StatusMethodNotAllowed {
		t.Errorf("GET /api/v1/login: got %d, want 405", rec.Code)
	}
	allow := rec.Header().Get("Allow")
	if !strings.Contains(allow, http.MethodPost) {
		t.Errorf("Allow header: got %q, want it to contain POST", allow)
	}
}

// A 405 body must not be a login error message, which would leak that the
// route exists and invite a GET-based client.
func TestLoginGETDoesNotInvokeHandler(t *testing.T) {
	mux := http.NewServeMux()
	registerLoginRoutes(mux)

	rec := httptest.NewRecorder()
	// A GET with a body that would satisfy login's form branch, to prove the
	// handler is not reachable regardless of what a client sends.
	req := httptest.NewRequest(http.MethodGet, "/api/v1/login",
		strings.NewReader("email=a@b.c&password=x"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	mux.ServeHTTP(rec, req)

	if strings.Contains(rec.Body.String(), "token") {
		t.Errorf("GET reached the login handler: body %q", rec.Body.String())
	}
}

var routeReg = regexp.MustCompile(`smux\.Handle(?:Func)?\("([A-Z]+) (\S+)"\s*,\s*([A-Za-z_][\w.]*)`)

// sanctionedDualMethod lists the handlers allowed on both GET and POST. Each is
// a token-in-path route where the credential arrives as an emailed
// click-through link, so the GET must keep working, while the POST lets an
// app-driven caller supply the same credential in a header instead (#1382).
// Adding an entry is a deliberate statement that the GET form is
// click-through-only; anything not listed here is a bug of the #1378 shape.
var sanctionedDualMethod = map[string]string{
	"verifyBooking":               "GET is the emailed booking-verification link; POST takes the token in a header",
	"useBoardSessionRenewHandler": "GET is the emailed board-session renew link; POST takes the token in a header",
}

// No handler may be registered on both a safe and a state-changing method
// unless it is explicitly listed in sanctionedDualMethod. The GET/POST login
// alias was the original instance (#1378); auth and optAuth appear on many
// methods but are middleware wrappers, so they are excluded by requiring the
// name to resolve to a declared func.
func TestNoMutatingHandlerAliasedOntoGET(t *testing.T) {
	src := readAllSources(t)

	byHandler := map[string]map[string]bool{}
	for _, m := range routeReg.FindAllStringSubmatch(src, -1) {
		method, handler := m[1], m[3]
		if !isHandlerFunc(src, handler) {
			continue
		}
		if byHandler[handler] == nil {
			byHandler[handler] = map[string]bool{}
		}
		byHandler[handler][method] = true
	}
	if len(byHandler) == 0 {
		t.Fatal("no handlers resolved — the source-scanning assumptions broke, not the routes")
	}

	for handler := range sanctionedDualMethod {
		if _, ok := byHandler[handler]; !ok {
			t.Errorf("sanctionedDualMethod lists %q, which is not registered on any method — remove the stale entry", handler)
		}
	}

	safe := map[string]bool{http.MethodGet: true, http.MethodHead: true}
	for handler, methods := range byHandler {
		hasSafe, hasUnsafe := false, false
		for m := range methods {
			if safe[m] {
				hasSafe = true
			} else {
				hasUnsafe = true
			}
		}
		if !hasSafe || !hasUnsafe {
			continue
		}
		if reason, ok := sanctionedDualMethod[handler]; !ok {
			t.Errorf("handler %q is registered on both a safe and a state-changing method (%v) — a mutating handler is reachable by GET/HEAD; add it to sanctionedDualMethod if the GET form is an emailed click-through link",
				handler, sortedKeys(methods))
		} else if reason == "" {
			t.Errorf("handler %q is sanctioned for dual registration but has no reason recorded", handler)
		}
	}
}

func sortedKeys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// readAllSources concatenates every non-test .go file in the package so both
// the route registrations (main.go) and the handler declarations (auth.go,
// bookings.go, ...) are visible to the same scan. Scanning only main.go makes
// isHandlerFunc always false, which silently turns the audit into a no-op.
func readAllSources(t *testing.T) string {
	t.Helper()
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	var b strings.Builder
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") {
			continue
		}
		raw, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		b.Write(raw)
		b.WriteString("\n")
	}
	return b.String()
}

func isHandlerFunc(src, name string) bool {
	return regexp.MustCompile(`(?m)^func ` + regexp.QuoteMeta(name) + `\(`).MatchString(src)
}
