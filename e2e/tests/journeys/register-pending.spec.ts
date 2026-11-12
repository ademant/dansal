import { test, expect } from "../../helpers/fixtures";
import { Browser, BrowserContext, Page } from "@playwright/test";
import { AUTH_FILE } from "../../helpers/auth";
import { recordCreated, tokenFromAuthFile } from "../../helpers/cleanup";

// The pending-registration journey (#1823): withdraw, the waiting view, the
// approved view, and the status endpoint the waiting page polls. Each test
// creates its own registration. The register endpoint allows 3 requests per
// 10 minutes per IP, so the spec runs on desktop only, without retries, and
// makes two registrations per run.

const API_BASE = process.env.API_URL ?? "http://localhost:8000";
const WEB_BASE = process.env.BASE_URL ?? "http://localhost:8080";
const PASSWORD = "E2e-Register-1823!";

test.use({ storageState: { cookies: [], origins: [] } });
test.describe.configure({ retries: 0 });
test.beforeEach(({}, testInfo) => {
  test.skip(
    testInfo.project.name !== "desktop",
    "register rate limit: 3 per 10 min per IP, one registration budget per run"
  );
});

async function createRegistration(
  browser: Browser,
  label: string
): Promise<{ email: string; pendingId: number; token: string; name: string }> {
  const ctx = await browser.newContext({ storageState: { cookies: [], origins: [] } });
  try {
    const stamp = `${Date.now()}-${Math.random().toString(36).slice(2, 8)}`;
    const email = `e2e-register-${stamp}@dansal.test`;
    // Display names are unique (case-insensitive), so each run needs its own.
    const name = `E2E Register ${label} ${stamp}`;
    const resp = await ctx.request.post(`${API_BASE}/api/v1/register`, {
      data: {
        email,
        reg_type: "new_org",
        org_name: name,
        org_contact_email: email,
        channel: "email",
        description: "e2e test #1823",
      },
    });
    expect(resp.status(), "registration request accepted").toBe(202);
    const body = await resp.json();
    const pendingId = Number(body.pending_id);
    recordCreated("registration", pendingId);
    return { email, pendingId, token: body.verification_token, name };
  } finally {
    await ctx.close();
  }
}

// Verifies the email and sets a password, leaving the page on the waiting view.
async function completeToWaiting(
  page: Page,
  token: string,
  name: string,
  email: string
): Promise<void> {
  await page.goto(`/register/verify/email/${token}`, { waitUntil: "domcontentloaded" });
  await page.fill("#field-auth-display-name", name);
  await page.fill("#field-auth-password", PASSWORD);
  await page.fill("#field-auth-password-confirm", PASSWORD);
  await page.locator("#btn-set-password").click();
  // The waiting view replaces the password form only once the set-password
  // request is done. Name that state in the failure, not just a missing element (#1830).
  await expect(
    page.locator("[data-pending-poll]"),
    `waiting view after setting the password (url ${page.url()})`
  ).toBeVisible({ timeout: 20_000 });
  // The password step creates the applicant's user account.
  recordCreated("user", 0, email);
}

// Approval creates the org (new_org) and links the applicant to it. Both are
// recorded so the teardown removes them; the user is removed by its own entry.
async function approveAsAdmin(browser: Browser, email: string, orgName: string): Promise<void> {
  const ctx: BrowserContext = await browser.newContext({ storageState: AUTH_FILE });
  try {
    const page = await ctx.newPage();
    page.on("dialog", (d) => d.accept());
    await page.goto("/admin/registrations", { waitUntil: "domcontentloaded" });
    const row = page.locator("tr").filter({ hasText: email });
    await expect(row).toHaveCount(1);
    await row
      .first()
      .locator("button[data-fn=approveReg], button[data-fn=approveRegWithRole]")
      .first()
      .click();
    await page.waitForLoadState("domcontentloaded");
  } finally {
    await ctx.close();
  }
  const token = tokenFromAuthFile(AUTH_FILE);
  const resp = await fetch(
    `${API_BASE}/api/v1/organizations?name=${encodeURIComponent(orgName)}&limit=100`,
    { headers: { Authorization: `Bearer ${token}` } }
  );
  const list = await resp.json();
  const match = Array.isArray(list) ? list.find((o: any) => o.name === orgName) : null;
  if (!match) throw new Error(`approved org not found: ${orgName}`);
  recordCreated("organisation", match.id);
}

async function statusState(page: Page): Promise<string> {
  const resp = await page.request.get(`${WEB_BASE}/register/status`);
  expect(resp.status()).toBe(200);
  return (await resp.json()).state;
}

test("withdraw asks first, then deletes a registration awaiting review", async ({
  browser,
  page,
}) => {
  const reg = await createRegistration(browser, "withdraw");
  await completeToWaiting(page, reg.token, reg.name, reg.email);

  // The waiting view is announced to screen readers.
  await expect(page.locator('[data-pending-poll][aria-live="polite"]')).toHaveCount(1);

  // Dismissing the confirmation keeps the registration.
  page.once("dialog", (d) => d.dismiss());
  await page.locator('form[action="/register/cancel"] button').click();
  await expect(page.locator("[data-pending-poll]")).toBeVisible();
  expect(await statusState(page)).toBe("waiting");

  // Accepting it deletes the registration: the API no longer knows it, and
  // the page shows the empty form again.
  page.once("dialog", (d) => d.accept());
  await page.locator('form[action="/register/cancel"] button').click();
  await page.waitForURL(/\/register$/);
  await expect(page.locator("[data-pending-poll]")).toHaveCount(0);
  expect(await statusState(page)).toBe("gone");
  const gone = await page.request.get(`${API_BASE}/api/v1/register/status/${reg.pendingId}`);
  expect(gone.status()).not.toBe(200);
});

test("approved registration: waiting -> approved, withdraw leaves it alone", async ({
  browser,
  page,
}) => {
  const reg = await createRegistration(browser, "approved");
  await completeToWaiting(page, reg.token, reg.name, reg.email);
  expect(await statusState(page)).toBe("waiting");

  await approveAsAdmin(browser, reg.email, reg.name);

  // The status endpoint reports the approval, and the approved view replaces
  // the waiting view after a reload (the page polls for this every 60 s).
  expect(await statusState(page)).toBe("approved");
  await page.reload({ waitUntil: "domcontentloaded" });
  await expect(page.locator("[data-pending-poll]")).toHaveCount(0);
  await expect(page.locator('.pending-reg-notice a[href="/login"]')).toBeVisible();

  // Withdrawing an approved registration sends the applicant to login and
  // keeps the approval.
  page.once("dialog", (d) => d.accept());
  await page.locator('form[action="/register/cancel"] button').click();
  await page.waitForURL(/\/login/);
  expect(await statusState(page)).toBe("approved");
  const still = await page.request.get(`${API_BASE}/api/v1/register/status/${reg.pendingId}`);
  expect(still.status()).toBe(200);
  expect((await still.json()).approved).toBe(true);
});
