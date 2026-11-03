import { test, expect } from "@playwright/test";

// #1367: a page revalidated by the browser (reload / revisit) must still run
// its scripts. HTML pages carry a per-request CSP nonce in both the body and
// the Content-Security-Policy header, so answering a conditional GET with a
// 304 (cached body, new header) blocks every inline script and base.js: blank
// week grid, no map. Anonymous context on purpose: that's the visitor who hit it.
test.describe("Reloading public pages keeps scripts working (#1367)", () => {
  async function anon(browser) {
    const context = await browser.newContext({
      storageState: { cookies: [], origins: [] },
    });
    return { context, page: await context.newPage() };
  }

  // #1782: the reload's own `load` event is the wrong thing to wait for — a
  // reload that the page redirects or re-navigates mid-flight aborts with
  // ERR_ABORTED and never fires it. Wait for the final URL and a page
  // element instead, which is what the test actually cares about.
  async function settle(page, path: string, readySelector: string) {
    await expect(page).toHaveURL(new RegExp(`${path.replace(/[.*+?^${}()|[\]\\/]/g, "\\$&")}$`));
    await expect(page.locator(readySelector).first()).toBeAttached();
  }

  async function loadTwiceCollectingCSP(page, path: string, readySelector = "h1") {
    const violations: string[] = [];
    page.on("console", (m) => {
      if (m.type() === "error" && /Content Security Policy/i.test(m.text())) {
        violations.push(m.text().slice(0, 160));
      }
    });
    await page.goto(path, { waitUntil: "domcontentloaded" });
    await settle(page, path, readySelector);
    await page.waitForTimeout(1500);
    await page.reload({ waitUntil: "domcontentloaded" });
    await settle(page, path, readySelector);
    await page.waitForTimeout(1500);
    await page.goto(path, { waitUntil: "domcontentloaded" }); // plain revisit
    await settle(page, path, readySelector);
    await page.waitForTimeout(1500);
    return violations;
  }

  test("index: week grid and map are rendered after reload/revisit", async ({ browser }) => {
    const { context, page } = await anon(browser);
    const violations = await loadTwiceCollectingCSP(page, "/", "#week-main");
    expect(violations, violations.join("\n")).toEqual([]);
    const weekHtmlLen = await page.evaluate(
      () => document.getElementById("week-main")?.innerHTML.length ?? 0
    );
    expect(weekHtmlLen).toBeGreaterThan(0);
    await context.close();
  });

  test("event page and list pages have no CSP violations after reload", async ({ browser }) => {
    const { context, page } = await anon(browser);
    await page.goto("/", { waitUntil: "load" });
    // Event detail pages are /events/<id>; a bare a[href^="/events/"] also
    // matches the nav link /events/suggest (#1782).
    const hrefs = await page.locator('a[href^="/events/"]').evaluateAll((as) =>
      as.map((a) => a.getAttribute("href") ?? "")
    );
    const eventHref = hrefs.find((h) => /^\/events\/\d+$/.test(h));
    expect(eventHref).toBeTruthy();
    for (const path of [eventHref!, "/organizations", "/musicians", "/board"]) {
      const violations = await loadTwiceCollectingCSP(page, path);
      expect(violations, `${path}\n${violations.join("\n")}`).toEqual([]);
    }
    await context.close();
  });
});
