/**
 * /search page — filter form scenarios (closes #1267).
 *
 * Covers:
 *   A  Town text filter narrows the visible event list (local-match path).
 *      Map markers stay in sync with the filtered list (#976 regression).
 *   B  Type filter (ball / workshop) hides and restores rows.
 *   C  Date-range picker narrows results to the queried window.
 *   D  Country → Region cascading filter (#1282): selecting a country scopes
 *      results to that country; Region appears only when the selected country
 *      has region data; selecting a region narrows further.
 *      Mutual exclusivity: typing in #sf-town clears the country select, and
 *      selecting a country clears the town input.
 *
 * Geocode-fallback (OSM Nominatim, GEOCODE_MIN_LEN path) is network-dependent
 * and therefore not covered here; A covers the local-match path only.
 *
 * Setup requires fullSeed (published events at LOCATION: France, Testville).
 * A second location — France / Bretagne — is created in beforeAll specifically
 * for scenario D so the Region dropdown appears without modifying the shared
 * LOCATION fixture used by every other spec.
 */
import { test, expect } from "../../helpers/fixtures";
import { Page } from "@playwright/test";
import {
  fullSeed,
  SeedResult,
  getTokenFromCookie,
  apiPost,
} from "../../helpers/seed";
import { AUTH_FILE } from "../../helpers/auth";

const WEB_BASE = process.env.BASE_URL ?? "http://localhost:8080";
const API_BASE = process.env.API_URL ?? "http://localhost:8000";

let seed: SeedResult;
/** Title of the Breton-location event seeded for the country/region test. */
let bretonTitle: string;

// ── Date helpers ──────────────────────────────────────────────────────────────

function addDays(n: number): Date {
  const d = new Date();
  d.setDate(d.getDate() + n);
  return d;
}

function isoDay(d: Date): string {
  const p = (n: number) => String(n).padStart(2, "0");
  return `${d.getFullYear()}-${p(d.getMonth() + 1)}-${p(d.getDate())}`;
}

function isoDateTime(d: Date, hour: number): string {
  return `${isoDay(d)}T${String(hour).padStart(2, "0")}:00:00`;
}

// ── Page helpers ──────────────────────────────────────────────────────────────

/**
 * Open the date-range calendar popup and pick two dates by clicking their
 * grid cells.  Navigates forward month by month until each cell is visible
 * (max 4 advances).  Waits for the /search/results fetch triggered by the
 * second click.
 */
async function setSearchDates(page: Page, from: Date, to: Date): Promise<void> {
  const fromIso = isoDay(from);
  const toIso = isoDay(to);

  await page.click("#sf-date-btn");

  // Navigate to from-date's month
  for (let i = 0; i < 4; i++) {
    if (await page.locator(`#sf-cal-grid td[data-iso="${fromIso}"]`).count()) break;
    await page.click("#sf-cal-next");
  }
  await page.click(`#sf-cal-grid td[data-iso="${fromIso}"]`);

  // After the first click the calendar re-renders with pendingStart set
  // (setTimeout 0 in search.html's picker) — give the browser one tick.
  await page.waitForTimeout(50);

  // Navigate to to-date's month, then click — this closes the popup and
  // triggers fetchResults() whose response we await for synchronisation.
  await Promise.all([
    page.waitForResponse((r) => r.url().includes("/search/results")),
    (async () => {
      for (let i = 0; i < 4; i++) {
        if (await page.locator(`#sf-cal-grid td[data-iso="${toIso}"]`).count()) break;
        await page.click("#sf-cal-next");
      }
      await page.click(`#sf-cal-grid td[data-iso="${toIso}"]`);
    })(),
  ]);
}

/**
 * Wait for the initial fetchResults() to finish and render() to populate
 * #sf-info (always non-empty after render, even with 0 results).
 */
async function waitForResults(page: Page): Promise<void> {
  await expect(page.locator("#sf-info")).not.toBeEmpty({ timeout: 15_000 });
}

// ── Seed ──────────────────────────────────────────────────────────────────────

test.beforeAll(async ({ browser }) => {
  const ctx = await browser.newContext({ storageState: AUTH_FILE });
  const page = await ctx.newPage();
  try {
    seed = await fullSeed(page);
    const token = await getTokenFromCookie(page);

    // Second location: France + region "Bretagne" — gives the country filter
    // a second option in the Region dropdown when "France" is selected, so
    // #sf-region-field becomes visible.  Jitter prevents geohash collisions
    // with the LOCATION fixture's own entries.
    const jitter = () => (Math.random() - 0.5) * 0.5;
    const locData = await apiPost(page, "/api/v1/locations", token, {
      location: "Salle Bretagne Test",
      short_name: "Salle Bretagne",
      town: "Rennes",
      country: "France",
      country_code: "FR",
      region: "Bretagne",
      latitude: 48.1173 + jitter(),
      longitude: -1.6778 + jitter(),
    });
    const bretonLocId: number = locData[0].location.id;

    // A published ball event at the Breton location, 15 days out.
    const eventDate = addDays(15);
    bretonTitle = `Fest-Noz Rennes Test ${Date.now()}`;
    await apiPost(page, "/api/v1/events", token, {
      title: bretonTitle,
      start_time: isoDateTime(eventDate, 20),
      end_time: isoDateTime(eventDate, 23),
      tags: ["bal-folk"],
      organization_id: seed.orgId,
      location_id: bretonLocId,
    });
  } finally {
    await ctx.close();
  }
});

// ── A: Town filter + map sync ─────────────────────────────────────────────────

test("town filter narrows results and clears on reset; map stays in sync [#976]", async ({
  page,
}) => {
  await page.goto(`${WEB_BASE}/search`);

  // Widen the date range to catch seeded events (3–45 days out by default).
  await setSearchDates(page, addDays(0), addDays(60));
  await waitForResults(page);

  // Wait for the Leaflet map to initialise (the deferred <script> must run
  // and attachTileLayer must have been called before we check markers).
  await expect(
    page.locator("#map-container .leaflet-pane")
  ).toBeAttached({ timeout: 10_000 });

  // ── local-match path: "Testville" is in the loaded locs array ──
  await page.fill("#sf-town", "Testville");

  const sfInfo = page.locator("#sf-info");
  // After typing, render() fires synchronously — info updates immediately.
  await expect(sfInfo).toContainText(/^[1-9]/);

  // Seeded events are visible.
  const firstSeedRow = page
    .locator("#sf-event-tbody .event-row")
    .filter({ hasText: seed.eventTitles[0] });
  await expect(firstSeedRow).toBeVisible();

  // ── filter to a nonexistent town → empty state + no map markers ──
  await page.fill("#sf-town", "XxzNoSuchTownZzq");
  await expect(page.locator("#sf-empty-state")).toBeVisible();
  await expect(sfInfo).toContainText("0");

  // #976 regression: markers must be removed from the map when the list is
  // empty, not left over from the previous full-set render.
  await expect(
    page.locator("#map-container .leaflet-marker-icon")
  ).toHaveCount(0);
  await expect(
    page.locator("#map-container .leaflet-marker-cluster")
  ).toHaveCount(0);

  // ── reset restores the full list ──
  await page.click("#sf-reset-btn");
  await expect(page.locator("#sf-event-table")).toBeVisible();
  await expect(sfInfo).toContainText(/^[1-9]/);
});

// ── B: Type filter ────────────────────────────────────────────────────────────

test("type filter hides and restores typed rows", async ({ page }) => {
  await page.goto(`${WEB_BASE}/search`);
  await setSearchDates(page, addDays(0), addDays(60));
  await waitForResults(page);

  // eventTitles[0] = "Bal de Testville …" → tags: [bal-folk] → data-ball="1"
  // eventTitles[1] = "Atelier Bourrée …"  → tags: [dance-workshop] → data-workshop="1"
  const balRow = page
    .locator("#sf-event-tbody .event-row")
    .filter({ hasText: seed.eventTitles[0] });
  const atelierRow = page
    .locator("#sf-event-tbody .event-row")
    .filter({ hasText: seed.eventTitles[1] });

  await expect(balRow).toBeVisible();
  await expect(atelierRow).toBeVisible();

  // Uncheck ball → pure-ball events disappear; workshop events remain.
  await page.locator("#sf-type-ball").uncheck();
  await expect(balRow).toBeHidden();
  await expect(atelierRow).toBeVisible();

  // Re-check ball → bal event reappears.
  await page.locator("#sf-type-ball").check();
  await expect(balRow).toBeVisible();
});

// ── C: Date range ─────────────────────────────────────────────────────────────

test("date range narrows results to the queried window", async ({ page }) => {
  await page.goto(`${WEB_BASE}/search`);

  // Far-future window: no seeded events (3–45 days out) land here.
  await setSearchDates(page, addDays(200), addDays(210));
  await waitForResults(page);

  // Either empty state or a very small count that doesn't include seeded events.
  // We can't guarantee zero (there may be real events on the instance), but
  // none of the seeded events should be visible.
  const balRow = page
    .locator("#sf-event-tbody .event-row")
    .filter({ hasText: seed.eventTitles[0] });
  await expect(balRow).toHaveCount(0);

  // Widen back to include seeded events → they reappear.
  await setSearchDates(page, addDays(0), addDays(60));
  await waitForResults(page);
  await expect(balRow).toBeVisible();
});

// ── D: Country → Region cascade + mutual exclusivity ─────────────────────────

test("country → region cascade narrows results; mutually exclusive with town filter [#1282]", async ({
  page,
}) => {
  await page.goto(`${WEB_BASE}/search`);
  await setSearchDates(page, addDays(0), addDays(60));
  await waitForResults(page);

  // ── Select country ──
  await page.locator("#sf-country").selectOption("France");

  // Town input must be cleared (mutual exclusivity).
  await expect(page.locator("#sf-town")).toHaveValue("");

  // French seeded events must be visible.
  const balRow = page
    .locator("#sf-event-tbody .event-row")
    .filter({ hasText: seed.eventTitles[0] });
  await expect(balRow).toBeVisible();

  // Breton event must also be visible at this level (both are France).
  const bretonRow = page
    .locator("#sf-event-tbody .event-row")
    .filter({ hasText: bretonTitle });
  await expect(bretonRow).toBeVisible();

  // ── Region dropdown appears ──
  // The Breton location has region="Bretagne", so after selecting France the
  // region select should become visible and include that option.
  await expect(page.locator("#sf-region-field")).toBeVisible();
  await expect(
    page.locator('#sf-region option[value="Bretagne"]')
  ).toBeAttached();

  // ── Select region → further narrows ──
  await page.locator("#sf-region").selectOption("Bretagne");

  // Only the Breton event survives (it's the only one with region=Bretagne).
  await expect(bretonRow).toBeVisible();
  // Testville events have no region set → data-region="" → excluded.
  await expect(balRow).toBeHidden();

  // ── Mutual exclusivity: typing in town clears country select ──
  await page.fill("#sf-town", "Testville");
  await expect(page.locator("#sf-country")).toHaveValue("");
  await expect(page.locator("#sf-region-field")).toBeHidden();

  // ── Mutual exclusivity: selecting country clears town ──
  await page.locator("#sf-country").selectOption("France");
  await expect(page.locator("#sf-town")).toHaveValue("");

  // ── Reset clears both selects ──
  await page.click("#sf-reset-btn");
  await expect(page.locator("#sf-country")).toHaveValue("");
  await expect(page.locator("#sf-region-field")).toBeHidden();
});
