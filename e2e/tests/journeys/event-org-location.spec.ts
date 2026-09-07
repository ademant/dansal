import { test, expect } from "../../helpers/fixtures";
import { Page } from "@playwright/test";
import {
  fullSeed,
  SeedResult,
  getTokenFromCookie,
  apiPost,
} from "../../helpers/seed";
import { AUTH_FILE } from "../../helpers/auth";
import { VIEWER, randomFutureDate, isoDate, hhmm, EVENT_DATE_MIN_DAYS, EVENT_DATE_MAX_DAYS } from "../../fixtures/data";

const API_BASE = process.env.API_URL ?? "http://localhost:8000";
const BASE_URL = process.env.BASE_URL ?? "http://localhost:8080";

let seed: SeedResult;

function unique(name: string): string {
  return `${name} ${Date.now()}`;
}

async function authedGet(page: Page, token: string, path: string) {
  return page.request.fetch(`${API_BASE}${path}`, {
    headers: { Authorization: `Bearer ${token}` },
  });
}

function idFromEditURL(url: string): number {
  const m = url.match(/\/admin\/events\/(\d+)\/edit/);
  if (!m) throw new Error(`not an event edit URL: ${url}`);
  return parseInt(m[1], 10);
}

async function createMinimalEvent(page: Page, title: string): Promise<number> {
  const eventDate = randomFutureDate(EVENT_DATE_MIN_DAYS, EVENT_DATE_MAX_DAYS);
  await page.goto("/admin/events/new");
  await page.fill('input[name="title"]', title);
  await page.fill("#date", isoDate(eventDate));
  await page.fill('input[name="start_time"]', hhmm(20, 0));
  await page.fill('input[name="end_time"]', hhmm(22, 0));
  await page.locator("#save-btn").click();
  await page.waitForURL(/\/admin\/events\/\d+\/edit/);
  return idFromEditURL(page.url());
}

// createOrg/createLocation go through the real admin forms rather than a
// direct API POST: dansal_web caches GetOrganizations/GetLocations for
// admin pickers (#1276), and only its own create handlers invalidate that
// cache — an org/location created by POSTing straight to the API server
// stays invisible to the org/location picker and the events-list
// quick-assign tool for up to that cache's TTL.
async function createOrg(page: Page, name: string): Promise<number> {
  await page.goto("/admin/organizations/new");
  await page.fill("#name", name);
  await page.locator("#save-btn").click();
  await page.waitForURL("**/admin/organizations");
  return idByName(page, "/api/v1/organizations", name);
}

async function createLocation(page: Page, name: string): Promise<number> {
  await page.goto("/admin/locations/new");
  await page.fill("#location", name);
  await openLocationSection(page, "sec-address");
  await page.fill("#town", "E2E Ville");
  await page.locator("#save-btn").click();
  await page.waitForURL("**/admin/locations");
  return idByName(page, "/api/v1/locations", name);
}

// The location edit/create form collapses every section except #sec-base;
// same click-to-open pattern locations.spec.ts uses for its own sections.
async function openLocationSection(page: Page, target: string): Promise<void> {
  const btn = page.locator(`.loc-nav-item[data-target="${target}"]`);
  if (!(await btn.isVisible())) {
    await page.locator("#loc-nav-toggle").click();
  }
  await btn.click();
}

// Reads the just-created org/location back by exact name via the (uncached)
// raw API — a lookup, not a mutation, so #1276's cache doesn't apply here.
async function idByName(page: Page, path: string, name: string): Promise<number> {
  const resp = await page.request.fetch(
    `${API_BASE}${path}?name=${encodeURIComponent(name)}&limit=100`
  );
  const body = await resp.json();
  const list = Array.isArray(body) ? body : [];
  const match = list.find(
    (item: any) => item.name === name || item.location === name
  );
  if (!match) throw new Error(`${path}: no exact match for ${JSON.stringify(name)}`);
  return match.id;
}

// On mobile the bulk-actions bar (quick-assign fields, merge, etc.) lives
// in a drawer that's CSS-hidden until body.actions-open — normally set by
// a real long-press gesture on a row (enterMultiSelect(), wired to
// touchstart+setTimeout) that Playwright doesn't simulate. Reproduce the
// end state directly instead of the gesture itself.
async function openMobileBulkActions(page: Page): Promise<void> {
  await page.evaluate(() => {
    document.body.classList.add("ms-active", "actions-open");
    const btn = document.getElementById("mt-actions-btn");
    if (btn) (btn as HTMLButtonElement).hidden = false;
  });
}

// Grabs the /org/{slug} href straight from the event page's "organised by"
// link rather than reimplementing orgSlug()'s transliteration/dash rules
// client-side.
async function orgPageURLFor(page: Page, eventId: number): Promise<string> {
  await page.goto(`/events/${eventId}`);
  const href = await page
    .locator('a[href^="/org/"]')
    .first()
    .getAttribute("href");
  if (!href) throw new Error("event page has no organiser link");
  return href;
}

test.describe("Admin: event org/location assignment", () => {
  test.beforeAll(async ({ browser }) => {
    const context = await browser.newContext({ storageState: AUTH_FILE });
    const setupPage = await context.newPage();
    seed = await fullSeed(setupPage);
    await context.close();
  });

  test("assigning and re-assigning org/location changes where an event is visible", async ({
    page,
  }) => {
    const token = await getTokenFromCookie(page);
    const title = unique("E2E Org-Loc Event");
    const eventId = await createMinimalEvent(page, title);

    const org1 = unique("E2E OrgLoc Org1");
    const org2 = unique("E2E OrgLoc Org2");
    const loc1 = unique("E2E OrgLoc Loc1");
    const loc2 = unique("E2E OrgLoc Loc2");
    const org1Id = await createOrg(page, org1);
    const org2Id = await createOrg(page, org2);
    const loc1Id = await createLocation(page, loc1);
    const loc2Id = await createLocation(page, loc2);

    // -- Assign to org1 via the /admin/events bulk quick-assign tool. --
    await page.goto("/admin/events?include_past=1");
    await page
      .locator(`tr[data-evt-id="${eventId}"] .event-cb`)
      .evaluate((el) => {
        (el as HTMLInputElement).checked = true;
        el.dispatchEvent(new Event("change", { bubbles: true }));
      });
    await openMobileBulkActions(page);
    await page.fill("#bq-org-input", org1);
    await page.press("#bq-org-input", "Enter");
    await page.locator("#bq-apply").click();
    await page.waitForTimeout(1500);

    let ev = await (await authedGet(page, token, `/api/v1/events/${eventId}`)).json();
    expect(ev.organization_id).toBe(org1Id);

    const org1URL = await orgPageURLFor(page, eventId);
    await page.goto(org1URL);
    await expect(page.locator(".event-list").filter({ hasText: title })).toBeVisible();

    // -- Assign to loc1 via the same tool (org left untouched). --
    await page.goto("/admin/events?include_past=1");
    await page
      .locator(`tr[data-evt-id="${eventId}"] .event-cb`)
      .evaluate((el) => {
        (el as HTMLInputElement).checked = true;
        el.dispatchEvent(new Event("change", { bubbles: true }));
      });
    await openMobileBulkActions(page);
    await page.fill("#bq-loc-input", loc1);
    await page.press("#bq-loc-input", "Enter");
    await page.locator("#bq-apply").click();
    await page.waitForTimeout(1500);

    ev = await (await authedGet(page, token, `/api/v1/events/${eventId}`)).json();
    expect(ev.location_id).toBe(loc1Id);
    expect(ev.organization_id).toBe(org1Id); // untouched by the location-only call

    await page.goto(`/location/${loc1Id}`);
    await expect(page.locator(".event-list").filter({ hasText: title })).toBeVisible();

    // -- Change both org and location via the regular edit form. Both are
    //    already assigned (org1/loc1), so the "Add" buttons are hidden in
    //    favour of an assigned-chip with a "Remove" button — clear each
    //    first to get back to the picker. --
    await page.goto(`/admin/events/${eventId}/edit`);
    await page.locator('[data-fn="removeOrgAssignment"]').click();
    await page.locator("#org-add-btn").click();
    await page
      .locator("#org-picker-table tr")
      .filter({ hasText: org2 })
      .locator('button[data-fn="assignOrg"]')
      .click();
    await page.locator('[data-fn="removeLocAssignment"]').click();
    await page.locator("#loc-add-btn").click();
    await page
      .locator("#loc-picker-table tr")
      .filter({ hasText: loc2 })
      .locator('button[data-fn="assignLoc"]')
      .click();
    await page.locator("#save-btn").click();
    await page.waitForURL(`**/admin/events/${eventId}/edit`);

    ev = await (await authedGet(page, token, `/api/v1/events/${eventId}`)).json();
    expect(ev.organization_id).toBe(org2Id);
    expect(ev.location_id).toBe(loc2Id);

    // -- Gone from the old org/location pages, present on the new ones. --
    await page.goto(org1URL);
    await expect(page.locator(".event-list").filter({ hasText: title })).toHaveCount(0);
    await page.goto(`/location/${loc1Id}`);
    await expect(page.locator(".event-list").filter({ hasText: title })).toHaveCount(0);

    const org2URL = await orgPageURLFor(page, eventId);
    await page.goto(org2URL);
    await expect(page.locator(".event-list").filter({ hasText: title })).toBeVisible();
    await page.goto(`/location/${loc2Id}`);
    await expect(page.locator(".event-list").filter({ hasText: title })).toBeVisible();
  });
});

test.describe("Org membership rules on create/assign (#1273, #1275)", () => {
  test.beforeAll(async ({ browser }) => {
    const context = await browser.newContext({ storageState: AUTH_FILE });
    const setupPage = await context.newPage();
    seed = await fullSeed(setupPage);
    await context.close();
  });

  test("a non-admin must specify an org they belong to; can claim an org-less event into it", async ({
    page,
    browser,
  }) => {
    const adminToken = await getTokenFromCookie(page);

    const memberOrg = unique("E2E Claim Org");
    const otherOrg = unique("E2E Claim Org Other");
    // memberOrg needs to actually appear in the edit form's org picker
    // (create through the admin UI, #1276), but otherOrg is only ever used
    // as a raw organization_id value in a direct API call below — it's
    // never looked up through any cached admin list, so a plain API POST
    // is fine and considerably faster.
    const memberOrgId = await createOrg(page, memberOrg);
    const otherOrgId = await apiPost(page, "/api/v1/organizations", adminToken, {
      name: otherOrg,
    }).then((o) => o.id);

    // VIEWER (role "user") is a shared seed account with no fixed org of
    // its own — add it to memberOrg only. INSERT OR IGNORE server-side
    // makes this safe to repeat across runs.
    const viewerId = seed.viewerId;
    await apiPost(page, `/api/v1/organizations/${memberOrgId}/members`, adminToken, {
      user_id: viewerId,
    });

    // An admin-created event with no org at all — the "unassigned
    // feed-imported event" scenario #1275 is about.
    const orglessTitle = unique("E2E Claim Orgless Event");
    const orglessId = await createMinimalEvent(page, orglessTitle);

    // loginAs() (the real /login form) reproducibly hung indefinitely here
    // even in complete isolation, for reasons unrelated to this test (see
    // the session's own login-form investigation) — sidestep the form
    // entirely: log in through the raw API (POST /api/v1/login) and inject
    // the resulting session token as the dsw_token cookie directly.
    // dansal_web has no way to verify the signed dsw_user cookie without
    // its server-side secret, but authRefreshMiddleware
    // (cmd/dansal_web/session.go) already handles exactly this — a valid
    // dsw_token with no/invalid dsw_user gets the session transparently
    // re-established (via GET /api/v1/me) on the very next request.
    const viewerContext = await browser.newContext({ baseURL: BASE_URL });
    const viewerPage = await viewerContext.newPage();
    const viewerLoginResp = await viewerPage.request.fetch(`${API_BASE}/api/v1/login`, {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      data: JSON.stringify({ email: VIEWER.email, password: VIEWER.password }),
    });
    const viewerLogin = await viewerLoginResp.json();
    await viewerContext.addCookies([
      { name: "dsw_token", value: viewerLogin.token, url: BASE_URL },
    ]);
    const viewerToken = await getTokenFromCookie(viewerPage);

    // #1273/create-time rule: a non-admin must name an org they belong to.
    const eventDate = randomFutureDate(EVENT_DATE_MIN_DAYS, EVENT_DATE_MAX_DAYS);
    const basePayload = {
      title: unique("E2E Viewer Create"),
      start_time: isoDateTimeFor(eventDate, 20, 0),
      end_time: isoDateTimeFor(eventDate, 22, 0),
    };
    const noOrgResp = await viewerPage.request.fetch(`${API_BASE}/api/v1/events`, {
      method: "POST",
      headers: {
        "Content-Type": "application/json",
        Authorization: `Bearer ${viewerToken}`,
      },
      data: JSON.stringify(basePayload),
    });
    expect(noOrgResp.status()).toBe(400);

    const foreignOrgResp = await viewerPage.request.fetch(`${API_BASE}/api/v1/events`, {
      method: "POST",
      headers: {
        "Content-Type": "application/json",
        Authorization: `Bearer ${viewerToken}`,
      },
      data: JSON.stringify({ ...basePayload, organization_id: otherOrgId }),
    });
    expect(foreignOrgResp.status()).toBe(403);

    const ownOrgResp = await viewerPage.request.fetch(`${API_BASE}/api/v1/events`, {
      method: "POST",
      headers: {
        "Content-Type": "application/json",
        Authorization: `Bearer ${viewerToken}`,
      },
      data: JSON.stringify({ ...basePayload, organization_id: memberOrgId }),
    });
    expect(ownOrgResp.status()).toBe(201);

    // #1275: claim the org-less event into memberOrg through the real
    // admin UI, as VIEWER (not admin).
    await viewerPage.goto(`/admin/events/${orglessId}/edit`);
    await viewerPage.locator("#org-add-btn").click();
    await viewerPage
      .locator("#org-picker-table tr")
      .filter({ hasText: memberOrg })
      .locator('button[data-fn="assignOrg"]')
      .click();
    await viewerPage.locator("#save-btn").click();
    await viewerPage.waitForTimeout(1500);

    const claimed = await (await authedGet(viewerPage, adminToken, `/api/v1/events/${orglessId}`)).json();
    expect(claimed.organization_id).toBe(memberOrgId);

    const orgURL = await orgPageURLFor(page, orglessId);
    await page.goto(orgURL);
    await expect(page.locator(".event-list").filter({ hasText: orglessTitle })).toBeVisible();

    await viewerContext.close();
  });
});

function isoDateTimeFor(d: Date, hour: number, minute: number): string {
  const pad = (n: number) => String(n).padStart(2, "0");
  return `${d.getFullYear()}-${pad(d.getMonth() + 1)}-${pad(d.getDate())}T${pad(hour)}:${pad(minute)}:00`;
}
