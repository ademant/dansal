import { test, expect, Browser, Page } from "@playwright/test";
import { fullSeed, SeedResult } from "../../helpers/seed";
import { AUTH_FILE } from "../../helpers/auth";
import { EVENT_HREF_PREFIX } from "../../fixtures/data";

// #1906: anonymous-visitor smoke that runs on every engine — the Chromium
// desktop/mobile projects as part of their normal set, and the firefox /
// webkit / webkit-iphone projects (playwright.config.ts) via
// grep /@public-smoke/. No login: every page is opened in a fresh context
// with empty storage, the way a first-time visitor sees it.
//
// The static public pages are swept in public-pages-js-errors.spec.ts (same
// tag). This file covers the detail pages that need data — seeded here so
// the suite doesn't depend on another spec having run first, since the
// non-Chromium projects run only the tagged tests — plus a few content checks.
//
// Engine-specific failures: mark them with
//   test.fixme(browserName === "webkit", "#<issue>")
// never skip silently.

const TAG = { tag: "@public-smoke" };

let seed: SeedResult;

async function anonPage(browser: Browser): Promise<{ page: Page; errors: string[]; close: () => Promise<void> }> {
  const context = await browser.newContext({ storageState: { cookies: [], origins: [] } });
  const page = await context.newPage();
  const errors: string[] = [];
  page.on("pageerror", (e) => errors.push(e.message.slice(0, 160)));
  return { page, errors, close: () => context.close() };
}

// Opens path as an anonymous visitor and fails on any uncaught page error.
async function expectNoPageErrors(browser: Browser, path: string): Promise<void> {
  const { page, errors, close } = await anonPage(browser);
  const resp = await page.goto(path, { waitUntil: "load" });
  expect(resp?.status(), `${path} status`).toBeLessThan(400);
  await page.waitForTimeout(800);
  expect(errors, errors.join("\n")).toEqual([]);
  await close();
}

// Reads the first href matching selector on page `from` — detail pages are
// reached the way a visitor reaches them. Short timeout: a missing link
// should fail here, not run into the 90 s test timeout.
async function firstHref(browser: Browser, from: string, selector: string): Promise<string> {
  const { page, close } = await anonPage(browser);
  await page.goto(from, { waitUntil: "load" });
  const href = await page.locator(selector).first().getAttribute("href", { timeout: 5_000 }).catch(() => null);
  await close();
  expect(href, `link ${selector} on ${from}`).toBeTruthy();
  return href!;
}

test.describe("Public smoke: anonymous visitor (#1906)", () => {
  test.beforeAll(async ({ browser }) => {
    const context = await browser.newContext({ storageState: AUTH_FILE });
    const setupPage = await context.newPage();
    seed = await fullSeed(setupPage);
    await context.close();
  });

  // ── Detail pages: no uncaught JS errors ──────────────────────────────
  // (The event page itself is in public-pages-js-errors.spec.ts.)
  test("location page has no page errors", TAG, async ({ browser }) => {
    await expectNoPageErrors(browser, `/location/${seed.locationId}`);
  });

  test("organization page has no page errors", TAG, async ({ browser }) => {
    await expectNoPageErrors(browser, await firstHref(browser, `${EVENT_HREF_PREFIX}${seed.eventIds[0]}`, 'a[href^="/org/"]'));
  });

  test("city page has no page errors", TAG, async ({ browser }) => {
    await expectNoPageErrors(browser, await firstHref(browser, "/cities", 'a[href^="/city/"]'));
  });

  test("musician page has no page errors", TAG, async ({ browser }) => {
    await expectNoPageErrors(browser, `/musicians/${seed.musicianId}`);
  });

  test("search loads its results without page errors", TAG, async ({ browser }) => {
    const { page, errors, close } = await anonPage(browser);
    const results = page.waitForResponse((r) => r.url().includes("/search/results"), { timeout: 15_000 });
    await page.goto("/search", { waitUntil: "load" });
    expect((await results).status()).toBe(200);
    // Either rows or the empty state, never neither.
    await expect
      .poll(async () =>
        (await page.locator("#sf-event-tbody tr").count()) > 0 ||
        (await page.locator("#sf-empty-state").isVisible())
      )
      .toBe(true);
    await page.waitForTimeout(500);
    expect(errors, errors.join("\n")).toEqual([]);
    await close();
  });

  test("festivals map builds without page errors", TAG, async ({ browser }) => {
    const { page, errors, close } = await anonPage(browser);
    await page.goto("/festivals", { waitUntil: "load" });
    await expect(page.locator("#map-container.leaflet-container, #map-container .leaflet-container")).toBeAttached({
      timeout: 15_000,
    });
    await page.waitForTimeout(500);
    expect(errors, errors.join("\n")).toEqual([]);
    await close();
  });

  // ── Content checks ───────────────────────────────────────────────────
  test("home page lists events", TAG, async ({ browser }) => {
    const { page, close } = await anonPage(browser);
    await page.goto("/", { waitUntil: "load" });
    // Attached, not visible: on a phone the list sits behind the map tab.
    await expect
      .poll(() => page.locator('a[href^="/events/"]').evaluateAll((as) =>
        as.filter((a) => /^\/events\/\d+$/.test(a.getAttribute("href") || "")).length))
      .toBeGreaterThan(0);
    await close();
  });

  test("home map renders at least one tile", TAG, async ({ browser }) => {
    const { page, close } = await anonPage(browser);
    await page.goto("/", { waitUntil: "load" });
    await expect(page.locator(".leaflet-container").first()).toBeVisible({ timeout: 15_000 });
    await expect
      .poll(
        () => page.locator("img.leaflet-tile").evaluateAll((imgs) =>
          imgs.filter((i) => (i as HTMLImageElement).complete && (i as HTMLImageElement).naturalWidth > 0).length),
        { timeout: 20_000 }
      )
      .toBeGreaterThan(0);
    await close();
  });

  test("event page shows title and venue", TAG, async ({ browser }) => {
    const { page, close } = await anonPage(browser);
    await page.goto(`${EVENT_HREF_PREFIX}${seed.eventIds[0]}`, { waitUntil: "load" });
    await expect(page.locator(".evt-header h1")).toContainText(seed.eventTitles[0]);
    // Venue: in the location panel (desktop) or the phone-only info line.
    await expect(page.locator(".evt-loc-name, .evt-info-venue").first()).toBeAttached();
    await expect(page.locator(".evt-loc-name, .evt-info-venue").first()).toContainText(/Testville/);
    await close();
  });

  test("event .ics link returns a calendar", TAG, async ({ browser }) => {
    const { page, close } = await anonPage(browser);
    const resp = await page.request.get(`${EVENT_HREF_PREFIX}${seed.eventIds[0]}.ics`);
    expect(resp.status()).toBe(200);
    const body = await resp.text();
    expect(body).toContain("BEGIN:VCALENDAR");
    expect(body).toContain("BEGIN:VEVENT");
    await close();
  });

  test("phone home opens on the map tab", TAG, async ({ browser }) => {
    const { page, close } = await anonPage(browser);
    await page.goto("/", { waitUntil: "load" });
    if (!(await page.locator("#mtab-list").isVisible().catch(() => false))) {
      await close();
      test.skip(true, "phone layout only: tab bar");
    }
    await expect(page.locator("#home-tabs")).toHaveAttribute("data-mtab", "map");
    await expect(page.locator("#mtab-map")).toHaveAttribute("aria-selected", "true");
    await close();
  });
});
