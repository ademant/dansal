import { test, expect } from "../../helpers/fixtures";

// Internationalisation coverage for the web frontend. The 12 UI languages
// (AGENTS.md / cmd/dansal_web/i18n.yaml) otherwise have zero e2e coverage —
// a missing translation key silently falls back and nothing would catch it.
//
// Language selection order (cmd/dansal_web/i18n.go detectLang):
//   1. ?lang= query param  2. dsw_lang cookie  3. Accept-Language header
//   4. holiday_country  5. i18n default ("de")
// Every test runs in its own anonymous visitor context (empty storageState —
// the config default storageState AUTH_FILE would otherwise log in as the
// e2e admin and is irrelevant to the purely server-rendered public index).

const LANGS = ["br", "ca", "cs", "de", "en", "es", "fr", "it", "nl", "pl", "pt", "uk"];

// events_title h1 on the index page; index_travel_notice paragraph directly
// below it. Both are server-rendered from i18n.yaml.
const EVENTS_TITLE: Record<string, string> = {
  en: "Upcoming Events",
  de: "Bevorstehende Veranstaltungen",
  fr: "Événements à venir",
  es: "Próximos eventos",
};
const TRAVEL_NOTICE: Record<string, string> = {
  en: "Always check that an event is still happening before travelling.",
  de: "Bitte vor der Anreise prüfen, ob die Veranstaltung noch stattfindet.",
  fr: "Vérifiez que l'événement a bien lieu avant de vous déplacer.",
  es: "Comprueba que el evento sigue adelante antes de desplazarte.",
};

test.describe("Internationalisation (web)", () => {
  test("language switcher persists dsw_lang and re-renders the page", async ({
    browser,
  }) => {
    const context = await browser.newContext({
      storageState: { cookies: [], origins: [] },
    });
    const page = await context.newPage();

    // New visitor: Playwright's default Accept-Language (en-US) yields English.
    await page.goto("/");
    await expect(page.locator("h1")).toHaveText(EVENTS_TITLE.en);

    // The language switcher lives in the User-menu dropdown (always collapsed
    // into the 👤 button, even on desktop), so open it first.
    await page.click('button[aria-label="User menu"]');
    // No dsw_lang cookie yet, so the switcher asks for consent before
    // persisting — then GET /lang?code=fr sets the cookie and redirects back.
    await page.selectOption("#lang-select", "fr");
    await page.click('button[data-fn="langConsentAccept"]');

    await expect(page.locator("h1")).toHaveText(EVENTS_TITLE.fr);
    await expect(page.locator("html")).toHaveAttribute("lang", "fr");
    const cookie = (await context.cookies()).find((c) => c.name === "dsw_lang");
    expect(cookie?.value).toBe("fr");

    // The choice is persisted: a plain reload keeps French, not a ?lang= hack.
    await page.reload();
    await expect(page.locator("h1")).toHaveText(EVENTS_TITLE.fr);
    await expect(page.locator(".travel-notice")).toHaveText(
      TRAVEL_NOTICE.fr
    );
    await context.close();
  });

  test("Accept-Language picks a supported language; cookie overrides it", async ({
    browser,
  }) => {
    const context = await browser.newContext({
      locale: "de-DE",
      storageState: { cookies: [], origins: [] },
    });
    const page = await context.newPage();

    // New visitor with a German browser but no cookie → German from the
    // Accept-Language header alone.
    await page.goto("/");
    await expect(page.locator("h1")).toHaveText(EVENTS_TITLE.de);
    await expect(page.locator("html")).toHaveAttribute("lang", "de");

    // Once the user picks a language it must win over the browser header.
    // Add the cookie to the storageState (fresh context) rather than
    // addCookies() on the live context: a browser context that has already
    // issued requests doesn't attach newly added cookies to later ones, a
    // Playwright cookie-jar quirk we ran into while writing this spec.
    const override = await browser.newContext({
      locale: "de-DE",
      storageState: {
        cookies: [
          {
            name: "dsw_lang",
            value: "fr",
            domain: new URL(page.url()).hostname,
            path: "/",
          },
        ],
        origins: [],
      },
    });
    const overridePage = await override.newPage();
    await overridePage.goto("/");
    await expect(overridePage.locator("h1")).toHaveText(EVENTS_TITLE.fr);
    await expect(overridePage.locator("html")).toHaveAttribute("lang", "fr");
    await override.close();
    await context.close();
  });

  test("unsupported visitor language falls back to a supported locale", async ({
    browser,
  }) => {
    const context = await browser.newContext({
      locale: "zh-CN",
      storageState: { cookies: [], origins: [] },
    });
    const page = await context.newPage();
    await page.goto("/");

    // zh is not in i18n.yaml, so detectLang must land on a supported language
    // (default "de", or whatever holiday_country implies on the instance).
    const htmlLang = await page.locator("html").getAttribute("lang");
    expect(LANGS).toContain(htmlLang);
    // The page stayed translated — a missing-language regression would render
    // the raw key "events_title" as the heading.
    await expect(page.locator("h1")).not.toHaveText("events_title");
    await context.close();
  });

  test("?lang= forces a language for one request and ignores invalid codes", async ({
    browser,
  }) => {
    const context = await browser.newContext({
      storageState: { cookies: [], origins: [] },
    });
    const page = await context.newPage();

    // shareable deep link: English browser, Spanish from the query param.
    await page.goto("/?lang=es");
    await expect(page.locator("h1")).toHaveText(EVENTS_TITLE.es);
    await expect(page.locator(".travel-notice")).toHaveText(
      TRAVEL_NOTICE.es
    );

    // Unknown code is ignored → back to the Accept-Language English.
    await page.goto("/?lang=zz");
    await expect(page.locator("h1")).toHaveText(EVENTS_TITLE.en);

    // The ?lang= override is per-request: it never writes a cookie.
    const cookies = await context.cookies();
    expect(cookies.some((c) => c.name === "dsw_lang")).toBe(false);
    await context.close();
  });

  test("denying the consent prompt does not change the language", async ({
    browser,
  }) => {
    const context = await browser.newContext({
      storageState: { cookies: [], origins: [] },
    });
    const page = await context.newPage();

    await page.goto("/");
    await expect(page.locator("h1")).toHaveText(EVENTS_TITLE.en);

    await page.click('button[aria-label="User menu"]');
    await page.selectOption("#lang-select", "fr");
    await page.click('button[data-fn="langConsentDeny"]');

    // Deny never navigates and never writes a cookie (base.js langConsentDeny
    // only hides the modal and snaps the select back to its default).
    await expect(page.locator("#lang-consent-modal")).toBeHidden();
    await expect(page.locator("#lang-select")).toHaveValue("en");
    expect((await context.cookies()).some((c) => c.name === "dsw_lang")).toBe(
      false
    );
    await expect(page.locator("h1")).toHaveText(EVENTS_TITLE.en);
    await context.close();
  });
});