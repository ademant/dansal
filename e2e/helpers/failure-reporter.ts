import type {
  Reporter,
  FullResult,
  TestCase,
  TestResult,
} from "@playwright/test/reporter";

// Prints each failure as it happens (title, error, location, artifacts) and
// repeats the failing and flaky tests at the end, so a long run's output
// ends with what to look at instead of a bare count.

const ANSI = /\x1b\[[0-9;]*m/g;
const MAX_LINES = 12;

interface Failure {
  title: string;
  project: string;
  retry: number;
  message: string;
  location: string;
  artifacts: string[];
}

function describe(test: TestCase, result: TestResult): Failure {
  const error = result.errors[0];
  const lines = (error?.message ?? "no error message").replace(ANSI, "").split("\n");
  const message =
    lines.length > MAX_LINES
      ? [...lines.slice(0, MAX_LINES), `... (${lines.length - MAX_LINES} more lines)`].join("\n")
      : lines.join("\n");
  const loc = error?.location ?? test.location;
  return {
    title: test.titlePath().filter(Boolean).join(" › "),
    project: test.parent.project()?.name ?? "?",
    retry: result.retry,
    message,
    location: `${loc.file}:${loc.line}`,
    artifacts: result.attachments
      .filter((a) => a.path && (a.name === "trace" || a.name === "screenshot" || a.name === "video"))
      .map((a) => `${a.name}: ${a.path}`),
  };
}

function print(label: string, f: Failure): void {
  const lines = [
    `✘ ${label} [${f.project}] (retry ${f.retry}) ${f.title}`,
    `  at ${f.location}`,
    ...f.message.split("\n").map((l) => `  ${l}`),
    ...f.artifacts.map((a) => `  ${a}`),
  ];
  console.log(lines.join("\n"));
}

export default class FailureReporter implements Reporter {
  private failed: Failure[] = [];
  private flaky: Failure[] = [];

  onTestEnd(test: TestCase, result: TestResult) {
    const outcome = test.outcome();
    if (result.status === "passed" || result.status === "skipped") return;
    if (outcome === "flaky") {
      // Each failed attempt is printed; the final pass is reported at the end.
      const f = describe(test, result);
      this.flaky.push(f);
      print("flaky attempt", f);
      return;
    }
    const f = describe(test, result);
    this.failed.push(f);
    print(result.status === "timedOut" ? "TIMEOUT" : "FAILED", f);
  }

  onEnd(result: FullResult) {
    const bar = "─".repeat(72);
    console.log(`\n${bar}\nFAILURE SUMMARY (${result.status})`);
    if (this.failed.length === 0) console.log("no failed tests");
    for (const f of this.failed) {
      console.log(`✘ [${f.project}] ${f.title}\n  ${f.message.split("\n")[0]}\n  at ${f.location}`);
    }
    if (this.flaky.length > 0) {
      console.log(`\nflaky (failed, then passed on retry): ${this.flaky.length} attempt(s)`);
      for (const f of this.flaky) {
        console.log(`~ [${f.project}] ${f.title}\n  ${f.message.split("\n")[0]}`);
      }
    }
    console.log(bar);
  }
}
