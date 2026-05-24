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
        run fmt vet clean install install-web install-units update deb

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
	install -m 644 dansal.service     $(SYSTEMDDIR)/dansal.service
	install -m 644 dansal-web.service $(SYSTEMDDIR)/dansal-web.service
	systemctl daemon-reload

update: build install-units
	@[ "$(shell id -u)" = "0" ] || { echo "update requires root"; exit 1; }
	install -m 755 dansal        $(BINDIR)/dansal
	install -m 755 dansal_admin  $(BINDIR)/dansal_admin
	install -m 755 dansal_web    $(BINDIR)/dansal-web
	systemctl restart $(SERVICE)
	systemctl try-restart dansal-web.service || true

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

# safe-update: update to new version with backup and verification
# Usage: make safe-update VERSION=1.2.3
safe-update:
	@[ "$(shell id -u)" = "0" ] || { echo "safe-update requires root"; exit 1; }
	# Validate version parameter
	@if [ -z "$$VERSION" ]; then \
		echo "ERROR: VERSION parameter required"; \
		echo "Usage: make safe-update VERSION=x.y.z"; \
		exit 1; \
	fi
	# Create backup directory with timestamp
	BACKUP_DIR="/var/backups/dansal/$$(date +%Y%m%d_%H%M%S)_v$$VERSION";
	echo "Creating backup to $$BACKUP_DIR...";
	install -d -m 755 "$$BACKUP_DIR";
	# Backup current binaries
	cp -a $(BINDIR)/dansal        "$$BACKUP_DIR/dansal.bak"
	cp -a $(BINDIR)/dansal_admin  "$$BACKUP_DIR/dansal_admin.bak"
	cp -a $(BINDIR)/dansal-web    "$$BACKUP_DIR/dansal-web.bak"
	# Backup configuration files
	cp -a $(SYSCONFDIR)/config.yaml "$$BACKUP_DIR/config.yaml.bak"
	cp -a $(SYSCONFDIR)/web.yaml    "$$BACKUP_DIR/web.yaml.bak"
	# Backup database
	cp -a $(STATEDIR)/calendar.db  "$$BACKUP_DIR/calendar.db.bak"
	# Verify backup
	if [ -f "$$BACKUP_DIR/dansal.bak" ] && \
	   [ -f "$$BACKUP_DIR/config.yaml.bak" ] && \
	   [ -f "$$BACKUP_DIR/calendar.db.bak" ]; then \
		echo "✅ Backup created successfully"; \
	else \
		echo "❌ Backup failed"; \
		exit 1; \
	fi
	# Check current service status
	echo "Checking current service status...";
	SERVICE_ACTIVE=$$(systemctl is-active $(SERVICE) && echo "active" || echo "inactive");
	WEB_ACTIVE=$$(systemctl is-active dansal-web.service 2>/dev/null && echo "active" || echo "inactive");
	echo "Current status: dansal=$$SERVICE_ACTIVE, dansal-web=$$WEB_ACTIVE";
	# Stop services for safe update
	echo "Stopping services...";
	systemctl stop $(SERVICE) 2>/dev/null || true;
	systemctl stop dansal-web.service 2>/dev/null || true;
	# Install new version
	echo "Installing new version $$VERSION...";
	install -m 755 dansal        $(BINDIR)/dansal
	install -m 755 dansal_admin  $(BINDIR)/dansal_admin
	install -m 755 dansal_web    $(BINDIR)/dansal-web
	# Verify installation
	if [ -f $(BINDIR)/dansal ] && [ -f $(BINDIR)/dansal-web ]; then \
		echo "✅ New binaries installed"; \
	else \
		echo "❌ Installation failed - restoring backup..."; \
		cp -a "$$BACKUP_DIR/dansal.bak"        $(BINDIR)/dansal
		cp -a "$$BACKUP_DIR/dansal_admin.bak"  $(BINDIR)/dansal_admin
		cp -a "$$BACKUP_DIR/dansal-web.bak"    $(BINDIR)/dansal-web
		exit 1; \
	fi
	# Start services
	echo "Starting services...";
	if [ "$$SERVICE_ACTIVE" = "active" ]; then \
		systemctl start $(SERVICE); \
	fi
	if [ "$$WEB_ACTIVE" = "active" ]; then \
		systemctl start dansal-web.service; \
	fi
	# Verify services are running
	sleep 2;
	NEW_SERVICE_STATUS=$$(systemctl is-active $(SERVICE) && echo "✅" || echo "❌");
	NEW_WEB_STATUS=$$(systemctl is-active dansal-web.service 2>/dev/null && echo "✅" || echo "❌");
	echo "Update complete!";
	echo "Service status: dansal=$$NEW_SERVICE_STATUS, dansal-web=$$NEW_WEB_STATUS";
	echo "Backup located at: $$BACKUP_DIR";
	echo "To rollback: sudo make rollback BACKUP_DIR=$$BACKUP_DIR";

# rollback: restore from backup
# Usage: make rollback BACKUP_DIR=/var/backups/dansal/TIMESTAMP_vVERSION
rollback:
	@[ "$(shell id -u)" = "0" ] || { echo "rollback requires root"; exit 1; }
	@if [ -z "$$BACKUP_DIR" ]; then \
		echo "ERROR: BACKUP_DIR parameter required"; \
		echo "Usage: make rollback BACKUP_DIR=/path/to/backup"; \
		exit 1; \
	fi
	@if [ ! -d "$$BACKUP_DIR" ]; then \
		echo "ERROR: Backup directory $$BACKUP_DIR not found"; \
		exit 1; \
	fi
	# Check current service status
	SERVICE_ACTIVE=$$(systemctl is-active $(SERVICE) && echo "active" || echo "inactive");
	WEB_ACTIVE=$$(systemctl is-active dansal-web.service 2>/dev/null && echo "active" || echo "inactive");
	# Stop services
	echo "Stopping services for rollback...";
	systemctl stop $(SERVICE) 2>/dev/null || true;
	systemctl stop dansal-web.service 2>/dev/null || true;
	# Restore from backup
	echo "Restoring from $$BACKUP_DIR...";
	cp -a "$$BACKUP_DIR/dansal.bak"        $(BINDIR)/dansal
	cp -a "$$BACKUP_DIR/dansal_admin.bak"  $(BINDIR)/dansal_admin
	cp -a "$$BACKUP_DIR/dansal-web.bak"    $(BINDIR)/dansal-web
	cp -a "$$BACKUP_DIR/config.yaml.bak"  $(SYSCONFDIR)/config.yaml
	cp -a "$$BACKUP_DIR/web.yaml.bak"     $(SYSCONFDIR)/web.yaml
	cp -a "$$BACKUP_DIR/calendar.db.bak" $(STATEDIR)/calendar.db
	chown $(SERVICE):$(SERVICE) $(STATEDIR)/calendar.db;
	chmod 640 $(STATEDIR)/calendar.db;
	# Start services
	echo "Starting services...";
	if [ "$$SERVICE_ACTIVE" = "active" ]; then \
		systemctl start $(SERVICE); \
	fi
	if [ "$$WEB_ACTIVE" = "active" ]; then \
		systemctl start dansal-web.service; \
	fi
	echo "Rollback complete!";

# Fresh installation with service configuration
fresh-install: install install-web
	@[ "$(shell id -u)" = "0" ] || { echo "fresh-install requires root"; exit 1; }
	# Configure nginx if installed
	if command -v nginx >/dev/null 2>&1; then \
		echo "Configuring nginx..."; \
		install -d -m 755 /etc/nginx/sites-enabled; \
		install -m 644 deploy/nginx/dansal.conf /etc/nginx/sites-available/dansal.conf; \
		ln -sf /etc/nginx/sites-available/dansal.conf /etc/nginx/sites-enabled/dansal.conf; \
		test -f /etc/nginx/nginx.conf && nginx -t && systemctl reload nginx || echo "nginx config test failed"; \
	else \
		echo "nginx not found — nginx configuration skipped (template in deploy/nginx/)"; \
	fi
	# Configure redis if installed
	if command -v redis-server >/dev/null 2>&1; then \
		echo "Configuring redis..."; \
		systemctl enable --now redis-server; \
	else \
		echo "redis not found — redis configuration skipped"; \
	fi
	# Configure certbot if installed
	if command -v certbot >/dev/null 2>&1; then \
		echo "Configuring certbot..."; \
		install -d -m 755 /etc/letsencrypt/renewal-hooks/deploy; \
		install -m 755 deploy/certbot/nginx-reload.sh /etc/letsencrypt/renewal-hooks/deploy/nginx-reload.sh; \
		systemctl enable --now certbot.timer 2>/dev/null || true; \
	else \
		echo "certbot not found — certbot configuration skipped"; \
	fi
	# Configure fail2ban if installed (already handled in install target)
	if [ -d /etc/fail2ban ]; then \
		echo "fail2ban already configured in install target"; \
	else \
		echo "fail2ban not found — fail2ban configuration skipped (templates in deploy/fail2ban/)"; \
	fi
	# Final status
	echo "Fresh installation complete!";
	echo "Services configured:";
	command -v nginx >/dev/null 2>&1 && echo "  ✅ nginx (HTTP/3 ready)" || echo "  ❌ nginx (not installed)";
	command -v redis-server >/dev/null 2>&1 && echo "  ✅ redis" || echo "  ❌ redis (not installed)";
	command -v certbot >/dev/null 2>&1 && echo "  ✅ certbot" || echo "  ❌ certbot (not installed)";
	[ -d /etc/fail2ban ] && echo "  ✅ fail2ban" || echo "  ❌ fail2ban (not installed)";

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
