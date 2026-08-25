import { test, expect, gotoAndMeasure } from "../../helpers/fixtures";
import { fullSeed, SeedResult } from "../../helpers/seed";
import { EVENT_HREF_PREFIX } from "../../fixtures/data";

let seed: SeedResult;

test.describe("Timetable view", () => {
  test.beforeAll(async ({ browser }) => {
    const setupPage = await browser.newPage();
    seed = await fullSeed(setupPage);
    await setupPage.context().close();
  });

  test("timetable section shows entries for seeded event", async ({
    page,
    metrics,
  }) => {
    const m = await gotoAndMeasure(
      page,
      `${EVENT_HREF_PREFIX}${seed.eventIds[0]}`,
      metrics,
      "timetable_view"
    );
    const ttRows = page.locator(".tt-row");
    const count = await ttRows.count();
    expect(count).toBeGreaterThanOrEqual(3);
    await expect(ttRows.first()).toContainText("Accueil");
    expect(m.dom.nodeCount).toBeGreaterThan(30);
  });

  test("workshop entries have distinct badge", async ({ page }) => {
    await page.goto(`${EVENT_HREF_PREFIX}${seed.eventIds[0]}`);
    const wsBadge = page.locator(".tt-badge-ws");
    await expect(wsBadge).toBeVisible();
  });
});
