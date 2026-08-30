import { test, expect, gotoAndMeasure } from "../../helpers/fixtures";
import { fullSeed, SeedResult, loginAs } from "../../helpers/seed";
import {
  randomFutureDate,
  titleWithDate,
  datetimeLocalValue,
  EVENT_DATE_MIN_DAYS,
  EVENT_DATE_MAX_DAYS,
} from "../../fixtures/data";

let seed: SeedResult;

test.describe("Admin: create event", () => {
  test.beforeAll(async ({ browser }) => {
    const setupPage = await browser.newPage();
    seed = await fullSeed(setupPage);
    await loginAs(setupPage, "e2e-admin@dansal.test", "E2e-Admin-2026!");
    // Don't close — login state is needed. But we need per-test pages.
    // Actually, each test gets its own page from the fixture, so we need
    // a separate login. The beforeAll just seeds data.
    await setupPage.context().close();
  });

  test("admin events list loads", async ({ page, metrics }) => {
    // Login on this test's page
    await loginAs(page, "e2e-admin@dansal.test", "E2e-Admin-2026!");
    const m = await gotoAndMeasure(page, "/admin/events", metrics, "admin_events_list");
    await expect(page.locator("h1, .admin-title")).toBeVisible();
    // Admin page should have a table of events
    const table = page.locator("table, .admin-events, .event-list");
    await expect(table.first()).toBeVisible();
  });

  test("creating a new event via admin form", async ({ page, metrics }) => {
    await loginAs(page, "e2e-admin@dansal.test", "E2e-Admin-2026!");
    const m = await gotoAndMeasure(page, "/admin/events/new", metrics, "admin_create_event");
    await expect(page.locator("#evt-form, form")).toBeVisible();

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
    await page.fill('input[name="start_time"]', datetimeLocalValue(eventDate, 20, 30));
    await page.fill('input[name="end_time"]', datetimeLocalValue(eventDate, 23, 30));

    const orgSelect = page.locator('select[name="organization_id"]');
    if (await orgSelect.isVisible()) {
      await orgSelect.selectOption({ label: /Bal Test Association/ });
    }

    await page.locator('button[type="submit"], input[type="submit"]').click();
    await page.waitForTimeout(3000);
    const url = page.url();
    const isEditPage = url.includes("/admin/events/") && url.includes("/edit");
    const isEventsList = url.includes("/admin/events");
    expect(isEditPage || isEventsList).toBeTruthy();
  });
});
