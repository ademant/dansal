import { test, expect } from "../../helpers/fixtures";
import type { Page } from "@playwright/test";
import { getTokenFromCookie } from "../../helpers/seed";
import { isoDate } from "../../fixtures/data";

// #1413: unusual event dates (before today, or > 2 years ahead) are flagged
// while editing and confirmed before they are saved/published:
//  - admin form: amber date field + note, and a "back to edit / save anyway"
//    dialog on save (nothing is saved here — "back" is used)
//  - an unpublished past event: notice on /admin/events, badge in the draft
//    banner, confirm dialog on publish, flash with an edit link afterwards

const API_BASE = process.env.API_URL ?? "http://localhost:8000";

function daysFromNow(n: number): string {
  const d = new Date();
  d.setDate(d.getDate() + n);
  return isoDate(d);
}

async function authed(page: Page, method: string, path: string, body?: unknown): Promise<any> {
  const token = await getTokenFromCookie(page);
  const resp = await page.request.fetch(`${API_BASE}${path}`, {
    method,
    headers: { Authorization: `Bearer ${token}`, "Content-Type": "application/json" },
    data: body === undefined ? undefined : JSON.stringify(body),
  });
  if (!resp.ok()) throw new Error(`${method} ${path} → ${resp.status()}: ${await resp.text()}`);
  return resp.status() === 204 ? null : resp.json();
}

test("admin form: past/far dates are flagged and saving asks first", async ({ page }) => {
  await page.goto("/admin/events/new");
  const date = page.locator("#date");
  const note = page.locator("#date-warn-note");

  await date.fill(daysFromNow(-20));
  await date.dispatchEvent("change");
  await expect(date).toHaveClass(/date-warn/);
  await expect(note).toBeVisible();

  await date.fill(daysFromNow(3 * 365));
  await date.dispatchEvent("change");
  await expect(note).toBeVisible();

  await date.fill(daysFromNow(10));
  await date.dispatchEvent("change");
  await expect(date).not.toHaveClass(/date-warn/);
  await expect(note).toBeHidden();

  // Saving a past date opens the dialog; "back to edit" saves nothing.
  await page.fill("#title", `E2E unusual date ${Date.now()}`);
  await date.fill(daysFromNow(-20));
  await date.dispatchEvent("change");
  await expect(page.locator("#save-btn")).toBeEnabled();
  await page.locator("#save-btn").click();
  const dlg = page.locator("#unusual-date-dialog");
  await expect(dlg).toBeVisible();
  await dlg.locator(".ud-back").click();
  await expect(dlg).toBeHidden();
  await expect(page).toHaveURL(/\/admin\/events\/new/);
  await expect(date).toBeFocused();
});

test("unpublished past event: notice, badge, publish dialog and flash", async ({ page }) => {
  const title = `E2E past suggestion ${Date.now()}`;
  const day = daysFromNow(-30);
  const res = await authed(page, "POST", "/api/v1/events", {
    title,
    start_time: `${day}T20:00:00`,
    end_time: `${day}T23:00:00`,
    tags: ["bal-folk"],
  });
  const id = (Array.isArray(res) ? res[0] : res).id as number;
  try {
    // An admin's API create is published right away — unpublish it, like a
    // pending suggestion.
    await page.request.fetch(`${API_BASE}/api/v1/events/${id}`, {
      method: "PATCH",
      headers: { Authorization: `Bearer ${await getTokenFromCookie(page)}`, "Content-Type": "application/merge-patch+json" },
      data: JSON.stringify({ is_published: false }),
    });
    expect((await authed(page, "GET", `/api/v1/events/${id}`)).is_published).toBe(false);

    await page.goto("/admin/events");
    await expect(page.locator(".unpublished-past-notice")).toBeVisible();

    await page.goto(`/events/${id}`);
    const banner = page.locator(".evt-draft-banner");
    await expect(banner.locator(".badge-unusual-date")).toBeVisible();

    await banner.locator('form[data-date-check] button[type="submit"]').first().click();
    const dlg = page.locator("#unusual-date-dialog");
    await expect(dlg).toBeVisible();
    await dlg.locator(".ud-ok").click();

    const flash = page.locator(".publish-unusual-flash");
    await expect(flash).toBeVisible();
    await expect(flash).toContainText(title);
    await expect(flash.locator(`a[href="/admin/events/${id}/edit"]`)).toBeVisible();
    expect((await authed(page, "GET", `/api/v1/events/${id}`)).is_published).toBe(true);
  } finally {
    await authed(page, "DELETE", `/api/v1/events/${id}`).catch(() => {});
  }
});
