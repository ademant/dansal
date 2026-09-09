import { test, expect } from "../../helpers/fixtures";
import { Page } from "@playwright/test";
import {
  fullSeed,
  SeedResult,
  getTokenFromCookie,
  apiPost,
  loginViaApi,
} from "../../helpers/seed";
import { AUTH_FILE } from "../../helpers/auth";
import { EDITOR, VIEWER, randomFutureDate } from "../../fixtures/data";

const API_BASE = process.env.API_URL ?? "http://localhost:8000";
const BASE_URL = process.env.BASE_URL ?? "http://localhost:8080";

let seed: SeedResult;

function unique(name: string): string {
  return `${name} ${Date.now()}`;
}

function isoDateTimeFor(d: Date, hour: number, minute: number): string {
  const pad = (n: number) => String(n).padStart(2, "0");
  return `${d.getFullYear()}-${pad(d.getMonth() + 1)}-${pad(d.getDate())}T${pad(hour)}:${pad(minute)}:00`;
}

async function authedGet(page: Page, token: string, path: string) {
  return page.request.fetch(`${API_BASE}${path}`, {
    headers: { Authorization: `Bearer ${token}` },
  });
}
async function authedJSON(page: Page, token: string, path: string): Promise<any> {
  return (await authedGet(page, token, path)).json();
}
async function patchEvent(page: Page, token: string, id: number, fields: object) {
  return page.request.fetch(`${API_BASE}/api/v1/events/${id}`, {
    method: "PATCH",
    headers: {
      "Content-Type": "application/merge-patch+json",
      Authorization: `Bearer ${token}`,
    },
    data: JSON.stringify(fields),
  });
}

// dansal_web caches GetOrganizations for admin pickers (#1276) — a fresh
// org only matters here as a role-access fixture (the editor never picks
// it from a dropdown, only gets denied touching it), so this test creates
// it through the real admin form purely out of habit/consistency, not
// because the cache would actually break anything below.
async function createOrg(page: Page, name: string): Promise<number> {
  await page.goto("/admin/organizations/new");
  await page.fill("#name", name);
  await page.locator("#save-btn").click();
  await page.waitForURL("**/admin/organizations");
  const resp = await page.request.fetch(
    `${API_BASE}/api/v1/organizations?name=${encodeURIComponent(name)}&limit=100`
  );
  const list = await resp.json();
  const match = Array.isArray(list) ? list.find((o: any) => o.name === name) : null;
  if (!match) throw new Error(`org not found: ${name}`);
  return match.id;
}

async function createOrgEvent(
  page: Page,
  adminToken: string,
  title: string,
  orgId: number
): Promise<number> {
  const d = randomFutureDate(3, 45);
  const data = await apiPost(page, "/api/v1/events", adminToken, {
    title,
    start_time: isoDateTimeFor(d, 20, 0),
    end_time: isoDateTimeFor(d, 22, 0),
    organization_id: orgId,
  });
  const created = Array.isArray(data) ? data[0] : data;
  return created.id;
}

test.describe("Role-based access boundaries", () => {
  test.beforeAll(async ({ browser }) => {
    const context = await browser.newContext({ storageState: AUTH_FILE });
    const setupPage = await context.newPage();
    seed = await fullSeed(setupPage);
    await context.close();
  });

  test("publisher can edit their own org's events but is denied a foreign org's, and cannot move into a foreign org", async ({
    page,
    browser,
  }) => {
    const adminToken = await getTokenFromCookie(page);

    // The editor (publisher) becomes a member of the seeded org — their
    // "own" org for this test. INSERT OR IGNORE server-side (#1273's
    // addOrganizationMember) makes this safe to repeat across runs.
    await apiPost(page, `/api/v1/organizations/${seed.orgId}/members`, adminToken, {
      user_id: seed.editorId,
    });

    // A second org + event the editor is *not* a member of.
    const foreignOrgId = await createOrg(page, unique("E2E Role Foreign Org"));
    const foreignEventId = await createOrgEvent(
      page,
      adminToken,
      unique("E2E Role Foreign Event"),
      foreignOrgId
    );

    // An event already in the editor's own org.
    const ownEventId = await createOrgEvent(
      page,
      adminToken,
      unique("E2E Role Own Event"),
      seed.orgId
    );

    const { context: editorContext, page: editorPage, token: editorToken } = await loginViaApi(
      browser,
      EDITOR.email,
      EDITOR.password
    );

    // -- Can edit their own org's event via the web save. --
    const descViaWeb = unique("Edited by publisher via web");
    await editorPage.goto(`/admin/events/${ownEventId}/edit`);
    await editorPage.fill('textarea[name="description"]', descViaWeb);
    await editorPage.locator("#save-btn").click();
    await editorPage.waitForURL(`**/admin/events/${ownEventId}/edit`);
    const afterWebSave = await authedJSON(page, adminToken, `/api/v1/events/${ownEventId}`);
    expect(afterWebSave.description).toBe(descViaWeb);

    // -- Can edit their own org's event via the API (PATCH). --
    const descViaAPI = unique("Edited by publisher via API");
    const ownPatchResp = await patchEvent(editorPage, editorToken, ownEventId, {
      description: descViaAPI,
    });
    expect(ownPatchResp.status()).toBe(200);
    const afterApiPatch = await authedJSON(page, adminToken, `/api/v1/events/${ownEventId}`);
    expect(afterApiPatch.description).toBe(descViaAPI);

    // -- 403 editing the foreign org's event. --
    const foreignPatchResp = await patchEvent(editorPage, editorToken, foreignEventId, {
      description: unique("Should not apply"),
    });
    expect(foreignPatchResp.status()).toBe(403);
    const foreignUnchanged = await authedJSON(page, adminToken, `/api/v1/events/${foreignEventId}`);
    expect(foreignUnchanged.description ?? "").not.toContain("Should not apply");

    // -- Cannot move their own event into the foreign org. --
    const moveResp = await patchEvent(editorPage, editorToken, ownEventId, {
      organization_id: foreignOrgId,
    });
    expect(moveResp.status()).toBe(403);
    const stillOwnOrg = await authedJSON(page, adminToken, `/api/v1/events/${ownEventId}`);
    expect(stillOwnOrg.organization_id).toBe(seed.orgId);

    // -- Denied publisher-forbidden admin surfaces (category mappings,
    //    fetch sources) — both explicitly allow role=user but forbid
    //    role=publisher (cmd/dansal/events.go's categoryAliasWriteAllowed,
    //    cmd/dansal/fetchurl.go's fetchURL). --
    const categoryResp = await editorPage.request.fetch(`${API_BASE}/api/v1/category-aliases`, {
      method: "POST",
      headers: {
        "Content-Type": "application/json",
        Authorization: `Bearer ${editorToken}`,
      },
      data: JSON.stringify({ category: "format", tag_slug: "bal-folk" }),
    });
    expect(categoryResp.status()).toBe(403);

    const fetchSourceResp = await editorPage.request.fetch(`${API_BASE}/api/v1/fetchurl`, {
      method: "POST",
      headers: {
        "Content-Type": "application/json",
        Authorization: `Bearer ${editorToken}`,
      },
      data: JSON.stringify({ url: "https://example.com/feed.ics", type: "ical" }),
    });
    expect(fetchSourceResp.status()).toBe(403);

    await editorContext.close();
  });

  test("viewer (plain user) is denied admin-only actions and sees a scoped /admin/users view", async ({
    page,
    browser,
  }) => {
    const { context: viewerContext, page: viewerPage, token: viewerToken } = await loginViaApi(
      browser,
      VIEWER.email,
      VIEWER.password
    );

    // Role change is admin-only, even on one's own account
    // (cmd/dansal/users.go's updateUser).
    const roleResp = await viewerPage.request.fetch(
      `${API_BASE}/api/v1/users/${seed.viewerId}`,
      {
        method: "PUT",
        headers: {
          "Content-Type": "application/json",
          Authorization: `Bearer ${viewerToken}`,
        },
        data: JSON.stringify({ role: "admin" }),
      }
    );
    expect(roleResp.status()).toBe(403);

    // The events-list merge tool is admin-only outright, not just
    // org-scoped (cmd/dansal_web/admin_events.go's adminEventMergeHandler)
    // — the request carries the session cookie automatically since it goes
    // through the same browser context loginViaApi injected dsw_token into.
    const mergeResp = await viewerPage.request.fetch(`${BASE_URL}/admin/events/merge`, {
      method: "POST",
      headers: { "Content-Type": "application/x-www-form-urlencoded" },
      data: "event_ids=1&event_ids=2",
    });
    expect(mergeResp.status()).toBe(403);

    // Fetch-source bulk org assignment is admin-only (unlike plain
    // create/delete, which allow role=user — cmd/dansal/fetchurl.go's
    // bulkAssignFetchSourceOrg checks X-User-Role directly for "admin").
    const bulkAssignResp = await viewerPage.request.fetch(
      `${API_BASE}/api/v1/fetchurl/bulk-assign-org`,
      {
        method: "POST",
        headers: {
          "Content-Type": "application/json",
          Authorization: `Bearer ${viewerToken}`,
        },
        data: JSON.stringify({ ids: [1], organization_id: seed.orgId }),
      }
    );
    expect(bulkAssignResp.status()).toBe(403);

    // /admin/users renders a scoped view for non-admins (admin_users.go) —
    // no hard 403, but the admin-only chrome (bulk role/org bar, the
    // select-all checkbox column) is absent.
    await viewerPage.goto("/admin/users");
    await expect(viewerPage.locator("h1")).toBeVisible();
    await expect(viewerPage.locator("#bulk-bar")).toHaveCount(0);
    await expect(viewerPage.locator("#cb-all")).toHaveCount(0);

    await viewerContext.close();
  });
});
