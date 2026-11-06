import { test as base, expect, Page, TestInfo } from "@playwright/test";
import {
  collectPageMetrics,
  gatherErrorContext,
  appendMetrics,
  writeErrorContext,
  PageMetrics,
  ObservedVitals,
  ConsoleEntry,
  NetworkFailure,
} from "./metrics";

// ── Extended types ───────────────────────────────────────────────────────────

interface DansalFixtures {
  /** Page with auto-capture of console errors and network failures. */
  page: Page;
  /** Collector — call collect() after navigating to get rich metrics. */
  metrics: MetricsCollector;
}

interface MetricsCollector {
  /** Call after navigation to collect Web Vitals + DOM + resource stats. */
  collect(testLabel: string): Promise<PageMetrics>;
  /** All console errors captured during the test. */
  consoleErrors: ConsoleEntry[];
  /** All network failures captured during the test. */
  networkFailures: NetworkFailure[];
}

// ── Fixture ──────────────────────────────────────────────────────────────────

export const test = base.extend<DansalFixtures>({
  page: async ({ page }, use, testInfo) => {
    const consoleErrors: ConsoleEntry[] = [];
    const networkFailures: NetworkFailure[] = [];

    // Capture console errors
    page.on("console", (msg) => {
      if (msg.type() === "error" || msg.type() === "warning") {
        const loc = msg.location();
        consoleErrors.push({
          type: msg.type(),
          text: msg.text(),
          url: loc?.url ?? "",
          line: loc?.lineNumber ?? 0,
          column: loc?.columnNumber ?? 0,
          timestamp: new Date().toISOString(),
        });
      }
    });

    // Capture page errors (uncaught exceptions)
    page.on("pageerror", (err) => {
      consoleErrors.push({
        type: "pageerror",
        text: err.message,
        url: "",
        line: 0,
        column: 0,
        timestamp: new Date().toISOString(),
      });
    });

    // Capture network failures
    page.on("requestfailed", (req) => {
      networkFailures.push({
        url: req.url(),
        method: req.method(),
        status: 0,
        statusText: req.failure()?.errorText ?? "unknown",
        resourceType: req.resourceType(),
        failureText: req.failure()?.errorText ?? "unknown",
        timestamp: new Date().toISOString(),
      });
    });

    page.on("response", (resp) => {
      if (resp.status() >= 400) {
        networkFailures.push({
          url: resp.url(),
          method: resp.request().method(),
          status: resp.status(),
          statusText: resp.statusText(),
          resourceType: resp.request().resourceType(),
          failureText: `HTTP ${resp.status()}`,
          timestamp: new Date().toISOString(),
        });
      }
    });

    const projectName = testInfo.project?.name || "unknown";

    // ── Web Vitals observers ───────────────────────────────────────────────
    // LCP/TBT/INP are recorded live during the page's lifetime; reading them
    // back via performance.getEntriesByType after `networkidle` is too late
    // (the entries have already fired or, for INP, are buffered/never read).
    try {
      await page.addInitScript(() => {
        const ov = (window as any).__dansalVitals = {
          lcp: null,
          tbt: 0,
          inp: null,
        };
        try {
          new PerformanceObserver((list) => {
            const entries = list.getEntries();
            const last = entries[entries.length - 1];
            if (last) ov.lcp = last.startTime;
          }).observe({ type: "largest-contentful-paint", buffered: true });
        } catch {}
        try {
          new PerformanceObserver((list) => {
            for (const e of list.getEntries()) {
              ov.tbt += Math.min(e.duration, 500);
            }
          }).observe({ type: "longtask" });
        } catch {}
        try {
          new PerformanceObserver((list) => {
            for (const e of list.getEntries() as any[]) {
              ov.inp = e.processingStart
                ? e.duration
                : null;
            }
          }).observe({ type: "event", durationThreshold: 16 });
        } catch {}
      });
    } catch {}

    // Provide the metrics collector
    const startMs = Date.now();
    const collector: MetricsCollector = {
      consoleErrors,
      networkFailures,
      async collect(testLabel: string) {
        let observedVitals: ObservedVitals | undefined;
        try {
          const pageVitals = await page.evaluate(
            () => (window as any).__dansalVitals ?? null
          );
          if (pageVitals) observedVitals = { ...pageVitals };
        } catch {
          observedVitals = undefined;
        }
        return collectPageMetrics(
          page,
          testLabel,
          startMs,
          projectName,
          observedVitals
        );
      },
    };

    // Give the test access to the collector via testInfo
    (testInfo as any).__metricsCollector = collector;

    await use(page);

    // ── After test: collect metrics ──
    const label = testInfo.titlePath.join(" > ");
    let metrics: PageMetrics | null = null;
    try {
      let observedVitals: ObservedVitals | undefined;
      try {
        const pageVitals = await page.evaluate(
          () => (window as any).__dansalVitals ?? null
        );
        if (pageVitals) observedVitals = { ...pageVitals };
      } catch {
        observedVitals = undefined;
      }
      metrics = await collectPageMetrics(
        page,
        label,
        startMs,
        projectName,
        observedVitals
      );
      appendMetrics(metrics);
    } catch {
      // page may be closed — skip metrics
    }

    // ── On failure: say where the page was (#1830) ──
    // A timeout alone says nothing about the page. Attach the URL and any
    // visible alert text, so the report shows what the page was doing.
    if (testInfo.status !== "passed" && testInfo.status !== "skipped") {
      try {
        const state = await page.evaluate(() => {
          const alerts = Array.from(
            document.querySelectorAll('[role="alert"], .alert, .flash, .error-msg, .form-error')
          )
            .filter((el) => (el as HTMLElement).offsetParent !== null)
            .map((el) => (el.textContent ?? "").trim())
            .filter((t) => t !== "");
          return { url: location.href, alerts };
        });
        await testInfo.attach("page-state", {
          body: `url: ${state.url}\nvisible alerts: ${state.alerts.length ? state.alerts.join(" | ") : "none"}\n`,
          contentType: "text/plain",
        });
      } catch {
        await testInfo.attach("page-state", {
          body: "page was closed before its state could be read\n",
          contentType: "text/plain",
        });
      }
    }

    // ── On failure: gather full error context ──
    if (testInfo.status !== "passed" && testInfo.status !== "skipped") {
      try {
        const ctx = await gatherErrorContext(
          page,
          label,
          testInfo.file,
          testInfo.error,
          consoleErrors,
          networkFailures,
          metrics,
          projectName
        );
        writeErrorContext(ctx);
      } catch {
        // page may be closed — write what we can
        writeErrorContext({
          test: label,
          file: testInfo.file,
          viewport: projectName,
          url: "",
          error:
            testInfo.error?.stack ?? testInfo.error?.message ?? "unknown",
          consoleErrors,
          networkFailures,
          domSnapshot: "[could not capture]",
          metrics,
          timestamp: new Date().toISOString(),
        });
      }
    }
  },

  metrics: async ({}, use, testInfo) => {
    const collector = (testInfo as any).__metricsCollector as
      | MetricsCollector
      | undefined;
    if (!collector) {
      // Fallback — should not happen
      await use({
        collect: async () => ({} as PageMetrics),
        consoleErrors: [],
        networkFailures: [],
      });
      return;
    }
    await use(collector);
  },
});

export { expect };

/**
 * Shorthand: navigate + wait for network idle + collect metrics.
 * Returns the metrics for assertions.
 */
export async function gotoAndMeasure(
  page: Page,
  url: string,
  collector: MetricsCollector,
  testLabel: string
): Promise<PageMetrics> {
  await page.goto(url, { waitUntil: "networkidle" });
  return collector.collect(testLabel);
}
