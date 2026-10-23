#!/bin/bash
# clean-e2e-events — delete events left behind by the Playwright e2e suite.
#
# Targets (dry run by default):
#   - events created by the e2e accounts (e2e-admin@, e2e-viewer@dansal.test)
#   - events imported from the e2e feed sources (fetch_sources whose URL
#     contains "dev.balfolk.jetzt/feed/festival/events.ics?e2e=")
# Events from real feeds and from real users are never selected.
#
# The database is backed up first, then each event is removed with
# dansal_admin delete-event (the same path the admin tools use).
#
# Usage:
#   scripts/clean-e2e-events.sh [instance]          # list targets, change nothing
#   scripts/clean-e2e-events.sh [instance] --apply  # back up, then delete
#
# instance defaults to dev. Only dev and test are accepted; prod is refused.
# Run as root (sudo) because the database and admin socket are owned by dansal.

set -euo pipefail

INSTANCE="${1:-dev}"
MODE="${2:-}"

case "$INSTANCE" in
  dev|test) ;;
  *) echo "refusing instance '$INSTANCE': only dev and test are allowed" >&2; exit 1 ;;
esac

if [ "$(id -u)" != "0" ]; then
  echo "run as root: sudo $0 $*" >&2
  exit 1
fi

DB="/var/lib/dansal/$INSTANCE/calendar.db"
SOCKET="/var/lib/dansal/$INSTANCE/dansal.sock"
ADMIN="/usr/lib/dansal/$INSTANCE/dansal_admin"
BACKUP_DIR="/var/lib/dansal/$INSTANCE/backups"

for f in "$DB" "$SOCKET" "$ADMIN"; do
  if [ ! -e "$f" ]; then
    echo "missing: $f" >&2
    exit 1
  fi
done

# Only user-visible identifiers in the list: id, start, title (truncated).
TARGET_SQL="
SELECT e.id
FROM events e
WHERE e.created_by_id IN (SELECT id FROM users WHERE email IN ('e2e-admin@dansal.test','e2e-viewer@dansal.test'))
   OR (e.fetch_source_id IN (SELECT id FROM fetch_sources WHERE url LIKE '%dev.balfolk.jetzt/feed/festival/events.ics?e2e=%')
       AND (e.created_by_id IS NULL
            OR e.created_by_id NOT IN (SELECT id FROM users WHERE email IN ('e2e-admin@dansal.test','e2e-viewer@dansal.test'))))
ORDER BY e.id;"

IDS=$(sqlite3 -readonly "$DB" "$TARGET_SQL")
COUNT=$(printf '%s' "$IDS" | grep -c . || true)

echo "instance:   $INSTANCE"
echo "database:   $DB"
echo "targets:    $COUNT event(s)"

if [ "$COUNT" -eq 0 ]; then
  echo "nothing to delete"
  exit 0
fi

if [ "$MODE" != "--apply" ]; then
  echo
  echo "first 20 targets (id, start, title):"
  sqlite3 -readonly -separator ' | ' "$DB" "
    SELECT e.id, datetime(e.start_time,'unixepoch'), substr(e.title,1,60)
    FROM events e WHERE e.id IN ($(printf '%s' "$IDS" | paste -sd, -))
    ORDER BY e.id LIMIT 20;"
  echo
  echo "dry run: nothing changed. Re-run with --apply to back up and delete."
  exit 0
fi

mkdir -p "$BACKUP_DIR"
STAMP=$(date -u +%Y%m%dT%H%M%SZ)
BACKUP="$BACKUP_DIR/calendar.db.before-e2e-cleanup-$STAMP"
sqlite3 "$DB" ".backup '$BACKUP'"
echo "backup:     $BACKUP"

deleted=0
failed=0
while IFS= read -r id; do
  [ -z "$id" ] && continue
  if "$ADMIN" --socket "$SOCKET" delete-event --id "$id" >/dev/null 2>&1; then
    deleted=$((deleted + 1))
  else
    failed=$((failed + 1))
    echo "failed to delete event $id" >&2
  fi
done <<< "$IDS"

echo "deleted:    $deleted"
echo "failed:     $failed"
[ "$failed" -eq 0 ]
