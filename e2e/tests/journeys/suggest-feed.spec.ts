/**
 * Suggest a new feed (#1333, all 3 phases) — preview, upfront location
 * mapping, admin/org-member approval, real import, and the phase-3
 * "create an account" link.
 *
 * The public form only accepts a feed URL (no file-upload alternative like
 * the admin import form has — see feed-import.spec.ts's own comment on
 * that), and the server-side fetch goes through the same SSRF-safe
 * safeClient as the regular admin import, which blocks loopback/private
 * IPs. There is therefore no way to point this at a locally-hosted
 * synthetic feed server the way feed-import.spec.ts does — a real,
 * publicly reachable feed is unavoidable here.
 *
 * dev.balfolk.jetzt's own tag-filtered "festival" feed
 * (https://dev.balfolk.jetzt/feed/festival/events.ics) was chosen over its
 * full sitewide feed deliberately: the sitewide feed has ~800 events across
 * ~130 unique locations, which blows the preview page's location-mapping
 * table up to a ~3.5 MB response (measured directly against a scratch
 * instance while building this test) — each of the ~130 rows renders a
 * <select> populated with the *entire* DB location list, so the row count
 * and the per-row option count multiply. The festival feed has a small,
 * stable handful of events (6 at time of writing) and is correspondingly
 * fast and light to drive.
 *
 * Each scenario appends a unique `?e2e=<id>` query param to the feed URL.
 * The param is inert (the server ignores it and returns the same feed
 * content), but it gives each run — and each scenario within a run — its
 * own distinct URL. fetchurl.go's upsertFetchSource is an upsert *by URL*:
 * reusing the literal same URL across the two scenarios below (existing-org
 * vs. new-org) would silently reassign one scenario's fetch_sources row to
 * the other's organization instead of creating an independent one.
 */
import { test, expect } from "../../helpers/fixtures";
import { Page } from "@playwright/test";
import {
  getTokenFromCookie,
  createUser,
  addOrgMember,
  loginViaApi,
} from "../../helpers/seed";

const WEB_BASE = process.env.BASE_URL ?? "http://localhost:8080";
const API_BASE = process.env.API_URL ?? "http://localhost:8000";

function unique(name: string): string {
  return `${name} ${Date.now()}`;
}

/** A fresh feed URL per call — see the file header comment for why. */
function testFeedURL(): string {
  const id = `${Date.now()}-${Math.random().toString(36).slice(2, 8)}`;
  return `https://dev.balfolk.jetzt/feed/festival/events.ics?e2e=${id}`;
}

// dansal_web caches GetOrganizations/GetLocations for admin pickers (#1276)
// — an org/location created by POSTing straight to the API stays invisible
// to this form's org-select/location-mapping dropdowns for up to that
// cache's TTL, so go through the real admin forms instead (same reasoning
// as feed-import.spec.ts / event-org-location.spec.ts).
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

async function fetchSuggestionRowByEmail(page: Page, email: string) {
  await page.goto("/admin/fetchurl-suggestions");
  return page.locator("tr").filter({ hasText: email });
}

test.describe("Suggest a feed (#1333)", () => {
  test.beforeAll(async ({ browser }) => {
    const checkCtx = await browser.newContext();
    const checkPage = await checkCtx.newPage();
    const resp = await checkPage.request.fetch(`${WEB_BASE}/feeds/suggest`);
    await checkCtx.close();
    if (resp.status() === 404) {
      test.skip(
        true,
        "suggest-a-feed not available (needs smtp_sendmail/smtp_host/telegram_bot_token configured in web.yaml)"
      );
    }
  });

  test("existing-org suggestion: preview auto-matches a known location, org member approves, real import runs, account link offered", async ({
    page,
    browser,
  }) => {
    const adminToken = await getTokenFromCookie(page);

    const orgName = unique("E2E FeedSuggest Org");
    const orgId = await createOrg(page, orgName);

    // One of the festival feed's real venue names, chosen because it has no
    // address baked into it (unlike most of the others, which carry a full
    // OSM-style address string as part of the name) — forces an auto-match
    // in the preview's location-mapping table (buildUniqueFeedLocs'
    // locByName-with-aliases lookup, the same one adminImportEventsHandler
    // uses) instead of every row defaulting to "create new".
    const matchedLocName = "Gemeinschaft Sonnenwald";
    await createLocation(page, matchedLocName);

    const memberEmail = `e2e-feedsuggest-member-${Date.now()}@example.com`;
    const memberPassword = "Xk7vQm2pLr9wZaFj";
    createUser(memberEmail, memberPassword, "user");
    addOrgMember(orgId, memberEmail);

    const submitterEmail = `e2e-feedsuggest-${Date.now()}@example.com`;
    const feedURL = testFeedURL();

    // suggestSubmitHandler-style public forms share publicThrottle
    // (ip+user-agent) with every other public form handler — a per-run UA
    // sidesteps repeated-local-run collisions (same fix as
    // suggest-wizard.spec.ts / board.spec.ts).
    const anonCtx = await browser.newContext({
      storageState: { cookies: [], origins: [] },
      userAgent: `Mozilla/5.0 (E2E suggest-feed test ${Date.now()})`,
    });
    const anonPage = await anonCtx.newPage();
    try {
      await anonPage.goto("/feeds/suggest");
      await anonPage.fill("#field-email", submitterEmail);
      // The "existing org" tab is already active by default.
      await anonPage.selectOption("#field-org-id", String(orgId));
      await anonPage.fill("#field-feed-url", feedURL);
      // Preview (POST /feeds/suggest) is not behind guardFormSubmit's
      // token-age check — only the final submit below is — so no wait is
      // needed before this click.
      await anonPage.locator('button[type="submit"]').click();

      await expect(anonPage.locator(".fetch-preview-box")).toBeVisible();
      const locRows = anonPage.locator(".fetch-loc-row");
      await expect(locRows.first()).toBeVisible();

      // Match on the row's own feed-location label, not the whole row: the
      // created location is also an <option> in every row's <select>, so a
      // whole-row hasText filter matches all of them.
      const matchedRow = locRows.filter({
        has: anonPage.locator(".fetch-loc-name", { hasText: matchedLocName }),
      });
      await expect(matchedRow).toHaveCount(1);
      // An auto-matched row pre-selects the existing location and hides the
      // "create new location" fields (toggleNewLocFields).
      await expect(matchedRow.locator("select")).not.toHaveValue("");
      await expect(matchedRow.locator(".fetch-loc-new-fields")).toBeHidden();

      // guardFormSubmit's consumeFormToken enforces a 1s *minimum* age on
      // the token this preview render just issued — a real visitor clears
      // this naturally while reviewing the preview, but goto→submit here
      // can complete in well under a second (same class of gotcha as
      // suggest-wizard.spec.ts / board.spec.ts's #1260 form-token wait).
      await anonPage.waitForTimeout(1100);
      await anonPage.locator('button[type="submit"]').click();
      await anonPage.waitForURL(/\/feeds\/suggest\/done/);

      // Phase 3 (#1336): the done page offers a prefilled "create an
      // account" link carrying the just-submitted org choice + email.
      const accountLink = anonPage.locator('a[href^="/register?"]');
      await expect(accountLink).toBeVisible();
      const href = (await accountLink.getAttribute("href")) ?? "";
      expect(href).toContain(`org_id=${orgId}`);
      expect(href).toContain("reg_type=join_org");
      expect(decodeURIComponent(href)).toContain(submitterEmail);

      // -- An org member (not an admin) can see and approve the suggestion --
      const { context: memberCtx, page: memberPage } = await loginViaApi(
        browser,
        memberEmail,
        memberPassword
      );
      try {
        const suggestionRow = await fetchSuggestionRowByEmail(memberPage, submitterEmail);
        await expect(suggestionRow).toBeVisible();
        await expect(suggestionRow).toContainText(orgName);

        await suggestionRow
          .locator('form[action$="/approve"] button[type="submit"]')
          .click();
        await memberPage.waitForURL(/\/admin\/fetchurl-suggestions/);
      } finally {
        await memberCtx.close();
      }

      // -- Approval created a real fetch source and ran a real import --
      const sourcesResp = await page.request.fetch(`${API_BASE}/api/v1/fetchurl`, {
        headers: { Authorization: `Bearer ${adminToken}` },
      });
      const sources = await sourcesResp.json();
      const createdSource = Array.isArray(sources)
        ? sources.find((s: any) => s.url === feedURL)
        : undefined;
      expect(
        createdSource,
        "approval must create a fetch_sources row for the suggested URL"
      ).toBeTruthy();
      expect(createdSource.organization_id).toBe(orgId);

      const eventsResp = await page.request.fetch(
        `${API_BASE}/api/v1/events?limit=1000&include_past=true&organization_id=${orgId}`,
        { headers: { Authorization: `Bearer ${adminToken}` } }
      );
      const events = await eventsResp.json();
      expect(
        Array.isArray(events) && events.length > 0,
        "approval must trigger a real, immediate import of the feed's events"
      ).toBe(true);
    } finally {
      await anonCtx.close();
    }
  });

  test("new-org suggestion: an unrelated member is forbidden, only an admin can approve", async ({
    page,
    browser,
  }) => {
    const adminToken = await getTokenFromCookie(page);

    const orgName = unique("E2E FeedSuggest NewOrg");
    const orgActorName = `e2efeedsuggest${Date.now()}`;
    const submitterEmail = `e2e-feedsuggest-neworg-${Date.now()}@example.com`;
    const feedURL = testFeedURL();

    // An org member with no relationship at all to the proposed org —
    // canReviewFetchSuggestion treats a new-org proposal as admin-only
    // (no membership exists yet to check against), so this user must be
    // denied regardless of which org (if any) they belong to.
    const outsiderEmail = `e2e-feedsuggest-outsider-${Date.now()}@example.com`;
    const outsiderPassword = "Ln4tRp8wQz2mXa7C";
    createUser(outsiderEmail, outsiderPassword, "user");

    const anonCtx = await browser.newContext({
      storageState: { cookies: [], origins: [] },
      userAgent: `Mozilla/5.0 (E2E suggest-feed new-org test ${Date.now()})`,
    });
    const anonPage = await anonCtx.newPage();
    try {
      await anonPage.goto("/feeds/suggest");
      await anonPage.fill("#field-email", submitterEmail);
      // Switch to the "new organization" tab (second tab-bar button).
      await anonPage.locator(".tab-bar .tab-btn").nth(1).click();
      await anonPage.fill("#field-org-name", orgName);
      await anonPage.fill("#field-org-actor-name", orgActorName);
      await anonPage.fill("#field-feed-url", feedURL);
      await anonPage.locator('button[type="submit"]').click();

      await expect(anonPage.locator(".fetch-preview-box")).toBeVisible();

      await anonPage.waitForTimeout(1100);
      await anonPage.locator('button[type="submit"]').click();
      await anonPage.waitForURL(/\/feeds\/suggest\/done/);

      // Phase 3 account-link offer applies to the new-org case too.
      const accountLink = anonPage.locator('a[href^="/register?"]');
      await expect(accountLink).toBeVisible();
      const href = (await accountLink.getAttribute("href")) ?? "";
      expect(href).toContain("reg_type=new_org");
      // URLSearchParams-style encoding writes spaces as "+", which
      // decodeURIComponent leaves as a literal "+".
      expect(decodeURIComponent(href).replace(/\+/g, " ")).toContain(orgName);

      const suggestionRow = await fetchSuggestionRowByEmail(page, submitterEmail);
      await expect(suggestionRow).toBeVisible();
      const suggestionId = await suggestionRow
        .locator('form[action$="/approve"]')
        .getAttribute("action")
        .then((action) => {
          const m = action?.match(/\/fetchurl-suggestions\/(\d+)\/approve/);
          if (!m) throw new Error(`could not parse suggestion id from action=${action}`);
          return m[1];
        });

      // -- The outsider is forbidden at the API layer --
      const { context: outsiderCtx, token: outsiderToken } = await loginViaApi(
        browser,
        outsiderEmail,
        outsiderPassword
      );
      try {
        const deniedResp = await page.request.fetch(
          `${API_BASE}/api/v1/fetchurl-suggestions/${suggestionId}/approve`,
          { method: "POST", headers: { Authorization: `Bearer ${outsiderToken}` } }
        );
        expect(
          deniedResp.status(),
          "a non-admin with no relation to a *new*-org proposal must be forbidden"
        ).toBe(403);
      } finally {
        await outsiderCtx.close();
      }

      // The suggestion must still be pending after the denied attempt.
      const stillPendingRow = await fetchSuggestionRowByEmail(page, submitterEmail);
      await expect(stillPendingRow).toBeVisible();

      // -- An admin can approve it, creating the org for real --
      const approveResp = await page.request.fetch(
        `${WEB_BASE}/admin/fetchurl-suggestions/${suggestionId}/approve`,
        { method: "POST" }
      );
      expect([200, 302, 303].includes(approveResp.status())).toBe(true);

      const orgsResp = await page.request.fetch(
        `${API_BASE}/api/v1/organizations?name=${encodeURIComponent(orgName)}&limit=10`,
        { headers: { Authorization: `Bearer ${adminToken}` } }
      );
      const orgs = await orgsResp.json();
      const createdOrg = Array.isArray(orgs) ? orgs.find((o: any) => o.name === orgName) : undefined;
      expect(createdOrg, "admin approval must create the proposed organization").toBeTruthy();

      const sourcesResp = await page.request.fetch(`${API_BASE}/api/v1/fetchurl`, {
        headers: { Authorization: `Bearer ${adminToken}` },
      });
      const sources = await sourcesResp.json();
      const createdSource = Array.isArray(sources)
        ? sources.find((s: any) => s.url === feedURL)
        : undefined;
      expect(createdSource, "approval must create a fetch_sources row").toBeTruthy();
      expect(createdSource.organization_id).toBe(createdOrg.id);
    } finally {
      await anonCtx.close();
    }
  });
});
