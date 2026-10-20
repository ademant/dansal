import { test, expect } from "../../helpers/fixtures";
import { fullSeed, SeedResult } from "../../helpers/seed";
import { AUTH_FILE } from "../../helpers/auth";
import { EVENT_HREF_PREFIX } from "../../fixtures/data";

// Watchlist (#1554): a visitor stars an event, sees it on /watchlist, removes
// it, and the header star follows. The list lives in localStorage, so these
// tests run as an anonymous visitor (no session cookies).

let seed: SeedResult;

test.describe("Watchlist (#1554)", () => {
  test.use({ storageState: { cookies: [], origins: [] } });

  test.beforeAll(async ({ browser }) => {
    const context = await browser.newContext({ storageState: AUTH_FILE });
    const setupPage = await context.newPage();
    seed = await fullSeed(setupPage);
    await context.close();
  });

  test("star an event, see it on the watchlist, remove it", async ({ page }) => {
    const id = seed.eventIds[0];
    await page.goto(`${EVENT_HREF_PREFIX}${id}`);

    const headerStar = page.locator("#wl-header-star");
    await expect(headerStar).toHaveText("☆");

    const eventStar = page.locator(`[data-wl-star="${id}"]`);
    await eventStar.click();
    await expect(eventStar).toHaveAttribute("aria-pressed", "true");
    await expect(headerStar).toHaveText("★");

    const stored = await page.evaluate(() => localStorage.getItem("dansal_watchlist"));
    expect(JSON.parse(stored!)).toEqual({ v: 1, ids: [id] });

    await page.goto("/watchlist");
    const row = page.locator("#wl-list li.wl-row").filter({ hasText: seed.eventTitles[0] });
    await expect(row).toBeVisible();

    await row.locator("button.wl-remove").click();
    await expect(page.locator("#wl-empty")).toBeVisible();
    await expect(page.locator("#wl-list li.wl-row")).toHaveCount(0);
    await expect(headerStar).toHaveText("☆");
  });

  test("ids the server reports as missing are dropped from the list", async ({ page }) => {
    const id = seed.eventIds[0];
    await page.goto(`${EVENT_HREF_PREFIX}${id}`);
    await page.evaluate((stored) => {
      localStorage.setItem("dansal_watchlist", JSON.stringify({ v: 1, ids: stored }));
    }, [id, 999999]);

    await page.goto("/watchlist");
    await expect(page.locator("#wl-list li.wl-row").filter({ hasText: seed.eventTitles[0] })).toBeVisible();
    await expect
      .poll(() => page.evaluate(() => localStorage.getItem("dansal_watchlist")))
      .toBe(JSON.stringify({ v: 1, ids: [id] }));
  });
});
