import { test, expect } from "../../helpers/fixtures";
import { applyLighthouseMobileThrottling } from "../../helpers/metrics";

// Core Web Vitals "good" thresholds (web.dev/vitals). Reported as warning
// annotations, not failures: a page missing them is a real regression worth
// looking at, but a lab measurement shouldn't turn the suite red by itself.
const LCP_GOOD_MS = 2500;
const CLS_GOOD = 0.1;

const ROUTES: Array<{ label: string; path: string }> = [
  { label: "homepage", path: "/" },
];

// #1358: this spec used to force a throttled phone profile onto every project
// and then run only under the project named "desktop" — so its "desktop"
// result was really a mobile measurement, and real desktop (where the
// homepage had a CLS of ~0.38) was never measured. Each profile now runs in
// the project it is named after.

async function measure(
  page: import("@playwright/test").Page,
  metrics: { collect(name: string): Promise<any> },
  testInfo: import("@playwright/test").TestInfo,
  label: string,
  profile: string,
  path: string
): Promise<void> {
  await page.goto(path, { waitUntil: "networkidle" });
  // Give late layout shifts (webfonts, map tiles, async widgets) a moment to
  // land before reading CLS — PSI's lab run traces the full load too.
  await page.waitForTimeout(2000);
  const m = await metrics.collect(`web_vitals_${label}_${profile.replace(/[^a-z]+/g, "_")}`);
  console.log(
    `[web-vitals] ${label} (${profile}): ` +
      `LCP=${m.vitals.lcp !== null ? m.vitals.lcp.toFixed(0) + "ms" : "n/a"}  ` +
      `CLS=${m.vitals.cls !== null ? m.vitals.cls.toFixed(3) : "n/a"}`
  );
  const warn = (text: string) => {
    testInfo.annotations.push({ type: "warning", description: `${label} (${profile}): ${text}` });
    console.warn(`[web-vitals] WARNING ${label} (${profile}): ${text}`);
  };
  if (m.vitals.lcp === null) warn("LCP was not recorded");
  else if (m.vitals.lcp > LCP_GOOD_MS) warn(`LCP ${m.vitals.lcp.toFixed(0)}ms is over the "good" limit of ${LCP_GOOD_MS}ms`);
  if (m.vitals.cls !== null && m.vitals.cls > CLS_GOOD) warn(`CLS ${m.vitals.cls.toFixed(3)} is over the "good" limit of ${CLS_GOOD}`);
}

test.describe("Core Web Vitals (mobile, Lighthouse-throttled)", () => {
  // Approximates PageSpeed Insights / Lighthouse's mobile lab device: a
  // mid-tier Android phone (Moto G Power-class) — overriding viewport/UA
  // rather than using the "mobile" project's Pixel 7 preset, which has no
  // network/CPU throttling and reads far better than PSI for that reason.
  test.use({
    viewport: { width: 412, height: 823 },
    deviceScaleFactor: 1.75,
    isMobile: true,
    hasTouch: true,
    userAgent:
      "Mozilla/5.0 (Linux; Android 11; moto g power (2022)) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/125.0.0.0 Mobile Safari/537.36",
  });
  for (const { label, path } of ROUTES) {
    test(`${label}: LCP and CLS`, async ({ page, metrics }, testInfo) => {
      test.skip(testInfo.project.name !== "mobile", "the throttled phone profile runs in the mobile project");
      await applyLighthouseMobileThrottling(page);
      await measure(page, metrics, testInfo, label, "mobile, throttled", path);
    });
  }
});

test.describe("Core Web Vitals (desktop, unthrottled)", () => {
  test.use({ viewport: { width: 1280, height: 720 }, isMobile: false, hasTouch: false });
  for (const { label, path } of ROUTES) {
    test(`${label}: LCP and CLS`, async ({ page, metrics }, testInfo) => {
      test.skip(testInfo.project.name !== "desktop", "the desktop profile runs in the desktop project");
      await measure(page, metrics, testInfo, label, "desktop", path);
    });
  }
});
