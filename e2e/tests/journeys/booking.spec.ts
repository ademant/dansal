import { test, expect, gotoAndMeasure } from "../../helpers/fixtures";
import { fullSeed, SeedResult } from "../../helpers/seed";
import { EVENT_HREF_PREFIX } from "../../fixtures/data";

const API_BASE = process.env.API_URL ?? "http://localhost:8000";

let seed: SeedResult;

test.describe("Booking flow", () => {
  test.beforeAll(async ({ browser }) => {
    const setupPage = await browser.newPage();
    seed = await fullSeed(setupPage);
    // Enable internal booking on the first event via PATCH. fullSeed()
    // already left setupPage logged in as admin — a second loginAs() here
    // would navigate to /login while already authenticated, which redirects
    // straight to /dashboard without ever rendering the form (see
    // loginPageHandler) and hangs the subsequent #identifier fill forever.
    const cookies = await setupPage.context().cookies();
    const token = cookies.find((c) => c.name === "dsw_token")?.value ?? "";
    await setupPage.request.fetch(
      `${API_BASE}/api/v1/events/${seed.eventIds[0]}`,
      {
        method: "PATCH",
        headers: {
          "Content-Type": "application/merge-patch+json",
          Authorization: `Bearer ${token}`,
        },
        data: JSON.stringify({
          booking_enabled: true,
          tickets_total: 30,
        }),
      }
    );
    await setupPage.context().close();
  });

  test("event page shows booking form when enabled", async ({
    page,
    metrics,
  }) => {
    const m = await gotoAndMeasure(
      page,
      `${EVENT_HREF_PREFIX}${seed.eventIds[0]}`,
      metrics,
      "booking_form_visible"
    );
    await expect(page.locator(".booking-section")).toBeVisible();
    // The form lives inside a collapsed <details>/<summary> — expand it
    // before asserting on anything inside.
    await page.locator(".booking-details summary").click();
    await expect(page.locator(".booking-form")).toBeVisible();
    // Form should have required fields
    await expect(page.locator(".booking-form input[name='name']")).toBeVisible();
    await expect(page.locator(".booking-form input[name='email']")).toBeVisible();
  });

  test("submitting booking form with valid data succeeds", async ({
    page,
    metrics,
  }) => {
    await page.goto(`${EVENT_HREF_PREFIX}${seed.eventIds[0]}`);
    await page.locator(".booking-details summary").click();
    const form = page.locator(".booking-form");
    await form.locator('input[name="name"]').fill("Alice Testeur");
    // Randomized per run: a fixed address accumulates pending (unverified)
    // bookings run over run against a shared/persistent dev DB until it
    // trips the server's real anti-abuse cap on open verifications per
    // address (#1206) — same category of fix as the event date/title/
    // location jitter from #1194.
    await form
      .locator('input[name="email"]')
      .fill(`alice+${Date.now()}@test.example.com`);
    // persons is a plain number input, not a <select>.
    await form.locator('input[name="persons"]').fill("2");
    await form.locator('textarea[name="message"]').fill("Avec mon partenaire");
    // guardFormSubmit rejects a form token younger than 1s (anti-bot
    // min-age, same mechanism as the login form) — filling the form takes
    // negligible time in a headless browser, so submitting immediately
    // after page load would trip it.
    await page.waitForTimeout(1500);
    await form.locator('button[type="submit"]').click();
    await expect(page.locator(".msg-ok")).toBeVisible({ timeout: 10_000 });
    await metrics.collect("booking_submit");
  });
});
