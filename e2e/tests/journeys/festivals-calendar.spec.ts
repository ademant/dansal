/**
 * Festivals page mobile month carousel (#1519): desktop keeps the fixed
 * 12-month grid (#1144/#1161); mobile (<=600px) switches #fest-calendar into
 * the shared carousel mode (#1167, reused from /org/{slug}'s rolling-month
 * calendar via base.js's renderMiniCalendar), opened on the current calendar
 * month rather than always January.
 */
import { test, expect } from "../../helpers/fixtures";
import { apiPost, getTokenFromCookie, seedLocation, seedOrg } from "../../helpers/seed";

function isoDateTime(d: Date, hour: number, minute = 0): string {
  const dd = new Date(d);
  dd.setHours(hour, minute, 0, 0);
  return dd.toISOString().replace(".000Z", "");
}

// Seeds one published, festival-tagged event a few days out (same month or
// the next — either way still well inside the current calendar year), so
// /festivals' default (current-year) view has at least one YearEvents entry
// and actually renders the calendar instead of hiding it (renderMiniCalendar
// hides #fest-calendar entirely when calEvents is empty).
async function seedFestivalEvent(page: import("@playwright/test").Page) {
  const token = await getTokenFromCookie(page);
  const orgId = await seedOrg(page, token);
  const locationId = await seedLocation(page, token);
  const date = new Date();
  date.setDate(date.getDate() + 5);
  const nonce = Math.random().toString(36).slice(2, 8);
  const data = await apiPost(page, "/api/v1/events", token, [
    {
      title: `E2E Festival ${nonce}`,
      description: "Seeded festival event for #1519 calendar carousel spec.",
      start_time: isoDateTime(date, 10, 0),
      end_time: isoDateTime(date, 22, 0),
      tags: ["festival"],
      organization_id: orgId,
      location_id: locationId,
    },
  ]);
  return (Array.isArray(data) ? data[0] : data).id as number;
}

test.describe("Festivals page month calendar (#1519)", () => {
  test("desktop shows the full 12-month grid, no carousel nav", async ({ page }, testInfo) => {
    test.skip(testInfo.project.name !== "desktop", "grid-vs-carousel split is viewport-driven");
    await seedFestivalEvent(page);
    await page.goto("/festivals");

    const wrap = page.locator("#fest-cal-wrap");
    await expect(wrap).toBeVisible();
    await expect(page.locator("#fest-calendar")).not.toHaveClass(/fest-carousel/);
    await expect(page.locator(".fest-month-title")).toHaveCount(12);
    await expect(page.locator("#fest-cal-prev")).toBeHidden();
    await expect(page.locator("#fest-cal-next")).toBeHidden();
  });

  test("mobile shows a one-month carousel opened on the current month; next advances exactly one month", async ({
    page,
  }, testInfo) => {
    test.skip(testInfo.project.name !== "mobile", "grid-vs-carousel split is viewport-driven");
    await seedFestivalEvent(page);
    await page.goto("/festivals");

    const wrap = page.locator("#fest-cal-wrap");
    await expect(wrap).toBeVisible();
    await expect(page.locator("#fest-calendar")).toHaveClass(/fest-carousel/);
    await expect(page.locator("#fest-cal-prev")).toBeVisible();
    await expect(page.locator("#fest-cal-next")).toBeVisible();
    // All 12 months still exist in the DOM (one wide carousel track) — only
    // one is scrolled into view at a time.
    await expect(page.locator(".fest-month-title")).toHaveCount(12);

    const monthIndex = () =>
      page.evaluate(() => {
        const cal = document.getElementById("fest-calendar")!;
        const months = Array.from(cal.querySelectorAll(".fest-month"));
        const step = months[0].getBoundingClientRect().width + 16;
        return Math.round(cal.scrollLeft / step);
      });

    const expectedCurrent = new Date().getMonth(); // 0-based, matches the page's startMonth-1
    await expect.poll(monthIndex).toBe(expectedCurrent);

    await page.locator("#fest-cal-next").click();
    await expect.poll(monthIndex).toBe(expectedCurrent + 1);

    await page.locator("#fest-cal-prev").click();
    await expect.poll(monthIndex).toBe(expectedCurrent);
  });
});
