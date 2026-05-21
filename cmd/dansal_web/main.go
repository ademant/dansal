package main

import (
	"flag"
	"fmt"
	"log"
	"log/syslog"
	"net/http"
	"os"
	"os/signal"
	"sync/atomic"
	"syscall"
	"time"
)

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

	if w, err := syslog.New(syslog.LOG_INFO|syslog.LOG_DAEMON, "dansal_web"); err == nil {
		log.SetOutput(w)
		log.SetFlags(0)
	}

	cfg := loadConfig()

	if *printVersion {
		fmt.Printf("dansal-web %s (built %s)\n", Version, BuildTime)
		os.Exit(0)
	}
	cfg.pagesContent = loadPagesContent(cfg.PagesFile)
	db := initDB(cfg.DBPath)
	if v := getSiteSetting(db, "site_name"); v != "" {
		cfg.SiteName = v
	}
	if v := getSiteSetting(db, "contact"); v != "" {
		cfg.ContactOverride = v
	}
	imp := make(map[string]string)
	for _, lang := range impressumLangs {
		if v := getSiteSetting(db, "impressum_"+lang); v != "" {
			imp[lang] = v
		}
	}
	cfg.ImpressumOverride = imp
	client := &DansalClient{
		BaseURL: cfg.DansalURL,
		HTTP:    &http.Client{Timeout: 15 * time.Second},
	}

	tmpls := loadTemplates()

	// buildHandler constructs all route closures from the given cfg and i18n.
	// Called at startup and again on each SIGHUP reload.
	buildHandler := func(cfg *Config, i18n *I18n) http.Handler {
		r := http.NewServeMux()

		r.HandleFunc("GET /actors", actorsListHandler(cfg, db))
		r.HandleFunc("GET /.well-known/webfinger", webfingerHandler(cfg, db, client))
		r.HandleFunc("GET /.well-known/nodeinfo", nodeinfoIndexHandler(cfg))
		r.HandleFunc("GET /nodeinfo/2.0", nodeinfoHandler(cfg))
		r.HandleFunc("GET /nodeinfo/2.1", nodeinfo21Handler(cfg))

		r.HandleFunc("GET /org/{name}", actorOrFrontendHandler(cfg, tmpls, db, client, i18n))
		r.HandleFunc("GET /org/{name}/outbox", outboxHandler(cfg, db, client))
		r.HandleFunc("GET /org/{name}/followers", followersHandler(cfg, db))
		r.HandleFunc("POST /org/{name}/inbox", inboxHandler(cfg, db, client))
		r.HandleFunc("POST /inbox", sharedInboxHandler(cfg, db, client))
		r.HandleFunc("POST /telegram/webhook", telegramWebhookProxyHandler(cfg))

		r.HandleFunc("GET /events/suggest", suggestPageHandler(cfg, tmpls, i18n))
		r.HandleFunc("POST /events/suggest", suggestPreviewHandler(cfg, tmpls, client, i18n))
		r.HandleFunc("POST /events/suggest/submit", suggestSubmitHandler(cfg, tmpls, client, i18n))
		r.HandleFunc("GET /events/suggest/done", suggestDoneHandler(cfg, tmpls, i18n))
		r.HandleFunc("GET /events/suggest/verify/{token}", suggestVerifyHandler(cfg, tmpls, client, i18n))

		r.HandleFunc("GET /favicon.svg", dynamicSVGHandler(cfg.ImagesDir, "favicon", faviconSVG))
		r.HandleFunc("GET /logo.svg", dynamicSVGHandler(cfg.ImagesDir, "logo", logoSVG))
		r.HandleFunc("GET /banner.svg", dynamicSVGHandler(cfg.ImagesDir, "banner", bannerSVG))
		r.HandleFunc("GET /federated-events/{id}", federatedEventHandler(db))
		r.HandleFunc("GET /", indexHandler(cfg, tmpls, db, client, i18n))
		r.HandleFunc("GET /events/{id}", eventHandler(cfg, tmpls, client, i18n))
		r.HandleFunc("POST /events/{id}/board", contactBoardPostHandler(cfg, client, i18n))
		r.HandleFunc("POST /events/{id}/board/{post_id}/delete", contactBoardDeleteHandler(cfg, client))
		r.HandleFunc("POST /events/{id}/board/{post_id}/contact", contactBoardContactHandler(cfg, client))
		r.HandleFunc("GET /contact-posts/verify/{token}", contactBoardVerifyHandler(cfg, tmpls, client, i18n))
		r.HandleFunc("GET /contact-posts/delete/{token}", contactBoardDeleteByTokenHandler(cfg, tmpls, client, i18n))
		r.HandleFunc("GET /contact-requests/verify/{token}", contactRequestVerifyHandler(cfg, tmpls, client, i18n))
		r.HandleFunc("GET /checkin/{qr_token}", checkinGetHandler(cfg, tmpls, i18n))
		r.HandleFunc("POST /checkin/{qr_token}", checkinPostHandler(cfg, client))
		r.HandleFunc("POST /events/{id}/book", bookingPostHandler(cfg, client, i18n))
		r.HandleFunc("GET /bookings/verify/{token}", bookingVerifyHandler(cfg, tmpls, client, i18n))
		r.HandleFunc("GET /musicians", musiciansHandler(cfg, tmpls, client, i18n))
		r.HandleFunc("GET /musicians/{id}", musicianHandler(cfg, tmpls, client, i18n))
		r.HandleFunc("GET /organizations", orgsHandler(cfg, tmpls, db, client, i18n))
		r.HandleFunc("GET /impressum", impressumHandler(cfg, tmpls, i18n))

		r.HandleFunc("GET /login", loginPageHandler(cfg, tmpls, i18n))
		r.HandleFunc("POST /login", loginHandler(cfg, tmpls, client, i18n))
		r.HandleFunc("POST /logout", logoutHandler(cfg, client))
		r.HandleFunc("GET /lang", langHandler(i18n))
		r.HandleFunc("GET /settings", settingsPageHandler(cfg, tmpls, client, i18n))
		r.HandleFunc("POST /settings", settingsUpdateHandler(cfg, tmpls, client, i18n))
		r.HandleFunc("POST /settings/verify", settingsSendVerifyHandler(cfg, tmpls, client, i18n))
		r.HandleFunc("POST /settings/verify-telegram", settingsTelegramVerifyHandler(cfg, tmpls, client, i18n))
		r.HandleFunc("POST /settings/apikeys/new", settingsCreateAPIKeyHandler(cfg, tmpls, client, i18n))
		r.HandleFunc("POST /settings/apikeys/{id}/delete", settingsDeleteAPIKeyHandler(cfg, client))
		r.HandleFunc("POST /magic", magicRequestHandler(cfg, tmpls, client, i18n))
		r.HandleFunc("GET /api/v1/login/magic/{token}", magicLoginHandler(cfg, tmpls, client, i18n))
		r.HandleFunc("GET /api/v1/verify/{token}", verifyEmailHandler(cfg, tmpls, client, i18n))

		r.HandleFunc("GET /admin/users", adminUsersHandler(cfg, tmpls, client, i18n))
		r.HandleFunc("POST /admin/users/bulk", adminUsersBulkHandler(cfg, client))
		r.HandleFunc("POST /admin/users/{id}/delete", adminUserDeleteHandler(cfg, client))
		r.HandleFunc("POST /admin/users/{id}/role", adminUserRoleHandler(cfg, client))
		r.HandleFunc("POST /admin/users/{id}/org", adminUserOrgHandler(cfg, client))
		r.HandleFunc("POST /admin/invites/new", adminInviteCreateHandler(cfg, client))
		r.HandleFunc("POST /admin/invites/{token}/revoke", adminInviteRevokeHandler(cfg, client))

		r.HandleFunc("GET /admin/events/{id}/bookings", adminBookingsHandler(cfg, tmpls, client, i18n))
		r.HandleFunc("POST /admin/bookings/{id}/approve", adminBookingApproveHandler(cfg, client))
		r.HandleFunc("POST /admin/bookings/{id}/cancel", adminBookingCancelHandler(cfg, client))
		r.HandleFunc("POST /admin/bookings/{id}/delete", adminBookingDeleteHandler(cfg, client))
		r.HandleFunc("POST /admin/events/{id}/delete", adminEventDeleteHandler(cfg, db, client))
		r.HandleFunc("POST /admin/events/{id}/image/delete", adminEventImageDeleteHandler(cfg, client))
		r.HandleFunc("POST /admin/musicians/{id}/image/delete", adminMusicianImageDeleteHandler(cfg, client))
		r.HandleFunc("POST /admin/organizations/{id}/image/delete", adminOrgImageDeleteHandler(cfg, client))
		r.HandleFunc("GET /admin/events", adminEventsHandler(cfg, tmpls, client, i18n))
		r.HandleFunc("GET /admin/events/import", adminImportEventsPageHandler(cfg, tmpls, i18n))
		r.HandleFunc("POST /admin/events/import", adminImportEventsHandler(cfg, tmpls, client, i18n))
		r.HandleFunc("POST /admin/events/import/confirm", adminImportConfirmHandler(cfg, client))
		r.HandleFunc("GET /admin/events/new", adminEventNewPageHandler(cfg, tmpls, db, client, i18n))
		r.HandleFunc("POST /admin/events/new", adminEventCreateHandler(cfg, tmpls, db, client, i18n))
		r.HandleFunc("GET /admin/events/{id}/edit", adminEventEditPageHandler(cfg, tmpls, client, i18n))
		r.HandleFunc("POST /admin/events/{id}/edit", adminEventSaveHandler(cfg, tmpls, db, client, i18n))

		r.HandleFunc("GET /admin/organizations", adminOrgsHandler(cfg, tmpls, client, i18n))
		r.HandleFunc("GET /admin/organizations/check-actor-name", adminOrgCheckActorNameHandler(cfg, client))
		r.HandleFunc("GET /admin/organizations/new", adminOrgNewPageHandler(cfg, tmpls, i18n))
		r.HandleFunc("POST /admin/organizations/new", adminOrgCreateHandler(cfg, tmpls, client, i18n))
		r.HandleFunc("GET /admin/organizations/{id}/edit", adminOrgEditPageHandler(cfg, tmpls, client, i18n, db))
		r.HandleFunc("POST /admin/organizations/{id}/edit", adminOrgSaveHandler(cfg, tmpls, client, i18n))
		r.HandleFunc("POST /admin/organizations/{id}/delete", adminOrgDeleteHandler(cfg, client))
		r.HandleFunc("POST /admin/organizations/{id}/run-feeds", adminOrgRunFeedsHandler(cfg, client))
		r.HandleFunc("POST /admin/organizations/{id}/members", adminOrgMemberHandler(cfg, client))
		r.HandleFunc("POST /admin/organizations/{id}/locations", adminOrgLocationsHandler(cfg, client))
		r.HandleFunc("POST /admin/organizations/{id}/follow", adminOrgFollowHandler(cfg, db, client))
		r.HandleFunc("POST /admin/organizations/{id}/unfollow", adminOrgUnfollowHandler(cfg, db, client))

		r.HandleFunc("GET /admin/dances", adminDancesHandler(cfg, tmpls, client, i18n))
		r.HandleFunc("POST /admin/dances", adminDanceCreateHandler(cfg, client))
		r.HandleFunc("POST /admin/dances/{id}/delete", adminDanceDeleteHandler(cfg, client))

		r.HandleFunc("GET /admin/site-config", adminSiteConfigHandler(cfg, tmpls, db, client, i18n))
		r.HandleFunc("POST /admin/site-config", adminSiteConfigSaveHandler(cfg, db, client))

		r.HandleFunc("GET /admin/info", adminInfoHandler(cfg, tmpls, client, i18n))

		r.HandleFunc("GET /admin/fetchurls", adminFetchurlsHandler(cfg, tmpls, client, i18n))
		r.HandleFunc("GET /admin/fetchurls/new", adminFetchurlNewPageHandler(cfg, tmpls, client, i18n))
		r.HandleFunc("POST /admin/fetchurls/new", adminFetchurlNewPostHandler(cfg, tmpls, client, i18n))
		r.HandleFunc("POST /admin/fetchurls/bulk", adminFetchurlBulkHandler(cfg, client))
		r.HandleFunc("GET /admin/fetchurls/{id}/edit", adminFetchurlEditPageHandler(cfg, tmpls, client, i18n))
		r.HandleFunc("POST /admin/fetchurls/{id}/edit", adminFetchurlSaveHandler(cfg, tmpls, client, i18n))
		r.HandleFunc("POST /admin/fetchurls/{id}/delete", adminFetchurlDeleteHandler(cfg, client))
		r.HandleFunc("POST /admin/fetchurls/{id}/run", adminFetchurlRunHandler(cfg, client))

		r.HandleFunc("GET /admin/musicians", adminMusiciansHandler(cfg, tmpls, client, i18n))
		r.HandleFunc("GET /admin/musicians/new", adminMusicianNewPageHandler(cfg, tmpls, i18n))
		r.HandleFunc("POST /admin/musicians/new", adminMusicianCreateHandler(cfg, tmpls, client, i18n))
		r.HandleFunc("GET /admin/musicians/{id}/edit", adminMusicianEditPageHandler(cfg, tmpls, client, i18n))
		r.HandleFunc("POST /admin/musicians/{id}/edit", adminMusicianSaveHandler(cfg, tmpls, client, i18n))
		r.HandleFunc("POST /admin/musicians/{id}/delete", adminMusicianDeleteHandler(cfg, client))

		r.HandleFunc("GET /admin/locations", adminLocationsHandler(cfg, tmpls, client, i18n))
		r.HandleFunc("GET /admin/locations/new", adminLocationNewPageHandler(cfg, tmpls, client, i18n))
		r.HandleFunc("POST /admin/locations/new", adminLocationCreateHandler(cfg, tmpls, client, i18n))
		r.HandleFunc("POST /admin/locations/bulk-assign", adminLocationBulkAssignHandler(cfg, client))
		r.HandleFunc("GET /admin/locations/{id}/edit", adminLocationEditPageHandler(cfg, tmpls, client, i18n))
		r.HandleFunc("POST /admin/locations/{id}/edit", adminLocationSaveHandler(cfg, tmpls, client, i18n))
		r.HandleFunc("POST /admin/locations/{id}/delete", adminLocationDeleteHandler(cfg, client))

		r.HandleFunc("GET /api/v1/images/{id}", imageProxyHandler(client, "/api/v1/images/"))
		r.HandleFunc("GET /api/v1/musician-images/{id}", imageProxyHandler(client, "/api/v1/musician-images/"))
		r.HandleFunc("GET /api/v1/org-images/{id}", imageProxyHandler(client, "/api/v1/org-images/"))

		return feedRouter(cfg, db, client)(r)
	}

	i18n := loadI18n(cfg.I18nFile)

	var live liveHandler
	live.store(buildHandler(cfg, i18n))

	// Reload config+i18n on SIGHUP without restarting the server.
	go func() {
		sig := make(chan os.Signal, 1)
		signal.Notify(sig, syscall.SIGHUP)
		for range sig {
			newCfg := reloadConfig(cfg.configPath, db)
			if newCfg == nil {
				log.Print("reload failed, keeping current configuration")
				continue
			}
			newI18n := loadI18n(newCfg.I18nFile)
			live.store(buildHandler(newCfg, newI18n))
			log.Print("configuration reloaded")
		}
	}()

	relayActor, err := ensureRelayActor(db)
	if err != nil {
		log.Printf("relay actor init: %v", err)
	}
	go startDelivery(cfg, db, client, relayActor)

	log.Printf("web server listening on %s (domain: %s, public base URL: %s)", cfg.Listen, cfg.Domain, cfg.publicBaseURL())
	if err := http.ListenAndServe(cfg.Listen, securityHeadersMiddleware(&live)); err != nil {
		log.Fatal(err)
	}
}

func securityHeadersMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("X-Frame-Options", "SAMEORIGIN")
		w.Header().Set("Referrer-Policy", "strict-origin-when-cross-origin")
		w.Header().Set("Strict-Transport-Security", "max-age=31536000; includeSubDomains")
		next.ServeHTTP(w, r)
	})
}
