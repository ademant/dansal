package main

import (
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

var httpClient = &http.Client{Timeout: 15 * time.Second}

type liveHandler struct {
	p atomic.Pointer[http.Handler]
}

func (lh *liveHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	(*lh.p.Load()).ServeHTTP(w, r)
}

func (lh *liveHandler) store(h http.Handler) {
	lh.p.Store(&h)
}

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
	printVersion := flag.Bool("version", false, "print version and build date then exit")

	tag := "dansal-webmin"
	if inst := instanceFromConfigArg(); inst != "" {
		tag = "dansal-webmin@" + inst
	}
	if w, err := syslog.New(syslog.LOG_INFO|syslog.LOG_DAEMON, tag); err == nil {
		log.SetOutput(w)
		log.SetFlags(0)
	}

	cfg := loadConfig()

	if *printVersion {
		fmt.Printf("dansal-webmin %s (built %s)\n", Version, BuildTime)
		os.Exit(0)
	}

	tmpls := loadTemplates()
	webDB := openWebDB(cfg.WebDBPath)

	buildHandler := func(cfg *Config) http.Handler {
		mux := http.NewServeMux()
		mux.HandleFunc("GET /login", loginPageHandler(cfg, tmpls))
		mux.HandleFunc("POST /login", loginPostHandler(cfg, tmpls))
		mux.HandleFunc("POST /logout", logoutHandler(cfg))
		mux.HandleFunc("GET /", requireLogin(cfg, dashboardHandler(cfg, tmpls)))
		mux.HandleFunc("GET /users", requireLogin(cfg, usersPageHandler(cfg, tmpls)))
		mux.HandleFunc("GET /users/{email}/sessions", requireLogin(cfg, userSessionsPageHandler(cfg, tmpls)))
		mux.HandleFunc("POST /users/{email}/sessions/{id}/revoke", requireLogin(cfg, userRevokeSessionHandler(cfg)))
		mux.HandleFunc("POST /users/{email}/magic-link", requireLogin(cfg, userMagicLinkHandler(cfg)))
		mux.HandleFunc("POST /users/{email}/invite-admin", requireLogin(cfg, userInviteAdminHandler(cfg)))
		mux.HandleFunc("GET /notifications", requireLogin(cfg, notificationsPageHandler(cfg, tmpls)))
		mux.HandleFunc("POST /notifications/smtp", requireLogin(cfg, notificationsSMTPSaveHandler(cfg)))
		mux.HandleFunc("POST /notifications/smtp-test", requireLogin(cfg, notificationsSMTPTestHandler(cfg)))
		mux.HandleFunc("POST /notifications/telegram-test", requireLogin(cfg, notificationsTelegramTestHandler(cfg)))
		mux.HandleFunc("POST /notifications/matrix-login", requireLogin(cfg, notificationsMatrixLoginHandler(cfg)))
		mux.HandleFunc("POST /notifications/matrix-test", requireLogin(cfg, notificationsMatrixTestHandler(cfg)))
		mux.HandleFunc("POST /notifications/telegram", requireLogin(cfg, notificationsTelegramSaveHandler(cfg)))
		mux.HandleFunc("POST /notifications/matrix", requireLogin(cfg, notificationsMatrixSaveHandler(cfg)))
		mux.HandleFunc("POST /notifications/heartbeat", requireLogin(cfg, notificationsHeartbeatSaveHandler(cfg)))
		mux.HandleFunc("GET /maintenance", requireLogin(cfg, maintenancePageHandler(cfg, tmpls)))
		mux.HandleFunc("POST /maintenance/vacuum", requireLogin(cfg, maintenanceVacuumHandler(cfg)))
		mux.HandleFunc("POST /maintenance/prune-images", requireLogin(cfg, maintenancePruneImagesHandler(cfg)))
		mux.HandleFunc("POST /maintenance/fetch-all", requireLogin(cfg, maintenanceFetchAllHandler(cfg)))
		mux.HandleFunc("POST /maintenance/backup", requireLogin(cfg, maintenanceBackupHandler(cfg)))
		mux.HandleFunc("POST /maintenance/restore", requireLogin(cfg, maintenanceRestoreHandler(cfg)))
		mux.HandleFunc("GET /site-config", requireLogin(cfg, siteConfigPageHandler(cfg, tmpls, webDB)))
		mux.HandleFunc("POST /site-config", requireLogin(cfg, siteConfigSaveHandler(cfg, webDB)))
		mux.HandleFunc("POST /site-config/relay/redeliver", requireLogin(cfg, siteConfigRelayRedeliverHandler(cfg)))
		mux.HandleFunc("GET /bot-stats", requireLogin(cfg, botStatsPageHandler(cfg, tmpls)))
		return mux
	}

	var live liveHandler
	live.store(buildHandler(cfg))

	go func() {
		sig := make(chan os.Signal, 1)
		signal.Notify(sig, syscall.SIGHUP)
		for range sig {
			newCfg := reloadConfig(cfg.configPath)
			if newCfg == nil {
				log.Print("reload failed, keeping current configuration")
				continue
			}
			live.store(buildHandler(newCfg))
			log.Print("configuration reloaded")
		}
	}()

	log.Printf("dansal-webmin %s listening on %s (timeouts: read=%ds write=%ds idle=%ds)",
		Version, cfg.Listen, cfg.ReadTimeoutSecs, cfg.WriteTimeoutSecs, cfg.IdleTimeoutSecs)
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
	log.Println("dansal-webmin shutting down...")
	shutCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := srv.Shutdown(shutCtx); err != nil {
		log.Printf("dansal-webmin shutdown error: %v", err)
	}
	log.Println("dansal-webmin stopped")
}

// webminCSP allows 'unsafe-inline' scripts/styles (templates use inline
// onclick=/<style>/<script>) plus https://unpkg.com, which serves the QR
// code library used on the users page.
const webminCSP = "default-src 'self'; " +
	"img-src 'self' data:; " +
	"font-src 'self' data:; " +
	"style-src 'self' 'unsafe-inline' https://unpkg.com; " +
	"script-src 'self' 'unsafe-inline' https://unpkg.com; " +
	"object-src 'none'; base-uri 'self'; frame-ancestors 'none'"

func securityHeadersMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("X-Frame-Options", "DENY")
		w.Header().Set("Referrer-Policy", "strict-origin-when-cross-origin")
		w.Header().Set("Strict-Transport-Security", "max-age=31536000; includeSubDomains; preload")
		w.Header().Set("Permissions-Policy", "geolocation=(), camera=(), microphone=(), usb=()")
		w.Header().Set("Cross-Origin-Opener-Policy", "same-origin")
		w.Header().Set("Content-Security-Policy", webminCSP)
		w.Header().Set("Cache-Control", "no-store")
		next.ServeHTTP(w, r)
	})
}
