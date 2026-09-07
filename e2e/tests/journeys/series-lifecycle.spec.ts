import { test, expect } from "../../helpers/fixtures";
import { Page } from "@playwright/test";
import { fullSeed, SeedResult, getTokenFromCookie } from "../../helpers/seed";
import { AUTH_FILE } from "../../helpers/auth";
import {
  randomFutureDate,
  isoDate,
  hhmm,
  EVENT_DATE_MIN_DAYS,
  EVENT_DATE_MAX_DAYS,
} from "../../fixtures/data";

const API_BASE = process.env.API_URL ?? "http://localhost:8000";

let seed: SeedResult;

function unique(name: string): string {
  return `${name} ${Date.now()}`;
}

async function authedGet(page: Page, token: string, path: string) {
  return page.request.fetch(`${API_BASE}${path}`, {
    headers: { Authorization: `Bearer ${token}` },
  });
}
async function authedJSON(page: Page, token: string, path: string): Promise<any> {
  return (await authedGet(page, token, path)).json();
}

function idFromSeriesURL(url: string): number {
  const m = url.match(/\/admin\/series\/(\d+)/);
  if (!m) throw new Error(`not a series URL: ${url}`);
  return parseInt(m[1], 10);
}

function idFromEditURL(url: string): number {
  const m = url.match(/\/admin\/events\/(\d+)\/edit/);
  if (!m) throw new Error(`not an event edit URL: ${url}`);
  return parseInt(m[1], 10);
}

async function createMinimalEvent(page: Page, title: string, eventDate: Date): Promise<number> {
  await page.goto("/admin/events/new");
  await page.fill('input[name="title"]', title);
  await page.fill("#date", isoDate(eventDate));
  await page.fill('input[name="start_time"]', hhmm(20, 0));
  await page.fill('input[name="end_time"]', hhmm(22, 0));
  await page.locator("#save-btn").click();
  await page.waitForURL(/\/admin\/events\/\d+\/edit/);
  return idFromEditURL(page.url());
}

// dansal_web caches GetLocations for admin pickers (#1276) and only its own
// create handler invalidates that cache — a location created by POSTing
// straight to the API server stays invisible to the series-new/series-edit
// location <select> for up to that cache's TTL, so go through the real
// admin form instead (same reasoning as event-org-location.spec.ts).
async function createLocation(page: Page, name: string): Promise<number> {
  await page.goto("/admin/locations/new");
  await page.fill("#location", name);
  const btn = page.locator('.loc-nav-item[data-target="sec-address"]');
  if (!(await btn.isVisible())) {
    await page.locator("#loc-nav-toggle").click();
  }
  await btn.click();
  await page.fill("#town", "E2E Ville");
  await page.locator("#save-btn").click();
  await page.waitForURL("**/admin/locations");
  const resp = await page.request.fetch(
    `${API_BASE}/api/v1/locations?name=${encodeURIComponent(name)}&limit=100`
  );
  const list = await resp.json();
  const match = Array.isArray(list) ? list.find((l: any) => l.location === name) : null;
  if (!match) throw new Error(`location not found: ${name}`);
  return match.id;
}

async function createOrg(page: Page, name: string): Promise<number> {
  await page.goto("/admin/organizations/new");
  await page.fill("#name", name);
  await page.locator("#save-btn").click();
  await page.waitForURL("**/admin/organizations");
  const resp = await page.request.fetch(
    `${API_BASE}/api/v1/organizations?name=${encodeURIComponent(name)}&limit=100`
  );
  const list = await resp.json();
  const match = Array.isArray(list) ? list.find((o: any) => o.name === name) : null;
  if (!match) throw new Error(`org not found: ${name}`);
  return match.id;
}

// Grabs the /org/{slug} href straight from an event page's "organised by"
// link rather than reimplementing orgSlug()'s transliteration/dash rules
// client-side (same pattern as event-org-location.spec.ts).
async function orgPageURLFor(page: Page, eventId: number): Promise<string> {
  await page.goto(`/events/${eventId}`);
  const href = await page.locator('a[href^="/org/"]').first().getAttribute("href");
  if (!href) throw new Error("event page has no organiser link");
  return href;
}

function daysOut(base: Date, extra: number): Date {
  const d = new Date(base);
  d.setDate(d.getDate() + extra);
  return d;
}

test.describe("Admin: recurring event series lifecycle", () => {
  test.beforeAll(async ({ browser }) => {
    const context = await browser.newContext({ storageState: AUTH_FILE });
    const setupPage = await context.newPage();
    seed = await fullSeed(setupPage);
    await context.close();
  });

  test("create, cadence, template defaults, cancel isolation, assignment, navigation, and description editing", async ({
    page,
  }) => {
    const token = await getTokenFromCookie(page);

    const locName = unique("E2E Series Loc");
    const locId = await createLocation(page, locName);
    // A dedicated org rather than the shared seeded one: an org page shows
    // recurring series derived from its "upcoming" events, fetched with a
    // 100-row cap ordered oldest-first (GetAllEventsByOrg) — after this
    // session's extensive reuse of the one seeded org across many spec
    // files, it now carries far more than 100 events, which would push our
    // brand-new future instances out of that window entirely.
    const orgName = unique("E2E Series Org");
    const orgId = await createOrg(page, orgName);

    // Three widely spaced future dates so ordering (prev/next nav) and
    // dedup (never within 3h of each other, let alone the same day) are
    // both unambiguous.
    const base = randomFutureDate(EVENT_DATE_MIN_DAYS, EVENT_DATE_MAX_DAYS);
    const date1 = base;
    const date2 = daysOut(base, 7);
    const date3 = daysOut(base, 14);

    const seriesTitle = unique("E2E Series");

    // -- Create the series with two instances (date1, date2), wired to the
    //    fresh org and location. --
    await page.goto("/admin/series/new");
    await page.fill('input[name="title"]', seriesTitle);
    await page.selectOption('select[name="organization_id"]', String(orgId));
    await page.selectOption("#loc-select", String(locId));
    await page.fill('input[name="default_start_time"]', hhmm(20, 0));
    await page.fill('input[name="default_end_time"]', hhmm(22, 0));
    const dateInputs = page.locator('input[name="series_date"]');
    await dateInputs.nth(0).fill(isoDate(date1));
    await dateInputs.nth(1).fill(isoDate(date2));
    await page.locator('form.evt-form button[type="submit"]').click();
    await page.waitForURL(/\/admin\/series\/\d+$/);
    const seriesId = idFromSeriesURL(page.url());

    let series = await authedJSON(page, token, `/api/v1/series/${seriesId}`);
    expect(series.events).toHaveLength(2);

    // -- Set a distinctive cadence and a template default (a tag) on the
    //    series edit page. The "Series Defaults" <details> is the second
    //    one inside the header form (first is Organisation & Location) —
    //    selected structurally to avoid depending on the page's language. --
    const cadenceText = unique("Every other Sunday");
    await page.goto(`/admin/series/${seriesId}`);
    await page.fill('input[name="cadence"]', cadenceText);
    await page.locator("#series-header-form .series-seg summary").nth(1).click();
    await page.locator('input[name="tags"][value="concert"]').check();
    await page.locator('button[form="series-header-form"]').click();
    await page.waitForURL(`**/admin/series/${seriesId}`);

    // -- Add a third date — created *after* the cadence/defaults save, so
    //    it alone should pick up the template default tag. --
    await page.goto(`/admin/series/${seriesId}`);
    await page.fill('.add-date-form input[name="date"]', isoDate(date3));
    await page.locator(".add-date-form button").click();
    await page.waitForTimeout(1000);

    series = await authedJSON(page, token, `/api/v1/series/${seriesId}`);
    expect(series.events).toHaveLength(3);
    const sorted = [...series.events].sort(
      (a: any, b: any) => new Date(a.start_time).getTime() - new Date(b.start_time).getTime()
    );
    const [ev1, ev2, ev3] = sorted;

    // Template defaults apply to new instances only.
    const [e1, e2, e3] = await Promise.all(
      [ev1, ev2, ev3].map((e: any) => authedJSON(page, token, `/api/v1/events/${e.id}`))
    );
    expect(e1.tags ?? []).not.toContain("concert");
    expect(e2.tags ?? []).not.toContain("concert");
    expect(e3.tags ?? []).toContain("concert");

    // -- Cadence renders on the org page and on each event page. --
    const orgURL = await orgPageURLFor(page, ev1.id);
    await page.goto(orgURL);
    const recurringItem = page.locator(".recurring-item").filter({ hasText: seriesTitle });
    await expect(recurringItem).toBeVisible();
    await expect(recurringItem.locator(".recurring-cadence")).toContainText(cadenceText);

    await page.goto(`/events/${ev1.id}`);
    await expect(page.locator(".series-cadence")).toContainText(cadenceText);

    // -- Cancelling one instance doesn't affect the siblings. --
    await page.goto("/admin/events?include_past=1");
    const cancelForm = page.locator(`tr[data-evt-id="${ev1.id}"] form[action*="/cancel"]`);
    page.once("dialog", (d) => d.accept());
    await cancelForm.locator("button").click();
    await page.waitForTimeout(1500);

    await page.goto(`/events/${ev1.id}`);
    await expect(page.locator(".badge.cancelled")).toBeVisible();
    const [e2After, e3After] = await Promise.all(
      [ev2, ev3].map((e: any) => authedJSON(page, token, `/api/v1/events/${e.id}`))
    );
    expect(e2After.is_cancelled).toBe(false);
    expect(e3After.is_cancelled).toBe(false);
    expect(e2After.is_published).toBe(true);
    expect(e3After.is_published).toBe(true);

    // Org page's recurring entry survives (other instances are still
    // upcoming and published).
    await page.goto(orgURL);
    await expect(page.locator(".recurring-item").filter({ hasText: seriesTitle })).toBeVisible();

    // -- Assign a standalone event to the series via its own edit page. --
    const standaloneTitle = unique("E2E Series Standalone");
    const standaloneDate = daysOut(base, 21);
    const standaloneId = await createMinimalEvent(page, standaloneTitle, standaloneDate);
    await page.goto(`/admin/events/${standaloneId}/edit`);
    await page.selectOption('select[name="series_id"]', String(seriesId));
    await page.locator('button[name="intent"][value="assign-series"]').click();
    await page.waitForURL(`**/admin/events/${standaloneId}/edit`);

    series = await authedJSON(page, token, `/api/v1/series/${seriesId}`);
    expect(series.events.map((e: any) => e.id)).toContain(standaloneId);

    // -- Prev/next navigation between instances. --
    await page.goto(`/events/${ev2.id}`);
    await expect(page.locator(".series-nav-prev")).toHaveAttribute("href", `/events/${ev1.id}`);
    await expect(page.locator(".series-nav-next")).toHaveAttribute("href", `/events/${ev3.id}`);
    await page.locator(".series-nav-next").click();
    await expect(page).toHaveURL(new RegExp(`/events/${ev3.id}$`));

    // -- Edit one instance's description via the series edit page. Below
    //    640px the per-row inline textarea is CSS-hidden (replaced by a
    //    read-only preview) in favour of a tap-to-open quick-edit popup. --
    const descFromSeriesPage = unique("Updated via series page");
    await page.goto(`/admin/series/${seriesId}`);
    const descTextarea = page.locator(`textarea[name="desc_${ev2.id}"]`);
    if (await descTextarea.isVisible().catch(() => false)) {
      await descTextarea.fill(descFromSeriesPage);
      await page
        .locator(`button[formaction="/admin/series/${seriesId}/descriptions"]`)
        .click();
    } else {
      await page.locator(`tr[data-evt-id="${ev2.id}"] .cell-date`).click();
      await page.locator("#sep-desc").fill(descFromSeriesPage);
      await page.locator("#sep-save-desc").click();
    }
    await page.waitForTimeout(1000);
    const e2Desc = await authedJSON(page, token, `/api/v1/events/${ev2.id}`);
    expect(e2Desc.description).toBe(descFromSeriesPage);

    // -- Edit another instance's description via the anonymous magic link.
    //    This series has no invite token yet — generate one. The
    //    surrounding <details> starts collapsed, so open it via its
    //    summary before the regenerate button becomes clickable. --
    await page.goto(`/admin/series/${seriesId}`);
    const inviteRegenForm = `form[action="/admin/series/${seriesId}/token/regenerate"]`;
    await page
      .locator("details.series-seg")
      .filter({ has: page.locator(inviteRegenForm) })
      .locator("summary")
      .click();
    await page.locator(`${inviteRegenForm} button`).click();
    await page.waitForTimeout(1000);

    series = await authedJSON(page, token, `/api/v1/series/${seriesId}`);
    const inviteToken = series.invite_token;
    expect(inviteToken).toBeTruthy();

    const siteOrigin = new URL(page.url()).origin;
    const anonContext = await page.context().browser()!.newContext();
    const anonPage = await anonContext.newPage();
    await anonPage.goto(`${siteOrigin}/series_token/${inviteToken}`);
    const descFromMagicLink = unique("Updated via magic link");
    const descForm = anonPage.locator(`form[action*="/events/${ev3.id}/description"]`);
    await descForm.locator("textarea[name='description']").fill(descFromMagicLink);
    await descForm.locator("button").click();
    await anonPage.waitForTimeout(1000);
    const e3Desc = await authedJSON(page, token, `/api/v1/events/${ev3.id}`);
    expect(e3Desc.description).toBe(descFromMagicLink);

    await anonContext.close();
  });
});
