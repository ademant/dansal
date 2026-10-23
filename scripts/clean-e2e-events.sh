#!/bin/bash
# clean-e2e-events — delete events and organizations left behind by the
# Playwright e2e suite.
#
# Targets (dry run by default):
#   - events created by the e2e accounts (e2e-admin@, e2e-viewer@dansal.test)
#   - events imported from the e2e feed sources (fetch_sources whose URL
#     contains "dev.balfolk.jetzt/feed/festival/events.ics?e2e=")
#   - organizations named "E2E Role Foreign Org <timestamp>" (role-access.spec.ts
#     creates one per run) that no other table references any more
# Events from real feeds and from real users are never selected.
#
# The database is backed up first. Events are removed with dansal_admin
# delete-event (the same path the admin tools use). There is no admin command
# for organizations, so they are removed with SQL in one transaction, after
# their organization_members and owner_media rows.
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

# An org is a target only when nothing else points at it; the NOT EXISTS list
# mirrors every table with a foreign key to organizations.
ORG_SQL="
SELECT o.id
FROM organizations o
WHERE o.name LIKE 'E2E Role Foreign Org %'
  AND NOT EXISTS (SELECT 1 FROM events x WHERE x.organization_id = o.id)
  AND NOT EXISTS (SELECT 1 FROM location_organizations x WHERE x.organization_id = o.id)
  AND NOT EXISTS (SELECT 1 FROM invite_links x WHERE x.org_id = o.id)
  AND NOT EXISTS (SELECT 1 FROM event_series x WHERE x.organization_id = o.id)
  AND NOT EXISTS (SELECT 1 FROM pending_registrations x WHERE x.org_id = o.id)
  AND NOT EXISTS (SELECT 1 FROM oidc_providers x WHERE x.org_id = o.id)
  AND NOT EXISTS (SELECT 1 FROM pending_fetch_suggestions x WHERE x.org_id = o.id)
  AND NOT EXISTS (SELECT 1 FROM fetch_sources x WHERE x.organization_id = o.id)
ORDER BY o.id;"

IDS=$(sqlite3 -readonly "$DB" "$TARGET_SQL")
COUNT=$(printf '%s' "$IDS" | grep -c . || true)
ORG_IDS=$(sqlite3 -readonly "$DB" "$ORG_SQL")
ORG_COUNT=$(printf '%s' "$ORG_IDS" | grep -c . || true)

echo "instance:   $INSTANCE"
echo "database:   $DB"
echo "targets:    $COUNT event(s), $ORG_COUNT organization(s)"

if [ "$COUNT" -eq 0 ] && [ "$ORG_COUNT" -eq 0 ]; then
  echo "nothing to delete"
  exit 0
fi

if [ "$MODE" != "--apply" ]; then
  if [ "$COUNT" -gt 0 ]; then
    echo
    echo "first 20 event targets (id, start, title):"
    sqlite3 -readonly -separator ' | ' "$DB" "
      SELECT e.id, datetime(e.start_time,'unixepoch'), substr(e.title,1,60)
      FROM events e WHERE e.id IN ($(printf '%s' "$IDS" | paste -sd, -))
      ORDER BY e.id LIMIT 20;"
  fi
  if [ "$ORG_COUNT" -gt 0 ]; then
    echo
    echo "organization targets (id, name):"
    sqlite3 -readonly -separator ' | ' "$DB" "
      SELECT id, substr(name,1,60) FROM organizations
      WHERE id IN ($(printf '%s' "$ORG_IDS" | paste -sd, -)) ORDER BY id;"
  fi
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

if [ "$ORG_COUNT" -gt 0 ]; then
  ORG_CSV=$(printf '%s' "$ORG_IDS" | paste -sd, -)
  # .timeout waits out a write from the running API instead of failing at once.
  sqlite3 "$DB" <<SQL
.timeout 5000
BEGIN;
DELETE FROM organization_members WHERE organization_id IN ($ORG_CSV);
DELETE FROM owner_media WHERE owner_type = 'organization' AND owner_id IN ($ORG_CSV);
DELETE FROM organizations WHERE id IN ($ORG_CSV);
COMMIT;
SQL
  echo "orgs deleted: $ORG_COUNT"
fi

echo "deleted:    $deleted"
echo "failed:     $failed"
[ "$failed" -eq 0 ]
