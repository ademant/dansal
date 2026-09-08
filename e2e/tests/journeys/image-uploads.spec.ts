/**
 * e2e: image-uploads
 *
 * Covers every image-upload path and its safety/fediverse contract:
 *
 *   API level
 *   ─────────
 *   • Format matrix: PNG, JPEG, WebP, AVIF input → AVIF served back
 *   • Resize: 1400×1400 source → served image ≤ 1024×1024 (verified with sharp)
 *   • Safety: file > 1 MB → 413; non-image bytes → 415; missing field → 400
 *   • Fediverse JPEG sibling: GET ?format=jpeg → image/jpeg (events only,
 *     per #1054)
 *   • Thumbnail variants: ?thumb=sq, ?thumb=wide → 200 after upload
 *   • All other entity types: musician, org, series, org-avatar,
 *     musician-avatar — upload + GET round-trip
 *
 *   UI level
 *   ────────
 *   • Event admin form: setInputFiles → #image-preview visible
 *   • Org edit form: upload banner → #existing-image visible after save
 *   • Musician edit form: upload image → #existing-image visible after save
 *   • AI-generated flag: check, save, reload → checkbox still checked
 *
 * Closes #1277
 */

import { test, expect } from "../../helpers/fixtures";
import { Page } from "@playwright/test";
import { fullSeed, SeedResult, getTokenFromCookie, apiPost } from "../../helpers/seed";
import { AUTH_FILE } from "../../helpers/auth";
import {
  randomFutureDate,
  isoDate,
  hhmm,
  EVENT_DATE_MIN_DAYS,
  EVENT_DATE_MAX_DAYS,
} from "../../fixtures/data";
import {
  makeImage,
  uploadImageAPI,
  fetchImageMeta,
  API_BASE,
  type ImageFormat,
} from "../../helpers/images";

let seed: SeedResult;
let token: string;

// A minimal event created via the API for a single test, avoiding the form
// overhead when only the image API path matters.
async function createMinimalEvent(page: Page, t: string): Promise<number> {
  const d = randomFutureDate(EVENT_DATE_MIN_DAYS, EVENT_DATE_MAX_DAYS);
  // start_time / end_time use the same ISO-string format as the seed fixtures.
  const start = new Date(d);
  start.setHours(20, 0, 0, 0);
  const end = new Date(d);
  end.setHours(22, 0, 0, 0);
  const data = await apiPost(page, "/api/v1/events", t, {
    title:      `E2E Img ${Date.now()}`,
    start_time: start.toISOString().replace(".000Z", ""),
    end_time:   end.toISOString().replace(".000Z", ""),
    published:  false,
  });
  return data.id;
}

// Create an event via the admin UI to get a proper event ID for UI tests.
async function createEventViaUI(page: Page): Promise<number> {
  const d = randomFutureDate(EVENT_DATE_MIN_DAYS, EVENT_DATE_MAX_DAYS);
  await page.goto("/admin/events/new");
  await page.fill('input[name="title"]', `E2E ImgUI ${Date.now()}`);
  await page.fill("#date", isoDate(d));
  await page.fill('input[name="start_time"]', hhmm(20, 0));
  await page.fill('input[name="end_time"]', hhmm(22, 0));
  await page.locator("#save-btn").click();
  await page.waitForURL(/\/admin\/events\/\d+\/edit/);
  const m = page.url().match(/\/admin\/events\/(\d+)\/edit/);
  if (!m) throw new Error("no event ID in URL");
  return parseInt(m[1], 10);
}

// Create a series with the minimum required fields.
async function createMinimalSeries(page: Page, t: string): Promise<number> {
  const data = await apiPost(page, "/api/v1/series", t, {
    title: `E2E Series ${Date.now()}`,
  });
  return data.id;
}

// ─────────────────────────────────────────────────────────────────────────────

test.describe("Image uploads", () => {
  test.beforeAll(async ({ browser }) => {
    const context = await browser.newContext({ storageState: AUTH_FILE });
    const setupPage = await context.newPage();
    seed = await fullSeed(setupPage);
    token = await getTokenFromCookie(setupPage);
    await context.close();
  });

  // ───────────────────────────── API: format matrix ──────────────────────────

  for (const fmt of ["png", "jpeg", "webp", "avif"] as ImageFormat[]) {
    test(`API: event image — ${fmt} input accepted and served as AVIF`, async ({ page }) => {
      const eventId = await createMinimalEvent(page, token);
      const buf = await makeImage(fmt, 120, 80);

      const up = await uploadImageAPI(page, token, `/api/v1/images/${eventId}`, buf, fmt);
      expect(up.status()).toBe(201);

      const resp = await page.request.fetch(`${API_BASE}/api/v1/images/${eventId}`, {
        headers: { Authorization: `Bearer ${token}` },
      });
      expect(resp.status()).toBe(200);
      // Server re-encodes everything to AVIF (default config); Content-Type
      // may fall back to image/jpeg on AVIF-incapable test instances.
      expect(resp.headers()["content-type"]).toMatch(/image\/(avif|jpeg)/);
    });
  }

  // ───────────────────────────── API: resize ─────────────────────────────────

  test("API: event image — oversized 1400×1400 is resized to ≤ 1024×1024", async ({ page }) => {
    const eventId = await createMinimalEvent(page, token);
    const buf = await makeImage("png", 1400, 1400);

    const up = await uploadImageAPI(page, token, `/api/v1/images/${eventId}`, buf, "png");
    expect(up.status()).toBe(201);

    const meta = await fetchImageMeta(
      page,
      `${API_BASE}/api/v1/images/${eventId}`
    );
    expect(meta.width).toBeLessThanOrEqual(1024);
    expect(meta.height).toBeLessThanOrEqual(1024);

    // JPEG sibling must also be resized.
    const jpegMeta = await fetchImageMeta(
      page,
      `${API_BASE}/api/v1/images/${eventId}?format=jpeg`
    );
    expect(jpegMeta.width).toBeLessThanOrEqual(1024);
    expect(jpegMeta.height).toBeLessThanOrEqual(1024);
  });

  // ──────────────────────────── API: safety ──────────────────────────────────

  test("API: event image — file > 1 MB returns 413", async ({ page }) => {
    const eventId = await createMinimalEvent(page, token);
    // 1.1 MB of random-ish bytes — the MaxBytesReader fires during
    // ParseMultipartForm before any image decoding occurs.
    const big = Buffer.alloc(1.1 * 1024 * 1024, 0xab);

    const resp = await page.request.fetch(`${API_BASE}/api/v1/images/${eventId}`, {
      method: "POST",
      headers: { Authorization: `Bearer ${token}` },
      multipart: {
        image: {
          name: "big.bin",
          mimeType: "image/png",
          buffer: big,
        },
      },
    });
    expect(resp.status()).toBe(413);
    const body = await resp.json();
    expect(body.error).toMatch(/too large/i);
  });

  test("API: event image — non-image bytes return 415", async ({ page }) => {
    const eventId = await createMinimalEvent(page, token);
    const notAnImage = Buffer.from("this is plain text, definitely not an image");

    const resp = await page.request.fetch(`${API_BASE}/api/v1/images/${eventId}`, {
      method: "POST",
      headers: { Authorization: `Bearer ${token}` },
      multipart: {
        image: {
          name: "text.png",
          mimeType: "image/png",
          buffer: notAnImage,
        },
      },
    });
    expect(resp.status()).toBe(415);
    const body = await resp.json();
    expect(body.error).toMatch(/not an image/i);
  });

  test("API: event image — missing 'image' field returns 400", async ({ page }) => {
    const eventId = await createMinimalEvent(page, token);

    const resp = await page.request.fetch(`${API_BASE}/api/v1/images/${eventId}`, {
      method: "POST",
      headers: {
        Authorization: `Bearer ${token}`,
        "Content-Type": "multipart/form-data; boundary=boundary",
      },
      // Send a valid-but-empty multipart body with the wrong field name.
      data: "--boundary\r\nContent-Disposition: form-data; name=\"wrong_field\"\r\n\r\nvalue\r\n--boundary--\r\n",
    });
    expect(resp.status()).toBe(400);
    const body = await resp.json();
    expect(body.error).toMatch(/missing|unreadable/i);
  });

  // ────────────────────────── API: fediverse JPEG sibling ────────────────────

  test("API: event image — ?format=jpeg returns image/jpeg after upload", async ({ page }) => {
    const eventId = await createMinimalEvent(page, token);
    const buf = await makeImage("png", 200, 150);
    const up = await uploadImageAPI(page, token, `/api/v1/images/${eventId}`, buf, "png");
    expect(up.status()).toBe(201);

    const jpeg = await page.request.fetch(
      `${API_BASE}/api/v1/images/${eventId}?format=jpeg`
    );
    expect(jpeg.status()).toBe(200);
    expect(jpeg.headers()["content-type"]).toContain("image/jpeg");
  });

  test("API: event image — ?thumb=sq and ?thumb=wide return 200 after upload", async ({ page }) => {
    const eventId = await createMinimalEvent(page, token);
    const buf = await makeImage("png", 600, 400);
    await uploadImageAPI(page, token, `/api/v1/images/${eventId}`, buf, "png");

    const sq = await page.request.fetch(`${API_BASE}/api/v1/images/${eventId}?thumb=sq`);
    expect(sq.status()).toBe(200);

    const wide = await page.request.fetch(`${API_BASE}/api/v1/images/${eventId}?thumb=wide`);
    expect(wide.status()).toBe(200);
  });

  // ───────────────────────── API: other entity types ─────────────────────────

  test("API: musician image — upload returns 204 and GET returns 200", async ({ page }) => {
    const buf = await makeImage("png", 200, 200);
    const up = await uploadImageAPI(
      page, token, `/api/v1/musician-images/${seed.musicianId}`, buf, "png"
    );
    expect(up.status()).toBe(204);

    const resp = await page.request.fetch(
      `${API_BASE}/api/v1/musician-images/${seed.musicianId}`
    );
    expect(resp.status()).toBe(200);
    expect(resp.headers()["content-type"]).toMatch(/image\//);
  });

  test("API: org image — upload returns 204 and GET returns 200", async ({ page }) => {
    const buf = await makeImage("png", 400, 200);
    const up = await uploadImageAPI(
      page, token, `/api/v1/org-images/${seed.orgId}`, buf, "png"
    );
    expect(up.status()).toBe(204);

    const resp = await page.request.fetch(
      `${API_BASE}/api/v1/org-images/${seed.orgId}`
    );
    expect(resp.status()).toBe(200);
    expect(resp.headers()["content-type"]).toMatch(/image\//);
  });

  test("API: series image — upload returns 204 and GET returns 200", async ({ page }) => {
    const seriesId = await createMinimalSeries(page, token);
    const buf = await makeImage("png", 400, 200);
    const up = await uploadImageAPI(
      page, token, `/api/v1/series-images/${seriesId}`, buf, "png"
    );
    expect(up.status()).toBe(204);

    const resp = await page.request.fetch(
      `${API_BASE}/api/v1/series-images/${seriesId}`
    );
    expect(resp.status()).toBe(200);
    expect(resp.headers()["content-type"]).toMatch(/image\//);
  });

  test("API: org avatar — upload returns 204 and GET returns 200", async ({ page }) => {
    // Avatars accept only JPEG/PNG (avatar_images.go), not the full matrix.
    const buf = await makeImage("jpeg", 200, 200);
    const up = await uploadImageAPI(
      page, token, `/api/v1/org-avatars/${seed.orgId}`, buf, "jpeg"
    );
    expect(up.status()).toBe(204);

    const resp = await page.request.fetch(
      `${API_BASE}/api/v1/org-avatars/${seed.orgId}`
    );
    expect(resp.status()).toBe(200);
    expect(resp.headers()["content-type"]).toMatch(/image\//);
  });

  test("API: musician avatar — upload returns 204 and GET returns 200", async ({ page }) => {
    const buf = await makeImage("jpeg", 200, 200);
    const up = await uploadImageAPI(
      page, token, `/api/v1/musician-avatars/${seed.musicianId}`, buf, "jpeg"
    );
    expect(up.status()).toBe(204);

    const resp = await page.request.fetch(
      `${API_BASE}/api/v1/musician-avatars/${seed.musicianId}`
    );
    expect(resp.status()).toBe(200);
    expect(resp.headers()["content-type"]).toMatch(/image\//);
  });

  // ───────────────────────────── UI: event form ──────────────────────────────

  test("UI: event admin form — file input shows #image-preview immediately", async ({ page }) => {
    const eventId = await createEventViaUI(page);
    await page.goto(`/admin/events/${eventId}/edit`);

    // The file input is hidden; setInputFiles still works on it.
    const buf = await makeImage("png", 300, 200);
    const input = page.locator('#image');
    await input.setInputFiles({
      name: "preview-test.png",
      mimeType: "image/png",
      buffer: buf,
    });

    // JS reads the file and sets #image-preview src via createObjectURL —
    // wait for it to become visible.
    await expect(page.locator('#image-preview')).toBeVisible({ timeout: 5000 });
  });

  test("UI: event admin form — image upload + save persists image", async ({ page }) => {
    const eventId = await createEventViaUI(page);
    await page.goto(`/admin/events/${eventId}/edit`);

    const buf = await makeImage("jpeg", 300, 200);
    await page.locator('#image').setInputFiles({
      name: "event-banner.jpg",
      mimeType: "image/jpeg",
      buffer: buf,
    });
    await expect(page.locator('#image-preview')).toBeVisible({ timeout: 5000 });

    // Save the form.
    await page.locator('#save-btn').click();
    await page.waitForURL(/\/admin\/events\/\d+\/edit/);

    // After redirect back to edit page, the existing image should be shown.
    await expect(page.locator('#existing-image')).toBeVisible();
  });

  test("UI: event admin form — AI-generated flag round-trips", async ({ page }) => {
    // Upload an image first so the AI-generated checkbox section renders.
    const eventId = await createMinimalEvent(page, token);
    const buf = await makeImage("png", 200, 150);
    const up = await uploadImageAPI(page, token, `/api/v1/images/${eventId}`, buf, "png");
    expect(up.status()).toBe(201);

    // Navigate to edit, check the flag, save.
    await page.goto(`/admin/events/${eventId}/edit`);
    const checkbox = page.locator('input[name="image_ai_generated"]');
    await expect(checkbox).toBeVisible();
    await checkbox.check();
    await page.locator('#save-btn').click();
    await page.waitForURL(/\/admin\/events\/\d+\/edit/);

    // Reload — checkbox must still be checked.
    await page.goto(`/admin/events/${eventId}/edit`);
    await expect(page.locator('input[name="image_ai_generated"]')).toBeChecked();
  });

  // ──────────────────────────── UI: org banner ───────────────────────────────

  test("UI: org edit form — banner upload persists image", async ({ page }) => {
    await page.goto(`/admin/organizations/${seed.orgId}/edit`);

    const buf = await makeImage("png", 400, 200);
    await page.locator('input[type="file"]#image').setInputFiles({
      name: "org-banner.png",
      mimeType: "image/png",
      buffer: buf,
    });

    await page.locator('#save-btn').click();
    // org save redirects to /admin/organizations/{id}/edit (with optional query params)
    await page.waitForURL(/\/admin\/organizations\/\d+\/edit/);
    await expect(page.locator('#existing-image')).toBeVisible();
  });

  // ───────────────────────────── UI: musician image ──────────────────────────

  test("UI: musician edit form — image upload persists image", async ({ page }) => {
    await page.goto(`/admin/musicians/${seed.musicianId}/edit`);

    const buf = await makeImage("png", 300, 300);
    await page.locator('input[type="file"]#image').setInputFiles({
      name: "musician-photo.png",
      mimeType: "image/png",
      buffer: buf,
    });

    // Musician template has no #save-btn — it's a plain submit button on
    // form#mus-form.
    await page.locator('button[type="submit"][form="mus-form"]').click();
    await page.waitForURL(/\/admin\/musicians\/\d+\/edit/);
    await expect(page.locator('#existing-image')).toBeVisible();
  });
});
