/**
 * The per-event bulletin board — posting, email verification, image
 * attachments (#1260).
 *
 * Two scenarios in one continuous flow, using the same fake-sendmail
 * infrastructure as the suggest wizard (#1257):
 *
 *   1. An unauthenticated visitor posts via the event page's board panel.
 *      The post must NOT be publicly visible until its email is verified
 *      (GET /api/v1/events/{id}/contact-posts already filters to
 *      email_verified=1 server-side) — visiting the manage/verify link
 *      (one combined URL for the board, unlike the suggest wizard's two)
 *      flips that and the post becomes visible.
 *   2. Using the same manage token, an image is attached via a multipart
 *      POST straight to the manage endpoint (no auth header — the token in
 *      the URL is the credential) and shows up on the event page.
 *
 * If BoardOpenPosting is enabled on the target instance, posts are already
 * visible immediately on submit — the test detects this and skips the
 * verify-link visit rather than failing.
 */
import { test, expect } from "@playwright/test";
import { clearMailbox, waitForBoardManageToken } from "../../helpers/mailbox";
import { fullSeed, SeedResult } from "../../helpers/seed";
import { AUTH_FILE } from "../../helpers/auth";
import { makeImage } from "../../helpers/images";

const WEB_BASE = process.env.BASE_URL ?? "http://localhost:8080";
const API_BASE = process.env.API_URL ?? "http://localhost:8000";

let seed: SeedResult;

async function boardPosts(page: import("@playwright/test").Page, eventId: number): Promise<any[]> {
  const resp = await page.request.fetch(`${API_BASE}/api/v1/events/${eventId}/contact-posts`);
  const body = await resp.json();
  return Array.isArray(body) ? body : [];
}

test.describe("Bulletin board", () => {
  test.beforeAll(async ({ browser }) => {
    const context = await browser.newContext({ storageState: AUTH_FILE });
    const setupPage = await context.newPage();
    seed = await fullSeed(setupPage);
    await context.close();
  });

  test("unauthenticated post → email verification → visible; then an image attachment", async ({
    page,
    browser,
  }) => {
    const eventId = seed.eventIds[0];
    clearMailbox();

    // playwright.config.ts's use.storageState (AUTH_FILE, the admin's saved
    // session) is a per-project default that browser.newContext() silently
    // inherits unless explicitly overridden — an unqualified newContext()
    // here would still carry the admin's session (see loginViaApi's own
    // doc comment in helpers/seed.ts for the full story), not the
    // anonymous visitor this test needs.
    // contactBoardPostHandler also throttles duplicate submissions by a
    // sha256(ip+"|"+user-agent) fingerprint, on cooldown FormTokenMaxAgeMins
    // (30 min by default) — every Playwright run otherwise shares the same
    // IP and default UA, so a second run within that window would get
    // "Too many submissions" regardless of this being a fresh browser
    // context. A per-run UA sidesteps it the same way a genuinely different
    // visitor would fingerprint differently.
    const anonCtx = await browser.newContext({
      storageState: { cookies: [], origins: [] },
      userAgent: `Mozilla/5.0 (E2E board test ${Date.now()})`,
    });
    const anonPage = await anonCtx.newPage();
    try {
      await anonPage.goto(`${WEB_BASE}/events/${eventId}`);

      // Opening the panel refreshes _form_token in the background and
      // disables the submit button until that fetch resolves (#979).
      await anonPage.locator(".board-post-details summary").click();
      const submitBtn = anonPage.locator(".btn-board-submit");
      await expect(submitBtn).toBeEnabled();
      // consumeFormToken (formguard.go) also enforces a 1s *minimum* age —
      // an anti-bot check that a real visitor filling the form would always
      // clear naturally, but Playwright's fast automated fill can beat.
      await anonPage.waitForTimeout(1200);

      // lost_item: no city required (ride/sleep types do) AND the event
      // page's board-item template only ever renders .board-item-imgs for
      // lost_item/found_item (event.html gates it on
      // `.Type == "lost_item" || .Type == "found_item"`) — Scenario 2 below
      // attaches an image, which would silently never appear in the DOM for
      // any other type regardless of upload success. Click the real tab
      // rather than setting #board-type-input directly, since the city
      // field's required attribute is toggled by the tab's click handler
      // (boardTypeChanged()), not derived from the hidden input's value at
      // submit time.
      await anonPage.locator('.board-type-tab[data-type="lost_item"]').click();

      const message = `E2E board message ${Date.now()}`;
      const nickname = `E2E Board ${Date.now()}`;
      const email = `e2e-board-${Date.now()}@example.com`;
      await anonPage.fill('textarea[name="message"]', message);
      await anonPage.fill('input[name="nickname"]', nickname);
      await anonPage.fill('input[name="email"]', email);

      await submitBtn.click();
      // The success redirect lands on /events/{id}?msg=<flash-token> — match
      // on pathname alone, not a regex anchored with $ (which never matches
      // once a query string is appended).
      await anonPage.waitForURL((url) => url.pathname === `/events/${eventId}`);
      await expect(anonPage.locator(".msg-ok")).toBeVisible();

      // Board posts always get a manage-link email regardless of
      // BoardOpenPosting (only the wording/verify-requirement differs) —
      // wait for it either way, it's needed for the image-attachment step
      // regardless of whether the post is already visible.
      const manageToken = await waitForBoardManageToken();

      const postsBeforeVerify = await boardPosts(page, eventId);
      const openPosting = postsBeforeVerify.some((p) => p.message === message);

      if (!openPosting) {
        expect(
          postsBeforeVerify.some((p) => p.message === message),
          "unverified post must not be publicly visible"
        ).toBe(false);

        // The manage/verify link is one combined URL for the board (unlike
        // the suggest wizard's separate verify+manage links) —
        // getContactPostByToken flips email_verified=1 on first visit.
        await anonPage.goto(`${WEB_BASE}/contact-posts/manage/${manageToken}`);

        const postsAfterVerify = await boardPosts(page, eventId);
        expect(
          postsAfterVerify.some((p) => p.message === message),
          "post must be publicly visible after verifying"
        ).toBe(true);
      }

      await anonPage.goto(`${WEB_BASE}/events/${eventId}`);
      const boardItem = anonPage.locator(".board-item").filter({ hasText: message });
      await expect(boardItem).toBeVisible();

      // -- Scenario 2: attach an image via the manage endpoint. No auth
      //    header needed — the token in the URL is the credential. --
      const imageBuf = await makeImage("png", 128, 128);
      const uploadResp = await anonPage.request.fetch(
        `${WEB_BASE}/contact-posts/manage/${manageToken}/images`,
        {
          method: "POST",
          multipart: {
            image: { name: "board.png", mimeType: "image/png", buffer: imageBuf },
          },
        }
      );
      expect(uploadResp.status()).toBe(200); // follows the 303 back to the manage page

      await anonPage.goto(`${WEB_BASE}/events/${eventId}`);
      const boardItemWithImage = anonPage.locator(".board-item").filter({ hasText: message });
      await expect(boardItemWithImage.locator(".board-item-imgs img")).toBeVisible();
    } finally {
      await anonCtx.close();
    }
  });
});
