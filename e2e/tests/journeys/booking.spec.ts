import { test, expect, gotoAndMeasure } from "../../helpers/fixtures";
import { fullSeed, SeedResult, loginAs, getTokenFromCookie } from "../../helpers/seed";
import { EVENT_HREF_PREFIX } from "../../fixtures/data";

const API_BASE = process.env.API_URL ?? "http://localhost:8000";

let seed: SeedResult;

test.describe("Booking flow", () => {
  test.beforeAll(async ({ browser }) => {
    const setupPage = await browser.newPage();
    seed = await fullSeed(setupPage);
    // Enable internal booking on the first event via PATCH
    await loginAs(setupPage, "e2e-admin@dansal.test", "E2e-Admin-2026!");
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
    const form = page.locator(".booking-form");
    await form.locator('input[name="name"]').fill("Alice Testeur");
    await form.locator('input[name="email"]').fill("alice@test.example.com");
    await form.locator('select[name="persons"]').selectOption("2");
    await form.locator('textarea[name="message"]').fill("Avec mon partenaire");
    await form.locator('button[type="submit"]').click();
    await expect(page.locator(".msg-ok")).toBeVisible({ timeout: 10_000 });
    await metrics.collect("booking_submit");
  });
});
