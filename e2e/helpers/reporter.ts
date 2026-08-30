import * as fs from "fs";
import * as path from "path";
import type {
  Reporter,
  FullResult,
  TestCase,
  TestResult,
} from "@playwright/test/reporter";

const RESULTS_DIR = path.resolve(__dirname, "..", "test-results");

interface MetricSummary {
  test: string;
  file: string;
  viewport: string;
  status: string;
  duration_ms: number;
  navigation: {
    ttfb: number;
    domContentLoaded: number;
    load: number;
  };
  vitals: {
    fcp: number | null;
    lcp: number | null;
    cls: number;
    tbt: number;
  };
  dom: {
    nodeCount: number;
    imgWithoutAlt: number;
  };
  resources: {
    totalRequests: number;
    totalBytes: number;
  };
  memory: {
    jsHeapUsed: number | null;
  };
  errors: {
    console: number;
    network: number;
  };
  timestamp: string;
}

interface ErrorEntry {
  test: string;
  file: string;
  viewport: string;
  error: string;
  url: string;
  consoleErrors: any[];
  networkFailures: any[];
  timestamp: string;
}

interface AnalysisReport {
  generatedAt: string;
  summary: {
    totalTests: number;
    passed: number;
    failed: number;
    skipped: number;
    flaky: number;
    duration_s: number;
  };
  metrics: MetricSummary[];
  errors: ErrorEntry[];
  performanceHighlights: {
    slowestPages: Array<{ test: string; load_ms: number }>;
    highestCls: Array<{ test: string; cls: number }>;
    largestDom: Array<{ test: string; nodes: number }>;
    memoryHeaviest: Array<{ test: string; heap_mb: number }>;
  };
  accessibilitySummary: {
    totalViolations: number;
    byRule: Record<string, number>;
  };
}

export default class DansalReporter implements Reporter {
  private testResults: any[] = [];
  private startTime = Date.now();

  onTestEnd(test: TestCase, result: TestResult) {
    this.testResults.push({
      title: test.title,
      titlePath: test.titlePath(),
      file: test.location.file,
      status: result.status,
      duration: result.duration,
      retry: result.retry,
      errors: result.errors,
      annotations: result.annotations,
    });
  }

  async onEnd(result: FullResult) {
    const elapsed = (Date.now() - this.startTime) / 1000;

    // ── Read metrics.jsonl ──
    const metricsFile = path.join(RESULTS_DIR, "metrics.jsonl");
    const rawMetrics: any[] = [];
    if (fs.existsSync(metricsFile)) {
      for (const line of fs
        .readFileSync(metricsFile, "utf-8")
        .split("\n")
        .filter(Boolean)) {
        try {
          rawMetrics.push(JSON.parse(line));
        } catch {}
      }
    }

    // ── Read errors.jsonl ──
    const errorsFile = path.join(RESULTS_DIR, "errors.jsonl");
    const rawErrors: any[] = [];
    if (fs.existsSync(errorsFile)) {
      for (const line of fs
        .readFileSync(errorsFile, "utf-8")
        .split("\n")
        .filter(Boolean)) {
        try {
          rawErrors.push(JSON.parse(line));
        } catch {}
      }
    }

    // ── Build metric summaries (group by test name) ──
    const byTest = new Map<string, any[]>();
    for (const m of rawMetrics) {
      const key = m.test;
      if (!byTest.has(key)) byTest.set(key, []);
      byTest.get(key)!.push(m);
    }

    const metrics: MetricSummary[] = [];
    for (const [test, entries] of byTest) {
      const latest = entries[entries.length - 1];
      metrics.push({
        test,
        file: latest.url,
        viewport: latest.viewport,
        status: this.testResults.find((r) =>
          r.titlePath?.join(" > ")?.includes(test.split(" > ").pop())
        )?.status ?? "unknown",
        duration_ms: latest.testDurationMs,
        navigation: {
          ttfb: latest.navigation.ttfb,
          domContentLoaded: latest.navigation.domContentLoaded,
          load: latest.navigation.load,
        },
        vitals: {
          fcp: latest.vitals.fcp,
          lcp: latest.vitals.lcp,
          cls: latest.vitals.cls,
          tbt: latest.vitals.tbt,
        },
        dom: {
          nodeCount: latest.dom.nodeCount,
          imgWithoutAlt: latest.dom.imgWithoutAlt,
        },
        resources: {
          totalRequests: latest.resources.totalRequests,
          totalBytes: latest.resources.totalBytes,
        },
        memory: {
          jsHeapUsed: latest.memory.jsHeapUsed,
        },
        errors: {
          console: latest.vitals ? 0 : 0, // will be filled from error entries
          network: 0,
        },
        timestamp: latest.timestamp,
      });
    }

    // Enrich with error counts
    for (const m of metrics) {
      const errs = rawErrors.filter((e) => e.test === m.test);
      m.errors.console = errs.reduce(
        (sum, e) => sum + (e.consoleErrors?.length ?? 0),
        0
      );
      m.errors.network = errs.reduce(
        (sum, e) => sum + (e.networkFailures?.length ?? 0),
        0
      );
    }

    // ── Performance highlights ──
    const withLoad = metrics.filter((m) => m.navigation.load > 0);
    const slowestPages = [...withLoad]
      .sort((a, b) => b.navigation.load - a.navigation.load)
      .slice(0, 5)
      .map((m) => ({ test: m.test, load_ms: m.navigation.load }));

    const highestCls = [...metrics]
      .sort((a, b) => b.vitals.cls - a.vitals.cls)
      .slice(0, 5)
      .filter((m) => m.vitals.cls > 0)
      .map((m) => ({ test: m.test, cls: m.vitals.cls }));

    const largestDom = [...metrics]
      .sort((a, b) => b.dom.nodeCount - a.dom.nodeCount)
      .slice(0, 5)
      .map((m) => ({ test: m.test, nodes: m.dom.nodeCount }));

    const withHeap = metrics.filter(
      (m) => m.memory.jsHeapUsed !== null
    );
    const memoryHeaviest = [...withHeap]
      .sort(
        (a, b) =>
          (b.memory.jsHeapUsed ?? 0) - (a.memory.jsHeapUsed ?? 0)
      )
      .slice(0, 5)
      .map((m) => ({
        test: m.test,
        heap_mb: Math.round(((m.memory.jsHeapUsed ?? 0) / 1024 / 1024) * 100) / 100,
      }));

    // ── Accessibility summary from errors.jsonl (axe violations) ──
    const axeErrors = rawErrors.filter((e) =>
      e.error?.includes("WCAG violations")
    );
    const byRule: Record<string, number> = {};
    for (const e of axeErrors) {
      const matches = e.error.matchAll(/\] (\w+):/g);
      for (const m of matches) {
        byRule[m[1]] = (byRule[m[1]] || 0) + 1;
      }
    }

    // ── Build report ──
    const report: AnalysisReport = {
      generatedAt: new Date().toISOString(),
      summary: {
        totalTests: this.testResults.length,
        passed: this.testResults.filter((r) => r.status === "passed").length,
        failed: this.testResults.filter((r) => r.status === "failed").length,
        skipped: this.testResults.filter((r) => r.status === "skipped").length,
        flaky: this.testResults.filter((r) => r.retry > 0 && r.status === "passed").length,
        duration_s: elapsed,
      },
      metrics,
      errors: rawErrors.map((e) => ({
        test: e.test,
        file: e.file,
        viewport: e.viewport,
        error: e.error?.split("\n").slice(0, 8).join("\n") ?? "",
        url: e.url,
        consoleErrors: e.consoleErrors ?? [],
        networkFailures: e.networkFailures ?? [],
        timestamp: e.timestamp,
      })),
      performanceHighlights: {
        slowestPages,
        highestCls,
        largestDom,
        memoryHeaviest,
      },
      accessibilitySummary: {
        totalViolations: axeErrors.length,
        byRule,
      },
    };

    fs.mkdirSync(RESULTS_DIR, { recursive: true });
    fs.writeFileSync(
      path.join(RESULTS_DIR, "analysis.json"),
      JSON.stringify(report, null, 2)
    );

    // ── Human-readable summary to stdout ──
    console.log("\n═══════════════════════════════════════════════");
    console.log("  DANSAL E2E ANALYSIS REPORT");
    console.log("═══════════════════════════════════════════════\n");
    console.log(
      `  Tests: ${report.summary.totalTests} total, ${report.summary.passed} passed, ${report.summary.failed} failed, ${report.summary.skipped} skipped, ${report.summary.flaky} flaky`
    );
    console.log(`  Duration: ${report.summary.duration_s.toFixed(1)}s\n`);

    if (slowestPages.length > 0) {
      console.log("  ── Slowest page loads ──");
      for (const p of slowestPages) {
        console.log(`    ${p.load_ms.toFixed(0)}ms  ${p.test}`);
      }
      console.log();
    }

    if (highestCls.length > 0) {
      console.log("  ── Highest CLS (layout shift) ──");
      for (const c of highestCls) {
        console.log(`    ${c.cls.toFixed(3)}  ${c.test}`);
      }
      console.log();
    }

    if (largestDom.length > 0) {
      console.log("  ── Largest DOM trees ──");
      for (const d of largestDom) {
        console.log(
          `    ${d.nodes} nodes  ${d.test}`
        );
      }
      console.log();
    }

    if (memoryHeaviest.length > 0) {
      console.log("  ── Heaviest memory usage ──");
      for (const m of memoryHeaviest) {
        console.log(
          `    ${m.heap_mb} MB  ${m.test}`
        );
      }
      console.log();
    }

    if (report.errors.length > 0) {
      console.log("  ── Errors ──");
      for (const e of report.errors) {
        console.log(`    [${e.viewport}] ${e.test}`);
        console.log(`      ${e.error.split("\n")[0]}`);
        if (e.networkFailures.length > 0) {
          for (const f of e.networkFailures) {
            console.log(`      network: ${f.url} → ${f.status}`);
          }
        }
        console.log();
      }
    }

    console.log(
      `  Full report: ${path.join(RESULTS_DIR, "analysis.json")}`
    );
    console.log("═══════════════════════════════════════════════\n");
  }
}
