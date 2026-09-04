import { test, expect } from "../../helpers/fixtures";
import { fullSeed } from "../../helpers/seed";
import { AUTH_FILE } from "../../helpers/auth";

const API_BASE = process.env.API_URL ?? "http://localhost:8000";

function unique(name: string): string {
  return `${name} ${Date.now()}`;
}

// Fresh coordinates for the locatable test locations (B and C). locations.geohash
// carries a UNIQUE index (cmd/dansal/locations.go), so a fixed lat/lon would
// deterministically 500 once a prior run (or any other test) already occupies
// that ~150m precision-7 cell — exactly what seedLocation guards against.
// Each call draws its base from a disjoint Brittany town (~50–100km apart), so B
// and C can never collide with each other by construction, plus a random jitter
// to keep them off any existing cell (and off leftovers from interrupted runs).
const BRITTANY_BASES: { lat: number; lon: number }[] = [
  { lat: 48.1173, lon: -1.6778 }, // Rennes
  { lat: 47.2184, lon: -1.5536 }, // Nantes
  { lat: 48.3904, lon: -4.4861 }, // Brest
  { lat: 48.118, lon: -3.6027 }, // Gourin
  { lat: 47.8283, lon: -3.5509 }, // Quimperlé
];
let coordsCall = 0;
function freshCoords(): { lat: string; lon: string } {
  const base = BRITTANY_BASES[coordsCall % BRITTANY_BASES.length];
  coordsCall++;
  const jitter = () => (Math.random() - 0.5) * 0.2; // ±0.1°, keeps a clear gap
  const lat = (base.lat + jitter()).toFixed(6);
  const lon = (base.lon + jitter()).toFixed(6);
  return { lat, lon };
}

// The edit/create form collapses every section except #sec-base. Clicking the
// matching .loc-nav-item toggles .is-open on the target section, without which
// Playwright refuses to fill the (display:none) inputs inside it. On mobile the
// whole .loc-nav is a drawer hidden until #loc-nav-toggle (☰) is pressed, and
// each section click re-closes it — so reopen the drawer whenever the asked-for
// nav item isn't visible yet.
async function openSection(
  page: import("@playwright/test").Page,
  target: string
): Promise<void> {
  const btn = page.locator(`.loc-nav-item[data-target="${target}"]`);
  if (!(await btn.isVisible())) {
    await page.locator("#loc-nav-toggle").click();
  }
  await btn.click();
}

async function locationsByName(
  page: import("@playwright/test").Page,
  name: string
): Promise<any[]> {
  const resp = await page.request.fetch(
    `${API_BASE}/api/v1/locations?name=${encodeURIComponent(name)}&limit=100`
  );
  const body = await resp.json();
  if (!Array.isArray(body)) return [];
  return body.filter((l: any) => l.location === name);
}

async function locationIdByName(
  page: import("@playwright/test").Page,
  name: string
): Promise<number> {
  const matches = await locationsByName(page, name);
  const id = matches[0]?.id;
  if (!id) throw new Error(`location not found on API: ${name}`);
  return id;
}

test.describe("Admin: location lifecycle", () => {
  test.beforeAll(async ({ browser }) => {
    const context = await browser.newContext({ storageState: AUTH_FILE });
    const setupPage = await context.newPage();
    await fullSeed(setupPage);
    await context.close();
  });

  // `page` loads pre-authenticated as admin via playwright.config.ts's
  // use.storageState (#1252) — no explicit login needed here.
  test("create, merge, modify and delete locations", async ({ page }) => {

    const minName = unique("E2E Loc Min"); // A (minimal)
    const fullName = unique("E2E Loc Full"); // B (fully featured)
    const modName = unique("E2E Loc Mod"); // C (moderate, same town as A)

    const town = "E2E Ville";

    // -- A: create the minimal location (name + town only) --
    await page.goto("/admin/locations/new");
    await expect(page.locator("#loc-edit-form")).toBeVisible();
    await page.fill("#location", minName);
    await openSection(page, "sec-address");
    await page.fill("#town", town);
    await page.locator("#save-btn").click();
    await page.waitForURL("**/admin/locations");
    const minId = await locationIdByName(page, minName);
    expect(minId).toBeGreaterThan(0);
    // The minimal location must have been created without coordinates.
    const minResp = await page.request.fetch(
      `${API_BASE}/api/v1/locations/${minId}`
    );
    const minJson = await minResp.json();
    // omitempty on *float64 omits nil entirely, so normalize undefined→null.
    expect(minJson.latitude ?? null).toBeNull();
    expect(minJson.longitude ?? null).toBeNull();

    // -- B: create the fully-featured location (no site-plan upload) --
    await page.goto("/admin/locations/new");
    await page.fill("#location", fullName);
    await page.fill("#short_name", "E2EFull");
    await openSection(page, "sec-address");
    await page.fill("#address", "12 Rue du Bal");
    await page.fill("#zipcode", "35000");
    await page.fill("#town", town);
    await page.fill("#country", "France");
    await page.fill("#country_code", "FR");
    await page.fill("#region", "Bretagne");
    const bCoords = freshCoords();
    await page.fill("#latitude", bCoords.lat);
    await page.fill("#longitude", bCoords.lon);
    await openSection(page, "sec-website");
    await page.fill("#internetsite", "https://example.com");
    await openSection(page, "sec-parking");
    await page.selectOption("#parking", "free");
    await openSection(page, "sec-floor");
    await page.selectOption("#floor_condition", "parquet");
    // The attr checkboxes/radios sit behind a custom control whose native
    // input is 0x0, and even the wrapping label — while logically present —
    // reports as hidden, so Playwright can't act on either. Set the native
    // inputs directly: the form reads `checked` on submit, so the (hidden)
    // values are submitted normally.
    await page
      .locator('input[name="no_street_shoes"]')
      .evaluate((el) => (el.checked = true));
    await openSection(page, "sec-amenities");
    await page
      .locator('input[name="attr_wheelchair"][value="1"]')
      .evaluate((el) => (el.checked = true));
    await openSection(page, "sec-capacity");
    await page.fill("#capacity", "200");
    await page.fill("#size_sqm", "400");
    await page.fill("#notes_md", "**Notes** markdown");
    await page.locator("#save-btn").click();
    await page.waitForURL("**/admin/locations");
    const fullId = await locationIdByName(page, fullName);
    expect(fullId).toBeGreaterThan(0);

    // -- D: create a duplicate-coordinates location sharing B's exact lat/lon
    //    and assert the create FAILS. locations.geohash carries a UNIQUE index,
    //    so a second location in the same ~150m precision-7 cell is rejected by
    //    the API (500 → generic save error): the form re-renders in place with
    //    the error banner and nothing is created. --
    const dupName = unique("E2E Loc Dup");
    await page.goto("/admin/locations/new");
    await page.fill("#location", dupName);
    await openSection(page, "sec-address");
    await page.fill("#town", town);
    await page.fill("#latitude", bCoords.lat);
    await page.fill("#longitude", bCoords.lon);
    await page.locator("#save-btn").click();
    // Failure signal: the error banner appears instead of a redirect.
    await expect(page.locator('.form-error[role="alert"]')).toBeVisible();
    expect(new URL(page.url()).pathname).toBe("/admin/locations/new");
    const dups = await locationsByName(page, dupName);
    expect(dups).toHaveLength(0);

    // -- C: create the moderate location sharing A's town (duplicate target) --
    await page.goto("/admin/locations/new");
    await page.fill("#location", modName);
    await page.fill("#short_name", "E2EMod");
    await openSection(page, "sec-address");
    await page.fill("#address", "3 Place Centrale");
    await page.fill("#zipcode", "35000");
    await page.fill("#town", town);
    await page.fill("#country", "France");
    await page.fill("#country_code", "FR");
    const cCoords = freshCoords();
    await page.fill("#latitude", cCoords.lat);
    await page.fill("#longitude", cCoords.lon);
    await page.locator("#save-btn").click();
    await page.waitForURL("**/admin/locations");
    const modId = await locationIdByName(page, modName);
    expect(modId).toBeGreaterThan(0);

    // sanity: all three exist in the list (by their IDs)
    for (const id of [minId, fullId, modId]) {
      await expect(
        page.locator(`tr[data-loc-id="${id}"]`)
      ).toBeVisible();
    }

    // -- Merge A and C via the list-page checkbox merge. Survivor is the
    //    lowest ID (A = minId), which keeps its own fields and fills any
    //    blank ones from C. --
    // The .loc-cb checkboxes are hidden on mobile (shown only in touch
    // multi-select mode), so set them via JS and dispatch change — this still
    // runs the page's updateCount() that enables the merge button.
    for (const id of [minId, modId]) {
      await page
        .locator(`tr[data-loc-id="${id}"] .loc-cb`)
        .evaluate((el) => {
          (el as HTMLInputElement).checked = true;
          el.dispatchEvent(new Event("change", { bubbles: true }));
        });
    }
    const mergeBtn = page.locator(".btn-merge");
    await expect(mergeBtn).toBeEnabled();
    page.once("dialog", (d) => d.accept());
    await mergeBtn.click();
    await page.waitForURL("**/admin/locations");

    // After the merge, C (modId) is gone and A (minId) survives.
    await expect(
      page.locator(`tr[data-loc-id="${modId}"]`)
    ).toHaveCount(0);
    await expect(page.locator(`tr[data-loc-id="${minId}"]`)).toBeVisible();
    await expect(page.locator(`tr[data-loc-id="${fullId}"]`)).toBeVisible();

    // -- Modify the merged survivor (A): set a short name + capacity --
    await page.goto(`/admin/locations/${minId}/edit`);
    await page.fill("#short_name", "E2E Merged");
    await openSection(page, "sec-capacity");
    await page.fill("#capacity", "120");
    await page.locator("#save-btn").click();
    await page.waitForURL(`**/admin/locations/${minId}/edit?saved=1`);
    await expect(page.locator(".form-saved")).toBeVisible();
    // the survivor's filled short name persists after save (via API GET)
    const saved = await page.request.fetch(
      `${API_BASE}/api/v1/locations/${minId}`
    );
    const savedJson = await saved.json();
    expect(savedJson.short_name).toBe("E2E Merged");

    // -- Cleanup: delete every created location (A survivor + B) --
    // A -- from its edit page, confirming the native confirm() dialog.
    await page.goto(`/admin/locations/${minId}/edit`);
    page.once("dialog", (d) => d.accept());
    await page.locator('form[action*="/delete"] button[type="submit"]').click();
    await page.waitForURL("**/admin/locations");

    // B -- delete from its edit page (the list row's delete button is
    // mobile-hide). Accept the native confirm() dialog on submit.
    await page.goto(`/admin/locations/${fullId}/edit`);
    page.once("dialog", (d) => d.accept());
    await page.locator('form[action*="/delete"] button[type="submit"]').click();

    // Re-fetch via API to confirm each created location is gone. modId was
    // absorbed by the merge, minId/fullId were deleted — all must 404.
    for (const id of [minId, fullId, modId]) {
      await expect
        .poll(
          async () => {
            const resp = await page.request.fetch(
              `${API_BASE}/api/v1/locations/${id}`
            );
            return resp.status();
          },
          { message: `location ${id} should be gone`, timeout: 10_000 }
        )
        .toBe(404);
    }

    // -- E + org deletion: a fresh throwaway org (never the shared seedOrg)
    //    is created via the admin form, a location E is assigned to it, then
    //    the org is deleted. E must survive with the link dropped —
    //    location_organizations.organization_id is ON DELETE CASCADE, so the
    //    junction row disappears while the location row is untouched. --
    const orgName = unique("E2E Org Drop");
    await page.goto("/admin/organizations/new");
    // The org save button is gate-disabled until every [required] field is
    // set (#name); filling it fires the input listener that re-enables it.
    await page.fill("#name", orgName);
    await page.locator("#save-btn").click();
    await page.waitForURL("**/admin/organizations");

    const orgsResp = await page.request.fetch(
      `${API_BASE}/api/v1/organizations?limit=1000`
    );
    const orgList = await orgsResp.json();
    const createdOrg = (orgList as any[]).find((o: any) => o.name === orgName);
    expect(createdOrg?.id).toBeGreaterThan(0);
    const orgId = createdOrg.id;

    const orgLocName = unique("E2E Loc Org");
    await page.goto("/admin/locations/new");
    await page.fill("#location", orgLocName);
    await openSection(page, "sec-address");
    await page.fill("#town", town);
    await page
      .locator(`input[name="organization_ids"][value="${orgId}"]`)
      .check();
    await page.locator("#save-btn").click();
    await page.waitForURL("**/admin/locations");
    const orgLocId = await locationIdByName(page, orgLocName);
    expect(orgLocId).toBeGreaterThan(0);

    // E is linked to O.
    const linked = await page.request.fetch(
      `${API_BASE}/api/v1/locations/${orgLocId}`
    );
    const linkedJson = await linked.json();
    expect(linkedJson.organization_ids ?? []).toContain(orgId);

    // Delete org O from the admin orgs list. The 🗑️ button is mobile-hide and
    // opens a type-the-name modal; on mobile, fall back to the page's global
    // delOrg(id) helper (same POST /admin/organizations/{id}/delete).
    await page.goto("/admin/organizations");
    const orgDelBtn = page.locator(
      `tr:has-text("${orgName}") button[data-fn="confirmTypedDeleteOrg"]`
    );
    if (await orgDelBtn.isVisible()) {
      await orgDelBtn.click();
      await page.fill("#ctd-input", orgName);
      await page.locator("#ctd-ok").click();
    } else {
      await page.evaluate((id: number) => (window as any).delOrg(id), orgId);
    }
    await expect(page.locator(`tr:has-text("${orgName}")`)).toHaveCount(0);

    // O is gone from the API; E survives but no longer lists the org.
    const goneOrg = await page.request.fetch(
      `${API_BASE}/api/v1/organizations/${orgId}`
    );
    expect(goneOrg.status()).toBe(404);
    const survivor = await page.request.fetch(
      `${API_BASE}/api/v1/locations/${orgLocId}`
    );
    expect(survivor.status()).toBe(200);
    const survivorJson = await survivor.json();
    expect(survivorJson.organization_ids ?? []).toHaveLength(0);

    // Cleanup: delete E from its edit page.
    await page.goto(`/admin/locations/${orgLocId}/edit`);
    page.once("dialog", (d) => d.accept());
    await page.locator('form[action*="/delete"] button[type="submit"]').click();
    await page.waitForURL("**/admin/locations");
    await expect
      .poll(
        async () => {
          const resp = await page.request.fetch(
            `${API_BASE}/api/v1/locations/${orgLocId}`
          );
          return resp.status();
        },
        { message: "org-linked location E should be gone", timeout: 10_000 }
      )
      .toBe(404);
  });
});
