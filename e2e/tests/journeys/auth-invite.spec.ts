/**
 * Invitation-link registration (#1262).
 *
 * An admin creates a plain (no preset email) org-linked invite from
 * /admin/users, a brand-new anonymous visitor redeems it via the password
 * track on /invites/{token}, and the resulting account is logged in
 * immediately with org membership applied. A plain invite leaves the new
 * account email_verified=0 (useInvite, invites.go, only sets it when the
 * invite carries a preset_email) — that's verified via the same
 * self-service flow /settings offers: POST /api/v1/users/{id}/verify then
 * the mbox /verify/{token} link (not the suggest wizard's differently-
 * shaped /register/verify/email/{token}, which belongs to a separate
 * self-registration+approval feature this scenario doesn't touch).
 *
 * Reuse of the same invite token is asserted both at the UI layer (the
 * invite page itself reports expired once used_at is set) and directly
 * against the API (a second redemption attempt is rejected).
 */
import { test, expect } from "@playwright/test";
import { fullSeed, SeedResult, getTokenFromCookie, loginViaApi } from "../../helpers/seed";
import { clearMailbox, waitForVerifyToken } from "../../helpers/mailbox";
import { AUTH_FILE } from "../../helpers/auth";

const WEB_BASE = process.env.BASE_URL ?? "http://localhost:8080";
const API_BASE = process.env.API_URL ?? "http://localhost:8000";

function unique(name: string): string {
  return `${name} ${Date.now()}`;
}

let seed: SeedResult;

test.describe("Invitation-link registration", () => {
  test.beforeAll(async ({ browser }) => {
    const context = await browser.newContext({ storageState: AUTH_FILE });
    const setupPage = await context.newPage();
    seed = await fullSeed(setupPage);
    await context.close();
  });

  test("redeem a plain org invite, then verify email; the token can't be reused", async ({
    page,
    browser,
  }) => {
    const adminToken = await getTokenFromCookie(page);

    // ── Admin creates a plain (no preset email), org-linked invite ──────────
    const createResp = await page.request.fetch(`${WEB_BASE}/admin/invites/new`, {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      data: JSON.stringify({ type: "link", org_id: seed.orgId }),
    });
    expect(createResp.status()).toBe(200);
    const invite = await createResp.json();
    expect(invite.token).toBeTruthy();

    // ── A brand-new anonymous visitor redeems it via the password track ──────
    const email = `e2e-invite-${Date.now()}@example.com`;
    const password = "E2e-Invite-2026!";
    const displayName = unique("E2E Invitee");

    const inviteCtx = await browser.newContext({ storageState: { cookies: [], origins: [] } });
    try {
      const invitePage = await inviteCtx.newPage();
      await invitePage.goto(`${WEB_BASE}/invites/${invite.token}`);

      await invitePage.locator("#btn-password").click();
      await invitePage.fill("#inv-display-name", displayName);
      await invitePage.fill("#inv-email", email);
      await invitePage.fill("#inv-password", password);
      await invitePage.fill("#inv-password-confirm", password);
      await invitePage.locator("#inv-pw-form button[type=submit]").click();

      // The page shows a success message, then JS navigates after ~1.2s.
      await invitePage.waitForURL((url) => url.pathname === "/dashboard", { timeout: 10_000 });
      const cookies = await inviteCtx.cookies();
      expect(
        cookies.some((c) => c.name === "dsw_token"),
        "redeeming an invite must log the new account in immediately"
      ).toBe(true);

      // ── Org membership was applied ───────────────────────────────────────
      const membersResp = await page.request.fetch(
        `${API_BASE}/api/v1/organizations/${seed.orgId}/members`,
        { headers: { Authorization: `Bearer ${adminToken}` } }
      );
      const members = await membersResp.json();
      expect(
        Array.isArray(members) && members.some((m: any) => m.email === email),
        "the invited account must be a member of the invite's org"
      ).toBe(true);

      // ── Reuse: the UI reports the invite as expired ──────────────────────
      const reuseCtx = await browser.newContext({ storageState: { cookies: [], origins: [] } });
      try {
        const reusePage = await reuseCtx.newPage();
        await reusePage.goto(`${WEB_BASE}/invites/${invite.token}`);
        await expect(reusePage.locator(".invite-err")).toBeVisible();
      } finally {
        await reuseCtx.close();
      }

      // ── Reuse: the API itself rejects a second redemption ────────────────
      const reuseResp = await invitePage.request.fetch(`${WEB_BASE}/invites/${invite.token}/password`, {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        data: JSON.stringify({
          email: `e2e-invite-reuse-${Date.now()}@example.com`,
          display_name: unique("E2E Invitee Reuse"),
          password,
        }),
      });
      expect(reuseResp.status()).toBeGreaterThanOrEqual(400);

      // ── A plain invite leaves email_verified=0 — verify it, self-service ──
      const { context: selfCtx, page: selfPage, token: userToken } = await loginViaApi(
        browser,
        email,
        password
      );
      try {
        const meBefore = await (
          await selfPage.request.fetch(`${API_BASE}/api/v1/me`, {
            headers: { Authorization: `Bearer ${userToken}` },
          })
        ).json();
        expect(meBefore.email_verified, "a plain invite must not pre-verify the email").toBeFalsy();

        // waitForMailboxURL returns the *first* match in the file — clear it
        // first so a still-unconsumed /verify/ link from an earlier run (or
        // another spec sharing this mbox) can't be picked up instead of the
        // one this request is about to send.
        clearMailbox();
        const verifyReqResp = await selfPage.request.fetch(
          `${API_BASE}/api/v1/users/${meBefore.id}/verify`,
          {
            method: "POST",
            headers: { "Content-Type": "application/json", Authorization: `Bearer ${userToken}` },
            data: JSON.stringify({ channel: "email" }),
          }
        );
        expect(verifyReqResp.status()).toBe(204);

        const verifyToken = await waitForVerifyToken();
        const anonVerifyCtx = await browser.newContext({ storageState: { cookies: [], origins: [] } });
        try {
          const verifyPage = await anonVerifyCtx.newPage();
          await verifyPage.goto(`${WEB_BASE}/verify/${verifyToken}`);
          await expect(verifyPage.locator(".verify-ok")).toBeVisible();
        } finally {
          await anonVerifyCtx.close();
        }

        const meAfter = await (
          await selfPage.request.fetch(`${API_BASE}/api/v1/me`, {
            headers: { Authorization: `Bearer ${userToken}` },
          })
        ).json();
        expect(meAfter.email_verified, "the account must be verified after visiting the link").toBeTruthy();
      } finally {
        await selfCtx.close();
      }
    } finally {
      await inviteCtx.close();
    }
  });
});
