import { test, expect, gotoAndMeasure } from "../../helpers/fixtures";
import { fullSeed, SeedResult } from "../../helpers/seed";
import { AUTH_FILE } from "../../helpers/auth";
import { EVENT_HREF_PREFIX } from "../../fixtures/data";

let seed: SeedResult;

test.describe("Timetable view", () => {
  test.beforeAll(async ({ browser }) => {
    const context = await browser.newContext({ storageState: AUTH_FILE });
    const setupPage = await context.newPage();
    seed = await fullSeed(setupPage);
    await context.close();
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
    // event.html renders each timetable entry twice — once in the compact
    // list (.tt-title) and once in the detail panel (.tt-panel-title) — so
    // one workshop entry legitimately produces two .tt-badge-ws elements.
    const wsBadge = page.locator(".tt-badge-ws").first();
    await expect(wsBadge).toBeVisible();
  });
});
