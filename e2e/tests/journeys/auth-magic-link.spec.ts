/**
 * Passwordless magic-link login (#1262).
 *
 * requestMagicLogin (magic.go) only sends a link for an email-verified
 * user, so this first gets MAGIC_USER (fixtures/data.ts) into that state
 * using the same self-service flow a real user follows from /settings:
 * POST /api/v1/users/{id}/verify (channel=email, self-call — no admin
 * needed) sends a /verify/{token} link; visiting it flips email_verified.
 * That bootstrap is unrelated to magic links themselves but is the
 * documented precondition (issue #1262's own suggested approach).
 *
 * The actual scenario: request a magic link from /login, receive it via
 * the fake-sendmail mbox, and use it to establish a session — then confirm
 * the same link can't be replayed.
 */
import { test, expect } from "@playwright/test";
import { MAGIC_USER } from "../../fixtures/data";
import { createUser, loginViaApi } from "../../helpers/seed";
import { clearMailbox, waitForMagicLoginToken, waitForVerifyToken } from "../../helpers/mailbox";

const WEB_BASE = process.env.BASE_URL ?? "http://localhost:8080";
const API_BASE = process.env.API_URL ?? "http://localhost:8000";

function extractHidden(html: string, name: string): string {
  const m = html.match(new RegExp(`name="${name}"\\s+value="([^"]*)"`));
  if (!m) throw new Error(`extractHidden: no hidden field named ${name} found`);
  return m[1];
}

test("magic link: request, receive by email, log in, then can't replay", async ({ browser }) => {
  const userId = createUser(MAGIC_USER.email, MAGIC_USER.password, "user");
  clearMailbox();

  // ── Bootstrap: get MAGIC_USER to email_verified=1 (self-service) ──────────
  const { context: selfCtx, page: selfPage, token } = await loginViaApi(
    browser,
    MAGIC_USER.email,
    MAGIC_USER.password
  );
  try {
    const verifyReqResp = await selfPage.request.fetch(`${API_BASE}/api/v1/users/${userId}/verify`, {
      method: "POST",
      headers: { "Content-Type": "application/json", Authorization: `Bearer ${token}` },
      data: JSON.stringify({ channel: "email" }),
    });
    expect(verifyReqResp.status()).toBe(204);

    const verifyToken = await waitForVerifyToken();

    // A fresh, anonymous visit to the verify link — a real recipient may
    // click it from anywhere, not necessarily still signed in.
    const anonVerifyCtx = await browser.newContext({ storageState: { cookies: [], origins: [] } });
    try {
      const verifyPage = await anonVerifyCtx.newPage();
      await verifyPage.goto(`${WEB_BASE}/verify/${verifyToken}`);
      await expect(verifyPage.locator(".verify-ok")).toBeVisible();
    } finally {
      await anonVerifyCtx.close();
    }

    const meResp = await selfPage.request.fetch(`${API_BASE}/api/v1/me`, {
      headers: { Authorization: `Bearer ${token}` },
    });
    const me = await meResp.json();
    expect(me.email_verified, "bootstrap must leave the user email-verified").toBeTruthy();
  } finally {
    await selfCtx.close();
  }

  // ── The actual scenario: request and use a magic link ─────────────────────
  clearMailbox();
  const anonCtx = await browser.newContext({ storageState: { cookies: [], origins: [] } });
  try {
    const anonPage = await anonCtx.newPage();

    const loginHtml = await (await anonPage.request.fetch(`${WEB_BASE}/login`)).text();
    const formToken = extractHidden(loginHtml, "_form_token");

    // consumeFormToken enforces a 100ms *minimum* age on login-page tokens.
    await new Promise((r) => setTimeout(r, 200));

    const magicReqResp = await anonPage.request.fetch(`${WEB_BASE}/magic`, {
      method: "POST",
      form: {
        _form_token: formToken,
        phone2: "",
        identifier: MAGIC_USER.email,
        channel: "email",
      },
    });
    expect(magicReqResp.url()).toBe(`${WEB_BASE}/login?magic_sent=email`);

    const magicToken = await waitForMagicLoginToken();

    await anonPage.goto(`${WEB_BASE}/login/magic/${magicToken}`);
    await anonPage.waitForURL((url) => url.pathname === "/dashboard");
    const cookies = await anonCtx.cookies();
    expect(
      cookies.some((c) => c.name === "dsw_token"),
      "a session cookie must be set after using the magic link"
    ).toBe(true);

    // ── Replay: the same link must not work a second time (one-time use) ─────
    await anonCtx.clearCookies();
    await anonPage.goto(`${WEB_BASE}/login/magic/${magicToken}`);
    // .login-error is reused by two hidden placeholder elements
    // (#passkey-msg, #wa-totp-msg) — scope to the rendered alert paragraph.
    await expect(anonPage.locator('p.login-error[role="alert"]')).toBeVisible();
    expect(anonPage.url()).toContain("/login");
  } finally {
    await anonCtx.close();
  }
});
