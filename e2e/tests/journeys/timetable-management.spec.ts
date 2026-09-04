import { test, expect, gotoAndMeasure } from "../../helpers/fixtures";
import { fullSeed, SeedResult, getTokenFromCookie } from "../../helpers/seed";
import { AUTH_FILE } from "../../helpers/auth";
import { EVENT_HREF_PREFIX } from "../../fixtures/data";

const API_BASE = process.env.API_URL ?? "http://localhost:8000";

let seed: SeedResult;

test.describe("Admin: manage timetable", () => {
  test.beforeAll(async ({ browser }) => {
    const context = await browser.newContext({ storageState: AUTH_FILE });
    const setupPage = await context.newPage();
    seed = await fullSeed(setupPage);
    await context.close();
  });

  // `page` loads pre-authenticated as admin via playwright.config.ts's
  // use.storageState (#1252) — just read the token off its cookies.
  test("timetable API add and replace entries", async ({ page, metrics }) => {
    const token = await getTokenFromCookie(page);
    const eventId = seed.eventIds[0];

    // Verify existing entries from seed. There is no GET .../timetable
    // route (only POST/PUT/DELETE) — the timetable is read via the
    // `timetable` field embedded in the event itself.
    const getResp = await page.request.fetch(
      `${API_BASE}/api/v1/events/${eventId}`
    );
    const existingEvent = await getResp.json();
    expect(existingEvent.timetable.length).toBeGreaterThanOrEqual(3);

    // Replace timetable
    const newEntries = [
      {
        start_time: "20:00",
        end_time: "20:30",
        title: "Ouverture",
        entry_type: "bal",
      },
      {
        start_time: "20:30",
        end_time: "21:00",
        title: "Cours découverte",
        entry_type: "workshop",
      },
      {
        start_time: "21:00",
        end_time: "23:30",
        title: "Grand Bal",
        entry_type: "bal",
      },
    ];

    const putResp = await page.request.fetch(
      `${API_BASE}/api/v1/events/${eventId}/timetable`,
      {
        method: "PUT",
        headers: {
          "Content-Type": "application/json",
          Authorization: `Bearer ${token}`,
        },
        data: JSON.stringify(newEntries),
      }
    );
    expect(putResp.status()).toBe(200);

    // Verify replacement
    const verifyResp = await page.request.fetch(
      `${API_BASE}/api/v1/events/${eventId}`
    );
    const verifiedEvent = await verifyResp.json();
    const verified = verifiedEvent.timetable;
    expect(verified.length).toBe(3);
    expect(verified[0].title).toBe("Ouverture");
    expect(verified[2].title).toBe("Grand Bal");
    await metrics.collect("timetable_api_replace");
  });

  test("timetable renders on public event page", async ({ page, metrics }) => {
    const m = await gotoAndMeasure(
      page,
      `${EVENT_HREF_PREFIX}${seed.eventIds[0]}`,
      metrics,
      "timetable_public_render"
    );
    const ttRows = page.locator(".tt-row");
    const count = await ttRows.count();
    expect(count).toBe(3);
    await expect(ttRows.nth(0)).toContainText("Ouverture");
    await expect(ttRows.nth(2)).toContainText("Grand Bal");
  });
});
