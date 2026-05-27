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

.PHONY: build build-dansal build-dansal_web build-dansal_admin \
        run fmt vet clean install install-web install-units update check-config deb deploy-nginx

build:
	$(MAKE) -j3 build-dansal build-dansal_web build-dansal_admin

build-dansal:
	go build $(LDFLAGS) $(BUILDFLAGS) -o dansal ./cmd/dansal

build-dansal_web:
	go build $(LDFLAGS) $(BUILDFLAGS) -o dansal_web ./cmd/dansal_web

build-dansal_admin:
	go build $(LDFLAGS) $(BUILDFLAGS) -o dansal_admin ./cmd/dansal_admin

fmt:
	go fmt ./...

vet:
	go vet ./...

run: build-dansal
	./dansal --config ./config.yaml

clean:
	rm -f dansal dansal_web dansal_admin *.deb

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

install-units:
	@[ "$(shell id -u)" = "0" ] || { echo "install-units requires root"; exit 1; }
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
	systemctl daemon-reload
	systemctl enable --now dansal-fetch.timer dansal-backup.timer dansal-vacuum.timer dansal-prune-images.timer

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

# deploy: install pre-built binaries and restart services (no build step;
# run as root after 'make build' as a regular user).
deploy: install-units
	@[ "$(shell id -u)" = "0" ] || { echo "deploy requires root"; exit 1; }
	install -m 755 dansal        $(BINDIR)/dansal
	install -m 755 dansal_admin  $(BINDIR)/dansal_admin
	install -m 755 dansal_web    $(BINDIR)/dansal-web
	systemctl restart $(SERVICE)
	systemctl try-restart dansal-web.service || true
	@echo "deployed"

# Build a .deb package. VERSION may be overridden by the CI pipeline
# (e.g.  make deb DEB_VERSION=0.1.0).
DEB_VERSION ?= $(shell git describe --tags --always 2>/dev/null | \
                sed 's/^v//; s/-\([0-9]*\)-g[0-9a-f]*/+\1/' | \
                grep -E '^[0-9]' || echo "0.0~git.$(shell date +%Y%m%d).$(shell git rev-parse --short HEAD 2>/dev/null)")
DEB_ARCH    ?= amd64

deb: build-dansal build-dansal_web build-dansal_admin
	@set -e; \
	DEB_DIR=$$(mktemp -d /tmp/dansal-deb-XXXXXX); \
	trap 'rm -rf $$DEB_DIR' EXIT; \
	\
	mkdir -p $$DEB_DIR/DEBIAN \
	         $$DEB_DIR/usr/bin \
	         $$DEB_DIR/usr/lib/dansal \
	         $$DEB_DIR/$(SYSTEMDDIR) \
	         $$DEB_DIR/etc/dansal \
	         $$DEB_DIR/etc/fail2ban/filter.d \
	         $$DEB_DIR/etc/fail2ban/jail.d; \
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
	install -m 644 dansal.service                        $$DEB_DIR/$(SYSTEMDDIR)/dansal.service; \
	install -m 644 dansal-web.service                    $$DEB_DIR/$(SYSTEMDDIR)/dansal-web.service; \
	install -m 644 packaging/config.yaml                 $$DEB_DIR/etc/dansal/config.yaml; \
	install -m 644 packaging/web.yaml                    $$DEB_DIR/etc/dansal/web.yaml; \
	install -m 644 deploy/fail2ban/filter.d/dansal.conf  $$DEB_DIR/etc/fail2ban/filter.d/dansal.conf; \
	install -m 644 deploy/fail2ban/jail.d/dansal.conf    $$DEB_DIR/etc/fail2ban/jail.d/dansal.conf; \
	\
	dpkg-deb --build --root-owner-group $$DEB_DIR \
	    dansal_$(DEB_VERSION)_$(DEB_ARCH).deb; \
	echo "Built dansal_$(DEB_VERSION)_$(DEB_ARCH).deb"

# Deploy nginx configuration using domain from web.yaml
.ONESHELL:
deploy-nginx:
	@[ "$(shell id -u)" = "0" ] || { echo "deploy-nginx requires root"; exit 1; }
	@if [ ! -f /etc/dansal/web.yaml ]; then \
	    echo "Error: /etc/dansal/web.yaml not found"; \
	    echo "Run 'make install-web' first or ensure web.yaml is properly configured"; \
	    exit 1; \
	fi
	# Extract domain with more robust parsing (handles various YAML formats)
	DOMAIN=$$(grep -E '^[[:space:]]*domain:' /etc/dansal/web.yaml | sed -E 's/domain:[[:space:]]*"?([^"[:space:]]+)"?.*/\1/'); \
	[ -z "$$DOMAIN" ] && { echo "Error: Could not extract domain from /etc/dansal/web.yaml"; echo "Expected format: domain: \"your-domain.com\" or domain: your-domain.com"; echo "Current domain line in /etc/dansal/web.yaml:"; grep -E '^[[:space:]]*domain:' /etc/dansal/web.yaml | sed -E 's/^/    /' || echo "    (no domain line found)"; exit 1; }; \
	echo "Deploying nginx configuration for domain: $$DOMAIN"
	# Validate domain format (basic check for dots and valid characters)
	if ! echo "$$DOMAIN" | grep -q '\.'; then \
	    echo "Error: Domain '$$DOMAIN' appears invalid (missing dot)"; \
	    echo "Please check the domain format in /etc/dansal/web.yaml"; \
	    exit 1; \
	fi
	# Create nginx conf.d directory if it doesn't exist
	install -d -m 755 /etc/nginx/conf.d
	# Deploy the nginx configuration template to conf.d
	# Replace domain in server_name, SSL certificate paths, and comments
	sed "s/events\.example\.com/$$DOMAIN/g" deploy/nginx/dansal.conf > /etc/nginx/conf.d/dansal.conf
	# Verify the replacement worked
	if ! grep -q "$$DOMAIN" /etc/nginx/conf.d/dansal.conf; then \
	    echo "Error: Domain replacement failed. Check if sed command worked."; \
	    echo "Expected domain: $$DOMAIN"; \
	    echo "Check the generated file: /etc/nginx/conf.d/dansal.conf"; \
	    exit 1; \
	fi
	# Specifically check that server_name was replaced
	if ! grep -q "server_name $$DOMAIN;" /etc/nginx/conf.d/dansal.conf; then \
	    echo "Error: server_name was not properly replaced with domain: $$DOMAIN"; \
	    echo "Check for empty server_name directives in the generated file"; \
	    exit 1; \
	fi
	# Test the nginx configuration
	if nginx -t; then \
	    echo "nginx configuration test passed"; \
	    # Reload nginx to apply changes \
	    systemctl reload nginx || systemctl restart nginx; \
	    echo "nginx configuration deployed and reloaded successfully"; \
	else \
	    echo "Error: nginx configuration test failed"; \
	    exit 1; \
	fi

# Deploy both web application and nginx configuration
deploy-full: install-web deploy-nginx
