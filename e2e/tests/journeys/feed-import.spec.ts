import { test, expect } from "../../helpers/fixtures";
import { Page } from "@playwright/test";
import { fullSeed, SeedResult, getTokenFromCookie } from "../../helpers/seed";
import { AUTH_FILE } from "../../helpers/auth";

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

function idFromEditURL(url: string): number {
  const m = url.match(/\/admin\/events\/(\d+)\/edit/);
  if (!m) throw new Error(`not an event edit URL: ${url}`);
  return parseInt(m[1], 10);
}

// dansal_web caches GetOrganizations/GetLocations for admin pickers (#1276)
// and only its own create handlers invalidate that cache — an org/location
// created by POSTing straight to the API server stays invisible to the
// import preview page's org/location dropdowns for up to that cache's TTL,
// so go through the real admin forms instead (same reasoning as
// event-org-location.spec.ts / series-lifecycle.spec.ts).
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

function icsTimestamp(d: Date): string {
  return d.toISOString().replace(/[-:]/g, "").replace(/\.\d{3}Z$/, "Z");
}

function vevent(opts: {
  uid: string;
  summary: string;
  location: string;
  start: Date;
  end: Date;
  description?: string;
}): string {
  const lines = [
    "BEGIN:VEVENT",
    `UID:${opts.uid}`,
    `DTSTAMP:${icsTimestamp(new Date())}`,
    `DTSTART:${icsTimestamp(opts.start)}`,
    `DTEND:${icsTimestamp(opts.end)}`,
    `SUMMARY:${opts.summary}`,
    `LOCATION:${opts.location}`,
  ];
  if (opts.description) lines.push(`DESCRIPTION:${opts.description}`);
  lines.push("END:VEVENT");
  return lines.join("\r\n");
}

function ics(vevents: string[]): string {
  return [
    "BEGIN:VCALENDAR",
    "VERSION:2.0",
    "PRODID:-//dansal e2e//feed-import//EN",
    ...vevents,
    "END:VCALENDAR",
  ].join("\r\n");
}

async function uploadFeed(page: Page, content: string, filename: string): Promise<void> {
  await page.goto("/admin/events/import");
  await page.locator("#file").setInputFiles({
    name: filename,
    mimeType: "text/calendar",
    buffer: Buffer.from(content, "utf-8"),
  });
  await page.selectOption("#feed-type", "ical");
  await page.locator('form.import-form button[type="submit"]').click();
}

test.describe("Admin: feed import, preview, and duplicate detection", () => {
  test.beforeAll(async ({ browser }) => {
    const context = await browser.newContext({ storageState: AUTH_FILE });
    const setupPage = await context.newPage();
    seed = await fullSeed(setupPage);
    await context.close();
  });

  test("new event imports cleanly, a near-duplicate is flagged and merged, and a manually mapped location persists as an alias", async ({
    page,
  }) => {
    const token = await getTokenFromCookie(page);

    const orgName = unique("E2E Import Org");
    const orgId = await createOrg(page, orgName);
    const l1Name = unique("E2E Import Loc1");
    const l1Id = await createLocation(page, l1Name);
    const l2Name = unique("E2E Import Loc2");
    const l2Id = await createLocation(page, l2Name);

    // Every timestamp is UTC and computed directly, sidestepping any
    // Berlin-local conversion question entirely — only relative spacing
    // (within/outside dedup's ±3h tier-3 window) matters here.
    const now = Date.now();
    const day = 24 * 60 * 60 * 1000;
    const baseStart = new Date(now + 20 * day);
    const baseEnd = new Date(baseStart.getTime() + 2 * 60 * 60 * 1000);

    // -- Seed the "existing" event via a single-VEVENT import: exactly one
    //    event redirects straight to the pre-filled new-event form
    //    (adminImportEventsHandler), so this needs its own upload+save pass
    //    before the real (multi-event) import batch below. --
    const seedTitle = unique("E2E Import Existing");
    const seedUID = unique("e2e-import-seed") + "@dansal-e2e";
    await uploadFeed(
      page,
      ics([
        vevent({ uid: seedUID, summary: seedTitle, location: l1Name, start: baseStart, end: baseEnd }),
      ]),
      "seed.ics"
    );
    // A single-event import renders the pre-filled "new event" form
    // in-process (adminImportEventsHandler) rather than redirecting — the
    // URL stays at /admin/events/import until this form is actually saved.
    await expect(page.locator('input[name="title"]')).toHaveValue(seedTitle);
    await page.locator("#save-btn").click();
    await page.waitForURL(/\/admin\/events\/\d+\/edit/);
    const seedId = idFromEditURL(page.url());

    // -- The real batch: a brand-new event, a near-duplicate of the seed
    //    (same location, start time within the ±3h tier-3 window, but a
    //    different title — tier 3 doesn't check title), and an event whose
    //    feed location name needs manual mapping to an existing location. --
    const newTitle = unique("E2E Import New");
    const newLoc = unique("E2E Import New Loc");
    const newStart = new Date(baseStart.getTime() + 40 * day);

    const dupTitle = unique("E2E Import Updated Title");
    const dupDescription = unique("Updated description from the feed");
    const dupStart = new Date(baseStart.getTime() + 60 * 60 * 1000); // +1h, inside ±3h

    const aliasTitle = unique("E2E Import Alias Event");
    const aliasFeedLocName = unique("E2E Import Feed-Only Loc Name");
    const aliasStart = new Date(baseStart.getTime() + 80 * day);

    await uploadFeed(
      page,
      ics([
        vevent({
          uid: unique("e2e-import-new") + "@dansal-e2e",
          summary: newTitle,
          location: newLoc,
          start: newStart,
          end: new Date(newStart.getTime() + 2 * 60 * 60 * 1000),
        }),
        vevent({
          uid: unique("e2e-import-dup") + "@dansal-e2e",
          summary: dupTitle,
          location: l1Name,
          start: dupStart,
          end: new Date(dupStart.getTime() + 2 * 60 * 60 * 1000),
          description: dupDescription,
        }),
        vevent({
          uid: unique("e2e-import-alias") + "@dansal-e2e",
          summary: aliasTitle,
          location: aliasFeedLocName,
          start: aliasStart,
          end: new Date(aliasStart.getTime() + 2 * 60 * 60 * 1000),
        }),
      ]),
      "batch.ics"
    );

    await expect(page.locator(".event-table tbody tr")).toHaveCount(3);

    const newRow = page.locator("tr").filter({ hasText: newTitle });
    await expect(newRow.locator(".badge.status-new")).toBeVisible();

    const dupRow = page.locator("tr").filter({ hasText: dupTitle });
    await expect(dupRow.locator(".badge.status-updated")).toBeVisible();

    // Manually map the alias event's feed-only location name to L2.
    const aliasLocRow = page.locator("tr").filter({ hasText: aliasFeedLocName });
    await aliasLocRow.locator('select[name^="loc_map_"]').selectOption(String(l2Id));

    await page.selectOption("#org_id", String(orgId));
    // On mobile, scrolling this long table keeps shifting the button under
    // Playwright's pointer mid-retry (bouncing between the table and the
    // wrapping form as the intercepting element) — force the click rather
    // than fighting the animation.
    await page.locator("#import-btn").scrollIntoViewIfNeeded();
    await page.locator("#import-btn").click({ force: true });
    await page.waitForURL(/\/admin\/events\?/);

    // -- New event created cleanly, wired to the selected org. --
    const newEvents = await authedJSON(
      page,
      token,
      `/api/v1/events?limit=1000&include_past=true`
    );
    const createdNew = (Array.isArray(newEvents) ? newEvents : []).find(
      (e: any) => e.title === newTitle
    );
    expect(createdNew).toBeTruthy();
    expect(createdNew.organization_id).toBe(orgId);
    expect(createdNew.is_published).toBe(true);

    // -- The near-duplicate merged into the seed event (tier 3: same
    //    location + start time within 3h, no title check) rather than
    //    creating a second row at L1. The import-confirm path creates
    //    events with no `source` set (the preview→confirm round-trip
    //    doesn't carry the feed's identity through), which takes
    //    insertEvent's plain (non-source) update branch — that one
    //    updates start_time/end_time/description but deliberately leaves
    //    title alone (titles are expected to be hand-edited over an
    //    event's lifetime), so assert on those instead. --
    const seedAfter = await authedJSON(page, token, `/api/v1/events/${seedId}`);
    expect(seedAfter.description).toBe(dupDescription);
    // iCal DTSTART only carries second precision — compare at that
    // granularity rather than exact milliseconds.
    expect(Math.floor(new Date(seedAfter.start_time).getTime() / 1000)).toBe(
      Math.floor(dupStart.getTime() / 1000)
    );
    const l1Events = await authedJSON(
      page,
      token,
      `/api/v1/events?location_id=${l1Id}&include_past=true&limit=1000`
    );
    expect(Array.isArray(l1Events) ? l1Events.length : -1).toBe(1);

    // -- The manually mapped location persisted the feed's name as an
    //    alias on L2, so future imports of the same feed auto-match. --
    const l2After = await authedJSON(page, token, `/api/v1/locations/${l2Id}`);
    expect(l2After.aliases ?? []).toContain(aliasFeedLocName);
  });
});
