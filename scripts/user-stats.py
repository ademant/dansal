#!/usr/bin/env python3
"""
user-stats.py — daily human interaction report for balfolk.jetzt

Usage:
    sudo python3 scripts/user-stats.py [YYYY-MM-DD] [--out FILE] [--store PATH]

    DATE defaults to yesterday. --out defaults to stdout.
    --store writes aggregated rows into a SQLite file (same DB as bot-stats.py).

Reads:
    /var/log/nginx/access.log (and .1) — human requests only (bots filtered).

Note: nginx log_format omits $remote_addr, so per-visitor session tracking is
impossible. Referrer-based attribution is the best available signal.
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
# Bot detection — same logic as bot-stats.py; keep in sync.
# ---------------------------------------------------------------------------
BOT_UA_RE = re.compile(
    r"Googlebot|Bingbot|DuckDuckBot|Baiduspider|YandexBot|Sogou|ia_archiver|"
    r"archive\.org_bot|NaverBot|msnbot|Slurp|"
    r"GPTBot|OAI-SearchBot|ChatGPT-User|ClaudeBot|anthropic-ai|Applebot|"
    r"Bytespider|Diffbot|PerplexityBot|YouBot|meta-externalagent|Amazonbot|"
    r"facebookexternalhit|LinkedInBot|Twitterbot|Discordbot|TelegramBot|WhatsApp|"
    r"SemrushBot|AhrefsBot|MJ12bot|DotBot|BLEXBot|MauiBot|SEOkicks|"
    r"RogerBot|CriteoBot|ScreamingFrog|DataForSeoBot|serpstatbot|"
    r"Mastodon|Misskey|Pleroma|Pixelfed|NodeInfo|PeerTube|Akkoma|Lemmy|Kbin|Sharkey|"
    r"UptimeRobot|Pingdom|StatusCake|BetterUptime|freshping|Site24x7|Zabbix|uptimia|"
    r"chimney|Nuclei|masscan|zgrab|sqlmap|nikto|acunetix|netsparker|"
    r"^curl/|^Wget/|python-requests|python-urllib|Go-http-client|"
    r"^Java/|libwww-perl|^Ruby|^PHP/|^axios|^node-fetch|^okhttp|HTTPie|Scrapy|^WordPress/|"
    r"bot|crawler|spider|scraper|checker|monitor|fetch|harvest|"
    r"fediscope|FediDB|PetalBot|leakix",
    re.I,
)

# Paths that scanners hit with browser UAs — count separately, exclude from
# human navigation metrics (they pollute entry-page and referrer stats).
SCANNER_PATH_RE = re.compile(
    r"^/wp-(?:login|admin|json|includes|content)|"
    r"^/xmlrpc\.php|^/\.env|^/phpmyadmin|^/admin\.php|"
    r"^/yunohost/|^/owa/|^/cgi-bin/|"
    r"\.php$",
    re.I,
)

SITE_HOST = "balfolk.jetzt"

SEARCH_ENGINES = {
    "google.":       "Google",
    "bing.com":      "Bing",
    "duckduckgo.com":"DuckDuckGo",
    "yahoo.com":     "Yahoo",
    "ecosia.org":    "Ecosia",
    "qwant.com":     "Qwant",
    "yandex.":       "Yandex",
    "baidu.com":     "Baidu",
    "startpage.com": "Startpage",
    "brave.com":     "Brave Search",
    "kagi.com":      "Kagi",
}

# ---------------------------------------------------------------------------
# Nginx log parser
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


def parse_date_from_time(time_str: str) -> date | None:
    m = TIME_RE.match(time_str)
    if not m:
        return None
    mon = MONTH_MAP.get(m.group(2))
    if mon is None:
        return None
    return date(int(m.group(3)), mon, int(m.group(1)))


def is_bot(ua: str) -> bool:
    if not ua or ua == "-":
        return True
    return bool(BOT_UA_RE.search(ua))


def classify_referrer(ref: str) -> str:
    """Return a short source key for the given Referer header value."""
    if not ref or ref == "-":
        return "direct"
    try:
        host = urlparse(ref).netloc.lower().removeprefix("www.")
    except Exception:
        return "other"
    if SITE_HOST in host:
        return "internal"
    for pattern, name in SEARCH_ENGINES.items():
        if pattern in host:
            return f"search:{name}"
    # Strip port for cleanliness
    host = host.split(":")[0]
    return f"external:{host}"


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


# ---------------------------------------------------------------------------
# Main analysis
# ---------------------------------------------------------------------------

def analyse(target: date) -> dict:
    total_human = 0
    scanner_slipthrough = 0

    referrer_counts: dict[str, int] = defaultdict(int)   # source key → count
    entry_pages: dict[str, int] = defaultdict(int)        # entry path → count
    internal_pages: dict[str, int] = defaultdict(int)     # internal-nav path → count
    all_pages: dict[str, int] = defaultdict(int)          # all human paths → count
    external_referrer_hosts: dict[str, int] = defaultdict(int)

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

                ua = m.group("ua")
                if is_bot(ua):
                    continue

                status = m.group("status")
                # Skip redirects — they inflate counts without adding info
                if status.startswith("3"):
                    continue

                request = m.group("request")
                parts = request.split(" ")
                if len(parts) < 2:
                    continue
                path = parts[1].split("?")[0]

                # Separate scanner traffic that slipped through UA filtering
                if SCANNER_PATH_RE.search(path):
                    scanner_slipthrough += 1
                    continue

                ref = m.group("referer")
                source = classify_referrer(ref)

                total_human += 1
                referrer_counts[source] += 1
                all_pages[path] += 1

                if source == "internal":
                    internal_pages[path] += 1
                else:
                    entry_pages[path] += 1
                    if source.startswith("search:") or source.startswith("external:"):
                        try:
                            host = urlparse(ref).netloc.lower().removeprefix("www.").split(":")[0]
                        except Exception:
                            host = ""
                        if host:
                            external_referrer_hosts[host] += 1

    # Aggregate referrer categories
    direct = referrer_counts.get("direct", 0)
    internal = referrer_counts.get("internal", 0)
    search = sum(v for k, v in referrer_counts.items() if k.startswith("search:"))
    external = sum(v for k, v in referrer_counts.items() if k.startswith("external:"))
    entries = direct + search + external
    clicks_per_entry = round(internal / entries, 3) if entries > 0 else 0.0

    def top(d: dict, n: int = 20) -> list[dict]:
        return [{"value": k, "count": v}
                for k, v in sorted(d.items(), key=lambda x: -x[1])[:n]]

    return {
        "date": str(target),
        "total_human_requests": total_human,
        "scanner_slipthrough": scanner_slipthrough,
        "referrer_summary": {
            "direct": direct,
            "search": search,
            "external": external,
            "internal": internal,
            "clicks_per_entry": clicks_per_entry,
        },
        "referrer_sources": top(referrer_counts),
        "top_external_referrers": top(external_referrer_hosts),
        "top_entry_pages": top(entry_pages),
        "top_internal_nav_pages": top(internal_pages),
        "top_pages_overall": top(all_pages),
    }


# ---------------------------------------------------------------------------
# SQLite storage
# ---------------------------------------------------------------------------

def store_to_sqlite(db_path: str, report: dict) -> None:
    con = sqlite3.connect(db_path)
    con.execute("PRAGMA journal_mode=WAL")
    con.executescript("""
        CREATE TABLE IF NOT EXISTS user_stats_meta (
            date                 TEXT PRIMARY KEY,
            total_requests       INTEGER NOT NULL DEFAULT 0,
            direct_count         INTEGER NOT NULL DEFAULT 0,
            search_count         INTEGER NOT NULL DEFAULT 0,
            external_count       INTEGER NOT NULL DEFAULT 0,
            internal_count       INTEGER NOT NULL DEFAULT 0,
            clicks_per_entry     REAL    NOT NULL DEFAULT 0,
            scanner_slipthrough  INTEGER NOT NULL DEFAULT 0
        );
        CREATE TABLE IF NOT EXISTS user_stats_referrers (
            date    TEXT NOT NULL,
            source  TEXT NOT NULL,
            count   INTEGER NOT NULL DEFAULT 0,
            PRIMARY KEY (date, source)
        );
        CREATE TABLE IF NOT EXISTS user_stats_entry_pages (
            date    TEXT NOT NULL,
            path    TEXT NOT NULL,
            count   INTEGER NOT NULL DEFAULT 0,
            PRIMARY KEY (date, path)
        );
        CREATE TABLE IF NOT EXISTS user_stats_top_pages (
            date    TEXT NOT NULL,
            path    TEXT NOT NULL,
            count   INTEGER NOT NULL DEFAULT 0,
            PRIMARY KEY (date, path)
        );
        CREATE TABLE IF NOT EXISTS user_stats_referrer_hosts (
            date    TEXT NOT NULL,
            host    TEXT NOT NULL,
            count   INTEGER NOT NULL DEFAULT 0,
            PRIMARY KEY (date, host)
        );
    """)

    d = report["date"]
    rs = report["referrer_summary"]

    with con:
        con.execute(
            """INSERT OR REPLACE INTO user_stats_meta
               (date, total_requests, direct_count, search_count, external_count,
                internal_count, clicks_per_entry, scanner_slipthrough)
               VALUES (?,?,?,?,?,?,?,?)""",
            (d, report["total_human_requests"], rs["direct"], rs["search"],
             rs["external"], rs["internal"], rs["clicks_per_entry"],
             report["scanner_slipthrough"]),
        )
        for row in report["referrer_sources"]:
            con.execute(
                "INSERT OR REPLACE INTO user_stats_referrers (date,source,count) VALUES (?,?,?)",
                (d, row["value"], row["count"]),
            )
        for row in report["top_entry_pages"]:
            con.execute(
                "INSERT OR REPLACE INTO user_stats_entry_pages (date,path,count) VALUES (?,?,?)",
                (d, row["value"], row["count"]),
            )
        for row in report["top_pages_overall"]:
            con.execute(
                "INSERT OR REPLACE INTO user_stats_top_pages (date,path,count) VALUES (?,?,?)",
                (d, row["value"], row["count"]),
            )
        for row in report["top_external_referrers"]:
            con.execute(
                "INSERT OR REPLACE INTO user_stats_referrer_hosts (date,host,count) VALUES (?,?,?)",
                (d, row["value"], row["count"]),
            )

    con.close()


# ---------------------------------------------------------------------------
# Entry point
# ---------------------------------------------------------------------------

def main():
    parser = argparse.ArgumentParser(description="Daily human interaction report")
    parser.add_argument(
        "date",
        nargs="?",
        default=str(date.today() - timedelta(days=1)),
        help="Target date (YYYY-MM-DD); defaults to yesterday",
    )
    parser.add_argument("--out", default="-", help="Output file; defaults to stdout")
    parser.add_argument(
        "--store",
        metavar="PATH",
        help="SQLite file to upsert stats into (same DB as bot-stats.py)",
    )
    args = parser.parse_args()

    try:
        target = date.fromisoformat(args.date)
    except ValueError:
        sys.exit(f"Invalid date: {args.date!r}  (use YYYY-MM-DD)")

    print(f"Analysing human traffic for {target}…", file=sys.stderr)
    report = analyse(target)

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


if __name__ == "__main__":
    main()
