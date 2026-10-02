import { test, expect } from "../../helpers/fixtures";
import { randomFutureDate, isoDate, EVENT_DATE_MIN_DAYS, EVENT_DATE_MAX_DAYS } from "../../fixtures/data";

// #1417: the suggest wizard keeps the date the imported source names and
// warns when the typed date moves away from it, with a one-click fix.
//
// The source is an uploaded HTML page with a JSON-LD Event (pretix-style) —
// a file upload goes through the same preview pipeline as a pasted URL but
// needs no network, and it also covers #1417's API change: an untyped HTML
// body is parsed as JSON-LD instead of being rejected as broken iCal.
// Nothing is submitted.

test("import: editing the date away from the source warns; 'use source date' fixes it", async ({ browser }) => {
  const ctx = await browser.newContext({
    storageState: { cookies: [], origins: [] },
    userAgent: `Mozilla/5.0 (E2E suggest-source-date test ${Date.now()})`,
  });
  const page = await ctx.newPage();

  const day = isoDate(randomFutureDate(EVENT_DATE_MIN_DAYS + 7, EVENT_DATE_MAX_DAYS));
  const title = `E2E Journée baroque ${Date.now()}`;
  const html = `<!DOCTYPE html><html><head><title>${title}</title>
<script type="application/ld+json">{"@context":"https://schema.org","@type":"Event","name":"${title}",
"startDate":"${day}T10:00:00+02:00","endDate":"${day}T18:00:00+02:00",
"location":{"@type":"Place","name":"Karlsburg Durlach","address":{"@type":"PostalAddress","streetAddress":"Pfinztalstraße 9","postalCode":"76227","addressLocality":"Karlsruhe"}}}</script>
</head><body></body></html>`;

  await page.goto("/events/suggest");
  await page.locator("#import-file").setInputFiles({ name: "event.html", mimeType: "text/html", buffer: Buffer.from(html, "utf-8") });
  await Promise.all([
    page.waitForResponse((r) => r.url().endsWith("/events/suggest") && r.request().method() === "POST"),
    page.locator("#import-form").evaluate((f: HTMLFormElement) => f.requestSubmit()),
  ]);

  // Prefilled from the page's JSON-LD.
  await expect(page.locator("#wiz-title")).toHaveValue(title);
  await expect(page.locator("#sg-date-from")).toHaveValue(day);

  // To step 2, move the date one week later.
  await page.locator("#wiz-next").click();
  const later = new Date(day + "T12:00:00");
  later.setDate(later.getDate() + 7);
  const laterIso = isoDate(later);
  const dateText = page.locator("#sg-date-text");
  await dateText.fill(laterIso);
  await dateText.blur();
  await expect(page.locator("#sg-date-from")).toHaveValue(laterIso);

  const warn = page.locator('.wiz-step[data-step="2"] .sg-src-date-warn');
  await expect(warn).toBeVisible();
  await expect(warn.locator(".sg-src-date-msg")).toContainText(day.slice(0, 4));

  // One click restores the source date and the warning goes away.
  await warn.locator(".sg-src-date-use").click();
  await expect(page.locator("#sg-date-from")).toHaveValue(day);
  await expect(warn).toBeHidden();
  await ctx.close();
});
