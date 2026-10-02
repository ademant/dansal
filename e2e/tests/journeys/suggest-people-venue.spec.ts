import { test, expect } from "../../helpers/fixtures";
import type { Browser, Page } from "@playwright/test";
import { randomFutureDate, isoDate, EVENT_DATE_MIN_DAYS, EVENT_DATE_MAX_DAYS } from "../../fixtures/data";

// Suggest wizard as an anonymous visitor (nothing is submitted, so no review
// queue clutter):
//  - #1418 ⓘ help buttons toggle their inline text
//  - #1412 comma-separated musician list → status chips; a typo opens the
//    "did you mean" dialog
//  - #1414 venue picker: a known venue becomes a chip; "venue not listed"
//    opens the new-venue block, warns about an address-like name and blocks
//    "next" until name + town are set
// Known musicians/venues are taken from what the target already has (the
// web layer caches those lists, so freshly API-seeded rows could be
// invisible for up to the cache TTL).

const API_BASE = process.env.API_URL ?? "http://localhost:8000";

async function anonPage(browser: Browser): Promise<Page> {
  const ctx = await browser.newContext({
    storageState: { cookies: [], origins: [] },
    userAgent: `Mozilla/5.0 (E2E suggest-people-venue test ${Date.now()})`,
  });
  return ctx.newPage();
}

// typo swaps two inner letters, e.g. "Accordéon" → "Accrodéon".
function typo(name: string): string {
  const i = Math.max(2, Math.floor(name.length / 2) - 1);
  return name.slice(0, i) + name[i + 1] + name[i] + name.slice(i + 2);
}

test("help buttons and the musician list with status chips", async ({ browser }) => {
  const page = await anonPage(browser);
  const musicians = await (await page.request.get(`${API_BASE}/api/v1/musicians?limit=200`)).json();
  const known = (Array.isArray(musicians) ? musicians : [])
    .map((m: any) => m.bandname as string)
    .find((n: string) => n && n.length >= 8 && !/[,;]/.test(n));
  test.skip(!known, "target has no suitable musician to match against");

  await page.goto("/events/suggest");

  // #1418: the ⓘ next to "Musicians" opens its help text.
  const help = page.locator("#sg-help-musicians");
  await expect(help).toBeHidden();
  await page.locator('[aria-controls="sg-help-musicians"]').click();
  await expect(help).toBeVisible();

  // #1412: known (lower-cased → stored spelling) + new.
  const fresh = `E2E New Band ${Date.now()}`;
  const input = page.locator("#sg-musicians");
  await input.fill(`${known!.toLowerCase()}, ${fresh}`);
  await input.press("Tab");
  const chips = page.locator("#sg-musician-chips .musician-chip");
  await expect(chips).toHaveCount(2);
  await expect(chips.nth(0)).toHaveClass(/is-known/);
  await expect(chips.nth(1)).toHaveClass(/is-new/);
  await expect(input).toHaveValue(`${known}, ${fresh}`);

  // A typo opens the dialog; picking the candidate makes it known.
  await input.fill(typo(known!));
  await input.press("Tab");
  const dlg = page.locator("#sg-people-dialog");
  await expect(dlg).toBeVisible();
  // The musician candidate (an instructor one would carry a ".people-kind"
  // label and move the name to the other field).
  await dlg.locator("label:not(:has(.people-kind))", { hasText: known! }).first().locator("input").check();
  await page.locator("#sg-people-dialog-ok").click();
  await expect(dlg).toBeHidden();
  await expect(input).toHaveValue(known!);
  await expect(page.locator("#sg-musician-chips .musician-chip").first()).toHaveClass(/is-known/);
});

test("venue picker: known venue chip, new-venue block with address warning", async ({ browser }) => {
  const page = await anonPage(browser);
  await page.goto("/events/suggest");

  // Step 1 → 3 (title + a future date are needed to move on).
  await page.fill("#wiz-title", `E2E venue picker ${Date.now()}`);
  await page.locator("#wiz-next").click();
  const d = isoDate(randomFutureDate(EVENT_DATE_MIN_DAYS, EVENT_DATE_MAX_DAYS));
  await page.evaluate((iso) => (window as any).sgDateApply(iso, iso), d);
  await page.locator("#wiz-next").click();
  await expect(page.locator('.wiz-step[data-step="3"]')).toBeVisible();

  const hits = await (await page.request.get(`/search/locations?q=Testville`)).json();
  if (Array.isArray(hits) && hits.length > 0) {
    await page.fill("#nominatim-q", "Testville");
    await page.locator("#nominatim-btn").click();
    const first = page.locator("#sg-venue-known li").first();
    await expect(first).toBeVisible();
    await first.click();
    await expect(page.locator("#sg-venue-chip")).toBeVisible();
    await expect(page.locator("#sg-venue-new")).toBeHidden();
    await expect(page.locator("#sg-location")).toHaveValue(hits[0].name);
    await page.locator("#sg-venue-change").click();
  }

  // Venue not listed → separate new-venue block.
  await page.locator("#sg-venue-new-btn").click();
  await expect(page.locator("#sg-venue-new")).toBeVisible();
  const name = page.locator("#sg-location");
  await name.fill("76227 Karlsruhe-Durlach, Pfinztalstraße 9");
  await expect(page.locator("#sg-venue-name-note")).toBeVisible();

  // Name + town are required for a new venue.
  await page.locator("#wiz-next").click();
  await expect(page.locator('.wiz-step[data-step="3"]')).toBeVisible();
  await expect(page.locator("#sg-venue-err")).toBeVisible();

  await name.fill("Karlsburg Durlach");
  await expect(page.locator("#sg-venue-name-note")).toBeHidden();
  await page.fill("#sg-town", "Karlsruhe");
  await page.locator("#wiz-next").click();
  await expect(page.locator('.wiz-step[data-step="4"]')).toBeVisible();
});
