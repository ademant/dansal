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
)

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
	printVersion := flag.Bool("version", false, "print version and build date then exit")

	if w, err := syslog.New(syslog.LOG_INFO|syslog.LOG_DAEMON, "dansal_webmin"); err == nil {
		log.SetOutput(w)
		log.SetFlags(0)
	}

	cfg := loadConfig()

	if *printVersion {
		fmt.Printf("dansal-webmin %s (built %s)\n", Version, BuildTime)
		os.Exit(0)
	}

	tmpls := loadTemplates()

	buildHandler := func(cfg *Config) http.Handler {
		mux := http.NewServeMux()
		mux.HandleFunc("GET /login", loginPageHandler(cfg, tmpls))
		mux.HandleFunc("POST /login", loginPostHandler(cfg, tmpls))
		mux.HandleFunc("POST /logout", logoutHandler(cfg))
		mux.HandleFunc("GET /", requireLogin(cfg, dashboardHandler(cfg, tmpls)))
		mux.HandleFunc("GET /users", requireLogin(cfg, usersPageHandler(cfg, tmpls)))
		mux.HandleFunc("POST /users/new", requireLogin(cfg, userCreateHandler(cfg)))
		mux.HandleFunc("GET /users/{username}/sessions", requireLogin(cfg, userSessionsPageHandler(cfg, tmpls)))
		mux.HandleFunc("POST /users/{username}/sessions/{id}/revoke", requireLogin(cfg, userRevokeSessionHandler(cfg)))
		mux.HandleFunc("POST /users/{username}/reset-password", requireLogin(cfg, userResetPasswordHandler(cfg)))
		mux.HandleFunc("POST /users/{username}/delete", requireLogin(cfg, userDeleteHandler(cfg)))
		mux.HandleFunc("POST /users/{username}/magic-link", requireLogin(cfg, userMagicLinkHandler(cfg)))
		mux.HandleFunc("GET /notifications", requireLogin(cfg, notificationsPageHandler(cfg, tmpls)))
		mux.HandleFunc("POST /notifications/smtp", requireLogin(cfg, notificationsSMTPSaveHandler(cfg)))
		mux.HandleFunc("POST /notifications/smtp-test", requireLogin(cfg, notificationsSMTPTestHandler(cfg)))
		mux.HandleFunc("POST /notifications/telegram", requireLogin(cfg, notificationsTelegramSaveHandler(cfg)))
		mux.HandleFunc("POST /notifications/matrix", requireLogin(cfg, notificationsMatrixSaveHandler(cfg)))
		mux.HandleFunc("POST /notifications/heartbeat", requireLogin(cfg, notificationsHeartbeatSaveHandler(cfg)))
		mux.HandleFunc("GET /maintenance", requireLogin(cfg, maintenancePageHandler(cfg, tmpls)))
		mux.HandleFunc("POST /maintenance/vacuum", requireLogin(cfg, maintenanceVacuumHandler(cfg)))
		mux.HandleFunc("POST /maintenance/prune-images", requireLogin(cfg, maintenancePruneImagesHandler(cfg)))
		mux.HandleFunc("POST /maintenance/fetch-all", requireLogin(cfg, maintenanceFetchAllHandler(cfg)))
		mux.HandleFunc("POST /maintenance/backup", requireLogin(cfg, maintenanceBackupHandler(cfg)))
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

	log.Printf("dansal-webmin %s listening on %s", Version, cfg.Listen)
	if err := http.ListenAndServe(cfg.Listen, securityHeadersMiddleware(&live)); err != nil {
		log.Fatal(err)
	}
}

func securityHeadersMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("X-Frame-Options", "DENY")
		w.Header().Set("Referrer-Policy", "strict-origin-when-cross-origin")
		w.Header().Set("Strict-Transport-Security", "max-age=31536000; includeSubDomains")
		w.Header().Set("Cache-Control", "no-store")
		next.ServeHTTP(w, r)
	})
}
