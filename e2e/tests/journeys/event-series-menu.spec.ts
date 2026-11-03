import { test, expect } from "../../helpers/fixtures";
import { fullSeed, getTokenFromCookie, apiPost } from "../../helpers/seed";

const API_BASE = process.env.API_URL ?? "http://localhost:8000";
import { isPhone } from "../../helpers/nav";
import { AUTH_FILE } from "../../helpers/auth";

// #1776: on a phone, the series bar ("← prev / next →") on an event page used
// to paint over the header's user menu, so the visitor's "Anmelden" entry
// could not be clicked. Both are checked at the element level: the login link
// must be the topmost element at its own centre point.

test.describe("Event in a series: user menu stays usable on phones (#1776)", () => {
  // The menu must show the anonymous "Anmelden" entry, so visit as a visitor.
  test.use({ storageState: { cookies: [], origins: [] } });

  let seriesEventId: number;

  test.beforeAll(async ({ browser }) => {
    const context = await browser.newContext({ storageState: AUTH_FILE });
    const setupPage = await context.newPage();
    await fullSeed(setupPage);
    const token = await getTokenFromCookie(setupPage);
    // Three fortnightly instances: the middle one has both a previous and a
    // next link, which is the layout where the next link sits under the login.
    const start = new Date();
    start.setDate(start.getDate() + 30);
    const startDate = start.toISOString().slice(0, 10);
    const series = await apiPost(setupPage, "/api/v1/series", token, {
      title: `E2E Menu Series ${Date.now()}`,
      // No organization: an org or series image would add a hero block above
      // the series bar and push it out from under the header (where the bug is).
      default_start_time: "19:00",
      default_end_time: "21:00",
      start_date: startDate,
      recurrence: "biweekly",
      occurrences: 3,
    });
    // The create response doesn't list the instances; read the series back.
    const detail = await setupPage.request.fetch(`${API_BASE}/api/v1/series/${series.id}`, {
      headers: { Authorization: `Bearer ${token}` },
    });
    const events: { id: number; start_time: string }[] = (await detail.json()).events ?? [];
    // Series instances created through the API start unpublished, and anonymous
    // visitors get a 404 for them; publish both so the public page renders.
    for (const e of events) {
      const pub = await setupPage.request.fetch(`${API_BASE}/api/v1/events/${e.id}/publish`, {
        method: "POST",
        headers: { Authorization: `Bearer ${token}` },
      });
      expect(pub.ok(), `publish event ${e.id}: ${pub.status()}`).toBe(true);
    }
    expect(events.length).toBe(3);
    const sorted = [...events].sort((a, b) => a.start_time.localeCompare(b.start_time));
    seriesEventId = sorted[1].id;
    await context.close();
  });

  test("login entry in the user menu is not covered by the series bar", async ({ page }) => {
    test.skip(!isPhone(page), "phone-only: the series bar only overlaps the header on small screens");
    await page.goto(`/events/${seriesEventId}`);
    await expect(page.locator(".series-nav")).toBeVisible();
    await page.locator(".nav-user-btn").click();
    const login = page.locator('#user-dropdown a[href="/login"]');
    await expect(login).toBeVisible();
    const covered = await login.evaluate((el) => {
      const r = el.getBoundingClientRect();
      const top = document.elementFromPoint(r.left + r.width / 2, r.top + r.height / 2);
      return !el.contains(top);
    });
    expect(covered, "the series bar paints over the Anmelden link").toBe(false);
  });
});
