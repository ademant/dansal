import { execSync } from "child_process";
import { test, expect } from "../../helpers/fixtures";

// #1542: the About popup and the pages it links to had no e2e coverage —
// #582 was a real regression (a duplicate toggleInfoPopup definition
// silently overwrote the working one) that nothing here would have caught.

// Never points at a production instance (see WEB_DB_PATH below), so seeding
// missing content directly is safe: impressum/privacy/terms/security.txt
// are config-gated (404 until an operator sets their site_settings text —
// see legalPageHandler/impressumHandler/securityTxtHandler), and a target
// with nothing configured would otherwise never exercise the "renders real
// content" path at all.

// setSiteSetting upserts a site_settings row directly into web.db — the web
// frontend has no endpoint that writes site_settings (only the separate
// webmin binary does, and it's not part of every e2e target — see #1536/
// #1537) — same established pattern as activitypub.spec.ts's
// setIndexNowKey. Values go through env vars so execSync never has to quote
// them into the shell command.
function setSiteSetting(webDbPath: string, key: string, value: string): void {
  const esc = (s: string) => s.replace(/'/g, "''");
  const sql = `INSERT INTO site_settings(key, value) VALUES('${esc(key)}', '${esc(value)}') ON CONFLICT(key) DO UPDATE SET value = excluded.value`;
  execSync(
    `python3 -c "import os, sqlite3; db = sqlite3.connect(os.environ['WEBDB']); db.execute(os.environ['SQL']); db.commit(); db.close()"`,
    { env: { ...process.env, WEBDB: webDbPath, SQL: sql }, timeout: 10_000 }
  );
}

// siteSettingsCache re-reads web.db at most once per 10 s (sitecache.go) —
// give a seed a full window to be picked up before asserting against it.
const SITE_SETTINGS_TTL_MS = 11_000;

// The About/Help buttons live inside #user-dropdown (the 👤 menu), hidden
// until that menu is opened — not directly clickable from the page itself.
async function openUserMenu(page: import("@playwright/test").Page): Promise<void> {
  await page.locator('button[aria-label="User menu"]').click();
  await expect(page.locator("#user-dropdown")).toBeVisible();
}

test.describe("About popup", () => {
  test.use({ storageState: { cookies: [], origins: [] } });

  // Deactivated (test.fixme) until the root cause is found: in headless
  // Chromium, page.goto("/") on the homepage never reaches the load event
  // because the deferred scripts (base.js, leaflet.js) never get response
  // headers. curl and a Node keep-alive client get the same responses in
  // milliseconds, so this is browser-side. Other pages load fine. See the
  // investigation notes in the PR that deactivated these tests.
  test.fixme("opens showing the app version; a second click closes it", async ({ page }) => {
    await page.goto("/");
    await openUserMenu(page);
    const aboutBtn = page.locator('button[title="About dansal"]');
    const popup = page.locator("#info-popup");
    await expect(popup).toBeHidden();

    await aboutBtn.click();
    await expect(popup).toBeVisible();
    await expect(popup.locator(".info-ver")).not.toBeEmpty();

    await aboutBtn.click();
    await expect(popup).toBeHidden();
  });

  test.fixme("the dansal link opens GitHub in a new tab without a referrer", async ({ page }) => {
    await page.goto("/");
    await openUserMenu(page);
    await page.locator('button[title="About dansal"]').click();
    const link = page.locator("#info-popup a");
    await expect(link).toHaveAttribute("href", "https://github.com/ademant/dansal");
    await expect(link).toHaveAttribute("target", "_blank");
    await expect(link).toHaveAttribute("rel", "noopener");
  });
});

test.describe("Help modal", () => {
  test.use({ storageState: { cookies: [], origins: [] } });

  test.fixme("Help button opens the help modal", async ({ page }) => {
    await page.goto("/");
    await openUserMenu(page);
    const backdrop = page.locator("#hm-backdrop");
    await expect(backdrop).toBeHidden();
    await page.locator('button[title="Help"]').click();
    await expect(backdrop).toBeVisible();
  });
});

test.describe("Legal and help pages", () => {
  test.use({ storageState: { cookies: [], origins: [] } });

  test("/help always renders its topic navigation", async ({ page }) => {
    const resp = await page.goto("/help");
    expect(resp?.status()).toBe(200);
    await expect(page.locator(".help-nav")).toBeVisible();
  });

  // /impressum, /privacy and /terms only render real content once an
  // operator has configured it (site_settings or LegalDir) — unconfigured,
  // legalPageHandler/impressumHandler correctly 404 rather than crash. Seed
  // the missing text directly (site_settings keys impressum_en/privacy_en/
  // terms_en) when one is missing and web.db is writable here, so the
  // actual "renders real content" path gets exercised instead of just
  // accepting whichever state the target happens to already be in. When
  // web.db isn't reachable (e.g. a shared target with no WEB_DB_PATH),
  // fall back to the weaker "never crashes" check.
  for (const { path, key } of [
    { path: "/impressum", key: "impressum_en" },
    { path: "/privacy", key: "privacy_en" },
    { path: "/terms", key: "terms_en" },
  ]) {
    test(`${path} renders its heading, seeding content first if none is configured`, async ({
      page,
    }) => {
      const webDbPath = process.env.WEB_DB_PATH;
      let resp = await page.goto(path);

      if (resp?.status() === 404) {
        if (!webDbPath) {
          // No writable web.db here — can't seed, so this only pins down
          // "404 is a clean not-found, not a crash", same as the public
          // JS-error sweep already covers for these three paths.
          expect(resp.status()).toBe(404);
          return;
        }
        setSiteSetting(webDbPath, key, `E2E test content for ${path}.`);
        await page.waitForTimeout(SITE_SETTINGS_TTL_MS);
        resp = await page.goto(path);
      }

      expect(resp?.status()).toBe(200);
      await expect(page.locator("h1")).not.toBeEmpty();
    });
  }
});

// Not required by #1542, but cheap and covers the other informational
// endpoints crawlers/browsers read directly (no UI involved).
test.describe("Other informational endpoints", () => {
  test.use({ storageState: { cookies: [], origins: [] } });

  test("robots.txt, sitemap.xml, security.txt and the AI badge are served with sane content types", async ({
    page,
  }) => {
    const robots = await page.request.fetch("/robots.txt");
    expect(robots.status()).toBe(200);
    expect(robots.headers()["content-type"]).toContain("text/plain");

    const sitemap = await page.request.fetch("/sitemap.xml");
    expect(sitemap.status()).toBe(200);
    expect(sitemap.headers()["content-type"]).toContain("xml");

    // Like impressum/privacy/terms, security.txt 404s rather than crashes
    // until an operator configures a security contact (site_settings or
    // cfg.SecurityContact) — seed one (mailto:, per RFC 9116) if missing
    // and web.db is writable here, same reasoning as the legal pages above.
    let securityTxt = await page.request.fetch("/.well-known/security.txt");
    if (securityTxt.status() === 404) {
      const webDbPath = process.env.WEB_DB_PATH;
      if (webDbPath) {
        setSiteSetting(webDbPath, "security_contact", "mailto:security@e2e-1542.test");
        await page.waitForTimeout(SITE_SETTINGS_TTL_MS);
        securityTxt = await page.request.fetch("/.well-known/security.txt");
      }
    }
    expect([200, 404]).toContain(securityTxt.status());
    if (securityTxt.status() === 200) {
      expect(securityTxt.headers()["content-type"]).toContain("text/plain");
      expect(await securityTxt.text()).toContain("Contact:");
    }

    const aiBadge = await page.request.fetch("/ai-badge");
    expect(aiBadge.status()).toBe(200);
    expect(aiBadge.headers()["content-type"]).toMatch(/^image\//);
  });
});
