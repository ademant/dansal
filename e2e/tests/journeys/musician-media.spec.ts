/**
 * Musician media links (#1360): a list of external https links (video, audio,
 * image, other) shown as plain links on the public page and edited as
 * repeatable rows on the admin form. Nothing is embedded or hotlinked, so the
 * page must contain no iframe/audio/video/img pointing at those URLs.
 */
import { test, expect } from "../../helpers/fixtures";
import { getTokenFromCookie } from "../../helpers/seed";

const API_BASE = process.env.API_URL ?? "http://localhost:8000";

async function createMusician(
  page: import("@playwright/test").Page,
  token: string,
  media: object[] | undefined
): Promise<number> {
  const resp = await page.request.fetch(`${API_BASE}/api/v1/musicians`, {
    method: "POST",
    headers: { "Content-Type": "application/json", Authorization: `Bearer ${token}` },
    data: JSON.stringify({ bandname: `E2E Media Band ${Date.now()}`, ...(media ? { media } : {}) }),
  });
  expect(resp.status()).toBe(201);
  const body = await resp.json();
  return (Array.isArray(body) ? body[0] : body).id as number;
}

async function getMedia(page: import("@playwright/test").Page, id: number) {
  const resp = await page.request.fetch(`${API_BASE}/api/v1/musicians/${id}`);
  return ((await resp.json()).media ?? []) as Array<{ kind: string; title: string; url: string }>;
}

test.describe("Musician media links (#1360)", () => {
  test("public page lists the links in order as plain external links, nothing embedded", async ({ page }) => {
    const token = await getTokenFromCookie(page);
    const id = await createMusician(page, token, [
      { kind: "video", title: "Live at the Bal", url: "https://www.youtube.com/watch?v=abc123" },
      { kind: "audio", title: "", url: "https://example.org/track.mp3" },
    ]);

    await page.goto(`/musicians/${id}`);
    const section = page.locator(".media-links");
    await expect(section).toBeVisible();
    await expect(section.locator(".media-notice")).toBeVisible();

    const items = section.locator(".media-list li");
    await expect(items).toHaveCount(2);

    const first = items.nth(0).locator("a");
    await expect(first).toHaveText("Live at the Bal");
    await expect(first).toHaveAttribute("href", "https://www.youtube.com/watch?v=abc123");
    await expect(first).toHaveAttribute("target", "_blank");
    await expect(first).toHaveAttribute("rel", /noopener/);
    await expect(first).toHaveAttribute("rel", /nofollow/);
    await expect(items.nth(0).locator(".media-host")).toHaveText("youtube.com");

    // No title -> falls back to the host.
    await expect(items.nth(1).locator("a")).toHaveText("example.org");

    // Privacy: no embeds, no third-party media loaded on page view.
    await expect(page.locator("iframe, audio, video, embed, object")).toHaveCount(0);
    await expect(page.locator('img[src*="youtube"], img[src*="example.org"]')).toHaveCount(0);
  });

  test("a musician with no links shows no media section", async ({ page }) => {
    const token = await getTokenFromCookie(page);
    const id = await createMusician(page, token, undefined);
    await page.goto(`/musicians/${id}`);
    await expect(page.locator(".media-links")).toHaveCount(0);
  });

  test("admin form: add, reorder, remove and clear links", async ({ page }) => {
    const token = await getTokenFromCookie(page);
    const id = await createMusician(page, token, [
      { kind: "video", title: "One", url: "https://example.org/1" },
      { kind: "audio", title: "Two", url: "https://example.org/2" },
    ]);

    await page.goto(`/admin/musicians/${id}/edit`);
    // Existing links open the section; the rows are prefilled.
    const rows = page.locator("#sec-media .media-row");
    await expect(rows).toHaveCount(2);
    await expect(rows.nth(0).locator('input[name="media_title"]')).toHaveValue("One");

    // Move "Two" above "One", add a third, save.
    await rows.nth(1).locator('button[data-fn="mediaMoveRow"]').first().click();
    await expect(rows.nth(0).locator('input[name="media_title"]')).toHaveValue("Two");

    await page.locator("#sec-media .media-editor > button", { hasText: "+" }).click();
    await expect(rows).toHaveCount(3);
    const added = rows.nth(2);
    await added.locator('select[name="media_kind"]').selectOption("image");
    await added.locator('input[name="media_title"]').fill("Photo");
    await added.locator('input[name="media_url"]').fill("https://example.org/p.jpg");

    await page.locator('button[type="submit"][form="mus-form"]').click();
    await page.waitForURL(/\/admin\/musicians/);
    await expect
      .poll(async () => (await getMedia(page, id)).map((m) => [m.kind, m.title]), { timeout: 15_000 })
      .toEqual([
        ["audio", "Two"],
        ["video", "One"],
        ["image", "Photo"],
      ]);

    // Remove every row -> the list is cleared (an empty form means "no links").
    await page.goto(`/admin/musicians/${id}/edit`);
    const editRows = page.locator("#sec-media .media-row");
    await expect(editRows).toHaveCount(3);
    for (let i = 0; i < 3; i++) {
      await editRows.first().locator('button[data-fn="mediaRemoveRow"]').click();
    }
    await expect(editRows).toHaveCount(0);
    await page.locator('button[type="submit"][form="mus-form"]').click();
    await page.waitForURL(/\/admin\/musicians/);
    await expect.poll(async () => (await getMedia(page, id)).length, { timeout: 15_000 }).toBe(0);
  });

  test("a non-https link is refused by the API and by the form", async ({ page }) => {
    const token = await getTokenFromCookie(page);
    const id = await createMusician(page, token, undefined);

    const bad = await page.request.fetch(`${API_BASE}/api/v1/musicians/${id}`, {
      method: "PUT",
      headers: { "Content-Type": "application/json", Authorization: `Bearer ${token}` },
      data: JSON.stringify({ bandname: "X", media: [{ kind: "video", url: "http://insecure.example/v" }] }),
    });
    expect(bad.status()).toBe(400);
    expect(await getMedia(page, id)).toHaveLength(0);

    await page.goto(`/admin/musicians/${id}/edit`);
    // On narrow viewports the section nav is collapsed behind a toggle.
    const navItem = page.locator('.evt-nav-item[data-target="sec-media"]');
    if (!(await navItem.isVisible())) {
      await page.locator("#mus-nav-toggle").click();
    }
    await navItem.click();
    await page.locator("#sec-media .media-editor > button", { hasText: "+" }).click();
    const url = page.locator('#sec-media input[name="media_url"]').last();
    await url.fill("http://insecure.example/v");
    // The pattern attribute blocks a non-https URL before it ever leaves the browser.
    expect(await url.evaluate((el: HTMLInputElement) => el.validity.patternMismatch)).toBe(true);
  });
});
