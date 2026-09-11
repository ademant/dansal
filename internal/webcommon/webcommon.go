// Package webcommon holds helpers shared by the dansal-web and dansal-webmin
// binaries: session-cookie helpers, client-IP extraction, site-settings
// accessors, and session-expiry parsing. The dansal API also imports ClientIP.
// Each helper was previously copy-pasted between the binaries and had started
// to drift (#1035).
package webcommon

import (
	"context"
	"database/sql"
	"log"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/ademant/dansal/internal/websession"
)

// Session wraps HMAC-signed session cookies (see websession) together with a
// request-context key for per-request user injection. dansal-web and
// dansal-webmin each instantiate one with their own cookie names, signing key,
// and user type; the login helpers themselves are shared so they can't drift.
type Session[T any] struct {
	cookies websession.Cookies
	ctxKey  any
}

// NewSession returns a Session backed by freshly generated websession cookies.
// tokenCookie and userCookie are the HTTP cookie names; ctxKey identifies the
// user in the request context. Each binary must pass a distinct ctxKey type so
// the two binaries' sessions can't collide.
func NewSession[T any](tokenCookie, userCookie string, ctxKey any) Session[T] {
	return Session[T]{cookies: websession.New(tokenCookie, userCookie), ctxKey: ctxKey}
}

// WithUser injects u into the request context for the remainder of the request.
func (s Session[T]) WithUser(r *http.Request, u *T) *http.Request {
	return r.WithContext(context.WithValue(r.Context(), s.ctxKey, u))
}

// User returns the session user, preferring the request-context value (set by
// auth middleware) over the signed user cookie. Returns nil when neither holds
// a valid session.
func (s Session[T]) User(r *http.Request) *T {
	if u, ok := r.Context().Value(s.ctxKey).(*T); ok && u != nil {
		return u
	}
	var u T
	if !s.cookies.GetUser(r, &u) {
		return nil
	}
	return &u
}

// Token returns the raw session token cookie value, or "" when unset.
func (s Session[T]) Token(r *http.Request) string {
	return s.cookies.GetToken(r)
}

// Set issues the token and signed-user cookies, both expiring at expiresAt.
func (s Session[T]) Set(w http.ResponseWriter, token string, user T, expiresAt time.Time) {
	s.cookies.Set(w, token, user, expiresAt)
}

// SetUser re-signs and overwrites only the user cookie, leaving the token
// cookie untouched. Used to refresh the signed user blob after a process
// restart without disturbing the raw session token in the browser.
func (s Session[T]) SetUser(w http.ResponseWriter, user *T, expiresAt time.Time) {
	s.cookies.SetUser(w, user, expiresAt)
}

// Clear deletes both session cookies.
func (s Session[T]) Clear(w http.ResponseWriter) {
	s.cookies.Clear(w)
}

// ClientIP returns the client's IP address, honouring X-Forwarded-For and
// X-Real-IP set by the reverse proxy and falling back to the remote peer.
func ClientIP(r *http.Request) string {
	if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
		if parts := strings.Split(xff, ","); len(parts) > 0 {
			return strings.TrimSpace(parts[0])
		}
	}
	if xr := r.Header.Get("X-Real-IP"); xr != "" {
		return strings.TrimSpace(xr)
	}
	if host, _, err := net.SplitHostPort(r.RemoteAddr); err == nil {
		return host
	}
	return r.RemoteAddr
}

// ParseExpiry parses an API-provided session expiry (RFC3339), falling back to
// ttl from now when it is missing or malformed.
func ParseExpiry(s string, ttl time.Duration) time.Time {
	expiresAt, err := time.Parse(time.RFC3339, s)
	if err != nil {
		return time.Now().Add(ttl)
	}
	return expiresAt
}

// GetSiteSetting returns the raw value for a site_settings key, or "" when the
// key is missing, db is nil (webmin without a web DB), or the query fails. A
// real DB error is logged rather than silently swallowed so a broken settings
// store is visible in the logs; a missing table is expected on a brand-new DB
// before createTables has run.
func GetSiteSetting(db *sql.DB, key string) string {
	if db == nil {
		return ""
	}
	var v string
	err := db.QueryRow("SELECT value FROM site_settings WHERE key = ?", key).Scan(&v)
	if err != nil {
		if err != sql.ErrNoRows && !strings.Contains(err.Error(), "no such table") {
			log.Printf("get site setting %s: %v", key, err)
		}
		return ""
	}
	return v
}

// SetSiteSetting upserts a site_settings row, logging (not propagating) errors
// and returning early when db is nil.
func SetSiteSetting(db *sql.DB, key, value string) {
	if db == nil {
		return
	}
	if _, err := db.Exec(
		"INSERT INTO site_settings(key,value) VALUES(?,?) ON CONFLICT(key) DO UPDATE SET value=excluded.value",
		key, value); err != nil {
		log.Printf("set site setting %s: %v", key, err)
	}
}

// DefaultHomeIntroYAML (#1298) is the shipped default for the homepage
// introductory paragraph, one YAML mapping of language code to text, "%s"
// standing in for the site name (filled via fmt.Sprintf). Both dansal-web
// (as the fallback when the admin-editable "home_intro" site_settings value
// is empty, or missing a specific language) and dansal-webmin (to pre-fill
// the site-config textarea with working content on first load, rather than
// an empty field of unclear expected format) need this same text, hence its
// home here rather than in either binary alone.
//
// This is the author's own translation, not a certified/professional one
// for every language listed — same caliber as most of dansal-web's existing
// i18n.yaml content. An instance operator can override any subset of
// languages directly via webmin's "Homepage introduction" field; a language
// missing from that override falls back to this default, not to blank.
const DefaultHomeIntroYAML = `de: "%s ist der Community-Kalender für Bal-Folk, Fest-Noz und verwandte traditionelle Tanzveranstaltungen in ganz Europa. Hier findest du Bälle, Workshops, Festivals und Musiksessions – filterbar nach Land, Region, Ort oder Tanzstil. Alle Veranstaltungen werden von lokalen Organisator:innen eingereicht und kostenlos veröffentlicht."
br: "%s eo deiziadur ar gumuniezh evit bal-folk, fest-noz hag darvoudoù dañs hengounel a-hed Europa. Kavit balioù, stajoù, gouelioù ha sesionoù sonerezh a zeu – siltrit dre vro, rannvro, kêr pe seurt dañs. An holl zarvoudoù a vez kaset gant aozourien lec'hel hag embannet digoust."
en: "%s is the community calendar for bal-folk, fest-noz, and related traditional dance events across Europe. Find upcoming balls, workshops, festivals, and music sessions — filter by country, region, town, or dance style. All events are submitted by local organisers and published free of charge."
es: "%s es el calendario comunitario de bal-folk, fest-noz y otros eventos de danza tradicional en toda Europa. Encuentra próximos bailes, talleres, festivales y sesiones musicales, filtrando por país, región, ciudad o estilo de danza. Todos los eventos son enviados por organizadores locales y publicados de forma gratuita."
fr: "%s est l'agenda communautaire des bals folk, fest-noz et autres événements de danse traditionnelle à travers l'Europe. Trouvez des bals, ateliers, festivals et sessions musicales à venir – filtrez par pays, région, ville ou style de danse. Tous les événements sont soumis par des organisateurs locaux et publiés gratuitement."
it: "%s è il calendario della comunità per bal-folk, fest-noz e altri eventi di danza tradizionale in tutta Europa. Trova balli, workshop, festival e sessioni musicali in programma – filtra per paese, regione, città o stile di ballo. Tutti gli eventi sono inviati da organizzatori locali e pubblicati gratuitamente."
nl: "%s is de communitykalender voor bal-folk, fest-noz en andere traditionele dansevenementen in heel Europa. Vind aankomende bals, workshops, festivals en muzieksessies – filter op land, regio, plaats of dansstijl. Alle evenementen worden aangeleverd door lokale organisatoren en gratis gepubliceerd."
uk: "%s — це громадський календар подій бал-фолк, фест-ноз та інших традиційних танцювальних заходів у Європі. Знаходьте майбутні бали, майстер-класи, фестивалі та музичні сесії — фільтруйте за країною, регіоном, містом або стилем танцю. Усі події подаються місцевими організаторами та публікуються безкоштовно."
ca: "%s és el calendari comunitari d'esdeveniments de bal-folk, fest-noz i altres danses tradicionals arreu d'Europa. Troba balls, tallers, festivals i sessions musicals properes – filtra per país, regió, població o estil de dansa. Tots els esdeveniments són enviats per organitzadors locals i es publiquen de manera gratuïta."
pt: "%s é o calendário comunitário de eventos de bal-folk, fest-noz e outras danças tradicionais por toda a Europa. Encontre bailes, workshops, festivais e sessões musicais – filtre por país, região, cidade ou estilo de dança. Todos os eventos são submetidos por organizadores locais e publicados gratuitamente."
pl: "%s to społecznościowy kalendarz wydarzeń bal-folk, fest-noz i innych tradycyjnych tańców w całej Europie. Znajdź nadchodzące bale, warsztaty, festiwale i sesje muzyczne – filtruj według kraju, regionu, miasta lub stylu tańca. Wszystkie wydarzenia są zgłaszane przez lokalnych organizatorów i publikowane bezpłatnie."
cs: "%s je komunitní kalendář akcí bal-folk, fest-noz a dalších tradičních tanečních akcí po celé Evropě. Najděte nadcházející bály, workshopy, festivaly a hudební sezení – filtrujte podle země, regionu, města nebo tanečního stylu. Všechny akce zadávají místní organizátoři a jsou zveřejňovány zdarma."
`
