/**
 * Dashboard "your orgs" org -> locations tree (#1332), plus the flush-right
 * button-alignment / name-truncation follow-up fixes made to it afterward.
 *
 * The org's own row is a <summary> that toggles its <details> open/closed on
 * click anywhere in it -- including on the name link and action buttons,
 * which are real navigation targets. Clicking the summary's bounding box
 * center (Playwright's default click point) risks landing on one of those
 * and navigating away instead of toggling. Every toggle click below uses an
 * explicit low-x `position` instead, landing on the disclosure marker area
 * before the name text, mirroring how this was verified by hand while
 * building the fix (see the ::before marker rule in dashboard.html's
 * <style>).
 */
import { test, expect } from "../../helpers/fixtures";
import { createUser, addOrgMember, loginViaApi } from "../../helpers/seed";

const API_BASE = process.env.API_URL ?? "http://localhost:8000";

function unique(name: string): string {
  return `${name} ${Date.now()}`;
}

test.describe("Dashboard org -> locations tree (#1332)", () => {
  test("an org's locations are nested under it, expand on click, and their action links carry the right org/location ids", async ({
    page,
    browser,
  }) => {
    const orgName = unique("E2E DashTree Org");
    await page.goto("/admin/organizations/new");
    await page.fill("#name", orgName);
    await page.locator("#save-btn").click();
    await page.waitForURL("**/admin/organizations");
    const orgsResp = await page.request.fetch(`${API_BASE}/api/v1/organizations?limit=1000`);
    const orgId = (await orgsResp.json()).find((o: any) => o.name === orgName)?.id;
    expect(orgId).toBeGreaterThan(0);

    // A long, realistic "Venue, Town" combination — exercises the
    // ellipsis-truncation fix (.dash-org-name) rather than a short name
    // that would happen to fit either way.
    const locName = unique("E2E DashTree Grosses Gemeindehaus am Marktplatz");
    const locTown = "Bad Musterhausen-Oberdorf";
    await page.goto("/admin/locations/new");
    await page.fill("#location", locName);
    const addrBtn = page.locator('.loc-nav-item[data-target="sec-address"]');
    if (!(await addrBtn.isVisible())) {
      await page.locator("#loc-nav-toggle").click();
    }
    await addrBtn.click();
    await page.fill("#town", locTown);
    await page.locator(`input[name="organization_ids"][value="${orgId}"]`).check();
    await page.locator("#save-btn").click();
    await page.waitForURL("**/admin/locations");
    const locsResp = await page.request.fetch(
      `${API_BASE}/api/v1/locations?name=${encodeURIComponent(locName)}&limit=10`
    );
    const locId = (await locsResp.json()).find((l: any) => l.location === locName)?.id;
    expect(locId).toBeGreaterThan(0);

    const memberEmail = `e2e-dashtree-${Date.now()}@example.com`;
    const memberPassword = "Rt5wNc8jVb2qXd6P";
    createUser(memberEmail, memberPassword, "user");
    addOrgMember(orgId, memberEmail);

    const { context: memberCtx, page: memberPage } = await loginViaApi(
      browser,
      memberEmail,
      memberPassword
    );
    try {
      await memberPage.goto("/dashboard");

      // "Your orgs" is collapsed by default.
      await memberPage.locator("details.dash-orgs summary").first().click();

      const orgTree = memberPage
        .locator("details.dash-org-row-tree")
        .filter({ hasText: orgName });
      await expect(orgTree).toBeVisible();

      // The location isn't rendered yet -- the org's own <details> starts
      // closed too.
      await expect(orgTree.locator(".dash-loc-list")).toBeHidden();

      const orgSummary = orgTree.locator("summary.dash-org-row").first();
      await orgSummary.click({ position: { x: 8, y: 15 } });
      await expect(orgTree.locator(".dash-loc-list")).toBeVisible();

      const locRow = orgTree.locator(".dash-loc-row").filter({ hasText: locTown });
      await expect(locRow).toBeVisible();
      // Truncated with an ellipsis (CSS text-overflow), full name available
      // via the title attribute rather than wrapping to a second line.
      await expect(locRow.locator(".dash-org-name")).toHaveAttribute(
        "title",
        `${locName}, ${locTown}`
      );

      await expect(
        locRow.locator(`a[href="/admin/events/new?org_id=${orgId}&loc_id=${locId}"]`)
      ).toBeVisible();
      await expect(
        locRow.locator(`a[href="/admin/series/new?org_id=${orgId}&loc_id=${locId}"]`)
      ).toBeVisible();
      await expect(
        locRow.locator(`a[href="/admin/locations/${locId}/edit"]`)
      ).toBeVisible();

      // The org's own action row (outside .dash-loc-list) carries org_id
      // only, no loc_id -- confirms the location's row isn't just a visual
      // copy of the org's.
      await expect(
        orgSummary.locator(`a[href="/admin/events/new?org_id=${orgId}"]`)
      ).toBeVisible();

      // Clicking the marker again collapses it back.
      await orgSummary.click({ position: { x: 8, y: 15 } });
      await expect(orgTree.locator(".dash-loc-list")).toBeHidden();
    } finally {
      await memberCtx.close();
    }
  });
});
