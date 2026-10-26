import { test, expect } from "../../helpers/fixtures";

// Phone homepage bottom tab bar (#1613): list first, calendar and map behind
// the tabs, the choice remembered, and the desktop layout left alone. The tab
// bar only shows below 640px, so the phone tests skip on the desktop project.

test.describe("Homepage phone tabs (#1613)", () => {
  test.use({ storageState: { cookies: [], origins: [] } });

  async function isPhoneLayout(page: import("@playwright/test").Page): Promise<boolean> {
    return page.locator("#mtab-list").isVisible().catch(() => false);
  }

  test("the list is the default tab and the map and calendar panels are hidden", { tag: "@smoke" }, async ({ page }) => {
    await page.goto("/");
    if (!(await isPhoneLayout(page))) test.skip(true, "mobile-only: tab bar");
    await expect(page.locator("#home-tabs")).toHaveAttribute("data-mtab", "list");
    await expect(page.locator("#mtab-list")).toHaveAttribute("aria-selected", "true");
    await expect(page.locator("#mpanel-map")).toBeHidden();
    await expect(page.locator("#mpanel-week")).toBeHidden();
  });

  test("the map library loads after the list is shown", async ({ page }) => {
    const requested: string[] = [];
    page.on("request", (r) => {
      if (r.url().includes("/static/leaflet/leaflet.js")) requested.push(r.url());
    });
    await page.goto("/");
    if (!(await isPhoneLayout(page))) test.skip(true, "mobile-only: tab bar");
    await expect.poll(() => requested.length, { timeout: 15000 }).toBeGreaterThan(0);
  });

  test("?view=map opens the map tab and ?week= opens the calendar", async ({ page }) => {
    await page.goto("/?view=map");
    if (!(await isPhoneLayout(page))) test.skip(true, "mobile-only: tab bar");
    await expect(page.locator("#home-tabs")).toHaveAttribute("data-mtab", "map");
    await expect(page.locator("#mpanel-map")).toBeVisible();

    await page.goto("/?week=2030-01-07&day=0");
    await expect(page.locator("#home-tabs")).toHaveAttribute("data-mtab", "week");
    await expect(page.locator(".week-mobile")).toBeVisible();
  });

  test("the last chosen tab is remembered on the next visit", async ({ page }) => {
    await page.goto("/");
    if (!(await isPhoneLayout(page))) test.skip(true, "mobile-only: tab bar");
    await page.locator("#mtab-week").click();
    await expect(page.locator("#home-tabs")).toHaveAttribute("data-mtab", "week");
    await page.goto("/");
    await expect(page.locator("#home-tabs")).toHaveAttribute("data-mtab", "week");
  });

  test("arrow keys and Home/End move between the tabs", async ({ page }) => {
    await page.goto("/");
    if (!(await isPhoneLayout(page))) test.skip(true, "mobile-only: tab bar");
    await page.locator("#mtab-list").focus();
    await page.keyboard.press("ArrowRight");
    await expect(page.locator("#mtab-week")).toHaveAttribute("aria-selected", "true");
    await page.keyboard.press("ArrowRight");
    await expect(page.locator("#mtab-map")).toHaveAttribute("aria-selected", "true");
    await page.keyboard.press("Home");
    await expect(page.locator("#mtab-list")).toHaveAttribute("aria-selected", "true");
    await page.keyboard.press("End");
    await expect(page.locator("#mtab-map")).toHaveAttribute("aria-selected", "true");
  });

  test("the tab bar and the week-table toggle are only where they belong", async ({ page }) => {
    await page.setViewportSize({ width: 1200, height: 800 });
    await page.goto("/");
    await expect(page.locator(".mtabs")).toBeHidden();
    await expect(page.locator("#tmt-all")).toBeVisible();
    await expect(page.locator("#week-mini-cal")).toBeVisible();
  });
});
