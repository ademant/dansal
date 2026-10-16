import { test, expect } from "../../helpers/fixtures";
import type { Page } from "@playwright/test";
import { fullSeed, SeedResult, getTokenFromCookie } from "../../helpers/seed";
import { AUTH_FILE } from "../../helpers/auth";

// #1542: /admin/events/new?clone_from=N has no e2e coverage, and has
// regressed twice before — #765 (start/end time cleared along with the
// date) and #728 (a failed save on the clone form wiped what was typed).
//
// Uses fullSeed's already-seeded event as the clone source (per the e2e
// skill's fixture rules) rather than creating a dedicated one — the source
// event's actual current field values are fetched via the API and used as
// the expected baseline throughout, so these tests don't need to hardcode
// anything about seedEvents()'s own fixture shape.

const API_BASE = process.env.API_URL ?? "http://localhost:8000";

let seed: SeedResult;

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

function hhmm(iso: string): string {
  // Matches cmd/dansal_web's own isoTimeStr: "YYYY-MM-DDTHH:MM:SS..." -> "HH:MM".
  return iso.length >= 16 ? iso.slice(11, 16) : "";
}

function idFromEditURL(url: string): number {
  const m = url.match(/\/admin\/events\/(\d+)\/edit/);
  if (!m) throw new Error(`not an event edit URL: ${url}`);
  return parseInt(m[1], 10);
}

test.describe("Admin event clone (#1542)", () => {
  test.beforeAll(async ({ browser }) => {
    const context = await browser.newContext({ storageState: AUTH_FILE });
    const setupPage = await context.newPage();
    seed = await fullSeed(setupPage);
    await context.close();
  });

  test("clone prefills every field from the source, including start/end time, but leaves the date for the admin to pick (#765)", async ({
    page,
  }) => {
    const sourceId = seed.eventIds[0];
    const source = await authed(page, "GET", `/api/v1/events/${sourceId}`);

    await page.goto(`/events/${sourceId}`);
    await page.locator(`a[href="/admin/events/new?clone_from=${sourceId}"]`).click();
    await page.waitForURL(`**/admin/events/new?clone_from=${sourceId}`);

    await expect(page.locator("#title")).toHaveValue(source.title);
    await expect(page.locator("#description")).toHaveValue(source.description ?? "");
    await expect(page.locator("#url")).toHaveValue(source.url ?? "");
    await expect(page.locator("#start_time")).toHaveValue(hhmm(source.start_time));
    await expect(page.locator("#end_time")).toHaveValue(hhmm(source.end_time));
    if (source.location_id) {
      // A cloned event keeps the source's resolved location_id, carried via
      // the form's data-prefill-loc attribute (checked directly rather than
      // through the location picker's rendered chip/dropdown: that picker
      // is backed by dansal_web's own cached location list, which can take
      // up to ~1 min to see a location fullSeed() just created via the raw
      // API — e2e-testing skill, #1276 — an unrelated, already-known
      // latency this test shouldn't become flaky on).
      await expect(page.locator("form.evt-form")).toHaveAttribute(
        "data-prefill-loc",
        String(source.location_id)
      );
    }
    for (const tag of source.tags ?? []) {
      await expect(page.locator(`input[name="tags"][value="${tag}"]`)).toBeChecked();
    }

    // #765: the date must NOT be prefilled — it's the one field the admin
    // is forced to actively change (enforced by the clone-mode save gate
    // below), precisely so a clone can never silently land on the exact
    // same day as its source.
    await expect(page.locator("#date")).toHaveValue("");
    await expect(page.locator("#end_date")).toHaveValue("");
    await expect(page.locator("#save-btn")).toBeDisabled();
  });

  test("a failed save keeps the entered clone data (#728); fixing it and saving creates a new event, leaving the original untouched", async ({
    page,
  }) => {
    const sourceId = seed.eventIds[0];
    const source = await authed(page, "GET", `/api/v1/events/${sourceId}`);

    await page.goto(`/admin/events/new?clone_from=${sourceId}`);

    // Any date 10 days past the source's own is guaranteed to differ from
    // it, satisfying the clone-mode gate deterministically.
    const cloneDate = new Date(new Date(source.start_time).getTime() + 10 * 24 * 60 * 60 * 1000);
    const pad = (n: number) => String(n).padStart(2, "0");
    const cloneDateStr = `${cloneDate.getFullYear()}-${pad(cloneDate.getMonth() + 1)}-${pad(cloneDate.getDate())}`;
    await page.fill("#date", cloneDateStr);
    await page.locator("#date").dispatchEvent("change");
    await expect(page.locator("#save-btn")).toBeEnabled();

    // Something to verify survives the failed save below.
    const keptURL = "https://example.com/e2e-1542-kept";
    await page.fill("#url", keptURL);

    // Force a save error deterministically, without depending on any
    // network behavior: bypass the native/JS required-field gates on
    // #title (removing the attribute, then clearing the value) so the
    // *server's* own evt_title_required check is what actually rejects the
    // save — the save button would otherwise just stay disabled client-side
    // and never submit at all.
    await page.evaluate(() => document.getElementById("title")!.removeAttribute("required"));
    await page.fill("#title", "");
    await page.locator("#title").dispatchEvent("input");
    await page.locator("#save-btn").click();

    // p.form-error[role="alert"] is specific to the top-level save error —
    // .form-error alone also matches #extra-loc-warn (an always-present,
    // normally-hidden div reused for a different, location-specific notice).
    await expect(page.locator('p.form-error[role="alert"]')).toBeVisible();
    // #728: the error re-render must preserve exactly what was typed in
    // every OTHER field, not reset to the pristine clone prefill or (worse)
    // a blank form. (#title itself is expected to still be empty — that's
    // the one field that's actually invalid here.)
    await expect(page.locator("#url")).toHaveValue(keptURL);
    await expect(page.locator("#date")).toHaveValue(cloneDateStr);
    await expect(page.locator("#start_time")).toHaveValue(hhmm(source.start_time));

    // Fix the one bad field and save for real.
    const cloneTitle = `${source.title} (clone ${Date.now()})`;
    await page.fill("#title", cloneTitle);
    await page.locator("#save-btn").click();
    await page.waitForURL(/\/admin\/events\/\d+\/edit/);
    const newId = idFromEditURL(page.url());
    expect(newId).not.toBe(sourceId);

    const created = await authed(page, "GET", `/api/v1/events/${newId}`);
    expect(created.title).toBe(cloneTitle);
    expect(created.description ?? "").toBe(source.description ?? "");
    expect(hhmm(created.start_time)).toBe(hhmm(source.start_time));
    expect(created.start_time.slice(0, 10)).toBe(cloneDateStr);

    const original = await authed(page, "GET", `/api/v1/events/${sourceId}`);
    expect(original.title).toBe(source.title);
    expect(original.start_time).toBe(source.start_time);
    expect(original.description ?? "").toBe(source.description ?? "");
  });
});
