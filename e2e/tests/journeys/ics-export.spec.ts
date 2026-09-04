import { test, expect } from "../../helpers/fixtures";
import { fullSeed, SeedResult } from "../../helpers/seed";
import { AUTH_FILE } from "../../helpers/auth";
import { EVENT_HREF_PREFIX } from "../../fixtures/data";

let seed: SeedResult;

test.describe("ICS export", () => {
  test.beforeAll(async ({ browser }) => {
    const context = await browser.newContext({ storageState: AUTH_FILE });
    const setupPage = await context.newPage();
    seed = await fullSeed(setupPage);
    await context.close();
  });

  test("event .ics link downloads valid VCALENDAR", async ({ page, metrics }) => {
    const start = Date.now();
    const resp = await page.request.fetch(
      `${EVENT_HREF_PREFIX}${seed.eventIds[0]}.ics`
    );
    expect(resp.status()).toBe(200);
    const body = await resp.text();
    expect(body).toContain("BEGIN:VCALENDAR");
    expect(body).toContain("BEGIN:VEVENT");
    expect(body).toContain(seed.eventTitles[0]);
    // ICS should be compact text, not huge
    expect(body.length).toBeLessThan(100_000);
    await metrics.collect("ics_download");
  });

  test("global feed .ics contains events", async ({ page }) => {
    const resp = await page.request.fetch("/feed/events.ical");
    expect(resp.status()).toBe(200);
    const body = await resp.text();
    expect(body).toContain("BEGIN:VCALENDAR");
    expect(body).toContain("BEGIN:VEVENT");
  });
});
