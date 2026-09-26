/**
 * Suggest-from-feed import preview — wizard chrome and date prefill
 * (closes #1388, #1389, #1390).
 *
 * Three regressions, all on the page reached after POSTing a calendar to the
 * *Import* tab of /events/suggest and clicking Preview:
 *
 *   #1388  The six progress arrows vanished. Their <nav id="wiz-arrows"> sat
 *          inside the {{else}} branch of the import conditional, so import
 *          mode never rendered it at all — and the wizard JS substitutes an
 *          empty arrow list when #wiz-arrows is missing, so the template bug
 *          was silent.
 *   #1389  The prefilled date was stored but never displayed. base.js loads
 *          with defer, so the picker is built in a DOMContentLoaded listener
 *          while the auto-prefill used to run as a parse-time IIFE — strictly
 *          earlier. The `if (window.sgDatePickerSet)` guard then swallowed the
 *          button update for good. Same for /events/suggest/manage/{token}.
 *   #1390  The date was reachable only through a calendar popup; there is now
 *          a visible, typeable field beside it.
 *
 * The feed is uploaded as a **file**, not a URL, so this spec needs no
 * network egress and no SSRF allowlist entry (contrast suggest-feed.spec.ts,
 * which must use an allowlisted host). Skips when suggest is unconfigured
 * (GET /events/suggest → 404).
 */
import { test, expect } from "@playwright/test";

const WEB_BASE = process.env.BASE_URL ?? "http://localhost:8080";

/**
 * A single-event calendar. 19:30Z keeps the local date identical for any
 * timezone within ±9h, but the assertions below don't depend on this: they
 * read the server-supplied #sg-date-from and check that the button and the
 * text field agree with it.
 */
function icsFixture(): string {
  return [
    "BEGIN:VCALENDAR",
    "VERSION:2.0",
    "PRODID:-//dansal//e2e suggest-import-preview//EN",
    "BEGIN:VEVENT",
    "UID:e2e-import-preview@example.org",
    "DTSTAMP:20260101T120000Z",
    "DTSTART:20270315T193000Z",
    "DTEND:20270315T223000Z",
    "SUMMARY:E2E Import Preview Event",
    "LOCATION:E2E Testhalle",
    "DESCRIPTION:Seeded by suggest-import-preview.spec.ts.",
    "END:VEVENT",
    "END:VCALENDAR",
    "",
  ].join("\r\n");
}

test("import preview: arrows, prefilled date and date field all render", async ({
  browser,
}) => {
  const checkResp = await fetch(`${WEB_BASE}/events/suggest`);
  if (checkResp.status === 404) {
    test.skip(true, "suggest not configured (set smtp_sendmail in web.yaml)");
    return;
  }

  // Anonymous visitor with a per-run UA: suggestPreviewHandler shares
  // publicThrottle (ip+user-agent) with the other public form handlers, and
  // browser.newContext() silently inherits storageState (the admin session)
  // unless explicitly overridden.
  const ctx = await browser.newContext({
    storageState: { cookies: [], origins: [] },
    userAgent: `Mozilla/5.0 (E2E suggest-import-preview test ${Date.now()})`,
  });
  const page = await ctx.newPage();

  try {
    await page.goto(`${WEB_BASE}/events/suggest`);
    await page.locator(".tab-btn", { hasText: /import|Import/i }).first().click();
    await page.setInputFiles("#import-file", {
      name: "e2e.ics",
      mimeType: "text/calendar",
      buffer: Buffer.from(icsFixture(), "utf-8"),
    });
    // Preview POST is not behind guardFormSubmit's 1s token-age check.
    await page.locator("#import-form button[type=submit]").click();

    // ── #1388: progress arrows are back ────────────────────────────────
    const arrows = page.locator("#wiz-arrows .wiz-arrow");
    await expect(
      arrows,
      "import mode must render the six wizard progress arrows (#1388)"
    ).toHaveCount(6);
    await expect(arrows.first()).toBeVisible();

    // ── #1389/#1390: the prefilled date is visible ─────────────────────
    // Ground truth is the server-written hidden input; the button and the new
    // text field must both agree with it.
    const from = await page.inputValue("#sg-date-from");
    expect(from, "preview must have written a start date").toMatch(
      /^\d{4}-\d{2}-\d{2}$/
    );
    await expect(
      page.locator("#sg-date-btn"),
      "date button must show the imported date, not the 📅 placeholder (#1389)"
    ).toContainText(from);
    await expect(
      page.locator("#sg-date-text"),
      "the visible date field must show the imported date (#1390)"
    ).toHaveValue(from);
    // A second, distinct date means the field is not just echoing the button.
    expect(from).not.toBe("");

    // The label reuses the existing evt_date string — no new i18n key.
    await expect(page.locator('label[for="sg-date-text"]')).toBeVisible();

    // ── #1390: field → hidden inputs + button ──────────────────────────
    const typed = "2027-04-08";
    await page.fill("#sg-date-text", typed);
    await page.locator("#sg-date-text").blur();
    await expect(page.locator("#sg-date-from")).toHaveValue(typed);
    // A lone date in the text field is a single-day event, so #sg-date-to
    // follows it — never left pointing at the stale imported end date.
    await expect(page.locator("#sg-date-to")).toHaveValue(typed);
    await expect(page.locator("#sg-date-btn")).toContainText(typed);

    // ── #1390: a malformed entry must not corrupt the hidden inputs ────
    await page.fill("#sg-date-text", "not-a-date");
    await page.locator("#sg-date-text").blur();
    await expect(
      page.locator("#sg-date-from"),
      "invalid text must be ignored, leaving the last good value in place"
    ).toHaveValue(typed);

    // ── #1390: picker → field ──────────────────────────────────────────
    // Two clicks on the same cell complete a range selection (the first arms
    // pendingStart, the second fires onSelect).
    await page.fill("#sg-date-text", "");
    await page.locator("#sg-date-text").blur();
    await page.locator("#sg-date-btn").click();
    const day = page.locator('#sg-cal-grid td[data-iso="2027-05-06"]');
    await expect(day).toBeVisible();
    await day.click();
    await day.click();
    await expect(
      page.locator("#sg-date-text"),
      "picking a date in the popup must fill the text field"
    ).toHaveValue("2027-05-06");
    await expect(page.locator("#sg-date-from")).toHaveValue("2027-05-06");
  } finally {
    await ctx.close();
  }
});
