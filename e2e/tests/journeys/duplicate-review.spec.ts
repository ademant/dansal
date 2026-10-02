import { test, expect } from "../../helpers/fixtures";
import type { Page } from "@playwright/test";
import { getTokenFromCookie } from "../../helpers/seed";
import { AUTH_FILE } from "../../helpers/auth";
import { randomFutureDate, isoDate, EVENT_DATE_MIN_DAYS, EVENT_DATE_MAX_DAYS } from "../../fixtures/data";

// #1427: compare & resolve a flagged possible-duplicate pair on
// /admin/duplicates/{id} — Save (fix the date), Accept, Merge with a chosen
// survivor — plus the entry points (list "Solve" link, edit-form check
// dialog, event-page banner).
//
// A flagged pair is produced through the real dedup path: two events from the
// same feed source, < 3h apart, with fuzzy-overlapping titles and *no* venue
// (a shared venue would hit tier 3 and merge instead) — dedup tier 5 inserts
// the second one and flags both.

const API_BASE = process.env.API_URL ?? "http://localhost:8000";

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

const created: number[] = [];

async function flaggedPair(page: Page): Promise<{ a: number; b: number; title: string }> {
  const sources = await authed(page, "GET", "/api/v1/feeds");
  test.skip(!Array.isArray(sources) || sources.length === 0, "needs at least one fetch source on the target to build a tier-5 pair");
  const sourceId = sources[0].id;
  const d = isoDate(randomFutureDate(EVENT_DATE_MIN_DAYS, EVENT_DATE_MAX_DAYS));
  const title = `E2E Duplicate Probe ${Date.now()}`;
  const mk = async (t: string, start: string, end: string) => {
    const res = await authed(page, "POST", "/api/v1/events", {
      title: t,
      start_time: `${d}T${start}:00`,
      end_time: `${d}T${end}:00`,
      tags: ["bal-folk"],
      fetch_source_id: sourceId,
    });
    const id = (Array.isArray(res) ? res[0] : res).id as number;
    created.push(id);
    return id;
  };
  const a = await mk(title, "20:00", "23:00");
  const b = await mk(`ABGESAGT – ${title}`, "20:30", "23:30");
  const check = await authed(page, "GET", `/api/v1/events/${b}/duplicate-check`);
  expect(check.flagged, "tier 5 should have flagged the second event").toBe(true);
  expect(check.partner_id).toBe(a);
  return { a, b, title };
}

async function flagged(page: Page, id: number): Promise<boolean> {
  return (await authed(page, "GET", `/api/v1/events/${id}/duplicate-check`)).flagged;
}

test.describe("Duplicate review (#1427)", () => {
  test.afterAll(async ({ browser }) => {
    // Best-effort cleanup of the probe events this run created.
    const ctx = await browser.newContext({ storageState: AUTH_FILE });
    const p = await ctx.newPage();
    for (const id of created) {
      await authed(p, "DELETE", `/api/v1/events/${id}`).catch(() => {});
    }
    await ctx.close();
  });

  test("Save stays disabled until the date no longer collides, then clears both flags", async ({ page }) => {
    const { a, b } = await flaggedPair(page);
    await page.goto(`/admin/duplicates/${b}`);
    const save = page.locator("#dup-save");
    await expect(save).toBeDisabled();

    // A and B are told apart by colour on both layouts.
    const bg = (sel: string) => page.locator(sel).first().evaluate((el) => getComputedStyle(el).backgroundColor);
    expect(await bg("td.dup-a")).not.toBe(await bg("td.dup-b"));

    // Same day, 1h later: still colliding.
    const bStart = page.locator('input[name="b_start"]');
    await bStart.fill("21:00");
    await bStart.dispatchEvent("change");
    await expect(save).toBeDisabled();
    await expect(page.locator("#dup-save-hint")).toBeVisible();

    // Copy/paste-date fix: move B to the next day → Save enabled.
    const bDate = page.locator('input[name="b_date"]');
    const next = await bDate.inputValue().then((v) => {
      const d = new Date(v + "T12:00:00");
      d.setDate(d.getDate() + 1);
      return isoDate(d);
    });
    await bDate.fill(next);
    await bDate.dispatchEvent("change");
    await expect(save).toBeEnabled();

    await save.click();
    await page.waitForURL(/\/admin\/events\?flagged=1/);
    expect(await flagged(page, a)).toBe(false);
    expect(await flagged(page, b)).toBe(false);
  });

  test("Accept keeps both events and removes the flag", async ({ page }) => {
    const { a, b } = await flaggedPair(page);
    await page.goto(`/admin/duplicates/${a}`);
    await page.locator('button[form="dup-accept-form"]').click();
    await page.waitForURL(/\/admin\/events\?flagged=1/);
    expect(await flagged(page, a)).toBe(false);
    expect(await flagged(page, b)).toBe(false);
    // both still exist
    await authed(page, "GET", `/api/v1/events/${a}`);
    await authed(page, "GET", `/api/v1/events/${b}`);
  });

  test("Merge keeps the event marked 'keep' and deletes the other", async ({ page }) => {
    const { a, b } = await flaggedPair(page);
    await page.goto(`/admin/duplicates/${b}`);
    // Page shows B (the flagged one) as A-column; keep the original (#a).
    await page.locator(`input[name="keep_id"][value="${a}"]`).check();
    page.once("dialog", (d) => d.accept()); // data-confirm on the merge form
    await page.locator('button[form="dup-merge-form"]').click();
    await page.waitForURL(/\/admin\/events/);
    await authed(page, "GET", `/api/v1/events/${a}`);
    const gone = await page.request.fetch(`${API_BASE}/api/v1/events/${b}`, {
      headers: { Authorization: `Bearer ${await getTokenFromCookie(page)}` },
    });
    expect(gone.status()).toBe(404);
  });

  test("entry points: list link, event-page banner, edit-form check dialog", async ({ page }) => {
    const { a, b } = await flaggedPair(page);

    await page.goto(`/events/${b}`);
    await expect(page.locator(".evt-dup-banner a[href='/admin/duplicates/" + b + "']")).toBeVisible();

    await page.goto(`/admin/events/${b}/edit`);
    await expect(page.locator("#dup-note")).toBeVisible();
    await page.locator("#dup-check-btn").click();
    const dlg = page.locator("#dup-check-dialog");
    await expect(dlg).toBeVisible();
    // Still colliding → the conflict is listed and "Cleaned" is not offered.
    await expect(dlg.locator(".dup-check-body")).toContainText(`#${a}`);
    await expect(dlg.locator(".dup-check-clean")).toBeHidden();
    await dlg.locator(".dup-check-close").click();
    await expect(dlg).toBeHidden();

    await page.goto(`/admin/events?flagged=1&include_past=1`);
    await expect(page.locator(`a.dup-solve-link[href="/admin/duplicates/${b}"]`)).toHaveCount(1);
  });
});
