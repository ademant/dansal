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
  // Pick the container by viewport, then wait for it. A plain isVisible()
  // check runs before the tiles are rendered and falls through to the
  // desktop table on phones (#1748, discover.spec.ts:53).
  if (isPhone(page)) {
    const tile = page.locator(`.event-tile:has-text("${title}")`).first();
    await tile.click();
  } else {
    const row = page.locator(`tr.event-row:has-text("${title}")`);
    await expect(row).toBeVisible();
    await row.locator("td:nth-child(2) a").click();
  }
  await expect(page.locator("h1")).toContainText(title);
}

// isPhone reports whether the test viewport is phone-sized (<= 640px, the
// breakpoint where index.html swaps the table for tiles and the event and
// musician pages move content into tabs).
export function isPhone(page: Page): boolean {
  return (page.viewportSize()?.width ?? 1024) <= 640;
}

// wizGo moves the suggest wizard to step n with the control the viewport
// shows. The Next/Back buttons were removed (#1666, c73865b5, and a Go test
// asserts they stay gone): desktop uses the progress arrows, phones the step
// tabs. #1747: the tests clicked #wiz-next and waited 90 s for it.
export async function wizGo(page: Page, step: number): Promise<void> {
  const sel = isPhone(page)
    ? `#wiz-mtabs .mtab[data-step="${step}"]`
    : `#wiz-arrows .wiz-arrow[data-step="${step}"]`;
  await page.locator(sel).click();
}

// wizNext moves the suggest wizard to the step after the one on screen.
export async function wizNext(page: Page): Promise<void> {
  const current = await page.locator(".wiz-step:not([hidden])").first().getAttribute("data-step");
  await wizGo(page, Number(current) + 1);
}

// expectEventListVisible asserts whichever of the two mutually-exclusive
// event-list containers the current viewport actually renders is visible
// (#event-table on desktop, #event-tiles below 640px — see the .table-wrap/
// #event-tiles CSS in index.html). Both exist in the DOM at all times but
// only one is ever unhidden, so a plain #event-table assertion always fails
// on mobile.
export async function expectEventListVisible(page: Page): Promise<void> {
  // Viewport, not isVisible(): the tiles may not be rendered yet (#1748).
  const target = isPhone(page) ? "#event-tiles" : "#event-table";
  await expect(page.locator(target)).toBeVisible();
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
  // CSS-hidden. There the list tab (#mtab-list, the default, #1613) shows
  // every future event, so no click is needed.
  const btn = page.locator("#tmt-all");
  if (await btn.isVisible().catch(() => false)) {
    const isActive = await btn.evaluate((el) =>
      el.classList.contains("tmt-active")
    );
    if (!isActive) {
      await btn.click();
      await page.waitForTimeout(300);
    }
  }
}

// openHomeTab switches the phone bottom tab bar (#1613) to list, week or map.
// Desktop has no tab bar, so this does nothing there.
export async function openHomeTab(page: Page, tab: "list" | "week" | "map"): Promise<void> {
  const btn = page.locator(`#mtab-${tab}`);
  if (await btn.isVisible().catch(() => false)) {
    await btn.click();
    await expect(page.locator("#home-tabs")).toHaveAttribute("data-mtab", tab);
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
