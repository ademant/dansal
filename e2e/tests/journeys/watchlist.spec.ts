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
  test("a watchlist file from this instance merges into the list; another instance is refused", async ({ page }) => {
    const id = seed.eventIds[0];
    await page.goto("/watchlist");
    const host = await page.evaluate(() => location.host);
    await page.evaluate(() => localStorage.setItem("dansal_watchlist", JSON.stringify({ v: 1, ids: [] })));

    const file = (instance: string, ids: number[]) => ({
      name: "dansal-watchlist.json",
      mimeType: "application/json",
      buffer: Buffer.from(JSON.stringify({ format: "dansal-watchlist", version: 1, instance, exported_at: "2026-10-20T12:00:00Z", ids })),
    });

    await page.locator("#wl-import").setInputFiles(file(host, [id]));
    await expect
      .poll(() => page.evaluate(() => localStorage.getItem("dansal_watchlist")))
      .toBe(JSON.stringify({ v: 1, ids: [id] }));

    await page.locator("#wl-import").setInputFiles(file("elsewhere.example.test", [424242]));
    await expect(page.locator("#wl-io-msg")).not.toBeEmpty();
    await expect
      .poll(() => page.evaluate(() => localStorage.getItem("dansal_watchlist")))
      .toBe(JSON.stringify({ v: 1, ids: [id] }));
  });
  test("save the list on the server, then load it with the key in another browser", async ({ page, browser }) => {
    const id = seed.eventIds[0];
    await page.goto(`${EVENT_HREF_PREFIX}${id}`);
    await page.locator(`[data-wl-star="${id}"]`).click();

    await page.goto("/watchlist");
    await page.locator(".wl-server button").nth(0).click();
    // The save waits briefly for the form token to age (formguard.go), so
    // poll for the key rather than reading it at once.
    await expect(page.locator("#wl-key-out")).toHaveText(/^[0-9a-f]{32}$/);
    const key = (await page.locator("#wl-key-out").textContent()) ?? "";

    const other = await browser.newContext({ storageState: { cookies: [], origins: [] } });
    const second = await other.newPage();
    await second.goto("/watchlist");
    await second.locator("#wl-key-in").fill(key);
    await second.locator(".wl-server button").nth(1).click();
    await expect(
      second.locator("#wl-list li.wl-row").filter({ hasText: seed.eventTitles[0] })
    ).toBeVisible();
    await other.close();
  });
  // The home page lists only the next 100 upcoming events, and a shared dev
  // instance accumulates more than that, so the seeded event may not be on
  // it. Its own page always shows the star.
  test("starring on the event page fills the star and raises no page errors", async ({ page }) => {
    const errors: string[] = [];
    page.on("pageerror", (e) => errors.push(e.message));
    const id = seed.eventIds[0];
    await page.goto(`/events/${id}`);
    const star = page.locator(`button[data-wl-star="${id}"]`).first();
    await star.click();
    await expect(star).toHaveText("★");
    expect(errors).toEqual([]);
  });
});
