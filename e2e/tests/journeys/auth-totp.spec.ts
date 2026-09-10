/**
 * TOTP second-factor login (#1262).
 *
 * A dedicated e2e user (TOTP_USER, fixtures/data.ts — never the shared
 * admin: enabling TOTP on that account would break global-setup.ts's
 * password-only login for every other spec on the next run) enables TOTP
 * from /settings/totp/setup, logs in through the web layer's two-step
 * pending_token flow (auth.go's pendingTOTPAuth), then disables TOTP again
 * — mandatory cleanup, so the account stays password-only.
 *
 * The two-step login itself is driven via page.request.fetch rather than
 * real form interaction: the e2e-testing skill documents a reproducible,
 * never-root-caused indefinite hang from a *second* password-form
 * submission (loginAs()) inside a test. POSTing the exact fields the login
 * form would submit exercises the identical loginHandler code path
 * (including the pending_token second step) without that risk. Setup/
 * confirm/disable on /settings, by contrast, are ordinary authenticated
 * POSTs on a page already holding a valid session — not the risky pattern
 * — so those go through real navigation and locators as usual.
 */
import { test, expect } from "@playwright/test";
import { TOTP_USER } from "../../fixtures/data";
import { createUser, loginViaApi } from "../../helpers/seed";
import { freshTOTPCode } from "../../helpers/totp";

const WEB_BASE = process.env.BASE_URL ?? "http://localhost:8080";

/** Pull a hidden <input name="X" value="Y"> field's value out of raw HTML. */
function extractHidden(html: string, name: string): string {
  const m = html.match(new RegExp(`name="${name}"\\s+value="([^"]*)"`));
  if (!m) throw new Error(`extractHidden: no hidden field named ${name} found`);
  return m[1];
}

test("TOTP: enable, log in with a second factor, then disable", async ({ browser }) => {
  // freshTOTPCode can wait out up to a full 30s window twice (once dodging
  // the setup-confirm code for login, once dodging the login code for
  // disable) — worst case, on top of normal network overhead, comfortably
  // exceeds Playwright's 30s per-test default and can even push past a 90s
  // CLI --timeout under an unlucky window alignment.
  test.setTimeout(120_000);

  createUser(TOTP_USER.email, TOTP_USER.password, "user");

  // ── Enable TOTP (ordinary authenticated settings POSTs — real navigation is fine) ──
  const { context: setupCtx, page: setupPage } = await loginViaApi(
    browser,
    TOTP_USER.email,
    TOTP_USER.password
  );
  try {
    await setupPage.goto(`${WEB_BASE}/settings/totp/setup`);
    const secret = (await setupPage.locator("#totp-secret").textContent())?.trim();
    expect(secret, "TOTP setup must render a manual-entry secret").toBeTruthy();

    // Driven via page.request.fetch, not a real form fill+click — settings
    // POSTs need no _form_token (requireLogin's session cookie is enough),
    // so there's no benefit to real UI interaction here, and it sidesteps a
    // click-actionability hang reproduced on the mobile project (the button
    // sits below the fold on a long settings page; the click landed but the
    // subsequent navigation never completed within the test timeout).
    const { code: confirmCode, window: confirmWindow } = await freshTOTPCode(secret!);
    const confirmResp = await setupPage.request.fetch(`${WEB_BASE}/settings/totp/confirm`, {
      method: "POST",
      form: { code: confirmCode },
    });
    expect(confirmResp.url()).toBe(`${WEB_BASE}/settings?saved=1`);

    // Enabled state: the disable form (not the setup link) is now shown.
    await setupPage.goto(`${WEB_BASE}/settings`);
    await expect(setupPage.locator("#totp_disable_code")).toBeVisible();

    // ── Log in with the second factor, via the web layer's exact POST shape ──
    const loginCtx = await browser.newContext({ storageState: { cookies: [], origins: [] } });
    const loginPage = await loginCtx.newPage();
    try {
      const initialHtml = await (await loginPage.request.fetch(`${WEB_BASE}/login`)).text();
      const token1 = extractHidden(initialHtml, "_form_token");

      // consumeFormToken enforces a 100ms *minimum* age on login tokens.
      await new Promise((r) => setTimeout(r, 200));

      const step1Resp = await loginPage.request.fetch(`${WEB_BASE}/login`, {
        method: "POST",
        form: {
          _form_token: token1,
          phone2: "",
          email: TOTP_USER.email,
          password: TOTP_USER.password,
        },
      });
      const step1Html = await step1Resp.text();
      // A bare password now yields the pending_token second step, not a session.
      expect(
        step1Html.includes('name="pending_token"'),
        "password-only login for a TOTP-enabled user must return the second step"
      ).toBe(true);
      const pendingToken = extractHidden(step1Html, "pending_token");
      const token2 = extractHidden(step1Html, "_form_token");

      await new Promise((r) => setTimeout(r, 200));

      const { code: loginCode, window: loginWindow } = await freshTOTPCode(secret!, confirmWindow);
      const step2Resp = await loginPage.request.fetch(`${WEB_BASE}/login`, {
        method: "POST",
        form: {
          _form_token: token2,
          phone2: "",
          pending_token: pendingToken,
          totp_code: loginCode,
        },
      });
      expect(step2Resp.url()).toBe(`${WEB_BASE}/dashboard`);
      const cookies = await loginCtx.cookies();
      expect(
        cookies.some((c) => c.name === "dsw_token"),
        "a session cookie must be set after completing the TOTP step"
      ).toBe(true);

      // ── Disable TOTP again (mandatory cleanup) — needs its own fresh code, ──
      // never the one just used to log in (totpCheckAndMark would reject a
      // repeat of the same code as a replay within its window).
      const { code: disableCode } = await freshTOTPCode(secret!, loginWindow);
      const disableResp = await loginPage.request.fetch(`${WEB_BASE}/settings/totp/disable`, {
        method: "POST",
        form: { code: disableCode },
      });
      expect(disableResp.url()).toBe(`${WEB_BASE}/settings?saved=1`);

      await loginPage.goto(`${WEB_BASE}/settings`);
      await expect(
        loginPage.locator('a[href="/settings/totp/setup"]'),
        "TOTP must be back to not-configured after disabling"
      ).toBeVisible();
    } finally {
      await loginCtx.close();
    }
  } finally {
    await setupCtx.close();
  }
});
