package main

import (
	"compress/gzip"
	"context"
	"flag"
	"fmt"
	"log"
	"log/syslog"
	"net/http"
	"os"
	"os/signal"
	"runtime/debug"
	"strings"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/ademant/dansal/internal/instance"
)

// authThrottle is the shared rate limiter for register, magic-login, and verify endpoints.
// Initialised in main() after config is loaded.
var authThrottle *submissionThrottle

// siteCfg caches contact/site_name/impressum from web.db with a short TTL so
// that webmin changes are visible without a restart or signal.
var siteCfg *siteSettingsCache

// publicThrottle is the shared rate limiter for all public form endpoints (booking, board, suggest).
// Key is IP + "|" + User-Agent. Initialised in main() after config is loaded.
var publicThrottle *submissionThrottle

// searchThrottle is the per-IP rate limiter for GET /search/results.
// Generous limit (default 60/min) blocks scraping/hammering without affecting normal browsing.
var searchThrottle *submissionThrottle

// geocodeThrottle is the per-IP rate limiter for GET /search/geocode. Tighter
// than searchThrottle since each miss triggers an outbound Nominatim call
// (itself additionally paced globally, see geocodeMinInterval in geocode.go).
var geocodeThrottle *submissionThrottle

// tokenThrottle is the per-IP rate limiter for GET handlers that issue form tokens.
var tokenThrottle *submissionThrottle

// liveHandler is an http.Handler whose inner handler can be swapped atomically.
// This lets systemctl reload rebuild all route closures with new config+i18n
// without stopping the server.
type liveHandler struct {
	p atomic.Pointer[http.Handler]
}

func (lh *liveHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	(*lh.p.Load()).ServeHTTP(w, r)
}

func (lh *liveHandler) store(h http.Handler) {
	lh.p.Store(&h)
}

func main() {
	// Register --version before loadConfig() calls flag.Parse().
	printVersion := flag.Bool("version", false, "print version and build date then exit")

	tag := "dansal-web"
	if inst := instance.FromConfigArg(); inst != "" {
		tag = "dansal-web@" + inst
	}
	if w, err := syslog.New(syslog.LOG_INFO|syslog.LOG_DAEMON, tag); err == nil {
		log.SetOutput(w)
		log.SetFlags(0)
	}

	cfg := loadConfig()

	authThrottle = newSubmissionThrottle(
		cfg.AuthRateLimit,
		time.Duration(cfg.AuthRateWindowMins)*time.Minute,
	)
	publicThrottle = newSubmissionThrottleForget(
		cfg.PublicRateLimit,
		time.Duration(cfg.PublicRateWindowMins)*time.Minute,
		time.Duration(cfg.PublicThrottleForgetHours)*time.Hour,
	)
	searchThrottle = newSubmissionThrottle(
		cfg.SearchRateLimit,
		time.Duration(cfg.SearchRateWindowMins)*time.Minute,
	)
	geocodeThrottle = newSubmissionThrottle(
		cfg.GeocodeRateLimit,
		time.Duration(cfg.GeocodeRateWindowMins)*time.Minute,
	)
	tokenThrottle = newSubmissionThrottle(
		cfg.TokenRateLimit,
		time.Duration(cfg.TokenRateWindowMins)*time.Minute,
	)
	// Use the longer of login/non-login max-ages as the cleanup cutoff.
	formTokenMaxAge := time.Duration(cfg.FormTokenMaxAgeMins) * time.Minute
	startFormTokenCleanup(formTokenMaxAge, cfg.FormTokenCleanupMins)
	startPendingSubmissionCleanup(formTokenMaxAge * 2)
	formTokenCap = int64(cfg.FormTokenCap)
	globalEmailSendRate = newEmailSendThrottle(
		time.Duration(cfg.EmailRateWindowSecs)*time.Second,
		cfg.EmailRateSoftLimit,
		cfg.EmailRateHardLimit,
	)
	userRateLimiter = newUserRateLimiter(cfg.UserRateLimitGlobal, cfg.UserRateLimits)
	userRateLimiter.startCleanup(2 * time.Minute)
	adminAllowedHost = cfg.Domain

	if *printVersion {
		fmt.Printf("dansal-web %s (built %s)\n", Version, BuildTime)
		os.Exit(0)
	}
	cfg.pagesContent = loadPagesContent(cfg.PagesFile)
	initDBKey()
	db := initDB(cfg.DBPath)
	migrateActorKeyEncryption(db)
	siteCfg = newSiteSettingsCache(db)
	client := &DansalClient{
		BaseURL:        cfg.DansalURL,
		HTTP:           &http.Client{Timeout: 180 * time.Second},
		InternalSecret: cfg.InternalSharedSecret,
	}

	tmpls := loadTemplates()

	// buildHandler constructs all route closures from the given cfg and i18n.
	// Called at startup and again on each SIGHUP reload.
	buildHandler := func(cfg *Config, i18n *I18n) http.Handler {
		r := http.NewServeMux()
		help := newHelpSystem(cfg.HelpDir)

		r.HandleFunc("GET /actors", actorsListHandler(cfg, db))
		r.HandleFunc("GET /.well-known/webfinger", webfingerHandler(cfg, db, client))
		r.HandleFunc("GET /.well-known/nodeinfo", nodeinfoIndexHandler(cfg))
		r.HandleFunc("GET /nodeinfo/2.0", nodeinfoHandler(cfg, client, "2.0"))
		r.HandleFunc("GET /nodeinfo/2.1", nodeinfoHandler(cfg, client, "2.1"))
		r.HandleFunc("GET /.well-known/security.txt", securityTxtHandler(cfg))
		r.HandleFunc("GET /.well-known/host-meta", hostMetaHandler(cfg))
		r.HandleFunc("GET /.well-known/host-meta.json", hostMetaJSONHandler(cfg))
		r.HandleFunc("GET /.well-known/dnt-policy.txt", dntPolicyHandler())
		r.HandleFunc("GET /.well-known/dnt", dntStatusHandler(cfg))
		r.HandleFunc("GET /health", healthHandler())
		r.HandleFunc("GET /robots.txt", robotsTxtHandler(cfg))
		r.HandleFunc("GET /sitemap.xml", sitemapHandler(cfg, client))
		r.HandleFunc("GET /{name}", indexNowKeyFileHandler)
		r.HandleFunc("GET /llms.txt", llmsTxtHandler(cfg))
		r.HandleFunc("GET /manifest.json", manifestHandler(cfg))
		r.HandleFunc("GET /opensearch.xml", opensearchHandler(cfg))

		r.HandleFunc("GET /location/{id}", locationPageHandler(cfg, tmpls, client, i18n))
		r.HandleFunc("GET /org/{name}", actorOrFrontendHandler(cfg, tmpls, db, client, i18n))
		r.HandleFunc("GET /org/{name}/outbox", outboxHandler(cfg, db, client))
		r.HandleFunc("GET /org/{name}/followers", followersHandler(cfg, db))
		r.HandleFunc("POST /org/{name}/inbox", inboxHandler(cfg, db, client))
		r.HandleFunc("POST /inbox", sharedInboxHandler(cfg, db, client))
		r.HandleFunc("POST /telegram/webhook", telegramWebhookProxyHandler(cfg))

		r.HandleFunc("GET /events/suggest", suggestPageHandler(cfg, tmpls, client, i18n))
		r.HandleFunc("POST /events/suggest", suggestPreviewHandler(cfg, tmpls, client, i18n))
		r.HandleFunc("POST /events/suggest/submit", suggestSubmitHandler(cfg, tmpls, client, i18n))
		r.HandleFunc("GET /events/suggest/done", suggestDoneHandler(cfg, tmpls, i18n))
		r.HandleFunc("GET /events/suggest/verify/{token}", suggestVerifyHandler(cfg, tmpls, client, i18n))
		r.HandleFunc("GET /events/suggest/manage/{token}", suggestManagePageHandler(cfg, tmpls, client, i18n))
		r.HandleFunc("POST /events/suggest/manage/{token}", suggestManageSubmitHandler(cfg, tmpls, client, i18n))

		r.HandleFunc("GET /invites/{token}", invitePageHandler(cfg, tmpls, client, i18n))
		r.HandleFunc("POST /invites/{token}/password", invitePasswordHandler(cfg, client))
		r.HandleFunc("POST /invites/{token}/webauthn/begin", webauthnInviteProxy(cfg, client, "begin"))
		r.HandleFunc("POST /invites/{token}/webauthn/finish", webauthnInviteProxy(cfg, client, "finish"))
		r.HandleFunc("POST /auth/webauthn/login/begin", webauthnProxy(cfg, client, "/api/v1/auth/webauthn/login/begin"))
		r.HandleFunc("POST /auth/webauthn/login/finish", webauthnProxy(cfg, client, "/api/v1/auth/webauthn/login/finish"))
		r.HandleFunc("POST /auth/webauthn/totp-challenge", webauthnProxy(cfg, client, "/api/v1/auth/webauthn/totp-challenge"))
		r.HandleFunc("POST /auth/webauthn/register/passkey/begin", webauthnProxy(cfg, client, "/api/v1/register/passkey/begin"))
		r.HandleFunc("POST /auth/webauthn/register/passkey/finish", webauthnProxy(cfg, client, "/api/v1/register/passkey/finish"))
		r.HandleFunc("GET /register", registerPageHandler(cfg, tmpls, client, i18n))
		r.HandleFunc("POST /register", registerSubmitHandler(cfg, tmpls, client, i18n))
		r.HandleFunc("GET /register/done", registerDoneHandler(cfg, tmpls, i18n))
		r.HandleFunc("GET /register/verify/email/{token}", registerVerifyHandler(cfg, tmpls, client, i18n))
		r.HandleFunc("POST /register/resend", registerResendHandler(cfg, client))
		r.HandleFunc("POST /register/cancel", registerCancelHandler(cfg, client))
		r.HandleFunc("GET /admin/registrations", adminRegistrationsHandler(cfg, tmpls, client, i18n))
		r.HandleFunc("POST /admin/registrations/{id}/approve", adminRateLimit(adminRegistrationApproveHandler(cfg, client)))
		r.HandleFunc("POST /admin/registrations/{id}/reject", adminRateLimit(adminRegistrationRejectHandler(cfg, client)))
		r.HandleFunc("POST /admin/registrations/{id}/resend-invite", adminRateLimit(adminRegistrationResendInviteHandler(cfg, client)))

		r.HandleFunc("GET /tiles/{scheme}/{z}/{x}/{yfile}", tileProxyHandler(cfg))
		r.HandleFunc("GET /favicon.svg", dynamicSVGHandler(cfg.ImagesDir, "favicon", faviconSVG))
		r.HandleFunc("GET /logo.avif", dynamicSVGHandler(cfg.ImagesDir, "logo", logoAVIF))
		r.HandleFunc("GET /banner.avif", dynamicSVGHandler(cfg.ImagesDir, "banner", bannerAVIF))
		r.HandleFunc("GET /relay-icon", func(w http.ResponseWriter, r *http.Request) {
			if !maybeServeSiteAsset(w, r, cfg.ImagesDir, "relay-avatar") {
				http.NotFound(w, r)
			}
		})
		r.HandleFunc("GET /relay-banner", func(w http.ResponseWriter, r *http.Request) {
			if !maybeServeSiteAsset(w, r, cfg.ImagesDir, "relay-banner") {
				http.NotFound(w, r)
			}
		})
		r.HandleFunc("GET /ai-badge", dynamicSVGHandler(cfg.ImagesDir, "ai-badge", aiBadgeDefault))
		r.HandleFunc("POST /internal/relay/redeliver", internalRelayRedeliverHandler(cfg, db, client))
		r.HandleFunc("POST /internal/relay/profile-update", internalRelayProfileUpdateHandler(cfg, db))
		r.HandleFunc("GET /static/qrcode.min.js", func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Vary", "Accept-Encoding")
			w.Header().Set("Content-Type", "application/javascript")
			w.Header().Set("Cache-Control", "public, max-age=604800")
			// Respect Save-Data: return a tiny stub to reduce traffic.
			if saveDataOn(r) {
				w.Write([]byte("/* Save-Data: on - script omitted */"))
				return
			}
			// If client supports gzip, compress on-the-fly to save bandwidth.
			if strings.Contains(r.Header.Get("Accept-Encoding"), "gzip") {
				w.Header().Set("Content-Encoding", "gzip")
				gw := gzip.NewWriter(w)
				defer gw.Close()
				gw.Write(qrcodeJS)
				return
			}
			w.Write(qrcodeJS)
		})
		r.HandleFunc("GET /federated-events/{id}", federatedEventHandler(db))
		// Legacy Gancio URL patterns dansal doesn't support: 301 instead of
		// silently falling through to the "/" catch-all with a 200 (issue #823).
		r.HandleFunc("GET /event/{slug}", legacyGancioRedirect("/"))
		r.HandleFunc("GET /tag/{slug}", legacyGancioRedirect("/"))
		r.HandleFunc("GET /collection/{name}", legacyGancioRedirect("/"))
		r.HandleFunc("GET /place/{id}/{slug...}", legacyGancioRedirect("/"))
		r.HandleFunc("GET /export", legacyGancioRedirect("/"))
		r.HandleFunc("GET /add", legacyGancioRedirect("/events/suggest"))
		r.HandleFunc("GET /", indexHandler(cfg, tmpls, db, client, i18n))
		r.HandleFunc("GET /events-more", eventsMoreHandler(tmpls, i18n, client))
		r.HandleFunc("GET /dashboard", dashboardHandler(cfg, tmpls, db, client, i18n))
		r.HandleFunc("GET /search", searchPageHandler(cfg, tmpls, client, i18n))
		r.HandleFunc("GET /search/results", searchResultsHandler(tmpls, i18n, client))
		r.HandleFunc("GET /search/geocode", geocodeHandler(cfg, db))
		r.HandleFunc("GET /events/{id}", eventHandler(cfg, tmpls, client, i18n))
		r.HandleFunc("POST /events/{id}/assign-org", adminRateLimit(eventAssignOrgHandler(cfg, client)))
		r.HandleFunc("POST /events/{id}/board", contactBoardPostHandler(cfg, db, client, i18n))
		r.HandleFunc("GET /events/{id}/board/form-token", boardFormTokenHandler(cfg))
		r.HandleFunc("POST /events/{id}/board/{post_id}/delete", contactBoardDeleteHandler(cfg, client))
		r.HandleFunc("POST /events/{id}/board/{post_id}/contact", contactBoardContactHandler(cfg, client))
		r.HandleFunc("GET /contact-posts/verify/{token}", contactBoardVerifyRedirect)
		r.HandleFunc("GET /contact-posts/delete/{token}", contactBoardDeleteRedirect)
		r.HandleFunc("GET /contact-posts/manage/{token}", contactManageGetHandler(cfg, db, tmpls, client, i18n))
		r.HandleFunc("POST /contact-posts/manage/{token}", contactManagePostHandler(cfg, client, i18n))
		r.HandleFunc("POST /contact-posts/manage/{token}/images", contactManageImageUploadHandler(client))
		r.HandleFunc("POST /contact-posts/manage/{token}/images/{img_id}/delete", contactManageImageDeleteHandler(client))
		r.HandleFunc("POST /contact-posts/manage/{token}/remember", contactManageRememberHandler(cfg, client))
		r.HandleFunc("POST /contact-posts/manage/{token}/forget", contactManageForgetHandler(cfg, client))
		r.HandleFunc("GET /contact-requests/verify/{token}", contactRequestVerifyHandler(cfg, tmpls, client, i18n))
		r.HandleFunc("GET /checkin/{qr_token}", checkinGetHandler(cfg, tmpls, i18n))
		r.HandleFunc("POST /checkin/{qr_token}", checkinPostHandler(cfg, client))
		r.HandleFunc("POST /events/{id}/book", bookingPostHandler(cfg, client, i18n))
		r.HandleFunc("GET /bookings/verify/{token}", bookingVerifyHandler(cfg, tmpls, client, i18n))
		r.HandleFunc("GET /board", boardHandler(cfg, tmpls, client, i18n))
		r.HandleFunc("POST /board/resend-manage", boardResendManageHandler(cfg, client, i18n))
		r.HandleFunc("POST /board/renew-session", boardRenewRequestHandler(cfg, client))
		r.HandleFunc("GET /board/renew-session/{token}", boardRenewUseHandler(client))
		r.HandleFunc("GET /tags", tagsIndexHandler(cfg, tmpls, client, i18n))
		r.HandleFunc("GET /tags/{slug}", tagHandler(cfg, tmpls, client, i18n))
		r.HandleFunc("GET /tags/{slug}/followers", tagFollowersHandler(cfg, db, client))
		r.HandleFunc("POST /tags/{slug}/inbox", tagInboxHandler(cfg, db, client))
		r.HandleFunc("GET /api/v1/tags/search", tagSearchHandler(client))
		r.HandleFunc("GET /cities", citiesHandler(cfg, tmpls, client, i18n))
		r.HandleFunc("GET /city/{slug}", cityHubHandler(cfg, tmpls, client, i18n))
		r.HandleFunc("GET /city/{slug}/past-events", cityPastEventsHandler(tmpls, i18n, client))
		r.HandleFunc("GET /search/musicians", musicianSearchHandler(client))
		r.HandleFunc("GET /search/instructors", instructorSearchHandler(client))
		r.HandleFunc("GET /musicians", musiciansHandler(cfg, tmpls, client, i18n))
		r.HandleFunc("GET /musicians/{id}", musicianHandler(cfg, tmpls, client, i18n))
		r.HandleFunc("GET /instructors", instructorsHandler(cfg, tmpls, client, i18n))
		r.HandleFunc("GET /instructors/{id}", instructorHandler(cfg, tmpls, client, i18n))
		r.HandleFunc("GET /organizations", orgsHandler(cfg, tmpls, db, client, i18n))
		r.HandleFunc("GET /impressum", impressumHandler(cfg, tmpls, i18n))
		r.HandleFunc("GET /privacy", legalPageHandler(cfg, tmpls, i18n, "privacy", "nav_privacy"))
		r.HandleFunc("GET /terms", legalPageHandler(cfg, tmpls, i18n, "terms", "nav_terms"))
		r.HandleFunc("GET /help", helpHandler(cfg, tmpls, i18n, help))

		r.HandleFunc("GET /login", loginPageHandler(cfg, tmpls, i18n))
		r.HandleFunc("POST /login", loginHandler(cfg, tmpls, client, i18n))
		r.HandleFunc("POST /logout", logoutHandler(cfg, client))
		r.HandleFunc("GET /lang", langHandler(i18n))
		r.HandleFunc("GET /settings", settingsPageHandler(cfg, tmpls, client, i18n))
		r.HandleFunc("POST /settings", settingsUpdateHandler(cfg, tmpls, client, i18n))
		r.HandleFunc("POST /settings/verify", settingsSendVerifyHandler(cfg, tmpls, client, i18n))
		r.HandleFunc("POST /settings/verify-telegram", settingsTelegramVerifyHandler(cfg, tmpls, client, i18n))
		r.HandleFunc("POST /settings/verify-matrix", settingsMatrixVerifyHandler(cfg, tmpls, client, i18n))
		r.HandleFunc("POST /settings/sessions/{id}/revoke", settingsSessionRevokeHandler(cfg, client))
		r.HandleFunc("POST /settings/apikeys/new", settingsCreateAPIKeyHandler(cfg, tmpls, client, i18n))
		r.HandleFunc("POST /settings/apikeys/{id}/delete", settingsDeleteAPIKeyHandler(cfg, client))
		r.HandleFunc("POST /settings/password", settingsChangePasswordHandler(cfg, tmpls, client, i18n))
		r.HandleFunc("POST /settings/magic-link", settingsMagicLinkHandler(cfg, client))
		r.HandleFunc("POST /settings/passkeys/register/begin", settingsPasskeyRegisterBeginHandler(cfg, client))
		r.HandleFunc("POST /settings/passkeys/register/finish", settingsPasskeyRegisterFinishHandler(cfg, client))
		r.HandleFunc("POST /settings/passkeys/{id}/delete", settingsPasskeyDeleteHandler(cfg, client))
		r.HandleFunc("GET /settings/totp/setup", settingsTOTPSetupHandler(cfg, tmpls, client, i18n))
		r.HandleFunc("POST /settings/totp/confirm", settingsTOTPConfirmHandler(cfg, tmpls, client, i18n))
		r.HandleFunc("POST /settings/totp/disable", settingsTOTPDisableHandler(cfg, client))
		r.HandleFunc("POST /settings/delete", settingsDeleteAccountHandler(cfg, tmpls, client, i18n))
		r.HandleFunc("POST /magic", magicRequestHandler(cfg, tmpls, client, i18n))
		r.HandleFunc("GET /login/magic/{token}", magicLoginHandler(cfg, tmpls, client, i18n))
		r.HandleFunc("GET /verify/{token}", verifyEmailHandler(cfg, tmpls, client, i18n))

		r.HandleFunc("GET /admin/users", adminUsersHandler(cfg, tmpls, client, i18n))
		r.HandleFunc("POST /admin/users/bulk", adminRateLimit(adminUsersBulkHandler(cfg, client)))
		r.HandleFunc("POST /admin/users/{id}/disable", adminRateLimit(adminUserDisableHandler(cfg, client)))
		r.HandleFunc("POST /admin/users/{id}/magic-link", adminRateLimit(adminGenerateMagicLinkHandler(cfg, client)))
		r.HandleFunc("POST /admin/users/{id}/telegram/message", adminRateLimit(adminUserTelegramMessageHandler(cfg, client)))
		r.HandleFunc("POST /admin/users/{id}/role", adminRateLimit(adminUserRoleHandler(cfg, client)))
		r.HandleFunc("POST /admin/users/{id}/org", adminRateLimit(adminUserOrgHandler(cfg, client)))
		r.HandleFunc("POST /admin/invites/new", adminRateLimit(adminInviteCreateHandler(cfg, client)))
		r.HandleFunc("POST /admin/invites/{token}/revoke", adminRateLimit(adminInviteRevokeHandler(cfg, client)))
		r.HandleFunc("POST /admin/invites/{token}/resend", adminRateLimit(adminInviteResendHandler(cfg, client)))
		r.HandleFunc("POST /admin/publishers/new", adminRateLimit(adminPublisherCreateHandler(cfg, client)))
		r.HandleFunc("POST /admin/publishers/invite", adminRateLimit(adminPublisherInviteHandler(cfg, client)))
		r.HandleFunc("POST /admin/publishers/{id}/regenerate-key", adminRateLimit(adminPublisherRegenerateKeyHandler(cfg, client)))
		r.HandleFunc("POST /admin/publishers/{id}/delete", adminRateLimit(adminPublisherDeleteHandler(cfg, client)))

		r.HandleFunc("GET /admin/events/{id}/bookings", adminBookingsHandler(cfg, tmpls, client, i18n))
		r.HandleFunc("POST /admin/bookings/{id}/approve", adminRateLimit(adminBookingApproveHandler(cfg, client)))
		r.HandleFunc("POST /admin/bookings/{id}/cancel", adminRateLimit(adminBookingCancelHandler(cfg, client)))
		r.HandleFunc("POST /admin/bookings/{id}/delete", adminRateLimit(adminBookingDeleteHandler(cfg, client)))
		r.HandleFunc("POST /admin/events/{id}/publish", adminRateLimit(adminEventPublishHandler(cfg, client)))
		r.HandleFunc("POST /admin/events/{id}/cancel", adminRateLimit(adminEventCancelHandler(cfg, client)))
		r.HandleFunc("POST /admin/events/{id}/pending-edit/approve", adminRateLimit(adminPendingEditHandler(cfg, client, true)))
		r.HandleFunc("POST /admin/events/{id}/pending-edit/reject", adminRateLimit(adminPendingEditHandler(cfg, client, false)))
		r.HandleFunc("POST /admin/events/{id}/delete", adminRateLimit(adminEventDeleteHandler(cfg, db, client)))
		r.HandleFunc("POST /admin/events/merge", adminRateLimit(adminEventMergeHandler(cfg, db, client)))
		r.HandleFunc("POST /admin/events/bulk-publish", adminRateLimit(adminEventBulkPublishHandler(cfg, client)))
		r.HandleFunc("POST /admin/events/bulk-cancel", adminRateLimit(adminEventBulkCancelHandler(cfg, client)))
		r.HandleFunc("POST /admin/events/bulk-delete", adminRateLimit(adminEventBulkDeleteHandler(cfg, db, client)))
		r.HandleFunc("POST /admin/events/bulk-assign-location", adminRateLimit(adminEventBulkAssignLocationHandler(cfg, client)))
		r.HandleFunc("POST /admin/events/bulk-set-time", adminRateLimit(adminEventBulkSetTimeHandler(cfg, client)))
		r.HandleFunc("POST /admin/events/bulk-set-attributes", adminRateLimit(adminEventBulkSetAttributesHandler(cfg, client)))
		r.HandleFunc("POST /admin/events/bulk-assign-series", adminRateLimit(adminEventBulkAssignSeriesHandler(cfg, tmpls, client, i18n)))
		r.HandleFunc("POST /admin/events/{id}/image/delete", adminRateLimit(adminEventImageDeleteHandler(cfg, client)))
		r.HandleFunc("POST /admin/musicians/{id}/image/delete", adminRateLimit(adminMusicianImageDeleteHandler(cfg, client)))
		r.HandleFunc("POST /admin/musicians/{id}/avatar/delete", adminRateLimit(adminMusicianAvatarDeleteHandler(cfg, client)))
		r.HandleFunc("POST /admin/organizations/{id}/image/delete", adminRateLimit(adminOrgImageDeleteHandler(cfg, client)))
		r.HandleFunc("POST /admin/organizations/{id}/avatar/delete", adminRateLimit(adminOrgAvatarDeleteHandler(cfg, client)))
		r.HandleFunc("POST /admin/instructors/{id}/avatar/delete", adminRateLimit(adminInstructorAvatarDeleteHandler(cfg, client)))
		r.HandleFunc("GET /admin/events/maintenance", adminEventsMaintenanceHandler(cfg, tmpls, client, i18n))
		r.HandleFunc("GET /admin/events", adminEventsHandler(cfg, tmpls, client, i18n))
		r.HandleFunc("GET /admin/events/import", adminImportEventsPageHandler(cfg, tmpls, i18n))
		r.HandleFunc("POST /admin/events/import", adminRateLimit(adminImportEventsHandler(cfg, tmpls, client, i18n)))
		r.HandleFunc("POST /admin/events/import/confirm", adminRateLimit(adminImportConfirmHandler(cfg, client)))
		r.HandleFunc("GET /admin/events/new", adminEventNewPageHandler(cfg, tmpls, db, client, i18n))
		r.HandleFunc("POST /admin/events/new", adminRateLimit(adminEventCreateHandler(cfg, tmpls, db, client, i18n)))
		r.HandleFunc("GET /admin/events/{id}/timetable", adminTimetablePageHandler(cfg, tmpls, client, i18n))
		r.HandleFunc("PUT /admin/events/{id}/timetable", adminRateLimit(adminTimetableSaveHandler(client)))
		r.HandleFunc("DELETE /admin/events/{id}/timetable", adminRateLimit(adminTimetableDeleteHandler(client)))
		r.HandleFunc("POST /admin/events/{id}/timetable/sync-times", adminRateLimit(adminTimetableSyncTimesHandler(client)))
		r.HandleFunc("PUT /admin/events/{id}/locations/{location_id}", adminRateLimit(adminAddEventExtraLocationHandler(client)))
		r.HandleFunc("DELETE /admin/events/{id}/locations/{location_id}", adminRateLimit(adminRemoveEventExtraLocationHandler(client)))
		r.HandleFunc("PUT /admin/events/{id}/locations/{location_id}/primary", adminRateLimit(adminSetEventExtraLocationPrimaryHandler(client)))
		r.HandleFunc("GET /admin/events/{id}/edit", adminEventEditPageHandler(cfg, tmpls, db, client, i18n))
		r.HandleFunc("POST /admin/events/{id}/edit", adminRateLimit(adminEventSaveHandler(cfg, tmpls, db, client, i18n)))
		r.HandleFunc("POST /admin/events/{id}/assign-series", adminRateLimit(adminEventAssignSeriesHandler(cfg, client)))
		r.HandleFunc("POST /admin/events/{id}/remove-from-series", adminRateLimit(adminEventRemoveFromSeriesHandler(cfg, client)))
		r.HandleFunc("POST /admin/events/{id}/save-template", adminRateLimit(adminTemplateSaveHandler(cfg, db, client)))
		r.HandleFunc("GET /admin/events/template-assign", adminTemplateAssignPageHandler(cfg, tmpls, db, client, i18n))
		r.HandleFunc("POST /admin/events/template-assign", adminRateLimit(adminTemplateAssignApplyHandler(cfg, db, client)))
		r.HandleFunc("GET /admin/templates", adminTemplatesHandler(cfg, tmpls, db, client, i18n))
		r.HandleFunc("GET /admin/templates/new", adminTemplateNewPageHandler(cfg, tmpls, db, client, i18n))
		r.HandleFunc("POST /admin/templates/new", adminRateLimit(adminTemplateCreateHandler(cfg, tmpls, db, client, i18n)))
		r.HandleFunc("POST /admin/templates/{id}/delete", adminRateLimit(adminTemplateDeleteHandler(db)))
		r.HandleFunc("GET /admin/templates/{id}/data", adminTemplateDataHandler(db, client))
		r.HandleFunc("POST /admin/templates/{id}/pin", adminRateLimit(adminTemplatePinHandler(db)))
		r.HandleFunc("POST /admin/templates/{id}/unpin", adminRateLimit(adminTemplateUnpinHandler(db)))

		r.HandleFunc("GET /admin/organization/{slug}", adminOrgDashboardHandler(cfg, tmpls, db, client, i18n))
		r.HandleFunc("GET /admin/location/{id}", adminLocationDashboardHandler(cfg, tmpls, client, i18n))
		r.HandleFunc("GET /admin/instructor/{id}", adminInstructorDashboardHandler(cfg, tmpls, db, client, i18n))
		r.HandleFunc("GET /admin/musician/{id}", adminMusicianDashboardHandler(cfg, tmpls, db, client, i18n))
		r.HandleFunc("GET /admin/organizations", adminOrgsHandler(cfg, tmpls, client, i18n))
		r.HandleFunc("GET /admin/organizations/check-actor-name", adminOrgCheckActorNameHandler(cfg, client))
		r.HandleFunc("GET /admin/organizations/new", adminOrgNewPageHandler(cfg, tmpls, i18n))
		r.HandleFunc("POST /admin/organizations/new", adminRateLimit(adminOrgCreateHandler(cfg, tmpls, client, i18n)))
		r.HandleFunc("GET /admin/organizations/{id}/edit", adminOrgEditPageHandler(cfg, tmpls, client, i18n, db))
		r.HandleFunc("POST /admin/organizations/{id}/edit", adminRateLimit(adminOrgSaveHandler(cfg, tmpls, db, client, i18n)))
		r.HandleFunc("POST /admin/organizations/{id}/delete", adminRateLimit(adminOrgDeleteHandler(cfg, client)))
		r.HandleFunc("POST /admin/organizations/{id}/run-feeds", adminRateLimit(adminOrgRunFeedsHandler(cfg, client)))
		r.HandleFunc("POST /admin/organizations/{id}/redeliver", adminRateLimit(adminOrgRedeliverHandler(cfg, db, client)))
		r.HandleFunc("POST /admin/organizations/{id}/members", adminRateLimit(adminOrgMemberHandler(cfg, client)))
		r.HandleFunc("POST /admin/organizations/{id}/locations", adminRateLimit(adminOrgLocationsHandler(cfg, client)))
		r.HandleFunc("POST /admin/organizations/{id}/follow", adminRateLimit(adminOrgFollowHandler(cfg, db, client)))
		r.HandleFunc("POST /admin/organizations/{id}/unfollow", adminRateLimit(adminOrgUnfollowHandler(cfg, db, client)))

		r.HandleFunc("GET /admin/series", adminSeriesListHandler(cfg, tmpls, client, i18n))
		r.HandleFunc("GET /admin/series/new", adminSeriesNewPageHandler(cfg, tmpls, client, i18n))
		r.HandleFunc("POST /admin/series/new", adminRateLimit(adminSeriesCreateHandler(cfg, client)))
		r.HandleFunc("GET /admin/series/{id}", adminSeriesEditPageHandler(cfg, tmpls, client, i18n))
		r.HandleFunc("POST /admin/series/{id}/edit", adminRateLimit(adminSeriesSaveHandler(cfg, client)))
		r.HandleFunc("POST /admin/series/{id}/descriptions", adminRateLimit(adminSeriesSaveDescriptionsHandler(cfg, client)))
		r.HandleFunc("POST /admin/series/{id}/delete", adminRateLimit(adminSeriesDeleteHandler(cfg, client)))
		r.HandleFunc("POST /admin/series/{id}/add-date", adminRateLimit(adminSeriesAddDateHandler(cfg, client)))
		r.HandleFunc("POST /admin/series/{id}/token/regenerate", adminRateLimit(adminSeriesRegenerateTokenHandler(cfg, client)))
		r.HandleFunc("POST /admin/series/{id}/token/revoke", adminRateLimit(adminSeriesRevokeTokenHandler(cfg, client)))
		r.HandleFunc("POST /admin/series/{id}/image", adminRateLimit(adminSeriesImageUploadHandler(cfg, client)))
		r.HandleFunc("POST /admin/series/{id}/image/delete", adminRateLimit(adminSeriesImageDeleteHandler(cfg, client)))
		r.HandleFunc("GET /series_token/{token}", seriesTokenPageHandler(cfg, tmpls, client, i18n))
		r.HandleFunc("POST /series_token/{token}/events/{eventID}/description", seriesTokenSaveDescriptionHandler(cfg, client))

		r.HandleFunc("GET /admin/dances", adminDancesHandler(cfg, tmpls, client, i18n))
		r.HandleFunc("POST /admin/dances", adminRateLimit(adminDanceCreateHandler(cfg, client)))
		r.HandleFunc("POST /admin/dances/{id}/delete", adminRateLimit(adminDanceDeleteHandler(cfg, client)))

		r.HandleFunc("GET /admin/management", adminManagementHandler(cfg, tmpls, i18n))
		r.HandleFunc("GET /admin/recent-changes", adminRecentChangesHandler(cfg, tmpls, client, i18n))
		r.HandleFunc("GET /admin/info", adminInfoHandler(cfg, tmpls, client, i18n))
		r.HandleFunc("GET /admin/stats", adminStatsHandler(cfg, tmpls, client, i18n))

		r.HandleFunc("GET /admin/fetchurls", adminFetchurlsHandler(cfg, tmpls, client, i18n))
		r.HandleFunc("GET /admin/fetchurls/new", adminFetchurlNewPageHandler(cfg, tmpls, db, client, i18n))
		r.HandleFunc("POST /admin/fetchurls/new", adminRateLimit(adminFetchurlNewPostHandler(cfg, tmpls, db, client, i18n)))
		r.HandleFunc("POST /admin/fetchurls/bulk", adminRateLimit(adminFetchurlBulkHandler(cfg, client)))
		r.HandleFunc("GET /admin/fetchurls/{id}/edit", adminFetchurlEditPageHandler(cfg, tmpls, db, client, i18n))
		r.HandleFunc("POST /admin/fetchurls/{id}/edit", adminRateLimit(adminFetchurlSaveHandler(cfg, tmpls, db, client, i18n)))
		r.HandleFunc("POST /admin/fetchurls/{id}/delete", adminRateLimit(adminFetchurlDeleteHandler(cfg, client)))
		r.HandleFunc("POST /admin/fetchurls/{id}/run", adminRateLimit(adminFetchurlRunHandler(cfg, client)))

		r.HandleFunc("POST /admin/api/musician/quick-create", adminRateLimit(adminMusicianQuickCreateHandler(client)))
		r.HandleFunc("POST /admin/api/instructor/quick-create", adminRateLimit(adminInstructorQuickCreateHandler(client)))
		r.HandleFunc("GET /admin/musicians", musicianEntity.List(cfg, tmpls, client, i18n))
		r.HandleFunc("GET /admin/musicians/new", musicianEntity.NewPage(cfg, tmpls, i18n))
		r.HandleFunc("POST /admin/musicians/new", adminRateLimit(musicianEntity.Create(cfg, tmpls, client, i18n)))
		r.HandleFunc("GET /admin/musicians/{id}/edit", musicianEntity.EditPage(cfg, tmpls, client, i18n))
		r.HandleFunc("POST /admin/musicians/{id}/edit", adminRateLimit(musicianEntity.Save(cfg, tmpls, client, i18n)))
		r.HandleFunc("POST /admin/musicians/{id}/delete", adminRateLimit(musicianEntity.Delete(cfg, client)))

		r.HandleFunc("GET /admin/instructors", instructorEntity.List(cfg, tmpls, client, i18n))
		r.HandleFunc("GET /admin/instructors/new", instructorEntity.NewPage(cfg, tmpls, i18n))
		r.HandleFunc("POST /admin/instructors/new", adminRateLimit(instructorEntity.Create(cfg, tmpls, client, i18n)))
		r.HandleFunc("GET /admin/instructors/{id}/edit", instructorEntity.EditPage(cfg, tmpls, client, i18n))
		r.HandleFunc("POST /admin/instructors/{id}/edit", adminRateLimit(instructorEntity.Save(cfg, tmpls, client, i18n)))
		r.HandleFunc("POST /admin/instructors/{id}/delete", adminRateLimit(instructorEntity.Delete(cfg, client)))

		r.HandleFunc("GET /admin/locations", adminLocationsHandler(cfg, tmpls, client, i18n))
		r.HandleFunc("GET /admin/locations/maintenance", adminLocationMaintenanceHandler(cfg, tmpls, client, i18n))
		r.HandleFunc("GET /admin/locations/new", adminLocationNewPageHandler(cfg, tmpls, client, i18n))
		r.HandleFunc("POST /admin/locations/new", adminRateLimit(adminLocationCreateHandler(cfg, tmpls, client, i18n)))
		r.HandleFunc("POST /admin/locations/bulk-assign", adminRateLimit(adminLocationBulkAssignHandler(cfg, client)))
		r.HandleFunc("POST /admin/locations/merge", adminRateLimit(adminLocationMergeHandler(cfg, client)))
		r.HandleFunc("GET /admin/locations/{id}/json", adminLocationJSONHandler(client))
		r.HandleFunc("POST /admin/locations/{id}/update-json", adminRateLimit(adminLocationUpdateJSONHandler(cfg, client)))
		r.HandleFunc("GET /admin/locations/{id}/edit", adminLocationEditPageHandler(cfg, tmpls, client, i18n))
		r.HandleFunc("POST /admin/locations/{id}/edit", adminRateLimit(adminLocationSaveHandler(cfg, tmpls, client, i18n)))
		r.HandleFunc("POST /admin/locations/{id}/delete", adminRateLimit(adminLocationDeleteHandler(cfg, client)))
		r.HandleFunc("POST /admin/locations/{id}/assign-org", adminRateLimit(adminLocationAssignOrgHandler(cfg, client)))
		r.HandleFunc("POST /admin/locations/{id}/rooms/new", adminRateLimit(adminLocationRoomCreateHandler(cfg, client)))
		r.HandleFunc("POST /admin/locations/{id}/rooms/{room_id}/delete", adminRateLimit(adminLocationRoomDeleteHandler(cfg, client)))
		r.HandleFunc("POST /admin/locations/{id}/rooms/{room_id}/quick-edit", adminRateLimit(adminRoomQuickEditHandler(client)))
		r.HandleFunc("POST /admin/locations/{id}/plan-position", adminRateLimit(adminLocationPlanPositionHandler(cfg, client)))
		r.HandleFunc("POST /admin/locations/{id}/site-plan", adminRateLimit(adminLocationSitePlanUploadHandler(cfg, client)))
		r.HandleFunc("POST /admin/locations/{id}/site-plan/delete", adminRateLimit(adminLocationSitePlanDeleteHandler(cfg, client)))
		r.HandleFunc("POST /admin/api/location/{id}/room/quick-create", adminRateLimit(adminRoomQuickCreateHandler(client)))

		r.HandleFunc("GET /admin/enrich", adminEnrichPageHandler(cfg, tmpls, db, client, i18n))
		r.HandleFunc("POST /admin/enrich/preview", adminRateLimit(adminEnrichPreviewHandler(cfg, tmpls, db, client, i18n)))
		r.HandleFunc("POST /admin/enrich/apply", adminRateLimit(adminEnrichApplyHandler(cfg, client)))
		r.HandleFunc("POST /admin/enrich/city-aliases/new", adminRateLimit(adminEnrichAliasNewHandler(db)))
		r.HandleFunc("POST /admin/enrich/city-aliases/{id}/delete", adminRateLimit(adminEnrichAliasDeleteHandler(db)))

		// Syndication (#971, #953)
		r.HandleFunc("GET /admin/orgs/{id}/syndication", adminSyndicationGetHandler(cfg, client))
		r.HandleFunc("POST /admin/orgs/{id}/syndication", adminRateLimit(adminSyndicationSaveHandler(cfg, client)))
		r.HandleFunc("POST /admin/events/{id}/syndicate/{platform}", adminRateLimit(adminSyndicatePlatformHandler(cfg, client)))
		r.HandleFunc("GET /admin/events/{id}/syndication", adminGetSyncStatusHandler(cfg, client))

		r.HandleFunc("GET /embed/events", embedEventsHandler(cfg, tmpls, client, i18n))
		r.HandleFunc("GET /embed/event/{id}", embedEventHandler(cfg, tmpls, client, i18n))
		r.HandleFunc("GET /embed/org/{slug}", embedOrgHandler(cfg, tmpls, client, i18n))
		r.HandleFunc("GET /embed/next", embedNextHandler(cfg, tmpls, client, i18n))
		r.HandleFunc("GET /embed/calendar", embedCalendarHandler(cfg, tmpls, client, i18n))
		r.HandleFunc("GET /embed/locations", embedLocationsHandler(cfg, tmpls, client, i18n))
		r.HandleFunc("GET /embed/manifest.json", embedManifestHandler(cfg))

		return authRefreshMiddleware(client)(dashboardAttentionMiddleware(client)(certAuthMiddleware(client)(feedRouter(cfg, db, client)(r))))
	}

	i18n := loadI18n(cfg.I18nFile)

	var live liveHandler
	live.store(buildHandler(cfg, i18n))

	// Reload config+i18n on SIGHUP without restarting the server.
	go func() {
		sig := make(chan os.Signal, 1)
		signal.Notify(sig, syscall.SIGHUP)
		for range sig {
			newCfg := reloadConfig(cfg.configPath)
			if newCfg == nil {
				log.Print("reload failed, keeping current configuration")
				continue
			}
			newI18n := loadI18n(newCfg.I18nFile)
			live.store(buildHandler(newCfg, newI18n))
			log.Print("configuration reloaded")
		}
	}()

	relayActor, err := ensureRelayActor(db, cfg.RelayActorName)
	if err != nil {
		log.Printf("relay actor init: %v", err)
	}
	go startDelivery(cfg, db, client, relayActor)
	startFlashSweeper()

	log.Printf("dansal-web %s (built %s) listening on %s (domain: %s, public base URL: %s, timeouts: read=%ds write=%ds idle=%ds)",
		Version, BuildTime, cfg.Listen, cfg.Domain, cfg.publicBaseURL(), cfg.ReadTimeoutSecs, cfg.WriteTimeoutSecs, cfg.IdleTimeoutSecs)
	srv := &http.Server{
		Addr:              cfg.Listen,
		Handler:           panicRecoveryMiddleware(securityHeadersMiddleware(&live)),
		ReadHeaderTimeout: time.Duration(cfg.ReadHeaderTimeoutSecs) * time.Second,
		ReadTimeout:       time.Duration(cfg.ReadTimeoutSecs) * time.Second,
		WriteTimeout:      time.Duration(cfg.WriteTimeoutSecs) * time.Second,
		IdleTimeout:       time.Duration(cfg.IdleTimeoutSecs) * time.Second,
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer stop()

	go func() {
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatal(err)
		}
	}()

	<-ctx.Done()
	stop()
	log.Println("web server shutting down...")
	shutCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := srv.Shutdown(shutCtx); err != nil {
		log.Printf("web server shutdown error: %v", err)
	}
	log.Println("web server stopped")
}

// baselineCSP allows 'unsafe-inline' scripts/styles (most templates rely on
// inline onclick=/<style>/<script>) plus https://unpkg.com, which serves
// Leaflet (maps) and its plugins. img-src allows https: for map tiles
// (OpenStreetMap/CARTO) and data: for inline SVG/icons.
const baselineCSP = "default-src 'self'; " +
	"img-src 'self' data: https:; " +
	"font-src 'self' data:; " +
	"style-src 'self' 'unsafe-inline' https://unpkg.com; " +
	"script-src 'self' 'unsafe-inline' https://unpkg.com https://challenges.cloudflare.com; " +
	"connect-src 'self' https://nominatim.openstreetmap.org https://musicbrainz.org https://api.discogs.com https://query.wikidata.org; " +
	"object-src 'none'; base-uri 'self'; "

// panicRecoveryMiddleware recovers from a panic anywhere downstream, logs the
// stack trace with the request method/path, and writes a generic 500 instead
// of letting net/http kill the connection with a bare stack trace (#991). It
// is outermost so it also covers a panic inside securityHeadersMiddleware.
func panicRecoveryMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if rec := recover(); rec != nil {
				log.Printf("panic: %v method=%s path=%s\n%s", rec, r.Method, r.URL.Path, debug.Stack())
				http.Error(w, "Internal server error", http.StatusInternalServerError)
			}
		}()
		next.ServeHTTP(w, r)
	})
}

func securityHeadersMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Referrer-Policy", "strict-origin-when-cross-origin")
		// HSTS is set by nginx; setting it here too produces a duplicate header.
		w.Header().Set("Permissions-Policy", "geolocation=(self), camera=(), microphone=(), usb=()")
		w.Header().Set("Cross-Origin-Opener-Policy", "same-origin")
		// Embed pages must be iframeable by any origin; all other pages restrict to same-origin.
		if strings.HasPrefix(r.URL.Path, "/embed/") {
			w.Header().Set("Content-Security-Policy", baselineCSP+"frame-ancestors *")
		} else {
			w.Header().Set("X-Frame-Options", "SAMEORIGIN")
			w.Header().Set("Content-Security-Policy", baselineCSP+"frame-ancestors 'self'")
		}
		next.ServeHTTP(w, r)
	})
}
