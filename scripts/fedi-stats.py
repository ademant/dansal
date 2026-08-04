#!/usr/bin/env python3
"""
fedi-stats.py — daily fediverse activity report for balfolk.jetzt

Usage:
    sudo python3 scripts/fedi-stats.py [YYYY-MM-DD] [--out FILE] [--store PATH]
                                        [--web-db PATH]

    DATE defaults to yesterday. --out defaults to stdout.
    --store writes aggregated rows into a SQLite file (same DB as bot-stats.py).
    --web-db path to dansal-web's SQLite database (web.db) for follower /
             delivery counts.  Defaults to /var/lib/dansal-web/prod/web.db.

Reads:
    /var/log/nginx/access.log (and .1/.2.gz) — AP endpoint traffic.
    web.db — follower gains and event delivery counts for the target day.
"""

import argparse
import gzip
import json
import re
import sqlite3
import sys
from collections import defaultdict
from datetime import date, timedelta
from urllib.parse import urlparse

# ---------------------------------------------------------------------------
# Nginx log parser (same format as bot-stats.py)
# ---------------------------------------------------------------------------
LOG_RE = re.compile(
    r'^\[(?P<time>[^\]]+)\] '
    r'"(?P<request>[^"]*)" '
    r'(?P<status>\d{3}) '
    r'(?P<bytes>\d+) '
    r'"(?P<referer>[^"]*)" '
    r'"(?P<ua>[^"]*)" '
    r'"(?P<xff>[^"]*)"'
)
TIME_RE = re.compile(r"^(\d{2})/(\w{3})/(\d{4}):")
MONTH_MAP = {
    "Jan": 1, "Feb": 2, "Mar": 3, "Apr": 4, "May": 5, "Jun": 6,
    "Jul": 7, "Aug": 8, "Sep": 9, "Oct": 10, "Nov": 11, "Dec": 12,
}

# ---------------------------------------------------------------------------
# AP endpoint path patterns
# ---------------------------------------------------------------------------
# Shared inbox
INBOX_SHARED_RE = re.compile(r"^/inbox$")
# Per-org inbox  e.g. /org/bfn/inbox
INBOX_ORG_RE    = re.compile(r"^/org/[^/]+/inbox$")
# Actor profile  e.g. /org/bfn  (not /inbox /outbox /followers)
ACTOR_RE        = re.compile(r"^/org/[^/]+$")
# Outbox
OUTBOX_RE       = re.compile(r"^/org/[^/]+/outbox(?:\?.*)?$")
# Followers collection
FOLLOWERS_RE    = re.compile(r"^/org/[^/]+/followers(?:\?.*)?$")
# WebFinger
WEBFINGER_RE    = re.compile(r"^/\.well-known/webfinger")
# NodeInfo
NODEINFO_RE     = re.compile(r"^/(?:\.well-known/nodeinfo|nodeinfo/)")
# Event fetch (GET /events/{id} by a remote AP server)
EVENT_FETCH_RE  = re.compile(r"^/events/\d+$")

# ---------------------------------------------------------------------------
# Source-instance extraction from User-Agent
# Mastodon:  "Mastodon/4.2.0 (compatible; +https://mastodon.social/)"
# Misskey:   "Misskey/2024.2 (https://example.social)"
# Pleroma:   "http.ex/0.1.0-dev (Pleroma 2.x; https://example.social)"
# Akkoma:    same pattern as Pleroma
# Lemmy:     "lemmy-user-agent/0.19 (https://example.community)"
# Generic:   look for https?://hostname/ pattern
# ---------------------------------------------------------------------------
INSTANCE_URL_RE = re.compile(r"https?://([a-zA-Z0-9](?:[a-zA-Z0-9\-]{0,61}[a-zA-Z0-9])?(?:\.[a-zA-Z0-9](?:[a-zA-Z0-9\-]{0,61}[a-zA-Z0-9])?)+)(?:/[^)\s\"]*)?")

# Known non-instance hosts to skip (CDNs, crawlers that happen to include URLs)
SKIP_INSTANCES = {
    "www.w3.org", "schema.org", "joinmastodon.org", "w3id.org",
    "github.com", "example.com", "example.org",
}

AP_UA_RE = re.compile(
    r"Mastodon|Misskey|Pleroma|Pixelfed|Akkoma|Lemmy|Kbin|Sharkey|"
    r"Calckey|Firefish|Catodon|Iceshrimp|Snac|Friendica|Hubzilla|"
    r"PeerTube|NodeInfo|Diaspora|GNU.Social|Hometown|Bookwyrm",
    re.I
)


def extract_instance(ua: str) -> str | None:
    """Return the hostname of the remote fediverse instance from a UA string."""
    if not ua or ua == "-":
        return None
    # Must look like an AP software UA
    if not AP_UA_RE.search(ua):
        return None
    m = INSTANCE_URL_RE.search(ua)
    if not m:
        return None
    host = m.group(1).lower()
    if host in SKIP_INSTANCES:
        return None
    # Skip our own domain (self-referential)
    if "balfolk" in host:
        return None
    return host


def parse_date_from_time(time_str: str) -> date | None:
    m = TIME_RE.match(time_str)
    if not m:
        return None
    mon = MONTH_MAP.get(m.group(2))
    if mon is None:
        return None
    return date(int(m.group(3)), mon, int(m.group(1)))


def nginx_log_files() -> list[str]:
    import os
    candidates = ["/var/log/nginx/access.log", "/var/log/nginx/access.log.1"]
    gz = "/var/log/nginx/access.log.2.gz"
    if os.path.exists(gz):
        candidates.append(gz)
    return candidates


def open_log(path: str):
    if path.endswith(".gz"):
        return gzip.open(path, "rt", encoding="utf-8", errors="replace")
    return open(path, encoding="utf-8", errors="replace")


def parse_nginx_fedi(target: date) -> dict:
    """Extract AP-endpoint traffic for target date from nginx logs."""
    counts = {
        "inbox_total":      0,
        "inbox_2xx":        0,
        "inbox_4xx":        0,
        "inbox_5xx":        0,
        "actor_fetches":    0,
        "webfinger":        0,
        "nodeinfo":         0,
        "outbox_fetches":   0,
        "followers_fetches":0,
        "event_fetches":    0,
    }
    instances: dict[str, int] = defaultdict(int)

    for log_path in nginx_log_files():
        try:
            fh = open_log(log_path)
        except (FileNotFoundError, PermissionError) as e:
            print(f"  [warn] {log_path}: {e}", file=sys.stderr)
            continue

        with fh:
            for line in fh:
                m = LOG_RE.match(line)
                if not m:
                    continue
                if parse_date_from_time(m.group("time")) != target:
                    continue

                req    = m.group("request")
                status = m.group("status")
                ua     = m.group("ua")

                parts = req.split(" ")
                method = parts[0] if parts else ""
                path   = parts[1] if len(parts) > 1 else ""
                # strip query string for path matching
                path_bare = path.split("?")[0]

                # ── Inbox (POST only) ──────────────────────────────────
                if method == "POST" and (INBOX_SHARED_RE.match(path_bare) or
                                          INBOX_ORG_RE.match(path_bare)):
                    counts["inbox_total"] += 1
                    if status.startswith("2"):
                        counts["inbox_2xx"] += 1
                    elif status.startswith("4"):
                        counts["inbox_4xx"] += 1
                    elif status.startswith("5"):
                        counts["inbox_5xx"] += 1

                # ── Actor profile (GET /org/<slug>) ────────────────────
                elif method == "GET" and ACTOR_RE.match(path_bare):
                    counts["actor_fetches"] += 1

                # ── Outbox ─────────────────────────────────────────────
                elif method == "GET" and OUTBOX_RE.match(path):
                    counts["outbox_fetches"] += 1

                # ── Followers collection ───────────────────────────────
                elif method == "GET" and FOLLOWERS_RE.match(path):
                    counts["followers_fetches"] += 1

                # ── WebFinger ─────────────────────────────────────────
                elif method == "GET" and WEBFINGER_RE.match(path):
                    counts["webfinger"] += 1

                # ── NodeInfo ──────────────────────────────────────────
                elif method == "GET" and NODEINFO_RE.match(path_bare):
                    counts["nodeinfo"] += 1

                # ── Event fetch by AP client ──────────────────────────
                elif method == "GET" and EVENT_FETCH_RE.match(path_bare) and AP_UA_RE.search(ua):
                    counts["event_fetches"] += 1

                else:
                    continue  # not an AP endpoint — skip instance extraction

                # ── Source instance ───────────────────────────────────
                inst = extract_instance(ua)
                if inst:
                    instances[inst] += 1

    return {"counts": counts, "instances": dict(instances)}


# ---------------------------------------------------------------------------
# web.db reading
# ---------------------------------------------------------------------------

def read_web_db(db_path: str, target: date) -> dict:
    """Read daily follower gains and delivery counts from web.db."""
    result = {
        "followers_gained":            0,
        "events_delivered_creates":    0,
        "events_delivered_updates":    0,
        "total_followers":             0,
        "pending_delivery_failures":   0,
    }
    try:
        db = sqlite3.connect(
            f"file:{db_path}?mode=ro&immutable=1&_busy_timeout=3000",
            uri=True,
        )
    except Exception as e:
        print(f"  [warn] web.db: {e}", file=sys.stderr)
        return result

    d = str(target)
    try:
        row = db.execute(
            "SELECT COUNT(*) FROM followers WHERE date(created_at)=?", (d,)
        ).fetchone()
        if row:
            result["followers_gained"] = row[0]

        row = db.execute(
            "SELECT COUNT(*) FROM delivered WHERE date(delivered_at)=? AND is_update=0", (d,)
        ).fetchone()
        if row:
            result["events_delivered_creates"] = row[0]

        row = db.execute(
            "SELECT COUNT(*) FROM delivered WHERE date(delivered_at)=? AND is_update=1", (d,)
        ).fetchone()
        if row:
            result["events_delivered_updates"] = row[0]

        row = db.execute("SELECT COUNT(*) FROM followers").fetchone()
        if row:
            result["total_followers"] = row[0]

        row = db.execute("SELECT COUNT(*) FROM delivery_failures").fetchone()
        if row:
            result["pending_delivery_failures"] = row[0]

    except Exception as e:
        print(f"  [warn] web.db query: {e}", file=sys.stderr)
    finally:
        db.close()

    return result


# ---------------------------------------------------------------------------
# SQLite storage
# ---------------------------------------------------------------------------

SCHEMA = """
CREATE TABLE IF NOT EXISTS fedi_stats_daily (
    date                        TEXT PRIMARY KEY,
    followers_gained            INTEGER NOT NULL DEFAULT 0,
    events_delivered_creates    INTEGER NOT NULL DEFAULT 0,
    events_delivered_updates    INTEGER NOT NULL DEFAULT 0,
    inbox_total                 INTEGER NOT NULL DEFAULT 0,
    inbox_2xx                   INTEGER NOT NULL DEFAULT 0,
    inbox_4xx                   INTEGER NOT NULL DEFAULT 0,
    inbox_5xx                   INTEGER NOT NULL DEFAULT 0,
    actor_fetches               INTEGER NOT NULL DEFAULT 0,
    webfinger_requests          INTEGER NOT NULL DEFAULT 0,
    nodeinfo_requests           INTEGER NOT NULL DEFAULT 0,
    outbox_fetches              INTEGER NOT NULL DEFAULT 0,
    followers_fetches           INTEGER NOT NULL DEFAULT 0,
    event_fetches               INTEGER NOT NULL DEFAULT 0
);
CREATE TABLE IF NOT EXISTS fedi_source_instances (
    date     TEXT    NOT NULL,
    instance TEXT    NOT NULL,
    count    INTEGER NOT NULL DEFAULT 0,
    PRIMARY KEY (date, instance)
);
"""


def store_to_sqlite(db_path: str, target: date, nginx: dict, webdb: dict) -> None:
    con = sqlite3.connect(db_path)
    con.execute("PRAGMA journal_mode=WAL")
    con.executescript(SCHEMA)

    d = str(target)
    nc = nginx["counts"]

    with con:
        con.execute(
            """INSERT OR REPLACE INTO fedi_stats_daily
               (date,
                followers_gained, events_delivered_creates, events_delivered_updates,
                inbox_total, inbox_2xx, inbox_4xx, inbox_5xx,
                actor_fetches, webfinger_requests, nodeinfo_requests,
                outbox_fetches, followers_fetches, event_fetches)
               VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?)""",
            (
                d,
                webdb["followers_gained"],
                webdb["events_delivered_creates"],
                webdb["events_delivered_updates"],
                nc["inbox_total"], nc["inbox_2xx"], nc["inbox_4xx"], nc["inbox_5xx"],
                nc["actor_fetches"], nc["webfinger"], nc["nodeinfo"],
                nc["outbox_fetches"], nc["followers_fetches"], nc["event_fetches"],
            ),
        )
        for inst, cnt in nginx["instances"].items():
            con.execute(
                "INSERT OR REPLACE INTO fedi_source_instances (date,instance,count) VALUES (?,?,?)",
                (d, inst, cnt),
            )

    con.close()


# ---------------------------------------------------------------------------
# Entry point
# ---------------------------------------------------------------------------

def main():
    parser = argparse.ArgumentParser(description="Daily fediverse activity report")
    parser.add_argument(
        "date",
        nargs="?",
        default=str(date.today() - timedelta(days=1)),
        help="Target date (YYYY-MM-DD); defaults to yesterday",
    )
    parser.add_argument("--out",    default="-",   help="Output file; defaults to stdout")
    parser.add_argument("--store",  metavar="PATH", help="SQLite file to upsert stats into")
    parser.add_argument(
        "--web-db",
        metavar="PATH",
        default="/var/lib/dansal-web/prod/web.db",
        help="Path to dansal-web's web.db (default: /var/lib/dansal-web/prod/web.db)",
    )
    args = parser.parse_args()

    try:
        target = date.fromisoformat(args.date)
    except ValueError:
        sys.exit(f"Invalid date: {args.date!r}  (use YYYY-MM-DD)")

    print(f"Parsing nginx logs for fediverse endpoints ({target})…", file=sys.stderr)
    nginx = parse_nginx_fedi(target)

    print(f"Reading web.db ({args.web_db})…", file=sys.stderr)
    webdb = read_web_db(args.web_db, target)

    report = {
        "date":   str(target),
        "nginx":  nginx,
        "web_db": webdb,
    }

    output = json.dumps(report, indent=2, ensure_ascii=False)
    if args.out == "-":
        print(output)
    else:
        with open(args.out, "w") as f:
            f.write(output + "\n")
        print(f"Written to {args.out}", file=sys.stderr)

    if args.store:
        store_to_sqlite(args.store, target, nginx, webdb)
        print(f"Stored in {args.store}", file=sys.stderr)


if __name__ == "__main__":
    main()
