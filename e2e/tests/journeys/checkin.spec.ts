/**
 * The internal-booking door check-in journey (#1382): a visitor books on the
 * event page, verifies by email, and a staff member scans the single-use QR
 * code to mark them attended.
 *
 *  1. Happy path: anonymous visitor submits the event page's booking form,
 *     clicks the emailed /api/v1/bookings/verify/{token} link (visited on the
 *     web as /bookings/verify/{token}), extracts the check-in link, then the
 *     logged-in admin scans it (POST /checkin/{qr_token}). The green
 *     "Checked in" card renders. Scanning the same QR again must be refused
 *     with the red error card — the QR token is single-use (nulled in the
 *     same UPDATE that records the check-in), so the re-scan is a 404.
 *  2. Cancelled refusal: a booking that was verified (has a QR) and then
 *     cancelled by admin must refuse the scan (410 → red card).
 *  3. Not-yet-opened refusal: a verified booking on a far-future event
 *     (check-in window opens only start−120 min) must refuse the scan
 *     (410 → red card).
 *
 * The happy path books through the *web form* exactly once per run (the form
 * is throttled per ip|user-agent, see board.spec.ts) with an anonymous
 * context whose UA is unique per run; the refusals create their bookings
 * straight against the API so the form throttle is never in play.
 */
import { test, expect } from "@playwright/test";
import { clearMailbox, waitForBookingVerifyToken } from "../../helpers/mailbox";
import {
  fullSeed,
  seedLocation,
  apiGet,
  getTokenFromCookie,
  SeedResult,
} from "../../helpers/seed";
import { AUTH_FILE } from "../../helpers/auth";

const WEB_BASE = process.env.BASE_URL ?? "http://localhost:8080";
const API_BASE = process.env.API_URL ?? "http://localhost:8000";

let seed: SeedResult;
// A published event starting ~30 min from now so the check-in window
// (start−120 min … end+240 min) is already open when the spec scans.
let nearEventId: number;

// Creates a dedicated near-now booking-enabled event. It needs its OWN fresh
// location and unique title: a shared location_id plus a near-now start would
// collide with dedup tier 3 (location_id + start_time ±3h, no title check)
// against the seeded events, and a fixed title would trip tier 4 across runs
// against the persistent dev DB (#1194/#1206 sleep on people, not unittest).
async function createNearNowEvent(
  page: import("@playwright/test").Page,
  token: string,
  orgId: number
): Promise<number> {
  const locationId = await seedLocation(page, token);
  const now = Date.now();
  const start = new Date(now + 30 * 60 * 1000).toISOString();
  const end = new Date(now + 150 * 60 * 1000).toISOString();
  const payload: Record<string, unknown> = {
    title: `Bal Check-in ${Date.now()}`,
    description: "E2E check-in journey event",
    start_time: start,
    end_time: end,
    organization_id: orgId,
    location_id: locationId,
  };
  const resp = await page.request.fetch(`${API_BASE}/api/v1/events`, {
    method: "POST",
    headers: {
      "Content-Type": "application/json",
      Authorization: `Bearer ${token}`,
    },
    data: JSON.stringify(payload),
  });
  const created = await resp.json();
  const ev = Array.isArray(created) ? created[0] : created;
  if (!ev?.id) {
    throw new Error(`createNearNowEvent: no event id in ${JSON.stringify(created)}`);
  }
  // booking_enabled/tickets_total are NOT persisted by the create path
  // (insertEvent's INSERT ignores them — only the PATCH path sets them, and
  // booking.spec.ts relies on exactly this), so enable booking afterwards,
  // same as booking.spec.ts does for the seeded event.
  const patchResp = await page.request.fetch(`${API_BASE}/api/v1/events/${ev.id}`, {
    method: "PATCH",
    headers: {
      "Content-Type": "application/merge-patch+json",
      Authorization: `Bearer ${token}`,
    },
    data: JSON.stringify({ booking_enabled: true, tickets_total: 30 }),
  });
  if (patchResp.status() !== 200) {
    throw new Error(`createNearNowEvent: PATCH ${patchResp.status()}`);
  }
  return ev.id;
}

// Books + verifies directly through the API (no web-form throttle), consuming
// the emailed verify token — returns the booking id and the QR token. The
// verify token is awaited out of the mbox so the flow still exercises the
// real fake-sendmail path.
async function bookAndVerifyDirectly(
  page: import("@playwright/test").Page,
  eventId: number,
  email: string
): Promise<{ bookingId: number; qrToken: string }> {
  const resp = await page.request.fetch(
    `${API_BASE}/api/v1/events/${eventId}/bookings`,
    {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      data: JSON.stringify({
        name: "E2E Direct Booking",
        email,
        persons: 1,
        message: "via API",
      }),
    }
  );
  const created = await resp.json();
  if (resp.status() !== 201) {
    throw new Error(`bookAndVerifyDirectly: ${resp.status()} ${JSON.stringify(created)}`);
  }
  const verifyToken = await waitForBookingVerifyToken();
  const verified = await apiGet(page, `/api/v1/bookings/verify/${verifyToken}`);
  if (!verified.qr_token) {
    throw new Error(`bookAndVerifyDirectly: verify gave ${JSON.stringify(verified)}`);
  }
  return { bookingId: created.id as number, qrToken: verified.qr_token };
}

test.describe("Check-in journey", () => {
  test.beforeAll(async ({ browser }) => {
    const context = await browser.newContext({ storageState: AUTH_FILE });
    const setupPage = await context.newPage();
    seed = await fullSeed(setupPage);
    const token = await getTokenFromCookie(setupPage);
    nearEventId = await createNearNowEvent(setupPage, token, seed.orgId);
    // Enable booking on a far-future seeded event so a *confirmed* ticket
    // exists whose check-in window has not opened yet (starts 3+ days out).
    await setupPage.request.fetch(
      `${API_BASE}/api/v1/events/${seed.eventIds[0]}`,
      {
        method: "PATCH",
        headers: {
          "Content-Type": "application/merge-patch+json",
          Authorization: `Bearer ${token}`,
        },
        data: JSON.stringify({ booking_enabled: true, tickets_total: 30 }),
      }
    );
    await context.close();
  });

  test("book → email verify → scan succeeds, re-scan is refused (single-use)", async ({
    page,
    browser,
  }) => {
    const name = `E2E Checkin ${Date.now()}`;
    const email = `checkin+${Date.now()}@test.example.com`;
    let qrToken = "";

    // Anonymous visitor (unique UA defeats the per-ip|UA submission throttle,
    // same trick as board.spec.ts) does the booking itself.
    clearMailbox();
    const anonCtx = await browser.newContext({
      storageState: { cookies: [], origins: [] },
      userAgent: `Mozilla/5.0 (E2E checkin ${Date.now()})`,
    });
    const anonPage = await anonCtx.newPage();
    try {
      await anonPage.goto(`${WEB_BASE}/events/${nearEventId}`);
      await anonPage.locator(".booking-details summary").click();
      const form = anonPage.locator(".booking-form");
      await form.locator('input[name="name"]').fill(name);
      await form.locator('input[name="email"]').fill(email);
      await form.locator('input[name="persons"]').fill("2");
      await form.locator('textarea[name="message"]').fill("Au stock");
      // guardFormSubmit rejects a form token younger than 1s (anti-bot
      // min-age) — see booking.spec.ts.
      await anonPage.waitForTimeout(1500);
      await form.locator('button[type="submit"]').click();
      await anonPage.waitForURL((url) => url.pathname === `/events/${nearEventId}`);
      await expect(anonPage.locator(".msg-ok")).toBeVisible({ timeout: 10_000 });

      const verifyToken = await waitForBookingVerifyToken();
      // Click the emailed link through the web page the API's link would
      // land on; .bv-qr-link carries the check-in URL generated by the API.
      await anonPage.goto(`${WEB_BASE}/bookings/verify/${verifyToken}`);
      await expect(anonPage.locator(".bv-ok")).toBeVisible();
      const qrHref = await anonPage.locator(".bv-qr-link").getAttribute("href");
      if (!qrHref) throw new Error("no .bv-qr-link href after verification");
      qrToken = qrHref.slice(qrHref.lastIndexOf("/") + 1);
      expect(qrToken.length).toBeGreaterThan(10);
      // The check-in URL points at the API's configured base_url, which is
      // unreachable from the test browser — scan via the web's own /checkin/.
      await anonPage.goto(`${WEB_BASE}/checkin/${qrToken}`);
      await anonPage.close();
    } finally {
      await anonCtx.close();
    }

    // Staff scan: the admin `page` fixture is logged in, so the action card
    // renders the scan button.
    await page.goto(`${WEB_BASE}/checkin/${qrToken}`);
    await expect(page.locator(".ci-card-neutral")).toBeVisible();
    await expect(page.locator(".ci-btn")).toBeVisible();
    await page.locator(".ci-btn").click();
    await page.waitForURL((url) => url.pathname === `/checkin/${qrToken}` && url.searchParams.get("ok") === "1");
    await expect(page.locator(".ci-card-green")).toBeVisible();
    await expect(page.locator(".ci-name")).toHaveText(name);
    await expect(page.locator(".ci-status-label")).toBeVisible();

    // Re-scan of the same QR: qr_token was nulled on the successful check-in,
    // so the API lookup finds no row → 404 → red error card.
    await page.goto(`${WEB_BASE}/checkin/${qrToken}`);
    await expect(page.locator(".ci-btn")).toBeVisible();
    await page.locator(".ci-btn").click();
    await page.waitForURL((url) => url.pathname === `/checkin/${qrToken}` && url.searchParams.get("err") === "1");
    await expect(page.locator(".ci-card-red")).toBeVisible();
  });

  test("scanning a cancelled booking is refused", async ({ page }) => {
    clearMailbox();
    const { bookingId, qrToken } = await bookAndVerifyDirectly(
      page,
      nearEventId,
      `cancel+${Date.now()}@test.example.com`
    );
    const token = await getTokenFromCookie(page);
    const cancelResp = await page.request.fetch(
      `${API_BASE}/api/v1/bookings/${bookingId}/status`,
      {
        method: "PATCH",
        headers: {
          "Content-Type": "application/json",
          Authorization: `Bearer ${token}`,
        },
        data: JSON.stringify({ status: "cancelled" }),
      }
    );
    expect(cancelResp.status()).toBe(204);

    await page.goto(`${WEB_BASE}/checkin/${qrToken}`);
    await expect(page.locator(".ci-btn")).toBeVisible();
    await page.locator(".ci-btn").click();
    await page.waitForURL((url) => url.pathname === `/checkin/${qrToken}` && url.searchParams.get("err") === "1");
    await expect(page.locator(".ci-card-red")).toBeVisible();
  });

  test("scanning a booking before its check-in window opens is refused", async ({
    page,
  }) => {
    clearMailbox();
    const { qrToken } = await bookAndVerifyDirectly(
      page,
      seed.eventIds[0], // far-future seeded event (starts 3+ days out)
      `early+${Date.now()}@test.example.com`
    );

    await page.goto(`${WEB_BASE}/checkin/${qrToken}`);
    await expect(page.locator(".ci-btn")).toBeVisible();
    await page.locator(".ci-btn").click();
    await page.waitForURL((url) => url.pathname === `/checkin/${qrToken}` && url.searchParams.get("err") === "1");
    await expect(page.locator(".ci-card-red")).toBeVisible();
  });
});