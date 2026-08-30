import { test, expect, gotoAndMeasure } from "../../helpers/fixtures";
import { fullSeed, SeedResult } from "../../helpers/seed";
import {
  loadAllRows,
  clickEventInTable,
  goToAllView,
  getEventRowCount,
  expectEventListVisible,
} from "../../helpers/nav";
import { SEARCH_TERM, ADMIN } from "../../fixtures/data";

let seed: SeedResult;

test.describe("Discover events on index", () => {
  test.beforeAll(async ({ browser }) => {
    const setupPage = await browser.newPage();
    seed = await fullSeed(setupPage);
    await setupPage.context().close();
  });

  test("index loads with event table visible", async ({ page, metrics }) => {
    const m = await gotoAndMeasure(page, "/", metrics, "index_load");
    // The default "week" view only lists whatever the *current* calendar
    // week has (often nothing, since seeded events land 3-45 days out) and
    // renders through a third container (#week-outer/.week-mobile) neither
    // #event-table nor #event-tiles — switch to "all" mode first so this
    // deterministically has the seeded events to find, matching the other
    // tests in this file.
    await goToAllView(page);
    await expectEventListVisible(page);
    const rows = await getEventRowCount(page);
    expect(rows).toBeGreaterThan(0);
    // Vitals are automatically captured — assert reasonable values
    expect(m.navigation.ttfb).toBeLessThan(3000);
    expect(m.dom.nodeCount).toBeGreaterThan(50);
  });

  test("load-more fetches additional events", async ({ page, metrics }) => {
    await page.goto("/?mode=all");
    await metrics.collect("load-more");
    const initial = await getEventRowCount(page);
    const btn = page.locator("#load-more");
    if (await btn.isVisible()) {
      await btn.click();
      await page.waitForTimeout(600);
      const after = await getEventRowCount(page);
      expect(after).toBeGreaterThanOrEqual(initial);
    }
  });

  test("clicking event row opens detail page", async ({ page, metrics }) => {
    await page.goto("/");
    await goToAllView(page);
    await loadAllRows(page);
    await clickEventInTable(page, seed.eventTitles[0]);
    await expect(page.locator("h1")).toContainText(seed.eventTitles[0]);
    await expect(page.locator(".evt-header")).toBeVisible();
    await metrics.collect("click-event-row");
  });
});
