#!/usr/bin/env bash
# Starts, stops and reports the dansal API, web and webmin binaries for the
# Playwright e2e suite, as the current user, without root (#1545-style local
# runs). Ports match what e2e/playwright.config.ts and CI expect:
# API 18000, web 18080, webmin 18090.
#
# Everything lives under e2e/.run/ (generated configs, data, logs, pidfiles),
# so the packaging defaults under /etc and /var/lib are never touched and a
# live instance such as `dev` is never shared. CI does this with sudo against
# the packaging paths; this script is the no-root equivalent.
#
#   scripts/e2e-stack.sh up        build configs, wipe data, start all three
#                                 (E2E_INTERNAL_SECRET=… exempts loopback load
#                                 tests from the rate limiter)
#   scripts/e2e-stack.sh down      stop them
#   scripts/e2e-stack.sh status    show what is running and answering
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
RUN="$ROOT/e2e/.run"
CONF="$RUN/config"
DATA="$RUN/data"
LOGS="$RUN/logs"

API_PORT=18000
WEB_PORT=18080
WEBMIN_PORT=18090

BINARIES=(dansal dansal_web dansal_webmin)

die() { echo "e2e-stack: $*" >&2; exit 1; }

# patch FILE SED_EXPR VERIFY_REGEX — apply one sed edit and fail loudly if the
# result doesn't contain the expected text, so packaging format drift can't
# silently leave a service on the wrong port (the lesson behind #1528).
patch() {
  local file="$1" expr="$2" verify="$3"
  sed -i "$expr" "$file"
  grep -qE "$verify" "$file" || die "config patch did not apply: $expr to $(basename "$file")"
}

# Paths are interpolated into sed expressions, so refuse characters that would
# break them rather than producing a silently wrong config.
case "$ROOT" in *'|'*) die "repository path must not contain '|'";; esac

cmd_up() {
  for b in "${BINARIES[@]}"; do
    [ -x "$ROOT/$b" ] || die "$b not built; run 'make build' first"
  done
  for pidfile in "$RUN"/*.pid; do
    [ -e "$pidfile" ] || continue
    if kill -0 "$(cat "$pidfile")" 2>/dev/null; then
      die "$(basename "$pidfile" .pid) is already running; run 'make e2e-down' first"
    fi
    rm -f "$pidfile"
  done

  # A fresh database per run: the suite seeds its own fixtures and expects
  # an empty instance, as CI gives it.
  rm -rf "$DATA" "$LOGS"
  mkdir -p "$CONF" "$DATA/images" "$DATA/web-images" "$LOGS"

  cp "$ROOT/packaging/config.yaml" "$ROOT/packaging/web.yaml" "$ROOT/packaging/webmin.yaml" "$CONF/"

  local c="$CONF/config.yaml" w="$CONF/web.yaml" m="$CONF/webmin.yaml"

  patch "$c" "s/port: 8000/port: $API_PORT/" "port: $API_PORT"
  patch "$c" "s|listen: \"127.0.0.1:8000\"|listen: \"127.0.0.1:$API_PORT\"|" "listen: \"127.0.0.1:$API_PORT\""
  patch "$c" "s/^  rate_limit: 100\$/  rate_limit: 1000/" "^  rate_limit: 1000\$"
  patch "$c" "s/^  account_mutation_rate_limit: 0\$/  account_mutation_rate_limit: 600/" "^  account_mutation_rate_limit: 600\$"
  patch "$c" "s|db_path: /var/lib/dansal/calendar.db|db_path: $DATA/calendar.db|" "db_path: $DATA/calendar.db"
  patch "$c" "s|images_dir: /var/lib/dansal/images|images_dir: $DATA/images|" "images_dir: $DATA/images"
  patch "$c" "s|admin_socket: /var/lib/dansal/dansal.sock|admin_socket: $DATA/dansal.sock|" "admin_socket: $DATA/dansal.sock"

  # Optional: exempt loopback callers that send X-Dansal-Internal from the
  # per-IP rate limiter. Used by the k6 load tests (stress/), which would
  # otherwise hit 429 almost immediately. Off unless asked for explicitly.
  if [ -n "${E2E_INTERNAL_SECRET:-}" ]; then
    case "$E2E_INTERNAL_SECRET" in
      *[!A-Za-z0-9_-]*) die "E2E_INTERNAL_SECRET may only contain letters, digits, '_' and '-'" ;;
    esac
    patch "$c" "s|^  internal_shared_secret: \"\"\$|  internal_shared_secret: \"$E2E_INTERNAL_SECRET\"|" "internal_shared_secret: \"$E2E_INTERNAL_SECRET\""
  fi

  patch "$w" "s|listen: \"127.0.0.1:8080\"|listen: \"127.0.0.1:$WEB_PORT\"|" "listen: \"127.0.0.1:$WEB_PORT\""
  patch "$w" "s|dansal_url: \"http://127.0.0.1:8000\"|dansal_url: \"http://127.0.0.1:$API_PORT\"|" "dansal_url: \"http://127.0.0.1:$API_PORT\""
  patch "$w" "s|^db_path: /var/lib/dansal-web/web.db|db_path: $DATA/web.db|" "^db_path: $DATA/web.db"
  patch "$w" "s|^images_dir: /var/lib/dansal-web\$|images_dir: $DATA/web-images|" "^images_dir: $DATA/web-images\$"

  patch "$m" "s|listen: \"127.0.0.1:8090\"|listen: \"127.0.0.1:$WEBMIN_PORT\"|" "listen: \"127.0.0.1:$WEBMIN_PORT\""
  patch "$m" "s|dansal_url: \"http://127.0.0.1:8000\"|dansal_url: \"http://127.0.0.1:$API_PORT\"|" "dansal_url: \"http://127.0.0.1:$API_PORT\""
  patch "$m" "s|admin_socket: \"/var/lib/dansal/dansal.sock\"|admin_socket: \"$DATA/dansal.sock\"|" "admin_socket: \"$DATA/dansal.sock\""
  patch "$m" "s|web_db_path: \"/var/lib/dansal-web/web.db\"|web_db_path: \"$DATA/web.db\"|" "web_db_path: \"$DATA/web.db\""

  cd "$ROOT"
  start dansal "$CONF/config.yaml"
  wait_http "http://127.0.0.1:$API_PORT/api/v1/health" dansal
  start dansal_web "$CONF/web.yaml"
  wait_http "http://127.0.0.1:$WEB_PORT/health" dansal_web
  start dansal_webmin "$CONF/webmin.yaml"
  wait_port "http://127.0.0.1:$WEBMIN_PORT/" dansal_webmin

  echo "e2e-stack: up — API :$API_PORT, web :$WEB_PORT, webmin :$WEBMIN_PORT"
  echo "e2e-stack: dansal logs syslog to the journal (journalctl -t dansal@config); dansal_web and dansal_webmin log to $LOGS"
}

start() {
  local name="$1" config="$2"
  nohup "$ROOT/$name" --config "$config" >"$LOGS/$name.log" 2>&1 &
  echo $! >"$RUN/$name.pid"
}

# wait_http URL NAME — poll until the URL returns 2xx or give up.
wait_http() {
  local url="$1" name="$2"
  for _ in $(seq 1 60); do
    if curl -fsS -o /dev/null "$url" 2>/dev/null; then return 0; fi
    kill -0 "$(cat "$RUN/$name.pid")" 2>/dev/null || { tail -n 20 "$LOGS/$name.log" >&2; die "$name exited during startup"; }
    sleep 0.5
  done
  tail -n 20 "$LOGS/$name.log" >&2
  die "$name did not answer at $url"
}

# wait_port URL NAME — webmin has no health route; any HTTP response means up.
wait_port() {
  local url="$1" name="$2" code
  for _ in $(seq 1 60); do
    code="$(curl -s -o /dev/null -w '%{http_code}' "$url" || true)"
    if [ "$code" != "000" ] && [ -n "$code" ]; then return 0; fi
    kill -0 "$(cat "$RUN/$name.pid")" 2>/dev/null || { tail -n 20 "$LOGS/$name.log" >&2; die "$name exited during startup"; }
    sleep 0.5
  done
  tail -n 20 "$LOGS/$name.log" >&2
  die "$name did not answer at $url"
}

cmd_down() {
  local stopped=0
  for pidfile in "$RUN"/*.pid; do
    [ -e "$pidfile" ] || continue
    local name pid
    name="$(basename "$pidfile" .pid)"
    pid="$(cat "$pidfile")"
    if kill -0 "$pid" 2>/dev/null; then
      kill -TERM "$pid" 2>/dev/null || true
      for _ in $(seq 1 20); do kill -0 "$pid" 2>/dev/null || break; sleep 0.5; done
      kill -0 "$pid" 2>/dev/null && kill -KILL "$pid" 2>/dev/null || true
      echo "e2e-stack: stopped $name (pid $pid)"
      stopped=1
    fi
    rm -f "$pidfile"
  done
  [ "$stopped" = 1 ] || echo "e2e-stack: nothing running"
}

cmd_status() {
  local name pid state pidfile
  for name in "${BINARIES[@]}"; do
    pidfile="$RUN/$name.pid"
    state="stopped"
    if [ -e "$pidfile" ] && kill -0 "$(cat "$pidfile")" 2>/dev/null; then
      state="running (pid $(cat "$pidfile"))"
    fi
    echo "$name: $state"
  done
}

case "${1:-status}" in
  up) cmd_up ;;
  down) cmd_down ;;
  status) cmd_status ;;
  *) die "usage: $0 {up|down|status}" ;;
esac
