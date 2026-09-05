import { test, expect } from "../../helpers/fixtures";
import { fullSeed, SeedResult, getTokenFromCookie } from "../../helpers/seed";
import { AUTH_FILE } from "../../helpers/auth";
import { ensureIndexWeekFixture, ensureFollowingWeekFixture, IndexWeekFixture } from "../../helpers/indexFixture";

// Covers the index page's actual discovery mechanics (#1266) — type
// filtering, the map (clustering + popups), the mini-calendar heatmap, and
// week/month/day navigation all staying in sync — as opposed to
// discover.spec.ts, which only covers the plain list/load-more/click-through.
let seed: SeedResult;
let fixture: IndexWeekFixture;

test.describe("Index page: discovery mechanics", () => {
  test.beforeAll(async ({ browser }) => {
    const context = await browser.newContext({ storageState: AUTH_FILE });
    const setupPage = await context.newPage();
    seed = await fullSeed(setupPage);
    const token = await getTokenFromCookie(setupPage);
    fixture = await ensureIndexWeekFixture(setupPage, token, seed.orgId);
    await ensureFollowingWeekFixture(setupPage, token, seed.orgId, fixture.nearbyLocationId);
    await context.close();
  });

  test("toggling a type filter narrows the week view", async ({ page }) => {
    await page.goto("/");
    const links = () => page.locator(".week-table a[href]");
    const before = await links().count();
    expect(before).toBeGreaterThan(0);

    // "workshop" is one of the home-group tags ensureIndexWeekFixture()
    // guarantees at least one representative event for this week.
    const workshopBtn = page.locator('.type-btn[data-type="workshop"]');
    await expect(workshopBtn).toHaveClass(/type-active/);
    await workshopBtn.click();
    await expect(workshopBtn).not.toHaveClass(/type-active/);

    const after = await links().count();
    expect(after).toBeLessThan(before);

    // Toggling back on restores the original count.
    await workshopBtn.click();
    await expect(workshopBtn).toHaveClass(/type-active/);
    expect(await links().count()).toBe(before);
  });

  test("each type filter button narrows results by its own tag", async ({ page }) => {
    await page.goto("/");
    const links = () => page.locator(".week-table a[href]");
    const buttons = await page.locator(".type-btn").all();
    expect(buttons.length).toBeGreaterThan(0);

    for (const btn of buttons) {
      const before = await links().count();
      await btn.click();
      const after = await links().count();
      // Every home-group has >=1 seeded event this week (ensureIndexWeekFixture
      // invariant 4), so toggling any one of them off must remove >=1 row —
      // a button with no effect would mean either the fixture invariant or
      // the filter wiring itself is broken.
      expect(after).toBeLessThan(before);
      await btn.click(); // restore before testing the next button
      expect(await links().count()).toBe(before);
    }
  });

  test("clicking the far marker opens its popup and links to the right event", async ({ page }) => {
    await page.goto("/");
    const marker = page.locator(`.leaflet-marker-icon[title="${fixture.farEventTitle}"]`);
    await expect(marker).toBeVisible();
    await marker.click();
    const popupLink = page.locator(".leaflet-popup a", { hasText: fixture.farEventTitle });
    await expect(popupLink).toBeVisible();
    await popupLink.click();
    await expect(page.locator("h1")).toContainText(fixture.farEventTitle);
  });

  test("clicking a cluster zooms in to reveal individual markers; zooming out re-groups them", async ({ page }) => {
    await page.goto("/");
    // The nearby pair shares one location, so at the map's initial
    // fit-bounds zoom they render as a single cluster icon, not two
    // separate markers.
    const cluster = page.locator(".marker-cluster").first();
    await expect(cluster).toBeVisible();
    await cluster.click();

    // Leaflet.markercluster's default click behavior zooms/fits to the
    // cluster's children — give the animation a moment to settle.
    await page.waitForTimeout(500);
    const individualMarkers = page.locator(".leaflet-marker-icon[title]:not(.marker-cluster)");
    await expect(individualMarkers.first()).toBeVisible();

    // Zoom back out a fixed handful of notches rather than trying to land on
    // one exact original zoom level (fragile — depends on fitBounds' own
    // math) — asserting it re-groups at *some* zoomed-out level is the
    // robust check.
    const zoomOut = page.locator(".leaflet-control-zoom-out");
    for (let i = 0; i < 6; i++) {
      await zoomOut.click();
    }
    await page.waitForTimeout(500);
    await expect(page.locator(".marker-cluster").first()).toBeVisible();
  });

  test("mini-calendar heatmap: colors vary and the busiest day is the most intense", async ({ page }) => {
    await page.goto("/");
    const rank: Record<string, number> = { "": 0, "heat-1": 1, "heat-2": 2, "heat-3": 3, "heat-4": 4 };
    // Mon=0 .. Sun=6 — ensureIndexWeekFixture() picks Friday when it hasn't
    // passed yet, otherwise the last remaining visible day, so this can't
    // be hardcoded to index 4.
    const busiestIdx = fixture.weekDates.indexOf(fixture.busiestDate);

    // Device-adaptive (see helpers/nav.ts's expectEventListVisible for the
    // established pattern): desktop shows the monthly mini-cal, mobile
    // swaps it out for the day-selector strip — both derive their color
    // from the same heatClass() function, so the same relative check
    // applies to whichever is actually visible.
    const miniCal = page.locator("#week-mini-cal");
    const isMiniCalVisible = await miniCal.isVisible().catch(() => false);

    let ranks: number[];
    if (isMiniCalVisible) {
      const selectedRow = miniCal.locator("tr.mc-week.mc-sel");
      const cells = await selectedRow.locator("td").all();
      expect(cells.length).toBe(7);
      const classes = await Promise.all(cells.map((c) => c.getAttribute("class")));
      ranks = classes.map((c) => rank[(c || "").match(/heat-\d/)?.[0] || ""]);
    } else {
      const dayCodes = await page.locator(".week-mobile-days .day-code").all();
      expect(dayCodes.length).toBe(7);
      const classes = await Promise.all(dayCodes.map((c) => c.getAttribute("class")));
      ranks = classes.map((c) => rank[(c || "").match(/heat-\d/)?.[0] || ""]);
    }

    expect(new Set(ranks).size).toBeGreaterThan(1); // colors do vary
    expect(ranks[busiestIdx]).toBeGreaterThanOrEqual(Math.max(...ranks));
  });

  test("week navigation (arrows and mini-cal week-row) updates the map together with the view", async ({ page }) => {
    await page.goto("/");
    const isDesktop = await page.locator("#week-prev").isVisible().catch(() => false);
    if (!isDesktop) test.skip(true, "desktop-only: mobile's week arrows are covered in the mobile view test");

    const weekLabel = page.locator(".week-label");
    const initialLabel = await weekLabel.textContent();
    // Wait for the map's async load (#1215) to actually settle before
    // reading a "before" count — otherwise a slow first paint reads as 0
    // and the later real count just looks like an unrelated change.
    await expect(page.locator(".leaflet-marker-icon, .marker-cluster").first()).toBeVisible();
    const initialMarkers = await page.locator(".leaflet-marker-icon, .marker-cluster").count();

    await page.click("#week-next");
    await page.waitForTimeout(300);
    expect(await weekLabel.textContent()).not.toBe(initialLabel);
    // The following week's fixture has far fewer events at a different
    // location than this week's nearby/far pair — the marker/cluster count
    // on-screen should change, proving updateMapMarkers(true) actually ran
    // rather than only the week table re-rendering.
    const nextWeekMarkers = await page.locator(".leaflet-marker-icon, .marker-cluster").count();
    expect(nextWeekMarkers).not.toBe(initialMarkers);

    await page.click("#week-prev");
    await page.waitForTimeout(300);
    expect(await weekLabel.textContent()).toBe(initialLabel);

    // Jumping via a mini-cal week-row should land on the same week and also
    // refresh the map.
    const nextRow = page.locator("#week-mini-cal tr.mc-week").nth(1);
    await nextRow.click();
    await page.waitForTimeout(300);
    expect(await weekLabel.textContent()).not.toBe(initialLabel);
  });

  test("month navigation updates the heatmap", async ({ page }) => {
    await page.goto("/");
    const mcNext = page.locator("#mc-next");
    if (!(await mcNext.isVisible().catch(() => false))) {
      test.skip(true, "desktop-only: mini-calendar is hidden on mobile");
    }
    const header = page.locator("#week-mini-cal .mc-header span");
    const before = await header.textContent();
    await mcNext.click();
    await page.waitForTimeout(300);
    expect(await header.textContent()).not.toBe(before);
  });

  test("mobile: daily view replaces the weekly table; day switching leaves the map alone", async ({ page }) => {
    await page.goto("/");
    const weekMobile = page.locator(".week-mobile");
    if (!(await weekMobile.isVisible().catch(() => false))) {
      test.skip(true, "mobile-only view");
    }
    await expect(page.locator("#week-mini-cal")).toBeHidden();

    // The map loads asynchronously (Leaflet's CSS/tiles load non-blocking,
    // #1215) — wait for at least one marker/cluster to actually render
    // before taking the "before" count, or a slow first paint reads as 0
    // and any later async settling looks like a false change.
    await expect(page.locator(".leaflet-marker-icon, .marker-cluster").first()).toBeVisible();
    const initialMarkers = await page.locator(".leaflet-marker-icon, .marker-cluster").count();
    const dayCodes = page.locator(".week-mobile-days .day-code");
    const otherDay = dayCodes.nth(2);
    await otherDay.click();
    await page.waitForTimeout(200);
    await expect(otherDay).toHaveClass(/active/);
    // Day switching re-renders only the daily content, not the map.
    expect(await page.locator(".leaflet-marker-icon, .marker-cluster").count()).toBe(initialMarkers);

    // The mobile week arrows (same handlers as desktop's) do move the map.
    await page.click("#wm-next");
    await page.waitForTimeout(300);
    expect(await page.locator(".leaflet-marker-icon, .marker-cluster").count()).not.toBe(initialMarkers);
  });
});
