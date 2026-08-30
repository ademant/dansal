import { Page, expect } from "@playwright/test";

const PAGE_SIZE = 25;

export async function loadAllRows(page: Page): Promise<void> {
  const btn = page.locator("#load-more");
  // Every row is rendered up front and merely hidden behind a
  // .row-filtered class as pagination advances (see index.html's
  // renderAll()), so tr.event-row's total count stays flat across clicks —
  // only the *visible* (non-filtered) subset grows. Counting the wrong one
  // makes this loop think the first click already did nothing and bail
  // immediately. A click past the initial ~100-event page also kicks off a
  // background fetch (ensureLoadedUntil) that can take longer than a
  // fraction of a second to land, so give each click real room to resolve.
  const visibleRows = () => page.locator("tr.event-row:not(.row-filtered)");
  for (let i = 0; i < 20; i++) {
    if (!(await btn.isVisible().catch(() => false))) break;
    if (await btn.isDisabled().catch(() => false)) break;
    const prevCount = await visibleRows().count();
    await btn.click();
    await page.waitForTimeout(1500);
    const newCount = await visibleRows().count();
    if (newCount <= prevCount) break;
  }
}

export async function clickEventInTable(
  page: Page,
  title: string
): Promise<void> {
  // Below 640px the index page hides .table-wrap entirely and renders the
  // same rows as .event-tile <a> cards instead (buildTile() in index.html —
  // shared by both #event-tiles in "all" mode and the mobile week view), so
  // a desktop-only tr.event-row selector never finds anything there.
  const tile = page.locator(`.event-tile:has-text("${title}")`).first();
  if (await tile.isVisible().catch(() => false)) {
    await tile.click();
  } else {
    const row = page.locator(`tr.event-row:has-text("${title}")`);
    await expect(row).toBeVisible();
    await row.locator("td:nth-child(2) a").click();
  }
  await expect(page.locator("h1")).toContainText(title);
}

// expectEventListVisible asserts whichever of the two mutually-exclusive
// event-list containers the current viewport actually renders is visible
// (#event-table on desktop, #event-tiles below 640px — see the .table-wrap/
// #event-tiles CSS in index.html). Both exist in the DOM at all times but
// only one is ever unhidden, so a plain #event-table assertion always fails
// on mobile.
export async function expectEventListVisible(page: Page): Promise<void> {
  const tiles = page.locator("#event-tiles");
  if (await tiles.isVisible().catch(() => false)) {
    await expect(tiles).toBeVisible();
  } else {
    await expect(page.locator("#event-table")).toBeVisible();
  }
}

export async function searchFilter(
  page: Page,
  term: string
): Promise<void> {
  const input = page.locator(".filter-row input[type=search]");
  if (await input.isVisible().catch(() => false)) {
    await input.fill(term);
    await input.press("Enter");
    await page.waitForTimeout(400);
  }
}

export async function goToAllView(page: Page): Promise<void> {
  // Below 640px the desktop #tmt-week/#tmt-all toggle (inside .tmt) is
  // CSS-hidden entirely — the mobile layout switches table mode via its own
  // #mobile-show-all/#mobile-show-week bar instead (see index.html). #tmt-all
  // being invisible there means the desktop branch below was silently a
  // no-op on mobile, leaving the page stuck in the default "week" view
  // (which usually has zero events, since seeded events land 3-45 days out).
  const btn = page.locator("#tmt-all");
  if (await btn.isVisible().catch(() => false)) {
    const isActive = await btn.evaluate((el) =>
      el.classList.contains("tmt-active")
    );
    if (!isActive) {
      await btn.click();
      await page.waitForTimeout(300);
    }
    return;
  }
  const mobileBtn = page.locator("#mobile-show-all");
  if (await mobileBtn.isVisible().catch(() => false)) {
    await mobileBtn.click();
    await page.waitForTimeout(300);
  }
}

export async function expectVisible(
  page: Page,
  selector: string,
  opts?: { hasText?: string; timeout?: number }
): Promise<void> {
  const el = page.locator(selector);
  if (opts?.hasText) {
    await expect(el.filter({ hasText: opts.hasText })).toBeVisible({
      timeout: opts?.timeout,
    });
  } else {
    await expect(el).toBeVisible({ timeout: opts?.timeout });
  }
}

export async function expectHidden(
  page: Page,
  selector: string
): Promise<void> {
  await expect(page.locator(selector)).toBeHidden();
}

export async function clickButton(page: Page, text: string): Promise<void> {
  await page.getByRole("button", { name: text }).click();
}

export async function clickLink(page: Page, text: string): Promise<void> {
  await page.getByRole("link", { name: text }).click();
}

export async function getEventRowCount(page: Page): Promise<number> {
  const desktop = await page.locator("tr.event-row").count();
  const mobile = await page.locator("#event-tiles .event-tile").count();
  return Math.max(desktop, mobile);
}
