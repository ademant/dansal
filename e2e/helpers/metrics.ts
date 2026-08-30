import * as fs from "fs";
import * as path from "path";
import { Page } from "@playwright/test";

// ── Types ────────────────────────────────────────────────────────────────────

export interface PageMetrics {
  url: string;
  test: string;
  viewport: string;
  timestamp: string;

  // Navigation timing (ms, relative to navigationStart)
  navigation: {
    dns: number;
    tcp: number;
    ttfb: number;
    domContentLoaded: number;
    load: number;
    domInteractive: number;
    responseEnd: number;
  };

  // Web Vitals
  vitals: {
    fp: number | null; // First Paint
    fcp: number | null; // First Contentful Paint
    lcp: number | null; // Largest Contentful Paint
    cls: number | null; // Cumulative Layout Shift
    tbt: number | null; // Total Blocking Time
    inp: number | null; // Interaction to Next Paint (from PerformanceObserver)
  };

  // DOM stats
  dom: {
    nodeCount: number;
    depth: number;
    imgCount: number;
    imgWithAlt: number;
    imgWithoutAlt: number;
    linkCount: number;
    formCount: number;
    inputCount: number;
    headingCount: number;
    ariaLandmarkCount: number;
    tabindexNegative: number;
  };

  // Resource summary
  resources: {
    totalRequests: number;
    totalBytes: number;
    byType: Record<string, { count: number; bytes: number }>;
  };

  // Memory (Chromium only)
  memory: {
    jsHeapUsed: number | null;
    jsHeapLimit: number | null;
    domNodes: number | null;
  };

  // Test duration (wall clock from test start)
  testDurationMs: number;
}

export interface ConsoleEntry {
  type: string;
  text: string;
  url: string;
  line: number;
  column: number;
  timestamp: string;
}

export interface NetworkFailure {
  url: string;
  method: string;
  status: number;
  statusText: string;
  resourceType: string;
  failureText: string;
  timestamp: string;
}

export interface TestErrorContext {
  test: string;
  file: string;
  viewport: string;
  url: string;
  error: string;
  screenshotPath?: string;
  tracePath?: string;
  consoleErrors: ConsoleEntry[];
  networkFailures: NetworkFailure[];
  domSnapshot: string; // outerHTML of <body> truncated to 50KB
  metrics: PageMetrics | null;
  timestamp: string;
}

// ── Collector ────────────────────────────────────────────────────────────────

export interface ObservedVitals {
  lcp: number | null;
  tbt: number | null;
  inp: number | null;
}

/**
 * Collect performance metrics for the current page. `projectName` is threaded
 * in from the fixture's `testInfo.project.name` (Playwright does not expose it
 * via an env var). `observedLcp`/`observedTbt`/`observedInp` come from
 * PerformanceObserver callbacks set up in the fixture before navigation; the
 * in-page read that follows would otherwise be too late (LCP entries already
 * fired by `networkidle`).
 */
export async function collectPageMetrics(
  page: Page,
  testLabel: string,
  startMs: number,
  projectName?: string,
  observedVitals?: ObservedVitals
): Promise<PageMetrics> {
  const viewport = projectName || "unknown";

  const raw = await page.evaluate(() => {
    const nav = performance.getEntriesByType(
      "navigation"
    )[0] as PerformanceNavigationTiming | undefined;
    const paint = performance.getEntriesByType("paint");
    const resources = performance.getEntriesByType(
      "resource"
    ) as PerformanceResourceTiming[];

    // Navigation timing
    const navStart = nav?.startTime ?? 0;
    const navigation = nav
      ? {
          dns: nav.domainLookupEnd - nav.domainLookupStart,
          tcp: nav.connectEnd - nav.connectStart,
          ttfb: nav.responseStart - navStart,
          domContentLoaded:
            nav.domContentLoadedEventEnd - navStart || null,
          load: nav.loadEventEnd - navStart || null,
          domInteractive: nav.domInteractive - navStart,
          responseEnd: nav.responseEnd - navStart,
        }
      : {
          dns: 0,
          tcp: 0,
          ttfb: 0,
          domContentLoaded: null,
          load: null,
          domInteractive: 0,
          responseEnd: 0,
        };

    // Paint metrics
    const fp = paint.find((p) => p.name === "first-paint")?.startTime ?? null;
    const fcp =
      paint.find((p) => p.name === "first-contentful-paint")?.startTime ??
      null;

    // CLS — walk layout-shift entries (non-deviated only)
    let cls = 0;
    try {
      const shifts = performance.getEntriesByType(
        "layout-shift"
      ) as any[];
      for (const s of shifts) {
        if (!s.hadRecentInput) cls += s.value;
      }
    } catch {}

    // TBT — sum blocking time from longtask entries
    let tbt = 0;
    try {
      const longtasks = performance.getEntriesByType("longtask");
      for (const lt of longtasks) {
        tbt += Math.min(lt.duration, 500);
      }
    } catch {}

    // DOM stats
    const body = document.body;
    const allEls = body.querySelectorAll("*");
    let maxDepth = 0;
    const walkDepth = (el: Element, depth: number) => {
      if (depth > maxDepth) maxDepth = depth;
      for (const child of el.children) walkDepth(child, depth + 1);
    };
    walkDepth(body, 0);

    const imgs = document.querySelectorAll("img");
    let imgWithAlt = 0;
    imgs.forEach((img) => {
      if (img.alt && img.alt.trim()) imgWithAlt++;
    });

    const dom = {
      nodeCount: allEls.length,
      depth: maxDepth,
      imgCount: imgs.length,
      imgWithAlt,
      imgWithoutAlt: imgs.length - imgWithAlt,
      linkCount: document.querySelectorAll("a[href]").length,
      formCount: document.querySelectorAll("form").length,
      inputCount: document.querySelectorAll(
        "input, textarea, select"
      ).length,
      headingCount: document.querySelectorAll(
        "h1, h2, h3, h4, h5, h6"
      ).length,
      ariaLandmarkCount: document.querySelectorAll(
        "main, nav, aside, header, footer, section[aria-label], section[aria-labelledby], form[aria-label], form[aria-labelledby], [role=main], [role=navigation], [role=complementary], [role=banner], [role=contentinfo], [role=search]"
      ).length,
      tabindexNegative: document.querySelectorAll("[tabindex]").length,
    };

    // Resource summary by type
    const byType: Record<string, { count: number; bytes: number }> = {};
    let totalBytes = 0;
    for (const r of resources) {
      const t = r.initiatorType || "other";
      if (!byType[t]) byType[t] = { count: 0, bytes: 0 };
      byType[t].count++;
      byType[t].bytes += r.transferSize || 0;
      totalBytes += r.transferSize || 0;
    }

    // Memory (Chromium only)
    const perfMem = (performance as any).memory;
    const memory = {
      jsHeapUsed: perfMem?.usedJSHeapSize ?? null,
      jsHeapLimit: perfMem?.jsHeapSizeLimit ?? null,
      domNodes: document.getElementsByTagName("*").length,
    };

    // LCP from largest-contentful-paint entries
    let lcp = null;
    try {
      const lcpEntries = performance.getEntriesByType(
        "largest-contentful-paint"
      ) as any[];
      if (lcpEntries.length > 0) {
        lcp = lcpEntries[lcpEntries.length - 1].startTime;
      }
    } catch {}

    return {
      navigation,
      paint: { fp, fcp },
      cls,
      tbt,
      lcp,
      dom,
      resources: {
        totalRequests: resources.length,
        totalBytes,
        byType,
      },
      memory,
    };
  });

  return {
    url: page.url(),
    test: testLabel,
    viewport,
    timestamp: new Date().toISOString(),
    navigation: raw.navigation,
    vitals: {
      fp: raw.paint.fp,
      fcp: raw.paint.fcp,
      // Prefer PerformanceObserver-captured values (recorded during the page's
      // lifetime) over the post-hoc snapshot, which can miss them entirely.
      lcp: observedVitals?.lcp ?? raw.lcp,
      cls: raw.cls,
      tbt: observedVitals?.tbt ?? raw.tbt,
      inp: observedVitals?.inp ?? null,
    },
    dom: raw.dom,
    resources: raw.resources,
    memory: raw.memory,
    testDurationMs: Date.now() - startMs,
  };
}

// ── Lighthouse-equivalent throttling ────────────────────────────────────────

/**
 * Applies network + CPU throttling matching Lighthouse's/PageSpeed Insights'
 * simulated mobile lab-data profile (a mid-tier Android device on "Slow 4G"):
 * ~1.6 Mbps down / 750 Kbps up / 150ms RTT, 4x CPU slowdown. An unthrottled
 * local Playwright run against a same-machine dev instance reads far better
 * than PSI's own numbers largely because of this gap — a page can only be
 * fairly compared against a PSI report once it's under equivalent
 * conditions. Chromium-only (CDP).
 */
export async function applyLighthouseMobileThrottling(page: Page): Promise<void> {
  const client = await page.context().newCDPSession(page);
  await client.send("Network.emulateNetworkConditions", {
    offline: false,
    downloadThroughput: (1.6 * 1024 * 1024) / 8,
    uploadThroughput: (750 * 1024) / 8,
    latency: 150,
  });
  await client.send("Emulation.setCPUThrottlingRate", { rate: 4 });
}

// ── Error context gatherer ───────────────────────────────────────────────────

export async function gatherErrorContext(
  page: Page,
  testLabel: string,
  file: string,
  error: unknown,
  consoleErrors: ConsoleEntry[],
  networkFailures: NetworkFailure[],
  metrics: PageMetrics | null,
  projectName?: string
): Promise<TestErrorContext> {
  const viewport = projectName || "unknown";

  let domSnapshot = "";
  try {
    domSnapshot = await page.evaluate(() => {
      const html = document.body?.outerHTML ?? "";
      return html.length > 51200 ? html.slice(0, 51200) + "\n... [truncated]" : html;
    });
  } catch {
    domSnapshot = "[could not capture DOM]";
  }

  return {
    test: testLabel,
    file,
    viewport,
    url: page.url(),
    // testInfo.error is Playwright's plain TestError object ({ message,
    // stack, ... }), never `instanceof Error` — that check was always false,
    // so every entry fell through to String(error), i.e. the useless
    // "[object Object]", silently discarding the actual assertion message
    // (including the WCAG violation report text the reporter's
    // accessibilitySummary greps for).
    error:
      (error as { stack?: string; message?: string } | undefined)?.stack ??
      (error as { stack?: string; message?: string } | undefined)?.message ??
      String(error),
    consoleErrors,
    networkFailures,
    domSnapshot,
    metrics,
    timestamp: new Date().toISOString(),
  };
}

// ── Aggregate write ──────────────────────────────────────────────────────────

const RESULTS_DIR = path.resolve(__dirname, "..", "test-results");

export function appendMetrics(m: PageMetrics): void {
  fs.mkdirSync(RESULTS_DIR, { recursive: true });
  const file = path.join(RESULTS_DIR, "metrics.jsonl");
  fs.appendFileSync(file, JSON.stringify(m) + "\n");
}

export function writeErrorContext(ctx: TestErrorContext): void {
  fs.mkdirSync(RESULTS_DIR, { recursive: true });
  const file = path.join(RESULTS_DIR, "errors.jsonl");
  fs.appendFileSync(file, JSON.stringify(ctx) + "\n");
}

export function writeAnalysisReport(
  allMetrics: PageMetrics[],
  allErrors: TestErrorContext[]
): void {
  fs.mkdirSync(RESULTS_DIR, { recursive: true });

  // ── Metric summaries by test ──
  const byTest = new Map<string, PageMetrics[]>();
  for (const m of allMetrics) {
    const arr = byTest.get(m.test) || [];
    arr.push(m);
    byTest.set(m.test, arr);
  }

  const summaries: any[] = [];
  for (const [test, entries] of byTest) {
    const validNav = entries.filter((e) => e.navigation.load > 0);
    const validFcp = entries.filter((e) => e.vitals.fcp !== null);
    const validLcp = entries.filter((e) => e.vitals.lcp !== null);

    summaries.push({
      test,
      runs: entries.length,
      navigation: {
        load_avg: avg(validNav.map((e) => e.navigation.load)),
        load_p95: p95(validNav.map((e) => e.navigation.load)),
        ttfb_avg: avg(validNav.map((e) => e.navigation.ttfb)),
        ttfb_p95: p95(validNav.map((e) => e.navigation.ttfb)),
        domContentLoaded_avg: avg(
          validNav.map((e) => e.navigation.domContentLoaded)
        ),
      },
      vitals: {
        fcp_avg: avg(validFcp.map((e) => e.vitals.fcp!)),
        fcp_p95: p95(validFcp.map((e) => e.vitals.fcp!)),
        lcp_avg: avg(validLcp.map((e) => e.vitals.lcp!)),
        lcp_p95: p95(validLcp.map((e) => e.vitals.lcp!)),
        cls_max: Math.max(...entries.map((e) => e.vitals.cls ?? 0)),
        tbt_avg: avg(entries.map((e) => e.vitals.tbt ?? 0)),
      },
      dom: {
        nodeCount_avg: avg(entries.map((e) => e.dom.nodeCount)),
        imgWithoutAlt_max: Math.max(
          ...entries.map((e) => e.dom.imgWithoutAlt)
        ),
      },
      resources: {
        totalRequests_avg: avg(
          entries.map((e) => e.resources.totalRequests)
        ),
        totalBytes_avg: avg(entries.map((e) => e.resources.totalBytes)),
      },
      memory: {
        jsHeapUsed_avg: avg(
          entries
            .map((e) => e.memory.jsHeapUsed)
            .filter((v): v is number => v !== null)
        ),
      },
      testDurationMs_avg: avg(entries.map((e) => e.testDurationMs)),
    });
  }

  // ── Error summary ──
  const errorSummary = allErrors.map((e) => ({
    test: e.test,
    viewport: e.viewport,
    url: e.url,
    error:
      e.error.split("\n").slice(0, 5).join("\n"), // first 5 lines of stack
    consoleErrors: e.consoleErrors.length,
    networkFailures: e.networkFailures.map(
      (f) => `${f.method} ${f.url} → ${f.status} ${f.statusText}`
    ),
  }));

  const report = {
    generatedAt: new Date().toISOString(),
    totalTests: allMetrics.length,
    totalErrors: allErrors.length,
    testSummaries: summaries,
    errors: errorSummary,
  };

  fs.writeFileSync(
    path.join(RESULTS_DIR, "analysis.json"),
    JSON.stringify(report, null, 2)
  );
}

// ── Helpers ──────────────────────────────────────────────────────────────────

function avg(nums: number[]): number {
  if (nums.length === 0) return 0;
  return Math.round((nums.reduce((a, b) => a + b, 0) / nums.length) * 100) / 100;
}

function p95(nums: number[]): number {
  if (nums.length === 0) return 0;
  const sorted = [...nums].sort((a, b) => a - b);
  const idx = Math.ceil(sorted.length * 0.95) - 1;
  return Math.round(sorted[Math.max(0, idx)] * 100) / 100;
}
