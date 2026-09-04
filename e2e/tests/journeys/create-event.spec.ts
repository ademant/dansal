import { test, expect, gotoAndMeasure } from "../../helpers/fixtures";
import { fullSeed, SeedResult } from "../../helpers/seed";
import { AUTH_FILE } from "../../helpers/auth";
import {
  randomFutureDate,
  titleWithDate,
  isoDate,
  hhmm,
  EVENT_DATE_MIN_DAYS,
  EVENT_DATE_MAX_DAYS,
} from "../../fixtures/data";

let seed: SeedResult;

test.describe("Admin: create event", () => {
  test.beforeAll(async ({ browser }) => {
    const context = await browser.newContext({ storageState: AUTH_FILE });
    const setupPage = await context.newPage();
    seed = await fullSeed(setupPage);
    await context.close();
  });

  test("admin events list loads", async ({ page, metrics }) => {
    // `page` loads pre-authenticated as admin via playwright.config.ts's
    // use.storageState (#1252) — no explicit login needed here.
    const m = await gotoAndMeasure(page, "/admin/events", metrics, "admin_events_list");
    await expect(page.locator("h1, .admin-title")).toBeVisible();
    // Admin page should have a table of events. Scoped to .event-table:
    // the bare "table" fallback also matches #ae-cal-grid, a hidden
    // date-range-picker calendar table that sits earlier in the DOM.
    await expect(page.locator(".event-table")).toBeVisible();
  });

  test("creating a new event via admin form", async ({ page, metrics }) => {
    const m = await gotoAndMeasure(page, "/admin/events/new", metrics, "admin_create_event");
    // Scoped to #evt-form: the "form" fallback also matches the page nav's
    // own logout form, which is a Playwright strict-mode violation.
    await expect(page.locator("#evt-form")).toBeVisible();

    // Random future date + a date-stamped title (not a fixed one) so
    // repeated runs against the shared/pre-filled dev database each create
    // a distinct, identifiable event instead of colliding on one that
    // dansal's dedup logic would silently merge into a stale prior run.
    const eventDate = randomFutureDate(EVENT_DATE_MIN_DAYS, EVENT_DATE_MAX_DAYS);
    await page.fill('input[name="title"]', titleWithDate("Nouveau Bal E2E", eventDate));
    await page.fill(
      'textarea[name="description"]',
      "Bal créé automatiquement par les tests E2E"
    );
    // The admin event form has separate <input type="date"> and
    // <input type="time"> fields (not a combined datetime-local input) —
    // filling a full datetime string into #start_time/#end_time throws
    // "Malformed value" since those are bare HH:MM time inputs.
    await page.fill("#date", isoDate(eventDate));
    await page.fill('input[name="start_time"]', hhmm(20, 30));
    await page.fill('input[name="end_time"]', hhmm(23, 30));

    const orgSelect = page.locator('select[name="organization_id"]');
    if (await orgSelect.isVisible()) {
      await orgSelect.selectOption({ label: /Bal Test Association/ });
    }

    // Scoped to #save-btn: the generic type="submit" selector also matches
    // the page nav's own Logout button, a Playwright strict-mode violation.
    await page.locator("#save-btn").click();
    await page.waitForTimeout(3000);
    const url = page.url();
    const isEditPage = url.includes("/admin/events/") && url.includes("/edit");
    const isEventsList = url.includes("/admin/events");
    expect(isEditPage || isEventsList).toBeTruthy();
  });
});
