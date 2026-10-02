/**
 * Musician picture gallery (#1362): pictures are uploaded on the admin form,
 * edited in place (caption, order, remove) and shown on the public page as a
 * thumbnail grid served from our own domain.
 */
import { test, expect } from "../../helpers/fixtures";
import type { Page } from "@playwright/test";
import { getTokenFromCookie } from "../../helpers/seed";

const API_BASE = process.env.API_URL ?? "http://localhost:8000";

// 32×24 solid-colour PNGs (red, blue).
const RED_PNG =
  "iVBORw0KGgoAAAANSUhEUgAAACAAAAAYCAIAAAAUMWhjAAAAJElEQVR4nGM4oaFBU8QwasGoBaMWjFowasGoBaMWjFowNCwAACkKSC62k8hMAAAAAElFTkSuQmCC";
const BLUE_PNG =
  "iVBORw0KGgoAAAANSUhEUgAAACAAAAAYCAIAAAAUMWhjAAAAJElEQVR4nGPQCDhBU8QwasGoBaMWjFowasGoBaMWjFowNCwAACjMwC6OVx5SAAAAAElFTkSuQmCC";

async function api(page: Page, method: string, path: string, body?: unknown): Promise<any> {
  const token = await getTokenFromCookie(page);
  const resp = await page.request.fetch(`${API_BASE}${path}`, {
    method,
    headers: { Authorization: `Bearer ${token}`, "Content-Type": "application/json" },
    data: body === undefined ? undefined : JSON.stringify(body),
  });
  if (!resp.ok()) throw new Error(`${method} ${path} → ${resp.status()}: ${await resp.text()}`);
  return resp.status() === 204 ? null : resp.json();
}

async function gallery(page: Page, id: number): Promise<Array<{ id: number; caption: string }>> {
  const resp = await page.request.fetch(`${API_BASE}/api/v1/musicians/${id}`);
  return (await resp.json()).gallery ?? [];
}

async function openGallery(page: Page): Promise<void> {
  const sec = page.locator("#sec-gallery");
  if (await sec.isVisible()) return;
  const navItem = page.locator('.evt-nav-item[data-target="sec-gallery"]');
  if (!(await navItem.isVisible())) await page.locator("#mus-nav-toggle").click();
  await navItem.click();
  await expect(sec).toBeVisible();
}

async function save(page: Page): Promise<void> {
  await page.locator('button[type="submit"][form="mus-form"]').click();
  await page.waitForURL(/\/admin\/musicians/);
}

test("upload, caption, reorder and remove gallery pictures; public grid", async ({ page }) => {
  const created = await api(page, "POST", "/api/v1/musicians", { bandname: `E2E Gallery Band ${Date.now()}` });
  const id = (Array.isArray(created) ? created[0] : created).id as number;
  try {
    // Upload two pictures through the form.
    await page.goto(`/admin/musicians/${id}/edit`);
    await openGallery(page);
    await page.locator("#gallery-new").setInputFiles([
      { name: "red.png", mimeType: "image/png", buffer: Buffer.from(RED_PNG, "base64") },
      { name: "blue.png", mimeType: "image/png", buffer: Buffer.from(BLUE_PNG, "base64") },
    ]);
    await save(page);
    await expect.poll(async () => (await gallery(page, id)).length, { timeout: 30_000 }).toBe(2);
    const [red, blue] = await gallery(page, id);

    // Caption the blue one, move it first, save.
    await page.goto(`/admin/musicians/${id}/edit`);
    await openGallery(page);
    const rows = page.locator("#gallery-edit li");
    await expect(rows).toHaveCount(2);
    await expect(page.locator("#sec-gallery .gallery-count")).toContainText("2");
    await page.locator(`input[name="gallery_caption_${blue.id}"]`).fill("Blue stage");
    await rows.nth(1).locator(".gallery-up").click();
    await expect(rows.nth(0).locator(`input[name="gallery_id"]`)).toHaveValue(String(blue.id));
    await save(page);
    await expect
      .poll(async () => (await gallery(page, id)).map((g) => [g.id, g.caption]), { timeout: 15_000 })
      .toEqual([
        [blue.id, "Blue stage"],
        [red.id, ""],
      ]);

    // Public page: thumbnails in order, caption, served from our own origin.
    await page.goto(`/musicians/${id}`);
    const figs = page.locator(".musician-gallery figure");
    await expect(figs).toHaveCount(2);
    await expect(figs.nth(0).locator("figcaption")).toHaveText("Blue stage");
    const img = figs.nth(0).locator("img");
    await expect(img).toHaveAttribute("alt", "Blue stage");
    await expect(img).toHaveAttribute("src", new RegExp(`^/api/v1/gallery-images/${blue.id}\\?thumb=sq$`));
    await expect.poll(() => img.evaluate((el: HTMLImageElement) => el.complete && el.naturalWidth > 0)).toBe(true);
    await expect(figs.nth(0).locator("a")).toHaveAttribute("href", `/api/v1/gallery-images/${blue.id}`);

    // Remove the red one.
    await page.goto(`/admin/musicians/${id}/edit`);
    await openGallery(page);
    await page.locator(`input[name="gallery_remove_${red.id}"]`).check();
    await expect(page.locator("#gallery-edit li").nth(1)).toHaveClass(/is-removed/);
    await save(page);
    await expect.poll(async () => (await gallery(page, id)).map((g) => g.id), { timeout: 15_000 }).toEqual([blue.id]);
    const gone = await page.request.get(`/api/v1/gallery-images/${red.id}`);
    expect(gone.status()).toBe(404);
  } finally {
    await api(page, "DELETE", `/api/v1/musicians/${id}`).catch(() => {});
  }
});

test("picking more files than fit warns before saving", async ({ page }) => {
  const created = await api(page, "POST", "/api/v1/musicians", { bandname: `E2E Gallery Limit ${Date.now()}` });
  const id = (Array.isArray(created) ? created[0] : created).id as number;
  try {
    await page.goto(`/admin/musicians/${id}/edit`);
    await openGallery(page);
    const max = Number(await page.locator("#sec-gallery").getAttribute("data-gallery-max"));
    test.skip(!max || max > 20, "gallery limit unknown or too large to exercise");
    const files = Array.from({ length: max + 1 }, (_, i) => ({
      name: `p${i}.png`,
      mimeType: "image/png",
      buffer: Buffer.from(RED_PNG, "base64"),
    }));
    await page.locator("#gallery-new").setInputFiles(files);
    const warn = page.locator("#gallery-too-many");
    await expect(warn).toBeVisible();
    await expect(warn).toContainText(String(max));
    await page.locator("#gallery-new").setInputFiles(files.slice(0, 1));
    await expect(warn).toBeHidden();
  } finally {
    await api(page, "DELETE", `/api/v1/musicians/${id}`).catch(() => {});
  }
});
