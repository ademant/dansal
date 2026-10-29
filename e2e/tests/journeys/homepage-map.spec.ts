import { test, expect, devices } from "@playwright/test";

// #1712: the homepage's inline script runs at parse time, but Leaflet is
// loaded by dansalLoadLeaflet() in base.js, which is deferred. The map must
// still build once base.js has run, on desktop and on a phone that remembers
// the map tab. Fails with "dansalLoadLeaflet is not defined" and no
// .leaflet-container when the call happens too early.
test.describe("Homepage map loads (#1712)", () => {
  test("desktop shows the Leaflet map with no page errors", async ({ browser }) => {
    const context = await browser.newContext({
      viewport: { width: 1280, height: 900 },
      storageState: { cookies: [], origins: [] },
    });
    const page = await context.newPage();
    const errors: string[] = [];
    page.on("pageerror", (e) => errors.push(e.message.slice(0, 160)));
    await page.goto("/", { waitUntil: "load" });
    await expect(page.locator(".leaflet-container")).toBeVisible({ timeout: 15_000 });
    expect(errors, errors.join("\n")).toEqual([]);
    await context.close();
  });

  test("phone with the map tab remembered shows the Leaflet map", async ({ browser }) => {
    const context = await browser.newContext({
      ...devices["Pixel 7"],
      storageState: { cookies: [], origins: [] },
    });
    await context.addInitScript(() => {
      try {
        localStorage.setItem("dansal_home_tab", "map");
      } catch {}
    });
    const page = await context.newPage();
    const errors: string[] = [];
    page.on("pageerror", (e) => errors.push(e.message.slice(0, 160)));
    await page.goto("/", { waitUntil: "load" });
    await expect(page.locator(".leaflet-container")).toBeVisible({ timeout: 15_000 });
    expect(errors, errors.join("\n")).toEqual([]);
    await context.close();
  });
});
