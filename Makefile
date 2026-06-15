VERSION    := $(shell git describe --tags --always --dirty 2>/dev/null || echo "dev")
BUILD_TIME := $(shell date -u +"%Y-%m-%dT%H:%M:%SZ")
LDFLAGS    := -ldflags "-X main.Version=$(VERSION) -X main.BuildTime=$(BUILD_TIME)"
BUILDFLAGS := -trimpath -buildvcs=false

SERVICE    := dansal
BINDIR     := /usr/bin
SYSCONFDIR := /etc/dansal
STATEDIR   := /var/lib/dansal
SYSTEMDDIR := /etc/systemd/system

.DEFAULT_GOAL := build

.PHONY: build build-dansal build-dansal_web build-dansal_admin build-dansal_webmin \
        run fmt vet vulncheck clean install install-web install-webmin install-units setup-instance \
        update check-config deb deploy-nginx deploy-nginx-webmin deploy-nginx-default deploy-full

build:
	$(MAKE) -j4 build-dansal build-dansal_web build-dansal_admin build-dansal_webmin

build-dansal:
	go build $(LDFLAGS) $(BUILDFLAGS) -o dansal ./cmd/dansal

build-dansal_web:
	go build $(LDFLAGS) $(BUILDFLAGS) -o dansal_web ./cmd/dansal_web

build-dansal_admin:
	go build $(LDFLAGS) $(BUILDFLAGS) -o dansal_admin ./cmd/dansal_admin

build-dansal_webmin:
	go build $(LDFLAGS) $(BUILDFLAGS) -o dansal_webmin ./cmd/dansal_webmin

fmt:
	go fmt ./...

vet:
	go vet ./...

vulncheck:
	go run golang.org/x/vuln/cmd/govulncheck@latest ./...

run: build-dansal
	./dansal --config ./config.yaml

clean:
	rm -f dansal dansal_web dansal_admin dansal_webmin *.deb

install: build
	@[ "$(shell id -u)" = "0" ] || { echo "install requires root"; exit 1; }
	# system user
	id -u $(SERVICE) >/dev/null 2>&1 || \
		adduser --system --group --no-create-home --home-dir $(STATEDIR) \
		        --shell /usr/sbin/nologin $(SERVICE)
	# directories
	install -d -m 750 -o $(SERVICE) -g $(SERVICE) $(SYSCONFDIR)
	install -d -m 750 -o $(SERVICE) -g $(SERVICE) $(STATEDIR)
	install -d -m 750 -o $(SERVICE) -g $(SERVICE) $(STATEDIR)/images
	# config — install only if not already present so local edits are preserved
	@if [ ! -f $(SYSCONFDIR)/config.yaml ]; then \
		install -m 640 -o root -g $(SERVICE) packaging/config.yaml $(SYSCONFDIR)/config.yaml; \
		echo "Installed $(SYSCONFDIR)/config.yaml"; \
	else \
		echo "$(SYSCONFDIR)/config.yaml already exists — not overwriting"; \
	fi
	# binaries
	install -m 755 dansal        $(BINDIR)/dansal
	install -m 755 dansal_admin  $(BINDIR)/dansal_admin
	# preflight helper
	install -d -m 755 /usr/lib/dansal
	install -m 755 packaging/dansal_preflight /usr/lib/dansal/dansal_preflight
	# systemd units
	$(MAKE) install-units
	# Ensure the database file is owned by the service user even if it was
	# previously created as root (e.g. during testing).
	touch $(STATEDIR)/calendar.db
	chown $(SERVICE):$(SERVICE) $(STATEDIR)/calendar.db
	chmod 640 $(STATEDIR)/calendar.db
	systemctl enable --now $(SERVICE)
	# fail2ban
	@if [ -d /etc/fail2ban ]; then \
		install -m 644 deploy/fail2ban/filter.d/dansal.conf /etc/fail2ban/filter.d/dansal.conf; \
		install -m 644 deploy/fail2ban/jail.d/dansal.conf   /etc/fail2ban/jail.d/dansal.conf; \
		systemctl reload fail2ban 2>/dev/null || true; \
		echo "Installed fail2ban filter and jail"; \
	else \
		echo "fail2ban not found — skipping (templates in deploy/fail2ban/)"; \
	fi

install-web: build-dansal_web
	@[ "$(shell id -u)" = "0" ] || { echo "install-web requires root"; exit 1; }
	install -d -m 750 -o $(SERVICE) -g $(SERVICE) /var/lib/dansal-web
	@if [ ! -f $(SYSCONFDIR)/web.yaml ]; then \
		install -m 640 -o root -g $(SERVICE) packaging/web.yaml $(SYSCONFDIR)/web.yaml; \
		echo "Installed $(SYSCONFDIR)/web.yaml — edit domain and dansal_url before starting"; \
	else \
		echo "$(SYSCONFDIR)/web.yaml already exists — not overwriting"; \
	fi
	install -m 755 dansal_web $(BINDIR)/dansal-web
	$(MAKE) install-units
	systemctl enable --now dansal-web.service

install-webmin: build-dansal_webmin
	@[ "$(shell id -u)" = "0" ] || { echo "install-webmin requires root"; exit 1; }
	@if [ ! -f $(SYSCONFDIR)/webmin.yaml ]; then \
		install -m 640 -o root -g $(SERVICE) packaging/webmin.yaml $(SYSCONFDIR)/webmin.yaml; \
		echo "Installed $(SYSCONFDIR)/webmin.yaml — set session_secret before starting"; \
	else \
		echo "$(SYSCONFDIR)/webmin.yaml already exists — not overwriting"; \
	fi
	install -m 755 dansal_webmin $(BINDIR)/dansal-webmin
	install -m 644 dansal-webmin.service $(SYSTEMDDIR)/dansal-webmin.service
	systemctl daemon-reload
	systemctl enable dansal-webmin.service

install-units:
	@[ "$(shell id -u)" = "0" ] || { echo "install-units requires root"; exit 1; }
	# Legacy non-template units (installed for reference; not enabled automatically)
	install -m 644 dansal.service           $(SYSTEMDDIR)/dansal.service
	install -m 644 dansal-web.service       $(SYSTEMDDIR)/dansal-web.service
	install -m 644 dansal-fetch.service     $(SYSTEMDDIR)/dansal-fetch.service
	install -m 644 dansal-fetch.timer       $(SYSTEMDDIR)/dansal-fetch.timer
	install -m 644 dansal-backup.service    $(SYSTEMDDIR)/dansal-backup.service
	install -m 644 dansal-backup.timer      $(SYSTEMDDIR)/dansal-backup.timer
	install -m 644 dansal-vacuum.service    $(SYSTEMDDIR)/dansal-vacuum.service
	install -m 644 dansal-vacuum.timer      $(SYSTEMDDIR)/dansal-vacuum.timer
	install -m 644 dansal-prune-images.service  $(SYSTEMDDIR)/dansal-prune-images.service
	install -m 644 dansal-prune-images.timer    $(SYSTEMDDIR)/dansal-prune-images.timer
	install -m 644 dansal-mailcheck.service     $(SYSTEMDDIR)/dansal-mailcheck.service
	install -m 644 dansal-mailcheck.timer       $(SYSTEMDDIR)/dansal-mailcheck.timer
	# Instance template units
	install -m 644 dansal@.service              $(SYSTEMDDIR)/dansal@.service
	install -m 644 dansal-web@.service          $(SYSTEMDDIR)/dansal-web@.service
	install -m 644 dansal-webmin@.service       $(SYSTEMDDIR)/dansal-webmin@.service
	install -m 644 dansal-fetch@.service        $(SYSTEMDDIR)/dansal-fetch@.service
	install -m 644 dansal-fetch@.timer          $(SYSTEMDDIR)/dansal-fetch@.timer
	install -m 644 dansal-backup@.service       $(SYSTEMDDIR)/dansal-backup@.service
	install -m 644 dansal-backup@.timer         $(SYSTEMDDIR)/dansal-backup@.timer
	install -m 644 dansal-vacuum@.service       $(SYSTEMDDIR)/dansal-vacuum@.service
	install -m 644 dansal-vacuum@.timer         $(SYSTEMDDIR)/dansal-vacuum@.timer
	install -m 644 dansal-prune-images@.service $(SYSTEMDDIR)/dansal-prune-images@.service
	install -m 644 dansal-prune-images@.timer   $(SYSTEMDDIR)/dansal-prune-images@.timer
	install -m 644 dansal-mailcheck@.service    $(SYSTEMDDIR)/dansal-mailcheck@.service
	install -m 644 dansal-mailcheck@.timer      $(SYSTEMDDIR)/dansal-mailcheck@.timer
	systemctl daemon-reload

update: build install-units
	@[ "$(shell id -u)" = "0" ] || { echo "update requires root"; exit 1; }
	install -m 755 dansal        $(BINDIR)/dansal
	install -m 755 dansal_admin  $(BINDIR)/dansal_admin
	install -m 755 dansal_web    $(BINDIR)/dansal-web
	systemctl restart $(SERVICE)
	systemctl try-restart dansal-web.service || true
	@$(MAKE) --no-print-directory check-config

check-config:
	@packaging/check-config packaging/config.yaml $(SYSCONFDIR)/config.yaml
	@packaging/check-config packaging/web.yaml    $(SYSCONFDIR)/web.yaml

# deploy: install pre-built binaries and restart a specific instance.
# Usage: sudo make deploy INSTANCE=dev   (or prod, nl, ...)
# Run as root after 'make build' as a regular user.
deploy: install-units
	@[ "$(shell id -u)" = "0" ] || { echo "deploy requires root"; exit 1; }
ifndef INSTANCE
	$(error INSTANCE is required: sudo make deploy INSTANCE=dev)
endif
	install -d -m 755 /usr/lib/dansal/$(INSTANCE)
	install -m 755 dansal        /usr/lib/dansal/$(INSTANCE)/dansal
	install -m 755 dansal_admin  /usr/lib/dansal/$(INSTANCE)/dansal_admin
	install -m 755 dansal_web    /usr/lib/dansal/$(INSTANCE)/dansal-web
	install -m 755 dansal_webmin /usr/lib/dansal/$(INSTANCE)/dansal-webmin
	install -m 755 packaging/dansal_preflight /usr/lib/dansal/dansal_preflight
	systemctl restart dansal@$(INSTANCE)
	systemctl try-restart dansal-web@$(INSTANCE).service || true
	systemctl try-restart dansal-webmin@$(INSTANCE).service || true
	@echo "deployed $(INSTANCE)"

# setup-instance: create directories, install template configs, enable units for a new instance.
# Idempotent — safe to re-run.  Does NOT start services (edit configs first).
# Usage: sudo make setup-instance INSTANCE=prod
setup-instance:
	@$(MAKE) generate-robots || true

	@[ "$(shell id -u)" = "0" ] || { echo "setup-instance requires root"; exit 1; }
ifndef INSTANCE
	$(error INSTANCE is required: sudo make setup-instance INSTANCE=prod)
endif
	$(MAKE) install-units
	# Allow dansal to submit mail via sendmail (needs postdrop group for maildrop write access)
	getent group postdrop >/dev/null && usermod -aG postdrop $(SERVICE) || true
	# Allow dansal to read /var/log/mail.log (needed by dansal-mailcheck@)
	getent group adm >/dev/null && usermod -aG adm $(SERVICE) || true
	# Binary directory for this instance
	install -d -m 755 /usr/lib/dansal/$(INSTANCE)
	# Config directory (770: group-writable so dansal-webmin can save configs)
	install -d -m 770 -o root -g $(SERVICE) $(SYSCONFDIR)/$(INSTANCE)
	# State directories
	install -d -m 750 -o $(SERVICE) -g $(SERVICE) $(STATEDIR)/$(INSTANCE)
	install -d -m 750 -o $(SERVICE) -g $(SERVICE) $(STATEDIR)/$(INSTANCE)/images
	install -d -m 750 -o $(SERVICE) -g $(SERVICE) $(STATEDIR)/$(INSTANCE)/backups
	install -d -m 750 -o $(SERVICE) -g $(SERVICE) /var/lib/dansal-web/$(INSTANCE)
	# Template configs — installed only if not already present (or empty from a failed prior run)
	@if [ ! -s $(SYSCONFDIR)/$(INSTANCE)/config.yaml ]; then \
		sed \
		    -e 's|/var/lib/dansal/dansal.sock|/var/lib/dansal/$(INSTANCE)/dansal.sock|' \
		    -e 's|/var/lib/dansal/calendar.db|/var/lib/dansal/$(INSTANCE)/calendar.db|' \
		    -e 's|/var/lib/dansal/images|/var/lib/dansal/$(INSTANCE)/images|' \
		    -e 's|/var/lib/dansal/backups|/var/lib/dansal/$(INSTANCE)/backups|' \
		    packaging/config.yaml > $(SYSCONFDIR)/$(INSTANCE)/config.yaml; \
		chown root:$(SERVICE) $(SYSCONFDIR)/$(INSTANCE)/config.yaml; \
		chmod 660 $(SYSCONFDIR)/$(INSTANCE)/config.yaml; \
		echo "Created $(SYSCONFDIR)/$(INSTANCE)/config.yaml — set port, base_url, smtp, etc."; \
	else \
		echo "$(SYSCONFDIR)/$(INSTANCE)/config.yaml already exists — not overwriting"; \
	fi
	@if [ ! -s $(SYSCONFDIR)/$(INSTANCE)/web.yaml ]; then \
		sed 's|/var/lib/dansal-web/web.db|/var/lib/dansal-web/$(INSTANCE)/web.db|' \
		    packaging/web.yaml > $(SYSCONFDIR)/$(INSTANCE)/web.yaml; \
		chown root:$(SERVICE) $(SYSCONFDIR)/$(INSTANCE)/web.yaml; \
		chmod 660 $(SYSCONFDIR)/$(INSTANCE)/web.yaml; \
		echo "Created $(SYSCONFDIR)/$(INSTANCE)/web.yaml — set listen, domain, dansal_url."; \
	else \
		echo "$(SYSCONFDIR)/$(INSTANCE)/web.yaml already exists — not overwriting"; \
	fi
	@if [ ! -s $(SYSCONFDIR)/$(INSTANCE)/webmin.yaml ]; then \
		sed \
		    -e 's|admin_socket: "/var/lib/dansal/dansal.sock"|admin_socket: "/var/lib/dansal/$(INSTANCE)/dansal.sock"|' \
		    -e 's|web_db_path: "/var/lib/dansal-web/web.db"|web_db_path: "/var/lib/dansal-web/$(INSTANCE)/web.db"|' \
		    -e 's|instance: ""|instance: "$(INSTANCE)"|' \
		    packaging/webmin.yaml > $(SYSCONFDIR)/$(INSTANCE)/webmin.yaml; \
		chown root:$(SERVICE) $(SYSCONFDIR)/$(INSTANCE)/webmin.yaml; \
		chmod 660 $(SYSCONFDIR)/$(INSTANCE)/webmin.yaml; \
		echo "Created $(SYSCONFDIR)/$(INSTANCE)/webmin.yaml — set listen, session_secret."; \
	else \
		echo "$(SYSCONFDIR)/$(INSTANCE)/webmin.yaml already exists — not overwriting"; \
	fi
	# Enable units (not started — configure first)
	systemctl enable dansal@$(INSTANCE) dansal-web@$(INSTANCE) dansal-webmin@$(INSTANCE)
	systemctl enable dansal-fetch@$(INSTANCE).timer dansal-backup@$(INSTANCE).timer \
	                 dansal-vacuum@$(INSTANCE).timer dansal-prune-images@$(INSTANCE).timer \
	                 dansal-mailcheck@$(INSTANCE).timer
	@echo ""
	@echo "Instance '$(INSTANCE)' is set up.  Next steps:"
	@echo "  1. Edit $(SYSCONFDIR)/$(INSTANCE)/config.yaml  (port, base_url, smtp, …)"
	@echo "  2. Edit $(SYSCONFDIR)/$(INSTANCE)/web.yaml     (listen, domain, dansal_url)"
	@echo "  3. Edit $(SYSCONFDIR)/$(INSTANCE)/webmin.yaml  (listen, admin_socket, session_secret)"
	@echo "  4. sudo systemctl start dansal@$(INSTANCE) dansal-web@$(INSTANCE) dansal-webmin@$(INSTANCE)"
	@echo "  5. sudo systemctl start dansal-fetch@$(INSTANCE).timer dansal-backup@$(INSTANCE).timer"

# Build a .deb package. VERSION may be overridden by the CI pipeline
# (e.g.  make deb DEB_VERSION=0.1.0).
DEB_VERSION ?= $(shell git describe --tags --always 2>/dev/null | \
                sed 's/^v//; s/-\([0-9]*\)-g[0-9a-f]*/+\1/' | \
                grep -E '^[0-9]' || echo "0.0~git.$(shell date +%Y%m%d).$(shell git rev-parse --short HEAD 2>/dev/null)")
DEB_ARCH    ?= $(shell dpkg --print-architecture)

deb: build-dansal build-dansal_web build-dansal_admin build-dansal_webmin
	@set -e; \
	DEB_DIR=$$(mktemp -d /tmp/dansal-deb-XXXXXX); \
	trap 'rm -rf $$DEB_DIR' EXIT; \
	\
	install -d -m 755 $$DEB_DIR/DEBIAN; \
	install -d -m 755 $$DEB_DIR/usr/bin; \
	install -d -m 755 $$DEB_DIR/usr/lib/dansal; \
	install -d -m 755 $$DEB_DIR/$(SYSTEMDDIR); \
	install -d -m 755 $$DEB_DIR/etc/dansal; \
	install -d -m 755 $$DEB_DIR/etc/fail2ban/filter.d; \
	install -d -m 755 $$DEB_DIR/etc/fail2ban/jail.d; \
	\
	sed 's/VERSION_PLACEHOLDER/$(DEB_VERSION)/; s/amd64/$(DEB_ARCH)/' \
	    packaging/control > $$DEB_DIR/DEBIAN/control; \
	cp  packaging/conffiles                              $$DEB_DIR/DEBIAN/conffiles; \
	install -m 755 packaging/preinst                     $$DEB_DIR/DEBIAN/preinst; \
	install -m 755 packaging/postinst                    $$DEB_DIR/DEBIAN/postinst; \
	install -m 755 packaging/prerm                       $$DEB_DIR/DEBIAN/prerm; \
	install -m 755 packaging/postrm                      $$DEB_DIR/DEBIAN/postrm; \
	install -m 755 packaging/dansal_preflight            $$DEB_DIR/usr/lib/dansal/dansal_preflight; \
	install -m 755 dansal                                $$DEB_DIR/usr/bin/dansal; \
	install -m 755 dansal_web                            $$DEB_DIR/usr/bin/dansal-web; \
	install -m 755 dansal_admin                          $$DEB_DIR/usr/bin/dansal_admin; \
	install -m 755 dansal_webmin                         $$DEB_DIR/usr/bin/dansal-webmin; \
	install -m 644 dansal.service                        $$DEB_DIR/$(SYSTEMDDIR)/dansal.service; \
	install -m 644 dansal-web.service                    $$DEB_DIR/$(SYSTEMDDIR)/dansal-web.service; \
	install -m 644 dansal-webmin.service                 $$DEB_DIR/$(SYSTEMDDIR)/dansal-webmin.service; \
	install -m 644 packaging/config.yaml                 $$DEB_DIR/etc/dansal/config.yaml; \
	install -m 644 packaging/web.yaml                    $$DEB_DIR/etc/dansal/web.yaml; \
	install -m 644 packaging/webmin.yaml                 $$DEB_DIR/etc/dansal/webmin.yaml; \
	install -m 644 deploy/fail2ban/filter.d/dansal.conf  $$DEB_DIR/etc/fail2ban/filter.d/dansal.conf; \
	install -m 644 deploy/fail2ban/jail.d/dansal.conf    $$DEB_DIR/etc/fail2ban/jail.d/dansal.conf; \
	\
	dpkg-deb --build --root-owner-group $$DEB_DIR \
	    dansal_$(DEB_VERSION)_$(DEB_ARCH).deb; \
	echo "Built dansal_$(DEB_VERSION)_$(DEB_ARCH).deb"

# Deploy nginx configuration for a specific instance.
# Usage: sudo make deploy-nginx INSTANCE=prod
.ONESHELL:
deploy-nginx:
	@[ "$(shell id -u)" = "0" ] || { echo "deploy-nginx requires root"; exit 1; }
ifndef INSTANCE
	$(error INSTANCE is required: sudo make deploy-nginx INSTANCE=prod)
endif
	set -e
	WEB_CONF=$(SYSCONFDIR)/$(INSTANCE)/web.yaml
	API_CONF=$(SYSCONFDIR)/$(INSTANCE)/config.yaml
	[ -f "$$WEB_CONF" ] || { echo "Error: $$WEB_CONF not found — run setup-instance first"; exit 1; }
	[ -f "$$API_CONF" ] || { echo "Error: $$API_CONF not found — run setup-instance first"; exit 1; }
	DOMAIN=$$(grep -E '^domain:' "$$WEB_CONF" | sed -E 's/domain:[[:space:]]*"?([^"[:space:]]+)"?.*/\1/')
	[ -z "$$DOMAIN" ] && { echo "Error: domain not set in $$WEB_CONF"; exit 1; }
	echo "$$DOMAIN" | grep -q '\.' || { echo "Error: domain '$$DOMAIN' looks invalid"; exit 1; }
	API_PORT=$$(grep -E '^  port:' "$$API_CONF" | head -1 | sed 's/.*:[[:space:]]*//')
	API_PORT=$${API_PORT:-8000}
	WEB_PORT=$$(grep -E '^listen:' "$$WEB_CONF" | sed -E 's/.*:([0-9]+).*/\1/')
	WEB_PORT=$${WEB_PORT:-8080}
	echo "Deploying nginx for instance '$(INSTANCE)': $$DOMAIN (api=$$API_PORT web=$$WEB_PORT)"
	install -d -m 755 /etc/nginx/conf.d
	rm -f /etc/nginx/conf.d/dansal.conf
	sed \
	    -e "s/events\.example\.com/$$DOMAIN/g" \
	    -e "s/127\.0\.0\.1:8000/127.0.0.1:$$API_PORT/g" \
	    -e "s/127\.0\.0\.1:8080/127.0.0.1:$$WEB_PORT/g" \
	    -e "s/\bdansal_api\b/dansal_api_$(INSTANCE)/g" \
	    -e "s/\bdansal_web\b/dansal_web_$(INSTANCE)/g" \
	    -e "s/zone=api_limit/zone=api_limit_$(INSTANCE)/g" \
	    -e "s/zone=auth_limit/zone=auth_limit_$(INSTANCE)/g" \
	    -e "s/zone=conn_limit/zone=conn_limit_$(INSTANCE)/g" \
	    deploy/nginx/dansal.conf > /etc/nginx/conf.d/dansal-$(INSTANCE).conf
	nginx -t || { rm -f /etc/nginx/conf.d/dansal-$(INSTANCE).conf; exit 1; }; systemctl reload nginx
	echo "Deployed /etc/nginx/conf.d/dansal-$(INSTANCE).conf"
	grep -qE '^\s*server_tokens\s+off\s*;' /etc/nginx/nginx.conf 2>/dev/null || \
	    echo "Warning: 'server_tokens off;' not found in /etc/nginx/nginx.conf — add it to the http{} block to hide the nginx version"

# Deploy nginx configuration for dansal-webmin for a specific instance.
# Usage: sudo make deploy-nginx-webmin INSTANCE=prod
.ONESHELL:
deploy-nginx-webmin:
	@[ "$(shell id -u)" = "0" ] || { echo "deploy-nginx-webmin requires root"; exit 1; }
ifndef INSTANCE
	$(error INSTANCE is required: sudo make deploy-nginx-webmin INSTANCE=prod)
endif
	set -e
	WEBMIN_CONF=$(SYSCONFDIR)/$(INSTANCE)/webmin.yaml
	[ -f "$$WEBMIN_CONF" ] || { echo "Error: $$WEBMIN_CONF not found — run setup-instance first"; exit 1; }
	WEBMIN_DOMAIN=$$(grep -E '^webmin_domain:' "$$WEBMIN_CONF" | sed -E 's/webmin_domain:[[:space:]]*"?([^"[:space:]]+)"?.*/\1/')
	[ -z "$$WEBMIN_DOMAIN" ] && { echo "Error: webmin_domain not set in $$WEBMIN_CONF"; exit 1; }
	WEBMIN_PORT=$$(grep -E '^listen:' "$$WEBMIN_CONF" | sed -E 's/.*:([0-9]+).*/\1/')
	WEBMIN_PORT=$${WEBMIN_PORT:-8090}
	echo "Deploying nginx-webmin for instance '$(INSTANCE)': $$WEBMIN_DOMAIN (port=$$WEBMIN_PORT)"
	install -d -m 755 /etc/nginx/conf.d
	rm -f /etc/nginx/conf.d/dansal-webmin.conf
	API_CONF=$(SYSCONFDIR)/$(INSTANCE)/config.yaml
	PKI_DIR=$$(grep -E '^\s+pki_dir:' "$$API_CONF" 2>/dev/null | sed -E 's/.*:[[:space:]]*//' | tr -d '"')
	if [ -z "$$PKI_DIR" ]; then \
	    DB_PATH=$$(grep -E '^\s+db_path:' "$$API_CONF" 2>/dev/null | sed -E 's/.*:[[:space:]]*//' | tr -d '"'); \
	    if [ -n "$$DB_PATH" ]; then \
	        PKI_DIR=$$(dirname "$$DB_PATH")/pki; \
	    else \
	        PKI_DIR=$(STATEDIR)/pki; \
	    fi; \
	fi
	CA_CERT=$$PKI_DIR/ca.crt
	sed \
	    -e "s/webmin\.example\.com/$$WEBMIN_DOMAIN/g" \
	    -e "s/127\.0\.0\.1:8090/127.0.0.1:$$WEBMIN_PORT/g" \
	    -e "s/\bdansal_webmin\b/dansal_webmin_$(INSTANCE)/g" \
	    -e "s/zone=conn_limit/zone=conn_limit_$(INSTANCE)/g" \
	    -e "s|/etc/dansal/mtls/ca\.crt|$$CA_CERT|g" \
	    deploy/nginx/dansal-webmin.conf > /etc/nginx/conf.d/dansal-webmin-$(INSTANCE).conf
	if [ ! -f "$$CA_CERT" ]; then \
	    sed -i \
	        -e '/ssl_verify_client/s/.*/    ssl_verify_client off;/' \
	        -e '/ssl_client_certificate/d' \
	        -e '/ssl_verify_depth/d' \
	        /etc/nginx/conf.d/dansal-webmin-$(INSTANCE).conf; \
	    echo "  Note: mTLS disabled (no CA cert for this instance)"; \
	fi
	nginx -t || { rm -f /etc/nginx/conf.d/dansal-webmin-$(INSTANCE).conf; exit 1; }
	systemctl reload nginx
	echo "Deployed /etc/nginx/conf.d/dansal-webmin-$(INSTANCE).conf"

# Install the global catch-all server block that rejects requests with an
# unrecognized Host header. One-time, independent of INSTANCE.
# Usage: sudo make deploy-nginx-default
.ONESHELL:
deploy-nginx-default:
	@[ "$(shell id -u)" = "0" ] || { echo "deploy-nginx-default requires root"; exit 1; }
	set -e
	install -d -m 755 /etc/nginx/conf.d
	install -m 644 deploy/nginx/00-default-catchall.conf /etc/nginx/conf.d/00-default-catchall.conf
	nginx -t || { rm -f /etc/nginx/conf.d/00-default-catchall.conf; exit 1; }
	systemctl reload nginx
	echo "Deployed /etc/nginx/conf.d/00-default-catchall.conf"

# Deploy both web application and nginx configuration for an instance.
# Usage: sudo make deploy-full INSTANCE=prod
deploy-full: deploy deploy-nginx
