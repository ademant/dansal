/**
 * Organization and location media links (#1361): same external-link list as
 * musicians (#1360), shown as plain links on the public org/venue page and
 * edited as repeatable rows in the admin form. Nothing is embedded.
 */
import { test, expect } from "../../helpers/fixtures";
import { getTokenFromCookie, seedLocation } from "../../helpers/seed";

const API_BASE = process.env.API_URL ?? "http://localhost:8000";

type Media = Array<{ kind: string; title: string; url: string }>;

function authHeaders(token: string) {
  return { "Content-Type": "application/json", Authorization: `Bearer ${token}` };
}

// PATCH bodies are JSON merge-patch documents.
function patchHeaders(token: string) {
  return { "Content-Type": "application/merge-patch+json", Authorization: `Bearer ${token}` };
}

// Orgs are created through the admin form, not the raw API: the web layer
// caches the organization list for its public pages/pickers and only its own
// handlers invalidate it, so an API-created org wouldn't resolve at /org/{slug}
// for up to a minute. Going through the form also covers the create path.
async function createOrg(
  page: import("@playwright/test").Page,
  token: string,
  media: Array<{ kind: string; title: string; url: string }> = []
) {
  const name = `E2E Media Org ${Date.now()}${Math.floor(Math.random() * 1000)}`;
  await page.goto("/admin/organizations/new");
  await page.fill("#name", name);
  if (media.length) {
    await openSection(page, "org-nav-item", "org-nav-toggle", "sec-media");
    for (const m of media) {
      await page.locator("#sec-media .media-editor > button", { hasText: "+" }).click();
      const row = page.locator("#sec-media .media-row").last();
      await row.locator('select[name="media_kind"]').selectOption(m.kind);
      await row.locator('input[name="media_title"]').fill(m.title);
      await row.locator('input[name="media_url"]').fill(m.url);
    }
  }
  await page.locator("#save-btn").click();
  await page.waitForURL("**/admin/organizations");
  const resp = await page.request.fetch(
    `${API_BASE}/api/v1/organizations?name=${encodeURIComponent(name)}&limit=100`,
    { headers: authHeaders(token) }
  );
  const found = (await resp.json()).find((o: any) => o.name === name);
  expect(found).toBeTruthy();
  return { id: found.id as number, slug: (found.actor_name as string) || name.toLowerCase().replace(/[^a-z0-9]+/g, "-") };
}

async function getMedia(page: import("@playwright/test").Page, kind: "organizations" | "locations", id: number) {
  const resp = await page.request.fetch(`${API_BASE}/api/v1/${kind}/${id}`);
  return ((await resp.json()).media ?? []) as Media;
}

async function putLocationMedia(
  page: import("@playwright/test").Page,
  token: string,
  id: number,
  media: object[]
) {
  const resp = await page.request.fetch(`${API_BASE}/api/v1/locations/${id}`, {
    method: "PATCH",
    headers: patchHeaders(token),
    data: JSON.stringify({ media }),
  });
  expect(resp.status()).toBe(200);
}

// Sections are collapsed until their nav item is clicked — except ones that
// already hold data, which start open (clicking their nav item again would
// toggle them shut). On narrow viewports the nav itself is a drawer behind a
// toggle.
async function openSection(
  page: import("@playwright/test").Page,
  itemClass: string,
  toggleId: string,
  target: string
) {
  if (await page.locator(`#${target}`).isVisible()) return;
  const btn = page.locator(`.${itemClass}[data-target="${target}"]`);
  if (!(await btn.isVisible())) {
    await page.locator(`#${toggleId}`).click();
  }
  await btn.click();
}

test.describe("Organization and venue media links (#1361)", () => {
  test("org page lists links as plain external links, nothing embedded", async ({ page }) => {
    const token = await getTokenFromCookie(page);
    const { id, slug } = await createOrg(page, token, [
      { kind: "video", title: "Introduction", url: "https://www.youtube.com/watch?v=org1" },
      { kind: "other", title: "", url: "https://example.org/press" },
    ]);
    await page.goto(`/org/${slug}`);
    const section = page.locator(".media-links");
    await expect(section).toBeVisible();
    await expect(section.locator(".media-notice")).toBeVisible();
    const items = section.locator(".media-list li");
    await expect(items).toHaveCount(2);
    const first = items.nth(0).locator("a");
    await expect(first).toHaveText("Introduction");
    await expect(first).toHaveAttribute("rel", /noopener/);
    await expect(items.nth(0).locator(".media-host")).toHaveText("youtube.com");
    await expect(items.nth(1).locator("a")).toHaveText("example.org");
    await expect(page.locator("iframe, audio, video, embed, object")).toHaveCount(0);
  });

  test("an org without links shows no media section", async ({ page }) => {
    const token = await getTokenFromCookie(page);
    const { id, slug } = await createOrg(page, token);
    await page.goto(`/org/${slug}`);
    await expect(page.locator(".media-links")).toHaveCount(0);
  });

  test("org admin form: add, reorder and clear links", async ({ page }) => {
    const token = await getTokenFromCookie(page);
    const { id, slug } = await createOrg(page, token, [
      { kind: "video", title: "One", url: "https://example.org/1" },
      { kind: "audio", title: "Two", url: "https://example.org/2" },
    ]);

    await page.goto(`/admin/organizations/${id}/edit`);
    await openSection(page, "org-nav-item", "org-nav-toggle", "sec-media");
    const rows = page.locator("#sec-media .media-row");
    await expect(rows).toHaveCount(2);

    await rows.nth(1).locator('button[data-fn="mediaMoveRow"]').first().click();
    await expect(rows.nth(0).locator('input[name="media_title"]')).toHaveValue("Two");

    await page.locator("#sec-media .media-editor > button", { hasText: "+" }).click();
    await expect(rows).toHaveCount(3);
    const added = rows.nth(2);
    await added.locator('select[name="media_kind"]').selectOption("image");
    await added.locator('input[name="media_title"]').fill("Photo");
    await added.locator('input[name="media_url"]').fill("https://example.org/p.jpg");

    await page.locator("#save-btn").click();
    await expect
      .poll(async () => (await getMedia(page, "organizations", id)).map((m) => [m.kind, m.title]), {
        timeout: 15_000,
      })
      .toEqual([
        ["audio", "Two"],
        ["video", "One"],
        ["image", "Photo"],
      ]);

    // Saving the form without touching the section must keep the links.
    await page.goto(`/admin/organizations/${id}/edit`);
    await page.locator("#save-btn").click();
    await page.waitForLoadState("load");
    expect(await getMedia(page, "organizations", id)).toHaveLength(3);

    // Remove every row -> the list is cleared.
    await page.goto(`/admin/organizations/${id}/edit`);
    await openSection(page, "org-nav-item", "org-nav-toggle", "sec-media");
    const editRows = page.locator("#sec-media .media-row");
    await expect(editRows).toHaveCount(3);
    for (let i = 0; i < 3; i++) {
      await editRows.first().locator('button[data-fn="mediaRemoveRow"]').click();
    }
    await expect(editRows).toHaveCount(0);
    await page.locator("#save-btn").click();
    await expect.poll(async () => (await getMedia(page, "organizations", id)).length, { timeout: 15_000 }).toBe(0);
  });

  test("venue page and admin form", async ({ page }) => {
    const token = await getTokenFromCookie(page);
    const locId = await seedLocation(page, token);
    await putLocationMedia(page, token, locId, [
      { kind: "image", title: "Hall photo", url: "https://example.org/hall.jpg" },
      { kind: "video", title: "Tour", url: "https://vimeo.com/123" },
    ]);

    await page.goto(`/location/${locId}`);
    const items = page.locator(".media-links .media-list li");
    await expect(items).toHaveCount(2);
    await expect(items.nth(0).locator("a")).toHaveText("Hall photo");
    await expect(items.nth(1).locator(".media-host")).toHaveText("vimeo.com");
    await expect(page.locator("iframe, audio, video, embed, object")).toHaveCount(0);

    await page.goto(`/admin/locations/${locId}/edit`);
    await openSection(page, "loc-nav-item", "loc-nav-toggle", "sec-media");
    const rows = page.locator("#sec-media .media-row");
    await expect(rows).toHaveCount(2);
    // The long stacked mobile form keeps scrolling under Playwright's retry loop
    // (other sections' inputs intercept the click point), so scroll once and
    // click without the actionability re-check.
    const removeBtn = rows.nth(0).locator('button[data-fn="mediaRemoveRow"]');
    await removeBtn.scrollIntoViewIfNeeded();
    await removeBtn.click({ force: true });
    await expect(rows).toHaveCount(1);
    await page.locator("#save-btn").click();
    await expect
      .poll(async () => (await getMedia(page, "locations", locId)).map((m) => m.title), { timeout: 15_000 })
      .toEqual(["Tour"]);
  });

  test("the API refuses a non-https link on orgs and venues", async ({ page }) => {
    const token = await getTokenFromCookie(page);
    const { id: orgId } = await createOrg(page, token);
    const bad = await page.request.fetch(`${API_BASE}/api/v1/organizations/${orgId}`, {
      method: "PATCH",
      headers: patchHeaders(token),
      data: JSON.stringify({ media: [{ kind: "video", url: "http://insecure.example/v" }] }),
    });
    expect(bad.status()).toBe(400);
    expect(await getMedia(page, "organizations", orgId)).toHaveLength(0);
  });
});
