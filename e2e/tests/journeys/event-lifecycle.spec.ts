import { test, expect } from "../../helpers/fixtures";
import { Page } from "@playwright/test";
import { fullSeed, SeedResult, getTokenFromCookie } from "../../helpers/seed";
import { AUTH_FILE } from "../../helpers/auth";
import { goToAllView, loadAllRows } from "../../helpers/nav";
import {
  randomFutureDate,
  titleWithDate,
  isoDate,
  hhmm,
  EVENT_DATE_MIN_DAYS,
  EVENT_DATE_MAX_DAYS,
} from "../../fixtures/data";

const API_BASE = process.env.API_URL ?? "http://localhost:8000";

let seed: SeedResult;

async function authedGet(page: Page, token: string, path: string) {
  return page.request.fetch(`${API_BASE}${path}`, {
    headers: { Authorization: `Bearer ${token}` },
  });
}

// The admin create form redirects to /admin/events/{id}/edit on success
// (adminEventCreateHandler's default intent) — pull the new id from there
// rather than looking events up by title.
function idFromEditURL(url: string): number {
  const m = url.match(/\/admin\/events\/(\d+)\/edit/);
  if (!m) throw new Error(`not an event edit URL: ${url}`);
  return parseInt(m[1], 10);
}

function inTable(page: Page, title: string) {
  return page.locator(".event-tile, tr.event-row").filter({ hasText: title });
}

// Both the desktop table row and the mobile tile for a given title can be
// present in the DOM at once (only one is CSS-hidden depending on
// viewport — see helpers/nav.ts's clickEventInTable), so a combined
// selector's toBeVisible() hits a strict-mode violation once the event
// actually exists. Check whichever variant is actually on screen instead;
// inTable()'s toHaveCount(0) above is fine as-is since an absent event
// matches neither selector regardless of viewport.
async function expectVisibleInTable(page: Page, title: string) {
  const tile = page.locator(".event-tile").filter({ hasText: title }).first();
  if (await tile.isVisible().catch(() => false)) {
    await expect(tile).toBeVisible();
  } else {
    await expect(
      page.locator("tr.event-row").filter({ hasText: title }).first()
    ).toBeVisible();
  }
}

test.describe("Admin: event lifecycle", () => {
  test.beforeAll(async ({ browser }) => {
    const context = await browser.newContext({ storageState: AUTH_FILE });
    const setupPage = await context.newPage();
    seed = await fullSeed(setupPage);
    await context.close();
  });

  test("create as draft, edit, publish, merge, and cancel", async ({
    page,
  }) => {
    const token = await getTokenFromCookie(page);
    const eventDate = randomFutureDate(EVENT_DATE_MIN_DAYS, EVENT_DATE_MAX_DAYS);
    const titleA = titleWithDate("E2E Lifecycle Bal", eventDate);

    // -- A: create as a draft, using only strict event fields (title, date,
    //    time — no org, no location, no timetable). Unchecking "Published"
    //    must actually keep it unpublished (#1273: the checkbox used to have
    //    no effect on create). --
    await page.goto("/admin/events/new");
    await expect(page.locator("#evt-form")).toBeVisible();
    await page.fill('input[name="title"]', titleA);
    await page.fill("#date", isoDate(eventDate));
    await page.fill('input[name="start_time"]', hhmm(20, 0));
    await page.fill('input[name="end_time"]', hhmm(22, 0));
    await page.locator('input[name="is_published"]').uncheck();
    await page.locator("#save-btn").click();
    await page.waitForURL(/\/admin\/events\/\d+\/edit/);
    const idA = idFromEditURL(page.url());

    // A draft must not be visible anywhere public: not via a single-event
    // fetch (the same is_published gate event.html/ics-export/syndication
    // all share, #1272), and not on the public index...
    const draftResp = await page.request.fetch(`${API_BASE}/api/v1/events/${idA}`);
    expect(draftResp.status()).toBe(404);
    await page.goto("/");
    await goToAllView(page);
    await loadAllRows(page);
    await expect(inTable(page, titleA)).toHaveCount(0);
    // ...only reachable through the admin "unpublished" filter.
    await page.goto("/admin/events?unpublished=1&include_past=1");
    await expect(page.locator(`tr[data-evt-id="${idA}"]`)).toBeVisible();

    // -- Edit A: the form must still carry the values just entered, then
    //    publish it. --
    await page.goto(`/admin/events/${idA}/edit`);
    await expect(page.locator('input[name="title"]')).toHaveValue(titleA);
    await expect(page.locator('input[name="start_time"]')).toHaveValue(
      hhmm(20, 0)
    );
    await page.locator('input[name="is_published"]').check();
    await page.locator("#save-btn").click();
    await page.waitForURL(`**/admin/events/${idA}/edit`);

    // Now published: visible both via a bare fetch and on the public index.
    await expect
      .poll(
        async () =>
          (await page.request.fetch(`${API_BASE}/api/v1/events/${idA}`)).status(),
        { message: "event should become publicly visible after publishing" }
      )
      .toBe(200);
    await page.goto("/");
    await goToAllView(page);
    await loadAllRows(page);
    await expectVisibleInTable(page, titleA);

    // -- B: a second event at the same date/hour as A, deliberately with no
    //    org and no location — a plausible accidental duplicate an admin
    //    would want to merge by hand, but not one dansal's own dedup tiers
    //    would ever catch automatically (tier 3 needs a shared location_id
    //    > 0; tier 4 needs *both* sides at location_id = 0 — A has a
    //    location_id of its own here, so it can never match B via either
    //    tier, whatever the titles are). --
    const titleB = titleWithDate("E2E Lifecycle Bal Dup", eventDate);
    await page.goto("/admin/events/new");
    await page.fill('input[name="title"]', titleB);
    await page.fill("#date", isoDate(eventDate));
    await page.fill('input[name="start_time"]', hhmm(20, 0));
    await page.fill('input[name="end_time"]', hhmm(22, 0));
    await page.locator("#save-btn").click();
    await page.waitForURL(/\/admin\/events\/\d+\/edit/);
    const idB = idFromEditURL(page.url());

    // -- Both visible on /admin/events, then merged via the multi-select
    //    tool. A is published by now and B defaults to published too (its
    //    form was never touched), so the earlier "unpublished" filter no
    //    longer applies — narrow by date instead, since both share one.
    const eventDateISO = isoDate(eventDate);
    await page.goto(
      `/admin/events?date_from=${eventDateISO}&date_to=${eventDateISO}`
    );
    await expect(page.locator(`tr[data-evt-id="${idA}"]`)).toBeVisible();
    await expect(page.locator(`tr[data-evt-id="${idB}"]`)).toBeVisible();

    // The .event-cb checkboxes are hidden on mobile (shown only in touch
    // multi-select mode), so set them via JS and dispatch change — same
    // pattern locations.spec.ts uses for its own merge tool, and it still
    // runs the page's updateCount() that enables the merge button.
    for (const id of [idA, idB]) {
      await page
        .locator(`tr[data-evt-id="${id}"] .event-cb`)
        .evaluate((el) => {
          (el as HTMLInputElement).checked = true;
          el.dispatchEvent(new Event("change", { bubbles: true }));
        });
    }
    // On mobile the bulk-actions bar (including .btn-merge) lives in a
    // drawer that's CSS-hidden until body.actions-open — which the page
    // only ever sets from a real long-press gesture on a row
    // (enterMultiSelect(), wired to touchstart+setTimeout). Reproducing an
    // actual long-press is unnecessary to test the merge tool itself: apply
    // the same end state directly, same spirit as the .event-cb checkboxes
    // above.
    await page.evaluate(() => {
      document.body.classList.add("ms-active", "actions-open");
      const btn = document.getElementById("mt-actions-btn");
      if (btn) (btn as HTMLButtonElement).hidden = false;
    });
    const mergeBtn = page.locator(".btn-merge");
    await expect(mergeBtn).toBeEnabled();
    page.once("dialog", (d) => d.accept());
    await mergeBtn.click();
    await page.waitForTimeout(1500);

    const [respA, respB] = await Promise.all([
      authedGet(page, token, `/api/v1/events/${idA}`),
      authedGet(page, token, `/api/v1/events/${idB}`),
    ]);
    expect(
      [respA.status(), respB.status()].sort(),
      "merge must leave exactly one of A/B alive (200) and drop the other (404)"
    ).toEqual([200, 404]);
    const survivorId = respA.status() === 200 ? idA : idB;

    // -- Cancel the survivor from the events list; the public event page
    //    must then render the cancelled state. --
    await page.goto(
      `/admin/events?date_from=${eventDateISO}&date_to=${eventDateISO}`
    );
    const cancelForm = page.locator(
      `tr[data-evt-id="${survivorId}"] form[action*="/cancel"]`
    );
    page.once("dialog", (d) => d.accept());
    await cancelForm.locator("button").click();
    await page.waitForTimeout(1500);

    await page.goto(`/events/${survivorId}`);
    await expect(page.locator(".badge.cancelled")).toBeVisible();
    const finalResp = await authedGet(page, token, `/api/v1/events/${survivorId}`);
    const finalJson = await finalResp.json();
    expect(finalJson.is_cancelled).toBe(true);
  });
});
