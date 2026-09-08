/**
 * Suggest-an-event wizard — full lifecycle (closes #1257).
 *
 * Covers four scenarios end-to-end:
 *   A  Unauthenticated visitor submits a suggestion via the 6-step wizard.
 *      The event must NOT appear publicly before an admin publishes it.
 *   B  Admin finds the suggestion in the "not verified" filter, publishes it,
 *      and the event becomes publicly visible.
 *   C  Suggester uses the manage-link (from the confirmation email) to update
 *      the event description before it is published.  The update is applied
 *      directly (no pending-edit queue needed while still unpublished).
 *   D  After publish, the suggester submits a change via the manage-link.
 *      This creates a pending edit (pending_edit_json) that an admin must
 *      approve; the description only updates after approval.
 *
 * The test skips automatically when suggest is not configured on the running
 * instance (GET /events/suggest → 404).  A one-time dev setup is required:
 *
 *   config.yaml:   smtp.sendmail: /abs/path/to/e2e/bin/fake-sendmail
 *                  smtp.from:     noreply@example.com
 *   web.yaml:      smtp_sendmail: /abs/path/to/e2e/bin/fake-sendmail
 *
 * The fake-sendmail script writes raw emails to $DANSAL_MAIL_FILE
 * (default /tmp/dansal-e2e-mail.mbox) so waitForManageToken() can extract
 * the token without a real MTA.
 */
import { test, expect } from "@playwright/test";
import { clearMailbox, waitForManageToken } from "../../helpers/mailbox";
import { getTokenFromCookie } from "../../helpers/seed";

const WEB_BASE = process.env.BASE_URL ?? "http://localhost:8080";
const API_BASE = process.env.API_URL ?? "http://localhost:8000";

/** A unique event title for this run so concurrent runs don't clash. */
function uniqueTitle(): string {
  return `E2E Suggest ${Date.now()}`;
}

/** ISO date string 30 days from now (no time component). */
function futureDateStr(): string {
  const d = new Date();
  d.setDate(d.getDate() + 30);
  return d.toISOString().slice(0, 10);
}

/**
 * Fill the 6-step wizard and submit as the current page's user.
 *
 * The custom date-picker widget is bypassed via page.evaluate() — we set the
 * hidden sg-date-from / sg-date-to inputs directly.  The form's onsubmit
 * handler then reads these to compose the final start_time / end_time values.
 *
 * The email field on step 6 is only rendered when HintSMTP is true (i.e.
 * when smtp_sendmail or smtp_host is configured in web.yaml), so `email` is
 * only required in that configuration — which is exactly when we need it to
 * receive the manage-link.
 */
async function submitSuggestWizard(
  page: import("@playwright/test").Page,
  title: string,
  email: string
): Promise<void> {
  await page.goto(`${WEB_BASE}/events/suggest`);

  // Step 1: fill title
  await page.fill("#wiz-title", title);

  // Set date hidden inputs (bypasses the calendar popup widget)
  const dateStr = futureDateStr();
  await page.evaluate((d) => {
    (document.getElementById("sg-date-from") as HTMLInputElement).value = d;
    (document.getElementById("sg-date-to") as HTMLInputElement).value = d;
  }, dateStr);

  // Advance through steps 1 → 2 → 3 → 4 → 5 → 6 by clicking Next 5 times.
  // Only step 1 has a Next-click guard (title must be non-empty); later steps
  // advance freely.
  for (let i = 0; i < 5; i++) {
    await page.locator("#wiz-next").click();
  }

  // Step 6: fill email (required when HintSMTP is true)
  const emailInput = page.locator('input[name="email"]');
  if (await emailInput.isVisible()) {
    await emailInput.fill(email);
  }

  // Submit (the form's onsubmit listener reads sg-date-from / sg-start-time
  // and writes the combined ISO datetime into sg-start / name="start_time")
  await page.locator('.wiz-step[data-step="6"] button[type="submit"]').click();
  await page.waitForURL(/\/events\/suggest\/done/);
}

/**
 * Submit the manage form (pre- or post-publish) to update the description.
 *
 * On the manage page the form is pre-filled via applyWizardData() (an IIFE
 * at page load), so title, date, and time are already set.  We only need to
 * navigate to step 4, change the description, then continue to the submit
 * button on step 6.  The email field is NOT rendered on manage pages
 * ({{if not .ManageToken}}) so no email input is needed here.
 */
async function submitManageUpdate(
  page: import("@playwright/test").Page,
  manageToken: string,
  description: string
): Promise<void> {
  await page.goto(`${WEB_BASE}/events/suggest/manage/${manageToken}`);
  // Wait for the prefill script to have populated wiz-title
  await page.waitForFunction(
    () => (document.getElementById("wiz-title") as HTMLInputElement | null)?.value !== ""
  );

  // Navigate to step 4 (description) — 3 Next clicks from step 1
  for (let i = 0; i < 3; i++) {
    await page.locator("#wiz-next").click();
  }
  await page.fill('textarea[name="description"]', description);

  // Navigate to step 6 — 2 more Next clicks
  for (let i = 0; i < 2; i++) {
    await page.locator("#wiz-next").click();
  }

  // Submit
  await page.locator('.wiz-step[data-step="6"] button[type="submit"]').click();
  await page.waitForURL(/\/events\/suggest\/done/);
}

test("suggest-wizard: full lifecycle (A→C→B→D→approve)", async ({
  page,
  browser,
}) => {
  // ── Skip guard ──────────────────────────────────────────────────────────
  // The suggest page returns 404 when SMTP / sendmail / Telegram is not
  // configured in web.yaml — skip gracefully in that case.
  const checkResp = await page.request.fetch(`${WEB_BASE}/events/suggest`);
  if (checkResp.status() === 404) {
    test.skip(
      true,
      "suggest not configured (set smtp_sendmail in web.yaml to run this test)"
    );
    return;
  }

  const title = uniqueTitle();
  const SUBMITTER_EMAIL = "e2e-suggest@example.com";
  const adminToken = await getTokenFromCookie(page);

  clearMailbox();

  // ── Scenario A: unauthenticated submission ─────────────────────────────
  const anonCtx = await browser.newContext(); // no storageState → public visitor
  const anonPage = await anonCtx.newPage();
  try {
    await submitSuggestWizard(anonPage, title, SUBMITTER_EMAIL);

    // ── Pre-publish visibility assertion ────────────────────────────────
    // The public events API only returns is_published = 1 rows.
    // The suggestion is inserted with is_published = 0, so it must not appear.
    const publicEventsResp = await page.request.fetch(
      `${API_BASE}/api/v1/events?limit=1000`
    );
    const publicEvents = await publicEventsResp.json();
    expect(
      Array.isArray(publicEvents) &&
        publicEvents.find((e: any) => e.title === title),
      "event must NOT be publicly visible before admin publishes it"
    ).toBeFalsy();

    // ── Get manage token from fake-sendmail mbox ─────────────────────────
    // The API sends the email in a goroutine; waitForManageToken polls with a
    // 15 s timeout.
    const manageToken = await waitForManageToken();

    // ── Scenario C: pre-publish manage update ───────────────────────────
    // The suggestion is still unpublished → update is applied directly.
    const prePublishDesc = `Pre-publish description ${Date.now()}`;
    await submitManageUpdate(anonPage, manageToken, prePublishDesc);

    // ── Admin finds the suggestion in the "not verified" filter ─────────
    // filterNotVerified keeps events where !is_published || pending_edit_json.
    await page.goto(`${WEB_BASE}/admin/events?not_verified=1`);
    const eventRow = page
      .locator("tr[data-evt-row]")
      .filter({ hasText: title });
    await expect(eventRow).toBeVisible({
      message: "suggestion must appear in admin not_verified filter",
    });

    const eventId = await eventRow.getAttribute("data-evt-id");
    expect(eventId, "event row must have a data-evt-id attribute").toBeTruthy();

    // Verify the pre-publish description was applied (admin can read
    // unpublished events from the API with a bearer token).
    const descResp = await page.request.fetch(
      `${API_BASE}/api/v1/events/${eventId}`,
      { headers: { Authorization: `Bearer ${adminToken}` } }
    );
    expect(descResp.status()).toBe(200);
    const descData = await descResp.json();
    expect(descData.description).toBe(prePublishDesc);

    // ── Scenario B: admin publishes ────────────────────────────────────
    // POST /admin/events/{id}/publish uses the session cookie (not bearer token).
    // page.request uses the admin browser context and therefore sends cookies.
    const publishResp = await page.request.fetch(
      `${WEB_BASE}/admin/events/${eventId}/publish`,
      { method: "POST" }
    );
    expect(
      [200, 302, 303].includes(publishResp.status()),
      `publish should succeed (got ${publishResp.status()})`
    ).toBe(true);

    // Verify the event is now publicly visible.
    const publicAfterResp = await page.request.fetch(
      `${API_BASE}/api/v1/events?limit=1000`
    );
    const publicAfterEvents = await publicAfterResp.json();
    const publishedEvent = Array.isArray(publicAfterEvents)
      ? publicAfterEvents.find((e: any) => e.title === title)
      : undefined;
    expect(publishedEvent, "event must be publicly visible after publish").toBeDefined();

    // ── Scenario D: post-publish pending edit ──────────────────────────
    // Same manage token, same page — but now the event is published, so the
    // API stores the change as pending_edit_json instead of applying it.
    const pendingDesc = `Post-publish pending description ${Date.now()}`;
    await submitManageUpdate(anonPage, manageToken, pendingDesc);
    // The manage-submit handler redirects to /events/suggest/done?review=1
    // when a pending edit was created.  waitForURL already matched the done
    // page; verify the ?review=1 query param was set.
    expect(anonPage.url()).toContain("review=1");

    // Admin sees the ✏ badge (pending_edit_json set) in the not_verified list.
    await page.goto(`${WEB_BASE}/admin/events?not_verified=1`);
    const rowWithEdit = page.locator(
      `tr[data-evt-row][data-evt-id="${eventId}"]`
    );
    await expect(rowWithEdit).toBeVisible();
    // The pending-edit badge is a <span class="badge"> containing ✏
    await expect(
      rowWithEdit.locator('span.badge').filter({ hasText: "✏" })
    ).toBeVisible({
      message: "pending-edit badge must be visible after post-publish manage submit",
    });

    // Admin approves the pending edit.
    const approveResp = await page.request.fetch(
      `${WEB_BASE}/admin/events/${eventId}/pending-edit/approve`,
      { method: "POST" }
    );
    expect(
      [200, 302, 303].includes(approveResp.status()),
      `pending-edit approve should succeed (got ${approveResp.status()})`
    ).toBe(true);

    // After approval the description on the public event matches the pending edit.
    const finalResp = await page.request.fetch(
      `${API_BASE}/api/v1/events/${eventId}`
    );
    expect(finalResp.status()).toBe(200);
    const finalEvent = await finalResp.json();
    expect(finalEvent.description).toBe(pendingDesc);
  } finally {
    await anonCtx.close();
  }
});
