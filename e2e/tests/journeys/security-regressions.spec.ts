import { test, expect } from "../../helpers/fixtures";
import { getTokenFromCookie } from "../../helpers/seed";

// Regression coverage for #1533/#1536 (two of the four findings from #1532's
// linked security review; see #1537).
//
// #1534 (X-Forwarded-For/X-Real-IP trust) and #1535 (the robots.txt SSRF
// guard) are deliberately NOT covered here: both fixes only change behavior
// for a direct, non-loopback connection to the Go process — the real nginx
// reverse proxy always connects over loopback, and so does this suite
// (there is no nginx hop in the e2e topology at all). A black-box Playwright
// request therefore looks identical to the proxy's own vantage point both
// before and after either fix, so no HTTP-level assertion here could ever
// distinguish the fixed behavior from the bug. Both are covered instead by
// Go unit tests added alongside each fix, which construct requests with a
// controlled RemoteAddr/target host directly:
//   - cmd/dansal/iputil_test.go, internal/webcommon/webcommon_test.go (#1534)
//   - cmd/dansal/robotstxt_import_test.go (#1535)

const API_BASE = process.env.API_URL ?? "http://localhost:8000";
const WEBMIN_URL = process.env.WEBMIN_URL;

test.describe("#1533: anonymous requests cannot forge X-User-Role/X-User-ID", () => {
  test("a hand-set admin role header does not reveal an unpublished event", async ({ page }) => {
    const token = await getTokenFromCookie(page);
    const title = `E2E security draft ${Date.now()}`;
    const createResp = await page.request.fetch(`${API_BASE}/api/v1/events`, {
      method: "POST",
      headers: { Authorization: `Bearer ${token}`, "Content-Type": "application/json" },
      data: JSON.stringify([
        { title, start_time: "2027-06-01T20:00:00", end_time: "2027-06-01T22:00:00" },
      ]),
    });
    expect(createResp.ok()).toBe(true);
    const created = (await createResp.json())[0];

    const patchResp = await page.request.fetch(`${API_BASE}/api/v1/events/${created.id}`, {
      method: "PATCH",
      headers: {
        Authorization: `Bearer ${token}`,
        "Content-Type": "application/merge-patch+json",
      },
      data: JSON.stringify({ is_published: false }),
    });
    expect(patchResp.ok()).toBe(true);

    // Baseline: a plain anonymous request (no Authorization header at all)
    // must not see the unpublished event.
    const plain = await page.request.fetch(`${API_BASE}/api/v1/events/${created.id}`);
    expect(plain.ok()).toBe(false);

    // Same request, still with NO Authorization header, but a hand-set
    // X-User-Role/X-User-ID — must be rejected exactly like the baseline.
    // Before #1533's fix, resolveCaller left these headers untouched when
    // no Authorization header was present, so this forged pair reached
    // getEvent as if a real admin session had resolved it.
    const forged = await page.request.fetch(`${API_BASE}/api/v1/events/${created.id}`, {
      headers: { "X-User-Role": "admin", "X-User-ID": "1" },
    });
    expect(forged.status()).toBe(plain.status());
    expect(forged.ok()).toBe(false);
  });
});

test.describe("#1536: webmin rejects a cross-site write", () => {
  test.skip(
    !WEBMIN_URL,
    "WEBMIN_URL not set — webmin isn't part of this e2e target (see e2e/README.md)"
  );

  test("a POST with a mismatched Origin is rejected before reaching the handler", async ({
    page,
  }) => {
    // Establish a real webmin session the same way a live one ends up:
    // hand webmin the dansal API bearer token as its dwm_token cookie.
    // requireLogin's refreshedSessionUser revalidates it against
    // GET /api/v1/me on the first request and, on success, re-issues the
    // signed dwm_user cookie — exactly what a genuine login produces.
    const token = await getTokenFromCookie(page);
    await page.context().addCookies([{ name: "dwm_token", value: token, url: WEBMIN_URL! }]);

    const warmup = await page.request.fetch(`${WEBMIN_URL}/site-config`);
    expect(warmup.ok()).toBe(true); // confirms the session is actually live

    const sameOrigin = await page.request.fetch(`${WEBMIN_URL}/notifications/heartbeat`, {
      method: "POST",
      headers: { Origin: WEBMIN_URL! },
    });
    expect(sameOrigin.status()).not.toBe(403);

    // Before #1536's fix this reached notificationsHeartbeatSaveHandler
    // the same way the same-origin request above just did.
    const crossSite = await page.request.fetch(`${WEBMIN_URL}/notifications/heartbeat`, {
      method: "POST",
      headers: { Origin: "https://evil.test" },
    });
    expect(crossSite.status()).toBe(403);
  });
});
