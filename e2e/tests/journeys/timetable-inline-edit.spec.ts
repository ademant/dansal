/**
 * Inline timetable editor on the event edit form.
 *
 * Two regressions reported from a live instance:
 *  1. Changing only a row's type (e.g. workshop -> meal) in the inline editor
 *     was silently dropped: only title/description typing raised the form's
 *     timetable_edited flag, and the server skips ReplaceTimetable without it.
 *  2. Once a single-day event mixed entries with an entry_date (saved from the
 *     dedicated /timetable editor) and without one (inline editor), the dated
 *     one sorted *after* the undated one regardless of start time.
 */
import { test, expect } from "../../helpers/fixtures";
import { getTokenFromCookie } from "../../helpers/seed";
import { randomFutureDate, EVENT_DATE_MIN_DAYS, EVENT_DATE_MAX_DAYS } from "../../fixtures/data";

const API_BASE = process.env.API_URL ?? "http://localhost:8000";

function pad(n: number): string {
  return String(n).padStart(2, "0");
}

async function createEvent(page: import("@playwright/test").Page, token: string, date: Date) {
  const day = `${date.getFullYear()}-${pad(date.getMonth() + 1)}-${pad(date.getDate())}`;
  const resp = await page.request.fetch(`${API_BASE}/api/v1/events`, {
    method: "POST",
    headers: { "Content-Type": "application/json", Authorization: `Bearer ${token}` },
    data: JSON.stringify({
      title: `E2E TT Inline ${Date.now()}`,
      start_time: `${day}T14:00:00`,
      end_time: `${day}T23:00:00`,
    }),
  });
  expect(resp.status()).toBe(201);
  // POST /events answers with an array of created events (it accepts bulk bodies).
  const body = await resp.json();
  const ev = Array.isArray(body) ? body[0] : body;
  return { id: ev.id as number, day };
}

async function putTimetable(
  page: import("@playwright/test").Page,
  token: string,
  eventId: number,
  entries: object[]
) {
  const resp = await page.request.fetch(`${API_BASE}/api/v1/events/${eventId}/timetable`, {
    method: "PUT",
    headers: { "Content-Type": "application/json", Authorization: `Bearer ${token}` },
    data: JSON.stringify(entries),
  });
  expect(resp.status()).toBe(200);
}

async function getTimetable(page: import("@playwright/test").Page, token: string, eventId: number) {
  const resp = await page.request.fetch(`${API_BASE}/api/v1/events/${eventId}`, {
    headers: { Authorization: `Bearer ${token}` },
  });
  return (await resp.json()).timetable as Array<{
    start_time: string;
    entry_type: string;
    title: string;
  }>;
}

test.describe("Inline timetable editor", () => {
  test("a dated entry sorts by start time among undated ones on a single-day event", async ({ page }) => {
    const token = await getTokenFromCookie(page);
    const { id, day } = await createEvent(page, token, randomFutureDate(EVENT_DATE_MIN_DAYS, EVENT_DATE_MAX_DAYS));

    // Later entry has no date (inline-editor style), the earlier one carries the
    // event's date (dedicated-editor style).
    await putTimetable(page, token, id, [
      { start_time: "15:45", end_time: "18:00", title: "Fest Deiz", entry_type: "bal" },
      { start_time: "14:30", end_time: "15:30", title: "Cafe", entry_type: "meal", entry_date: day },
    ]);

    const tt = await getTimetable(page, token, id);
    expect(tt.map((e) => e.title)).toEqual(["Cafe", "Fest Deiz"]);
  });

  test("changing only a row's type in the inline editor is saved", async ({ page }) => {
    const token = await getTokenFromCookie(page);
    const { id } = await createEvent(page, token, randomFutureDate(EVENT_DATE_MIN_DAYS, EVENT_DATE_MAX_DAYS));
    await putTimetable(page, token, id, [
      { start_time: "14:30", end_time: "15:30", title: "Atelier", entry_type: "workshop" },
      { start_time: "15:45", end_time: "18:00", title: "Bal", entry_type: "bal" },
    ]);

    await page.goto(`/admin/events/${id}/edit`);
    const firstType = page.locator("#tt-body .tt-entry").first().locator('select[name="tt_type"]');
    await expect(firstType).toHaveValue("workshop");
    // Touch nothing but the type.
    await firstType.selectOption("meal");
    // The edit must raise the form's flag, otherwise the server ignores the rows.
    await expect(page.locator("#tt-edited")).toHaveValue("1");

    await page.locator("#save-btn").click();

    // The save redirects back to this same /edit URL, so waitForURL can't tell
    // "saved" from "not yet submitted" — poll the API for the outcome instead.
    await expect
      .poll(async () => (await getTimetable(page, token, id)).map((e) => [e.title, e.entry_type]), {
        timeout: 15_000,
      })
      .toEqual([
        ["Atelier", "meal"],
        ["Bal", "bal"],
      ]);
  });
});
