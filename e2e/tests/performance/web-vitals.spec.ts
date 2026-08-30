import { test, expect } from "../../helpers/fixtures";
import { applyLighthouseMobileThrottling } from "../../helpers/metrics";

// Approximates PageSpeed Insights / Lighthouse's mobile lab-data device: a
// mid-tier Android phone (Moto G Power-class) — overriding viewport/UA here
// rather than relying on the "mobile" project's Pixel 7 preset, since that
// preset has no network/CPU throttling and reads far better than PSI's own
// numbers for that reason alone (see applyLighthouseMobileThrottling).
test.use({
  viewport: { width: 412, height: 823 },
  deviceScaleFactor: 1.75,
  isMobile: true,
  hasTouch: true,
  userAgent:
    "Mozilla/5.0 (Linux; Android 11; moto g power (2022)) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/125.0.0.0 Mobile Safari/537.36",
});

// Core Web Vitals "good" thresholds (web.dev/vitals). Reported as soft
// assertions: a page missing them is a real regression worth looking at,
// but shouldn't block the rest of the suite by itself.
const LCP_GOOD_MS = 2500;
const CLS_GOOD = 0.1;

const ROUTES: Array<{ label: string; path: string }> = [
  { label: "homepage", path: "/" },
];

test.describe("Core Web Vitals (mobile, Lighthouse-throttled)", () => {
  for (const { label, path } of ROUTES) {
    test(`${label}: LCP and CLS`, async ({ page, metrics }, testInfo) => {
      // test.use() above already forces the same mobile viewport/UA
      // regardless of project, so running under both "desktop" and
      // "mobile" would just measure the same thing twice — pin this to
      // one project.
      test.skip(
        testInfo.project.name !== "desktop",
        "mobile-emulated by test.use() above; only needs to run once"
      );
      await applyLighthouseMobileThrottling(page);
      await page.goto(path, { waitUntil: "networkidle" });
      // Give any late layout shifts (webfonts swapping in, lazy-loaded
      // images/map tiles, async widgets) a moment to land before reading
      // CLS — PSI's lab run effectively does the same by tracing the full
      // load, not just up to the "load" event.
      await page.waitForTimeout(2000);
      const m = await metrics.collect(`web_vitals_${label}`);

      console.log(
        `[web-vitals] ${label} (mobile, throttled): ` +
          `LCP=${m.vitals.lcp !== null ? m.vitals.lcp.toFixed(0) + "ms" : "n/a"}  ` +
          `CLS=${m.vitals.cls !== null ? m.vitals.cls.toFixed(3) : "n/a"}`
      );

      expect.soft(m.vitals.lcp, `${label}: LCP should have been recorded`).not.toBeNull();
      expect
        .soft(m.vitals.lcp ?? 0, `${label}: LCP should be "good" (<=${LCP_GOOD_MS}ms)`)
        .toBeLessThanOrEqual(LCP_GOOD_MS);
      expect
        .soft(m.vitals.cls ?? 0, `${label}: CLS should be "good" (<=${CLS_GOOD})`)
        .toBeLessThanOrEqual(CLS_GOOD);
    });
  }
});
