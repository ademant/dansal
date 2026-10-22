import { test, expect } from "../../helpers/fixtures";

// Homepage map (#1435): a locate control on the map itself, and the last map
// view remembered in this browser. Runs as an anonymous visitor.

test.describe("Homepage map: locate and remembered view (#1435)", () => {
  test.use({ storageState: { cookies: [], origins: [] } });

  test("the locate control is on the map and has an accessible name", async ({ page }) => {
    const errors: string[] = [];
    page.on("pageerror", (e) => errors.push(e.message));
    await page.goto("/");
    const overlay = page.locator("#map-locate-overlay");
    await expect(overlay).toBeVisible();
    await expect(overlay).toHaveAttribute("aria-label", /.+/);
    expect(errors).toEqual([]);
  });

  test("a remembered map view is kept after the page loads", async ({ page }) => {
    const errors: string[] = [];
    page.on("pageerror", (e) => errors.push(e.message));
    await page.addInitScript(() => {
      localStorage.setItem("dansal_map_view", JSON.stringify({ lat: 48.5, lng: 7.5, z: 9 }));
    });
    await page.goto("/");
    await expect(page.locator("#map-container .leaflet-container, #map-container")).toBeVisible();
    // The view is written back on moveend, so the stored view must stay valid.
    await expect
      .poll(() => page.evaluate(() => JSON.parse(localStorage.getItem("dansal_map_view") || "null")?.z))
      .toBe(9);
    expect(errors).toEqual([]);
  });
});
