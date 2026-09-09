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
 *   D  After publish, the suggester submits a change via the manage-link,
 *      touching both a "safe" field (description, applied immediately —
 *      patchSuggestManageEvent treats a plain-text edit from the event's
 *      own suggester as low-risk) and a "pending-review" field
 *      (contact_email, deferred to pending_edit_json) in one submit, so
 *      both halves of that split get exercised; the contact email only
 *      updates once an admin approves the pending edit.
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

  // /events/suggest/submit runs through the shared guardFormSubmit prologue
  // (formguard.go), whose consumeFormToken enforces a 1s *minimum* age on
  // the page's _form_token — an anti-bot check a real visitor clears
  // naturally while filling six steps, but this wizard has no per-step
  // network round trip to slow Playwright down, so the whole goto→submit
  // flow can complete in well under a second and get rejected as "too
  // fast" ("Submission failed. Please try again."). Same class of gotcha
  // as board.spec.ts's #1260 form-token wait.
  await page.waitForTimeout(1100);
  // Submit (the form's onsubmit listener reads sg-date-from / sg-start-time
  // and writes the combined ISO datetime into sg-start / name="start_time")
  await page.locator('.wiz-step[data-step="6"] button[type="submit"]').click();
  await page.waitForURL(/\/events\/suggest\/done/);
}

/**
 * Submit the manage form (pre- or post-publish) to update the description
 * and, optionally, the contact email (same step 4 as description).
 *
 * On the manage page the form is pre-filled via applyWizardData() (an IIFE
 * at page load), so title, date, and time are already set.  We only need to
 * navigate to step 4, change the description (and contact email, if given),
 * then continue to the submit button on step 6.  The email field on step 6
 * is NOT rendered on manage pages ({{if not .ManageToken}}) so no email
 * input is needed here — that's a different field from step 4's
 * contact_email (the event's own public contact address).
 *
 * description and contact_email sit on opposite sides of
 * patchSuggestManageEvent's safe/pending-review split (suggest_manage.go):
 * description auto-applies immediately (unless it contains a link) since a
 * plain-text edit from the event's own suggester is low-risk, while
 * contact_email always goes through pending_edit_json review once the event
 * is published. Passing contactEmail is how callers opt into exercising the
 * review path instead of (or alongside) the always-safe description edit.
 */
async function submitManageUpdate(
  page: import("@playwright/test").Page,
  manageToken: string,
  description: string,
  contactEmail?: string
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
  if (contactEmail !== undefined) {
    await page.fill('input[name="contact_email"]', contactEmail);
  }

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
  // playwright.config.ts's use.storageState (AUTH_FILE, the admin's saved
  // session) is a per-project default that browser.newContext() silently
  // inherits unless explicitly overridden — an unqualified newContext()
  // here still carries the admin's session, not the anonymous visitor this
  // scenario is meant to be (see helpers/seed.ts's loginViaApi doc comment
  // for the full story; caught while building board.spec.ts's #1260 work).
  // suggestSubmitHandler also shares publicThrottle (ip+user-agent,
  // 10 requests / 10 min by default) with every other public form handler
  // (booking, board) — a per-run UA sidesteps repeated-local-run collisions
  // the same way board.spec.ts's own UA fix does for its own throttle.
  const anonCtx = await browser.newContext({
    storageState: { cookies: [], origins: [] },
    userAgent: `Mozilla/5.0 (E2E suggest-wizard test ${Date.now()})`,
  });
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
    // Same manage token, same page — but now the event is published.
    // patchSuggestManageEvent (suggest_manage.go) splits the submitted
    // fields: description is a "safe" field (a plain-text edit from the
    // event's own suggester) and applies immediately regardless of
    // publish state, while contact_email is always deferred to
    // pending_edit_json once published, since it's a higher-risk field an
    // admin should see before it goes live. Changing both in one submit
    // exercises both halves of that split in a single flow.
    const pendingDesc = `Post-publish pending description ${Date.now()}`;
    const pendingContactEmail = `e2e-suggest-pending-${Date.now()}@example.com`;
    await submitManageUpdate(anonPage, manageToken, pendingDesc, pendingContactEmail);
    // The manage-submit handler redirects to /events/suggest/done?review=1
    // when a pending edit was created.  waitForURL already matched the done
    // page; verify the ?review=1 query param was set.
    expect(anonPage.url()).toContain("review=1");

    // The description (a safe field) is already live on the public event —
    // no admin action needed — while contact_email (pending review) is not.
    const preApproveResp = await page.request.fetch(
      `${API_BASE}/api/v1/events/${eventId}`
    );
    expect(preApproveResp.status()).toBe(200);
    const preApproveEvent = await preApproveResp.json();
    expect(
      preApproveEvent.description,
      "description is a safe field and must apply immediately, even pre-approval"
    ).toBe(pendingDesc);
    expect(
      preApproveEvent.contact_email,
      "contact_email must NOT apply until the pending edit is approved"
    ).not.toBe(pendingContactEmail);

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

    // After approval the contact email on the public event matches the pending edit.
    const finalResp = await page.request.fetch(
      `${API_BASE}/api/v1/events/${eventId}`
    );
    expect(finalResp.status()).toBe(200);
    const finalEvent = await finalResp.json();
    expect(finalEvent.contact_email).toBe(pendingContactEmail);
    expect(finalEvent.description).toBe(pendingDesc);
  } finally {
    await anonCtx.close();
  }
});
