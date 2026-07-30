#!/usr/bin/env python3
"""
bot-stats.py — daily bot activity report for balfolk.jetzt

Usage:
    sudo python3 scripts/bot-stats.py [YYYY-MM-DD] [--out FILE] [--store PATH]

    DATE defaults to yesterday. --out defaults to stdout.
    --store writes the aggregated rows into a SQLite file for dashboard use.

Reads:
    /var/log/nginx/access.log (and .1 if the target date falls in the
    previous rotation) for server_name balfolk.jetzt (dansal-prod).
    journalctl -u dansal@prod   for the target day.
    journalctl -u dansal-web@prod for the target day.

Note: nginx log_format 'main' on this server omits $remote_addr, so
IP-level attribution is not available — analysis is UA-based only.
"""

import argparse
import gzip
import json
import re
import sqlite3
import subprocess
import sys
from collections import defaultdict
from datetime import date, timedelta

# ---------------------------------------------------------------------------
# Bot classification rules — evaluated top-to-bottom, first match wins.
# ---------------------------------------------------------------------------
CATEGORIES = [
    ("search_engine", re.compile(
        r"Googlebot|Bingbot|DuckDuckBot|Baiduspider|YandexBot|Sogou|"
        r"ia_archiver|archive\.org_bot|Exabot|NaverBot|msnbot|Slurp",
        re.I)),
    ("ai_crawler", re.compile(
        r"GPTBot|OAI-SearchBot|ChatGPT-User|Claude-Web|ClaudeBot|anthropic-ai|"
        r"Applebot|Bytespider|Diffbot|PerplexityBot|YouBot|meta-externalagent|"
        r"Amazonbot|facebookexternalhit|LinkedInBot|Twitterbot|Discordbot|"
        r"TelegramBot|WhatsApp",
        re.I)),
    ("seo_scanner", re.compile(
        r"SemrushBot|AhrefsBot|MJ12bot|DotBot|BLEXBot|MauiBot|SEOkicks|"
        r"RogerBot|CriteoBot|ScreamingFrog|DataForSeoBot|BrightEdge|"
        r"sistrix|seokicks|SiteAuditBot|serpstatbot",
        re.I)),
    ("activitypub", re.compile(
        r"Mastodon|Misskey|Pleroma|Pixelfed|NodeInfo|PeerTube|"
        r"calckey|Akkoma|Lemmy|Kbin|Sharkey",
        re.I)),
    ("uptime_monitor", re.compile(
        r"UptimeRobot|Pingdom|StatusCake|BetterUptime|freshping|"
        r"Site24x7|Zabbix|uptimia",
        re.I)),
    ("spam_scanner", re.compile(
        r"chimney|Nuclei|masscan|zgrab|sqlmap|nikto|dirbuster|"
        r"hydra|metasploit|nessus|openvas|acunetix|netsparker",
        re.I)),
    ("http_library", re.compile(
        r"^curl/|^Wget/|python-requests|python-urllib|Go-http-client|"
        r"^Java/|libwww-perl|^Ruby|^PHP/|^axios|^node-fetch|"
        r"^okhttp|HTTPie|Scrapy|^WordPress/",
        re.I)),
    ("generic_bot", re.compile(
        r"bot|crawler|spider|scraper|checker|monitor|fetch|harvest",
        re.I)),
]

# ---------------------------------------------------------------------------
# Nginx log line parser
# Format: [$time_local] "$request" $status $body_bytes "$http_referer"
#         "$http_user_agent" "$http_x_forwarded_for"
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

# e.g. "28/Jul/2026:00:01:49 +0000"
TIME_RE = re.compile(r'^(\d{2})/(\w{3})/(\d{4}):')

MONTH_MAP = {
    "Jan": 1, "Feb": 2, "Mar": 3, "Apr": 4, "May": 5, "Jun": 6,
    "Jul": 7, "Aug": 8, "Sep": 9, "Oct": 10, "Nov": 11, "Dec": 12,
}


def classify_ua(ua: str) -> str:
    if not ua or ua == "-":
        return "empty_ua"
    for category, pattern in CATEGORIES:
        if pattern.search(ua):
            return category
    return "browser"


def parse_date_from_time(time_str: str) -> date | None:
    m = TIME_RE.match(time_str)
    if not m:
        return None
    day, mon, year = int(m.group(1)), MONTH_MAP.get(m.group(2)), int(m.group(3))
    if mon is None:
        return None
    return date(year, mon, day)


def open_log(path: str):
    """Open a plain or gzip-compressed log file."""
    if path.endswith(".gz"):
        return gzip.open(path, "rt", encoding="utf-8", errors="replace")
    return open(path, "r", encoding="utf-8", errors="replace")


def nginx_log_files() -> list[str]:
    """Return candidate nginx log files in read order (newest first)."""
    import os
    candidates = ["/var/log/nginx/access.log", "/var/log/nginx/access.log.1"]
    # include .2.gz if it exists (covers weekends with thin traffic)
    gz = "/var/log/nginx/access.log.2.gz"
    if os.path.exists(gz):
        candidates.append(gz)
    return candidates


def parse_nginx(target: date) -> dict:
    stats = {
        "total_requests": 0,
        "by_status": defaultdict(int),
        "by_category": defaultdict(lambda: {"count": 0, "top_ua": defaultdict(int), "top_paths": defaultdict(int)}),
        "top_paths_bots": defaultdict(int),
        "top_ua_all": defaultdict(int),
    }

    found_any = False
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
                line_date = parse_date_from_time(m.group("time"))
                if line_date != target:
                    continue
                found_any = True
                status = m.group("status")
                ua = m.group("ua")
                request = m.group("request")
                path = request.split(" ")[1] if " " in request else request

                stats["total_requests"] += 1
                stats["by_status"][status] += 1
                stats["top_ua_all"][ua] += 1

                cat = classify_ua(ua)
                bucket = stats["by_category"][cat]
                bucket["count"] += 1
                bucket["top_ua"][ua] += 1
                if cat != "browser":
                    bucket["top_paths"][path] += 1
                    stats["top_paths_bots"][path] += 1

        if found_any:
            # Stop after we've seen the target date — earlier files are older
            # (we might still need .1 if today's log doesn't contain yesterday)
            pass

    return stats


def top_n(d: dict, n: int = 10) -> list[dict]:
    return [{"value": k, "count": v}
            for k, v in sorted(d.items(), key=lambda x: -x[1])[:n]]


def render_nginx(raw: dict) -> dict:
    total = raw["total_requests"]

    bot_categories = {}
    bot_total = 0
    for cat, bucket in raw["by_category"].items():
        if cat == "browser":
            continue
        cnt = bucket["count"]
        bot_total += cnt
        bot_categories[cat] = {
            "count": cnt,
            "top_user_agents": top_n(bucket["top_ua"], 5),
            "top_paths": top_n(bucket["top_paths"], 5),
        }

    browser_cnt = raw["by_category"].get("browser", {}).get("count", 0)

    def pct(n):
        return f"{100*n/total:.1f}%" if total else "0.0%"

    return {
        "total_requests": total,
        "by_status": dict(sorted(raw["by_status"].items())),
        "bots": {
            "total": bot_total,
            "pct": pct(bot_total),
            "by_category": bot_categories,
        },
        "humans": {
            "total": browser_cnt,
            "pct": pct(browser_cnt),
        },
        "top_paths_by_bots": top_n(raw["top_paths_bots"], 20),
        "top_user_agents": top_n(raw["top_ua_all"], 20),
    }


# ---------------------------------------------------------------------------
# journalctl parsing
# ---------------------------------------------------------------------------

def run_journalctl(unit: str, since: str, until: str) -> list[str]:
    cmd = [
        "journalctl", "-u", unit,
        "--since", since, "--until", until,
        "--output=short-iso", "--no-pager",
    ]
    result = subprocess.run(cmd, capture_output=True, text=True)
    if result.returncode not in (0, 1):
        print(f"  [warn] journalctl {unit}: {result.stderr.strip()}", file=sys.stderr)
    return result.stdout.splitlines()


INBOX_FAIL_RE = re.compile(
    r"inbox: verification failed for (?P<actor>\S+): (?P<reason>.+)"
)


def parse_journalctl_web(lines: list[str]) -> dict:
    inbox_failures = defaultdict(int)
    inbox_actors = defaultdict(int)
    other_errors = []

    for line in lines:
        m = INBOX_FAIL_RE.search(line)
        if m:
            reason = m.group("reason").strip()
            actor = m.group("actor")
            # Normalise reason to a short key
            if "missing keyId" in reason:
                key = "missing_keyId_in_signature"
            elif "actor fetch returned HTTP 410" in reason:
                key = "actor_deleted_410"
            elif "actor fetch returned HTTP" in reason:
                key = "actor_fetch_error"
            elif "could not verify" in reason or "invalid signature" in reason.lower():
                key = "invalid_signature"
            else:
                key = "other"
            inbox_failures[key] += 1
            # Track unique actors (domain only)
            domain = actor.split("/")[2] if actor.startswith("https://") else actor
            inbox_actors[domain] += 1
            continue

        if " panic" in line or " FATAL" in line or " ERROR" in line:
            other_errors.append(line.strip())

    return {
        "inbox_verification_failures": {
            "total": sum(inbox_failures.values()),
            "by_reason": dict(inbox_failures),
            "top_actor_domains": top_n(inbox_actors, 10),
        },
        "other_errors": other_errors[:20],
    }


def parse_journalctl_api(lines: list[str]) -> dict:
    token_cleanups = 0
    removed_tokens = 0
    errors = []

    for line in lines:
        if "token cleanup" in line:
            token_cleanups += 1
            m = re.search(r"removed (\d+) expired", line)
            if m:
                removed_tokens += int(m.group(1))
        elif " panic" in line or " FATAL" in line or " ERROR" in line:
            errors.append(line.strip())

    return {
        "token_cleanup_runs": token_cleanups,
        "expired_tokens_removed": removed_tokens,
        "errors": errors[:20],
    }


# ---------------------------------------------------------------------------
# Entry point
# ---------------------------------------------------------------------------

def main():
    parser = argparse.ArgumentParser(description="Daily bot activity report")
    parser.add_argument(
        "date",
        nargs="?",
        default=str(date.today() - timedelta(days=1)),
        help="Target date (YYYY-MM-DD); defaults to yesterday",
    )
    parser.add_argument(
        "--out",
        default="-",
        help="Output file path; defaults to stdout",
    )
    parser.add_argument(
        "--store",
        metavar="PATH",
        help="SQLite file to upsert daily stats into (created if absent)",
    )
    args = parser.parse_args()

    try:
        target = date.fromisoformat(args.date)
    except ValueError:
        sys.exit(f"Invalid date: {args.date!r}  (use YYYY-MM-DD)")

    since = f"{target} 00:00:00"
    until = f"{target} 23:59:59"

    print(f"Parsing nginx logs for {target}…", file=sys.stderr)
    nginx_raw = parse_nginx(target)
    nginx_out = render_nginx(nginx_raw)

    print("Fetching journalctl dansal@prod…", file=sys.stderr)
    api_lines = run_journalctl("dansal@prod", since, until)
    api_out = parse_journalctl_api(api_lines)

    print("Fetching journalctl dansal-web@prod…", file=sys.stderr)
    web_lines = run_journalctl("dansal-web@prod", since, until)
    web_out = parse_journalctl_web(web_lines)

    report = {
        "date": str(target),
        "nginx": nginx_out,
        "dansal_api": api_out,
        "dansal_web": web_out,
    }

    output = json.dumps(report, indent=2, ensure_ascii=False)

    if args.out == "-":
        print(output)
    else:
        with open(args.out, "w") as f:
            f.write(output)
            f.write("\n")
        print(f"Written to {args.out}", file=sys.stderr)

    if args.store:
        store_to_sqlite(args.store, report)
        print(f"Stored in {args.store}", file=sys.stderr)


# ---------------------------------------------------------------------------
# SQLite storage
# ---------------------------------------------------------------------------

def store_to_sqlite(db_path: str, report: dict) -> None:
    con = sqlite3.connect(db_path)
    con.execute("PRAGMA journal_mode=WAL")
    con.executescript("""
        CREATE TABLE IF NOT EXISTS bot_stats_daily (
            date      TEXT NOT NULL,
            category  TEXT NOT NULL,
            count     INTEGER NOT NULL DEFAULT 0,
            PRIMARY KEY (date, category)
        );
        CREATE TABLE IF NOT EXISTS bot_stats_meta (
            date                TEXT PRIMARY KEY,
            total_requests      INTEGER NOT NULL DEFAULT 0,
            human_count         INTEGER NOT NULL DEFAULT 0,
            bot_count           INTEGER NOT NULL DEFAULT 0,
            inbox_failures      INTEGER NOT NULL DEFAULT 0,
            api_errors          INTEGER NOT NULL DEFAULT 0,
            web_errors          INTEGER NOT NULL DEFAULT 0
        );
    """)

    d = report["date"]
    ng = report["nginx"]
    bot_cats = ng["bots"]["by_category"]

    with con:
        for cat, data in bot_cats.items():
            con.execute(
                "INSERT OR REPLACE INTO bot_stats_daily (date, category, count) VALUES (?, ?, ?)",
                (d, cat, data["count"]),
            )
        # also store human traffic as its own "category" for unified trend queries
        con.execute(
            "INSERT OR REPLACE INTO bot_stats_daily (date, category, count) VALUES (?, ?, ?)",
            (d, "browser", ng["humans"]["total"]),
        )

        con.execute(
            """INSERT OR REPLACE INTO bot_stats_meta
               (date, total_requests, human_count, bot_count, inbox_failures, api_errors, web_errors)
               VALUES (?, ?, ?, ?, ?, ?, ?)""",
            (
                d,
                ng["total_requests"],
                ng["humans"]["total"],
                ng["bots"]["total"],
                report["dansal_web"]["inbox_verification_failures"]["total"],
                len(report["dansal_api"]["errors"]),
                len(report["dansal_web"]["other_errors"]),
            ),
        )

    con.close()


if __name__ == "__main__":
    main()
