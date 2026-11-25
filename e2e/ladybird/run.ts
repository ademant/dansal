// Ladybird smoke runner (#1906). Playwright can't drive Ladybird, so this
// talks plain W3C WebDriver over HTTP to Ladybird's own WebDriver server and
// walks the same public path list as the Playwright sweep
// (fixtures/public-paths.ts): navigate, wait for load, check a key selector,
// collect JS errors.
//
// REPORT-ONLY: exits 0 even when pages fail, until Ladybird's WebDriver
// support is stable. Set LADYBIRD_STRICT=1 to exit 1 on failures.
//
// Limitation: classic WebDriver has no "run this before the page's own
// scripts" hook (that's WebDriver BiDi's script.addPreloadScript, which
// Ladybird doesn't offer yet). The error collector is injected right after
// load, so it catches errors from timers, fetch callbacks and later events,
// but not a parse-time throw. Those show up indirectly: the key selector a
// broken init script would have built (e.g. the home map) is missing.
//
// Env:
//   BASE_URL              site under test (default http://localhost:8080)
//   LADYBIRD_WEBDRIVER    WebDriver server (default http://127.0.0.1:4444)
//   LADYBIRD_STRICT=1     exit 1 when any page fails
//
// Run: npx tsx ladybird/run.ts   (or `make e2e-smoke-ladybird`)

import { PUBLIC_PATHS, PUBLIC_DETAIL_LINKS } from "../fixtures/public-paths";

const BASE_URL = (process.env.BASE_URL ?? "http://localhost:8080").replace(/\/$/, "");
const WD = (process.env.LADYBIRD_WEBDRIVER ?? "http://127.0.0.1:4444").replace(/\/$/, "");
const STRICT = process.env.LADYBIRD_STRICT === "1";
const LOAD_TIMEOUT_MS = 20_000;
const SETTLE_MS = 1_500;

// A selector that must exist once the page's scripts ran; default "h1".
const KEY_SELECTOR: Record<string, string> = {
  "/": ".leaflet-container",
  "/festivals": ".leaflet-container",
  "/search": "#sf-event-table",
};
function keySelectorFor(path: string): string {
  if (KEY_SELECTOR[path]) return KEY_SELECTOR[path];
  if (/^\/events\/\d+$/.test(path)) return ".evt-header h1";
  return "h1";
}

// ── Minimal W3C WebDriver client ─────────────────────────────────────────
async function wd(method: string, path: string, body?: unknown): Promise<any> {
  const resp = await fetch(`${WD}${path}`, {
    method,
    headers: { "Content-Type": "application/json; charset=utf-8" },
    body: body === undefined ? undefined : JSON.stringify(body),
  });
  const text = await resp.text();
  let json: any;
  try {
    json = JSON.parse(text);
  } catch {
    throw new Error(`${method} ${path} → ${resp.status}: ${text.slice(0, 200)}`);
  }
  if (!resp.ok || json?.value?.error) {
    const v = json?.value ?? {};
    throw new Error(`${method} ${path} → ${resp.status} ${v.error ?? ""}: ${(v.message ?? "").slice(0, 200)}`);
  }
  return json.value;
}

const exec = (sid: string, script: string, args: unknown[] = []) =>
  wd("POST", `/session/${sid}/execute/sync`, { script, args });

const sleep = (ms: number) => new Promise((r) => setTimeout(r, ms));

// Installed right after load; see the limitation note at the top.
const INSTALL_COLLECTOR = `
  window.__e2eErrors = window.__e2eErrors || [];
  window.addEventListener('error', function(e){ window.__e2eErrors.push(String(e.message || e).slice(0,160)); });
  window.addEventListener('unhandledrejection', function(e){ window.__e2eErrors.push('unhandled rejection: ' + String(e.reason).slice(0,160)); });
  return true;`;

interface Result {
  path: string;
  ok: boolean;
  detail: string;
}

async function waitForLoad(sid: string): Promise<void> {
  const deadline = Date.now() + LOAD_TIMEOUT_MS;
  while (Date.now() < deadline) {
    if ((await exec(sid, "return document.readyState;")) === "complete") return;
    await sleep(200);
  }
  throw new Error("document.readyState never reached complete");
}

async function checkPath(sid: string, path: string): Promise<Result> {
  try {
    await wd("POST", `/session/${sid}/url`, { url: BASE_URL + path });
    await waitForLoad(sid);
    await exec(sid, INSTALL_COLLECTOR);
    await sleep(SETTLE_MS);
    const sel = keySelectorFor(path);
    const found = await exec(sid, "return !!document.querySelector(arguments[0]);", [sel]);
    const errors: string[] = await exec(sid, "return window.__e2eErrors || [];");
    const problems: string[] = [];
    if (!found) problems.push(`missing ${sel}`);
    if (errors.length) problems.push(`JS errors: ${errors.join(" | ")}`);
    return { path, ok: problems.length === 0, detail: problems.join("; ") };
  } catch (e) {
    return { path, ok: false, detail: (e as Error).message };
  }
}

// First href matching `link` on page `from`, for the detail pages.
async function firstLink(sid: string, from: string, link: string): Promise<string | null> {
  await wd("POST", `/session/${sid}/url`, { url: BASE_URL + from });
  await waitForLoad(sid);
  return exec(
    sid,
    `var as = document.querySelectorAll(arguments[0]);
     for (var i = 0; i < as.length; i++) {
       var h = as[i].getAttribute('href') || '';
       if (/^\\/[a-z]+\\/[^/?#]+$/.test(h) && !/\\.(ics|json|csv)$/.test(h) && h !== '/events/suggest') return h;
     }
     return null;`,
    [link]
  );
}

async function main(): Promise<void> {
  let sid: string;
  try {
    const session = await wd("POST", "/session", { capabilities: { alwaysMatch: {} } });
    sid = session.sessionId;
  } catch (e) {
    console.error(`ladybird: cannot start a WebDriver session at ${WD}: ${(e as Error).message}`);
    console.error("ladybird: is Ladybird's WebDriver server running? See e2e/README.md (Ladybird).");
    process.exit(STRICT ? 1 : 0);
  }

  const results: Result[] = [];
  try {
    for (const path of PUBLIC_PATHS) results.push(await checkPath(sid, path));

    const details = new Set<string>();
    for (const { from, link } of PUBLIC_DETAIL_LINKS) {
      try {
        const href = await firstLink(sid, from, link);
        if (href) details.add(href);
        else console.log(`ladybird: SKIP no ${link} on ${from}`);
      } catch (e) {
        results.push({ path: `${from} → ${link}`, ok: false, detail: (e as Error).message });
      }
    }
    // The venue page, reached from the first event page.
    const firstEvent = [...details].find((h) => h.startsWith("/events/"));
    if (firstEvent) {
      try {
        const loc = await firstLink(sid, firstEvent, 'a[href^="/location/"]');
        if (loc) details.add(loc);
      } catch {}
    }
    for (const path of details) results.push(await checkPath(sid, path));
  } finally {
    await wd("DELETE", `/session/${sid}`).catch(() => {});
  }

  const failed = results.filter((r) => !r.ok);
  for (const r of results) console.log(`ladybird: ${r.ok ? "PASS" : "FAIL"} ${r.path}${r.ok ? "" : ` — ${r.detail}`}`);
  console.log(`ladybird: ${results.length - failed.length}/${results.length} pages passed${STRICT ? "" : " (report-only)"}`);
  process.exit(STRICT && failed.length ? 1 : 0);
}

main();
