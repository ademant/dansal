import { test, expect } from "../../helpers/fixtures";

// The agenda (#1580): anonymous visitors reach /agenda from the hamburger menu,
// and the page renders its title and either day groups or the empty state.

test.describe("Agenda (#1580)", () => {
  test.use({ storageState: { cookies: [], origins: [] } });

  test("the agenda page loads and is reachable from the menu", async ({ page }) => {
    const errors: string[] = [];
    page.on("pageerror", (e) => errors.push(e.message));
    await page.goto("/agenda");
    await expect(page.locator("h1")).toBeVisible();
    const days = page.locator(".agenda-day");
    const empty = page.locator(".agenda-empty");
    await expect(days.first().or(empty)).toBeVisible();
    expect(errors).toEqual([]);

    // The menu is on every page; open it here, on the agenda.
    await page.locator(".nav-hamburger").click();
    await expect(page.locator('#nav-dropdown a[href="/agenda"]')).toBeVisible();
  });
});
