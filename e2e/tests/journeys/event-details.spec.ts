import { test, expect, gotoAndMeasure } from "../../helpers/fixtures";
import { fullSeed, SeedResult } from "../../helpers/seed";
import { EVENT_HREF_PREFIX } from "../../fixtures/data";

let seed: SeedResult;

test.describe("Event details page", () => {
  test.beforeAll(async ({ browser }) => {
    const setupPage = await browser.newPage();
    seed = await fullSeed(setupPage);
    await setupPage.context().close();
  });

  test("shows title, location, and description", async ({ page, metrics }) => {
    const m = await gotoAndMeasure(
      page,
      `${EVENT_HREF_PREFIX}${seed.eventIds[0]}`,
      metrics,
      "event_details_load"
    );
    await expect(page.locator("h1")).toContainText(seed.eventTitles[0]);
    await expect(page.locator(".evt-header")).toBeVisible();
    await expect(page.locator(".col-venue")).toContainText("Testville");
    // Title should be a single h1
    await expect(page.locator("h1")).toHaveCount(1);
    // Structured data present
    const ldJson = page.locator('script[type="application/ld+json"]');
    expect(await ldJson.count()).toBeGreaterThanOrEqual(1);
    expect(m.vitals.fcp).toBeLessThan(5000);
  });

  test("has ICS download link", async ({ page }) => {
    await page.goto(`${EVENT_HREF_PREFIX}${seed.eventIds[0]}`);
    const icsLink = page.locator('a[href$=".ics"]');
    await expect(icsLink.first()).toBeVisible();
  });

  test("description renders HTML content", async ({ page }) => {
    await page.goto(`${EVENT_HREF_PREFIX}${seed.eventIds[0]}`);
    const desc = page.locator(".md-content");
    await expect(desc).toBeVisible();
  });
});
