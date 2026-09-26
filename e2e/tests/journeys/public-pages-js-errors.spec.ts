import { test, expect } from "@playwright/test";

// #1385: base.js is loaded with `defer` (#1326), so an inline script that
// calls one of its functions at plain top-level parse time throws
// "X is not defined" and silently disables whatever it was meant to set up
// (the suggest wizard's address search lost its geo_token that way and
// answered 403 to every anonymous visitor). Load the public pages as an
// anonymous visitor and fail on any uncaught page error.
const PATHS = [
  "/",
  "/events/suggest",
  "/feeds/suggest",
  "/search",
  "/board",
  "/organizations",
  "/musicians",
  "/instructors",
  "/login",
  "/register",
  "/help",
  "/cities",
  "/festivals",
];

test.describe("Public pages load without JavaScript errors (#1385)", () => {
  for (const path of PATHS) {
    test(`no page errors on ${path}`, async ({ browser }) => {
      const context = await browser.newContext({
        storageState: { cookies: [], origins: [] },
      });
      const page = await context.newPage();
      const errors: string[] = [];
      page.on("pageerror", (e) => errors.push(e.message.slice(0, 160)));
      await page.goto(path, { waitUntil: "load" });
      await page.waitForTimeout(800);
      expect(errors, errors.join("\n")).toEqual([]);
      await context.close();
    });
  }

  test("an event page has no page errors", async ({ browser }) => {
    const context = await browser.newContext({
      storageState: { cookies: [], origins: [] },
    });
    const page = await context.newPage();
    await page.goto("/", { waitUntil: "load" });
    const href = await page.locator('a[href^="/events/"]').first().getAttribute("href");
    expect(href).toBeTruthy();
    const errors: string[] = [];
    page.on("pageerror", (e) => errors.push(e.message.slice(0, 160)));
    await page.goto(href!, { waitUntil: "load" });
    await page.waitForTimeout(800);
    expect(errors, errors.join("\n")).toEqual([]);
    await context.close();
  });

  test("the suggest wizard gets a geo token for its address search", async ({ browser }) => {
    const context = await browser.newContext({
      storageState: { cookies: [], origins: [] },
    });
    const page = await context.newPage();
    await page.goto("/events/suggest", { waitUntil: "load" });
    await page.waitForTimeout(800);
    const len = await page.evaluate(() =>
      typeof (window as any)._geoToken === "string" ? (window as any)._geoToken.length : -1
    );
    expect(len).toBeGreaterThan(0);
    await context.close();
  });
});
