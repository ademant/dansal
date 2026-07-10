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
	"path/filepath"
	"strings"
	"sync/atomic"
	"syscall"
	"time"
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

// instanceFromConfigArg extracts the instance name from the --config path
// (e.g. "/etc/dansal/dev/web.yaml" → "dev") without calling flag.Parse().
func instanceFromConfigArg() string {
	for i, arg := range os.Args {
		var path string
		switch {
		case (arg == "--config" || arg == "-config") && i+1 < len(os.Args):
			path = os.Args[i+1]
		case strings.HasPrefix(arg, "--config="):
			path = arg[len("--config="):]
		case strings.HasPrefix(arg, "-config="):
			path = arg[len("-config="):]
		}
		if path != "" {
			return filepath.Base(filepath.Dir(path))
		}
	}
	return ""
}

func main() {
	// Register --version before loadConfig() calls flag.Parse().
	printVersion := flag.Bool("version", false, "print version and build date then exit")

	tag := "dansal-web"
	if inst := instanceFromConfigArg(); inst != "" {
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
	db := initDB(cfg.DBPath)
	siteCfg = newSiteSettingsCache(db)
	client := &DansalClient{
		BaseURL:        cfg.DansalURL,
		HTTP:           &http.Client{Timeout: 15 * time.Second},
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
		r.HandleFunc("GET /nodeinfo/2.0", nodeinfoHandler(cfg, client))
		r.HandleFunc("GET /nodeinfo/2.1", nodeinfo21Handler(cfg, client))
		r.HandleFunc("GET /.well-known/security.txt", securityTxtHandler(cfg))
		r.HandleFunc("GET /.well-known/host-meta", hostMetaHandler(cfg))
		r.HandleFunc("GET /.well-known/dnt-policy.txt", dntPolicyHandler())
		r.HandleFunc("GET /.well-known/dnt", dntStatusHandler(cfg))
		r.HandleFunc("GET /health", healthHandler())
		r.HandleFunc("GET /robots.txt", robotsTxtHandler(cfg))
		r.HandleFunc("GET /sitemap.xml", sitemapHandler(cfg, client))
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

		r.HandleFunc("GET /invites/{token}", invitePageHandler(cfg, tmpls, client, i18n))
		r.HandleFunc("POST /invites/{token}/password", invitePasswordHandler(cfg, client))
		r.HandleFunc("POST /invites/{token}/webauthn/begin", webauthnInviteProxy(cfg, client, "begin"))
		r.HandleFunc("POST /invites/{token}/webauthn/finish", webauthnInviteProxy(cfg, client, "finish"))
		r.HandleFunc("POST /auth/webauthn/login/begin", webauthnProxy(cfg, client, "/api/v1/auth/webauthn/login/begin"))
		r.HandleFunc("POST /auth/webauthn/login/finish", webauthnProxy(cfg, client, "/api/v1/auth/webauthn/login/finish"))
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

		r.HandleFunc("GET /favicon.svg", dynamicSVGHandler(cfg.ImagesDir, "favicon", faviconSVG))
		r.HandleFunc("GET /logo.avif", dynamicSVGHandler(cfg.ImagesDir, "logo", logoAVIF))
		r.HandleFunc("GET /banner.avif", dynamicSVGHandler(cfg.ImagesDir, "banner", bannerAVIF))
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
		r.HandleFunc("GET /", indexHandler(cfg, tmpls, db, client, i18n))
		r.HandleFunc("GET /api/events-more", eventsMoreHandler(tmpls, i18n, client))
		r.HandleFunc("GET /dashboard", dashboardHandler(cfg, tmpls, client, i18n))
		r.HandleFunc("GET /search", searchPageHandler(cfg, tmpls, client, i18n))
		r.HandleFunc("GET /search/results", searchResultsHandler(tmpls, i18n, client))
		r.HandleFunc("GET /events/{id}", eventHandler(cfg, tmpls, client, i18n))
		r.HandleFunc("POST /events/{id}/assign-org", adminRateLimit(eventAssignOrgHandler(cfg, client)))
		r.HandleFunc("POST /events/{id}/board", contactBoardPostHandler(cfg, db, client, i18n))
		r.HandleFunc("POST /events/{id}/board/{post_id}/delete", contactBoardDeleteHandler(cfg, client))
		r.HandleFunc("POST /events/{id}/board/{post_id}/contact", contactBoardContactHandler(cfg, client))
		r.HandleFunc("GET /contact-posts/verify/{token}", contactBoardVerifyRedirect)
		r.HandleFunc("GET /contact-posts/delete/{token}", contactBoardDeleteRedirect)
		r.HandleFunc("GET /contact-posts/manage/{token}", contactManageGetHandler(cfg, db, tmpls, client, i18n))
		r.HandleFunc("POST /contact-posts/manage/{token}", contactManagePostHandler(cfg, client, i18n))
		r.HandleFunc("GET /contact-requests/verify/{token}", contactRequestVerifyHandler(cfg, tmpls, client, i18n))
		r.HandleFunc("GET /checkin/{qr_token}", checkinGetHandler(cfg, tmpls, i18n))
		r.HandleFunc("POST /checkin/{qr_token}", checkinPostHandler(cfg, client))
		r.HandleFunc("POST /events/{id}/book", bookingPostHandler(cfg, client, i18n))
		r.HandleFunc("GET /bookings/verify/{token}", bookingVerifyHandler(cfg, tmpls, client, i18n))
		r.HandleFunc("GET /board", boardHandler(cfg, tmpls, client, i18n))
		r.HandleFunc("GET /musicians", musiciansHandler(cfg, tmpls, client, i18n))
		r.HandleFunc("GET /musicians/{id}", musicianHandler(cfg, tmpls, client, i18n))
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
		r.HandleFunc("POST /admin/users/{id}/role", adminRateLimit(adminUserRoleHandler(cfg, client)))
		r.HandleFunc("POST /admin/users/{id}/org", adminRateLimit(adminUserOrgHandler(cfg, client)))
		r.HandleFunc("POST /admin/invites/new", adminRateLimit(adminInviteCreateHandler(cfg, client)))
		r.HandleFunc("POST /admin/invites/{token}/revoke", adminRateLimit(adminInviteRevokeHandler(cfg, client)))
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
		r.HandleFunc("POST /admin/events/{id}/delete", adminRateLimit(adminEventDeleteHandler(cfg, db, client)))
		r.HandleFunc("POST /admin/events/merge", adminRateLimit(adminEventMergeHandler(cfg, db, client)))
		r.HandleFunc("POST /admin/events/bulk-publish", adminRateLimit(adminEventBulkPublishHandler(cfg, client)))
		r.HandleFunc("POST /admin/events/bulk-cancel", adminRateLimit(adminEventBulkCancelHandler(cfg, client)))
		r.HandleFunc("POST /admin/events/bulk-delete", adminRateLimit(adminEventBulkDeleteHandler(cfg, db, client)))
		r.HandleFunc("POST /admin/events/bulk-assign-location", adminRateLimit(adminEventBulkAssignLocationHandler(cfg, client)))
		r.HandleFunc("POST /admin/events/bulk-set-attributes", adminRateLimit(adminEventBulkSetAttributesHandler(cfg, client)))
		r.HandleFunc("POST /admin/events/bulk-assign-series", adminRateLimit(adminEventBulkAssignSeriesHandler(cfg, tmpls, client, i18n)))
		r.HandleFunc("POST /admin/events/{id}/image/delete", adminRateLimit(adminEventImageDeleteHandler(cfg, client)))
		r.HandleFunc("POST /admin/musicians/{id}/image/delete", adminRateLimit(adminMusicianImageDeleteHandler(cfg, client)))
		r.HandleFunc("POST /admin/organizations/{id}/image/delete", adminRateLimit(adminOrgImageDeleteHandler(cfg, client)))
		r.HandleFunc("GET /admin/events/maintenance", adminEventsMaintenanceHandler(cfg, tmpls, client, i18n))
		r.HandleFunc("GET /admin/events", adminEventsHandler(cfg, tmpls, client, i18n))
		r.HandleFunc("GET /admin/events/import", adminImportEventsPageHandler(cfg, tmpls, i18n))
		r.HandleFunc("POST /admin/events/import", adminRateLimit(adminImportEventsHandler(cfg, tmpls, client, i18n)))
		r.HandleFunc("POST /admin/events/import/confirm", adminRateLimit(adminImportConfirmHandler(cfg, client)))
		r.HandleFunc("GET /admin/events/new", adminEventNewPageHandler(cfg, tmpls, db, client, i18n))
		r.HandleFunc("POST /admin/events/new", adminRateLimit(adminEventCreateHandler(cfg, tmpls, db, client, i18n)))
		r.HandleFunc("GET /admin/events/{id}/edit", adminEventEditPageHandler(cfg, tmpls, db, client, i18n))
		r.HandleFunc("POST /admin/events/{id}/edit", adminRateLimit(adminEventSaveHandler(cfg, tmpls, db, client, i18n)))
		r.HandleFunc("POST /admin/events/{id}/assign-series", adminRateLimit(adminEventAssignSeriesHandler(cfg, client)))
		r.HandleFunc("POST /admin/events/{id}/remove-from-series", adminRateLimit(adminEventRemoveFromSeriesHandler(cfg, client)))
		r.HandleFunc("POST /admin/events/{id}/save-template", adminRateLimit(adminTemplateSaveHandler(cfg, db, client)))
		r.HandleFunc("GET /admin/events/template-assign", adminTemplateAssignPageHandler(cfg, tmpls, db, client, i18n))
		r.HandleFunc("POST /admin/events/template-assign", adminRateLimit(adminTemplateAssignApplyHandler(cfg, db, client)))
		r.HandleFunc("GET /admin/templates", adminTemplatesHandler(cfg, tmpls, db, client, i18n))
		r.HandleFunc("POST /admin/templates/{id}/delete", adminRateLimit(adminTemplateDeleteHandler(db)))
		r.HandleFunc("GET /admin/templates/{id}/data", adminTemplateDataHandler(db))

		r.HandleFunc("GET /admin/organization/{slug}", adminOrgDashboardHandler(cfg, tmpls, db, client, i18n))
		r.HandleFunc("GET /admin/location/{id}", adminLocationDashboardHandler(cfg, tmpls, client, i18n))
		r.HandleFunc("GET /admin/organizations", adminOrgsHandler(cfg, tmpls, client, i18n))
		r.HandleFunc("GET /admin/organizations/check-actor-name", adminOrgCheckActorNameHandler(cfg, client))
		r.HandleFunc("GET /admin/organizations/new", adminOrgNewPageHandler(cfg, tmpls, i18n))
		r.HandleFunc("POST /admin/organizations/new", adminRateLimit(adminOrgCreateHandler(cfg, tmpls, client, i18n)))
		r.HandleFunc("GET /admin/organizations/{id}/edit", adminOrgEditPageHandler(cfg, tmpls, client, i18n, db))
		r.HandleFunc("POST /admin/organizations/{id}/edit", adminRateLimit(adminOrgSaveHandler(cfg, tmpls, db, client, i18n)))
		r.HandleFunc("POST /admin/organizations/{id}/delete", adminRateLimit(adminOrgDeleteHandler(cfg, client)))
		r.HandleFunc("POST /admin/organizations/{id}/run-feeds", adminRateLimit(adminOrgRunFeedsHandler(cfg, client)))
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
		r.HandleFunc("GET /admin/musicians", adminMusiciansHandler(cfg, tmpls, client, i18n))
		r.HandleFunc("GET /admin/musicians/new", adminMusicianNewPageHandler(cfg, tmpls, i18n))
		r.HandleFunc("POST /admin/musicians/new", adminRateLimit(adminMusicianCreateHandler(cfg, tmpls, client, i18n)))
		r.HandleFunc("GET /admin/musicians/{id}/edit", adminMusicianEditPageHandler(cfg, tmpls, client, i18n))
		r.HandleFunc("POST /admin/musicians/{id}/edit", adminRateLimit(adminMusicianSaveHandler(cfg, tmpls, client, i18n)))
		r.HandleFunc("POST /admin/musicians/{id}/delete", adminRateLimit(adminMusicianDeleteHandler(cfg, client)))

		r.HandleFunc("GET /admin/instructors", adminInstructorsHandler(cfg, tmpls, client, i18n))
		r.HandleFunc("GET /admin/instructors/new", adminInstructorNewPageHandler(cfg, tmpls, i18n))
		r.HandleFunc("POST /admin/instructors/new", adminRateLimit(adminInstructorCreateHandler(cfg, tmpls, client, i18n)))
		r.HandleFunc("GET /admin/instructors/{id}/edit", adminInstructorEditPageHandler(cfg, tmpls, client, i18n))
		r.HandleFunc("POST /admin/instructors/{id}/edit", adminRateLimit(adminInstructorSaveHandler(cfg, tmpls, client, i18n)))
		r.HandleFunc("POST /admin/instructors/{id}/delete", adminRateLimit(adminInstructorDeleteHandler(cfg, client)))

		r.HandleFunc("GET /admin/locations", adminLocationsHandler(cfg, tmpls, client, i18n))
		r.HandleFunc("GET /admin/locations/maintenance", adminLocationMaintenanceHandler(cfg, tmpls, client, i18n))
		r.HandleFunc("GET /admin/locations/new", adminLocationNewPageHandler(cfg, tmpls, client, i18n))
		r.HandleFunc("POST /admin/locations/new", adminRateLimit(adminLocationCreateHandler(cfg, tmpls, client, i18n)))
		r.HandleFunc("POST /admin/locations/bulk-assign", adminRateLimit(adminLocationBulkAssignHandler(cfg, client)))
		r.HandleFunc("POST /admin/locations/merge", adminRateLimit(adminLocationMergeHandler(cfg, client)))
		r.HandleFunc("POST /admin/locations/{id}/update-json", adminRateLimit(adminLocationUpdateJSONHandler(cfg, client)))
		r.HandleFunc("GET /admin/locations/{id}/edit", adminLocationEditPageHandler(cfg, tmpls, client, i18n))
		r.HandleFunc("POST /admin/locations/{id}/edit", adminRateLimit(adminLocationSaveHandler(cfg, tmpls, client, i18n)))
		r.HandleFunc("POST /admin/locations/{id}/delete", adminRateLimit(adminLocationDeleteHandler(cfg, client)))
		r.HandleFunc("POST /admin/locations/{id}/assign-org", adminRateLimit(adminLocationAssignOrgHandler(cfg, client)))

		r.HandleFunc("GET /admin/enrich", adminEnrichPageHandler(cfg, tmpls, db, client, i18n))
		r.HandleFunc("POST /admin/enrich/preview", adminRateLimit(adminEnrichPreviewHandler(cfg, tmpls, db, client, i18n)))
		r.HandleFunc("POST /admin/enrich/apply", adminRateLimit(adminEnrichApplyHandler(cfg, client)))
		r.HandleFunc("POST /admin/enrich/city-aliases/new", adminRateLimit(adminEnrichAliasNewHandler(db)))
		r.HandleFunc("POST /admin/enrich/city-aliases/{id}/delete", adminRateLimit(adminEnrichAliasDeleteHandler(db)))

		r.HandleFunc("GET /embed/events", embedEventsHandler(cfg, tmpls, client, i18n))
		r.HandleFunc("GET /embed/event/{id}", embedEventHandler(cfg, tmpls, client, i18n))
		r.HandleFunc("GET /embed/org/{slug}", embedOrgHandler(cfg, tmpls, client, i18n))
		r.HandleFunc("GET /embed/next", embedNextHandler(cfg, tmpls, client, i18n))
		r.HandleFunc("GET /embed/calendar", embedCalendarHandler(cfg, tmpls, client, i18n))
		r.HandleFunc("GET /embed/locations", embedLocationsHandler(cfg, tmpls, client, i18n))

		return dashboardAttentionMiddleware(client)(certAuthMiddleware(client)(feedRouter(cfg, db, client)(r)))
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

	log.Printf("web server listening on %s (domain: %s, public base URL: %s, timeouts: read=%ds write=%ds idle=%ds)",
		cfg.Listen, cfg.Domain, cfg.publicBaseURL(), cfg.ReadTimeoutSecs, cfg.WriteTimeoutSecs, cfg.IdleTimeoutSecs)
	srv := &http.Server{
		Addr:              cfg.Listen,
		Handler:           securityHeadersMiddleware(&live),
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

func securityHeadersMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Referrer-Policy", "strict-origin-when-cross-origin")
		w.Header().Set("Strict-Transport-Security", "max-age=31536000; includeSubDomains; preload")
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
