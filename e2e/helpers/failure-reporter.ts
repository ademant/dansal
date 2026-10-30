import type {
  Reporter,
  FullResult,
  TestCase,
  TestResult,
} from "@playwright/test/reporter";
import * as fs from "fs";
import * as path from "path";

// Prints each failure as it happens (title, error, location, artifacts) and
// repeats the failing and flaky tests at the end, so a long run's output
// ends with what to look at instead of a bare count.

const ANSI = /\x1b\[[0-9;]*m/g;
const MAX_LINES = 12;

// Cause tags tell an environment problem from a product bug at a glance. The
// order matters: the first matching rule wins.
//   infrastructure: the admin CLI, the API or the socket failed, not the page
//   timeout:        the test or a wait ran out of time
//   strict-mode:    a locator matched more than one element (a test bug)
//   assertion:      the page or API did not match the expectation (the default)
const CAUSES: [string, RegExp][] = [
  ["infrastructure", /admin CLI failed|ETIMEDOUT|ECONNREFUSED|ECONNRESET|socket hang up|getaddrinfo|EAI_AGAIN/],
  ["timeout", /Test timeout of \d+ms exceeded|Timeout \d+ms exceeded|exceeded while waiting/],
  ["strict-mode", /strict mode violation/],
];

function classify(status: string, message: string): string {
  if (status === "timedOut") return "timeout";
  for (const [tag, re] of CAUSES) if (re.test(message)) return tag;
  return "assertion";
}

// The first API error_id in a message, if the failure quoted one.
function errorId(message: string): string | undefined {
  return message.match(/error_id["':\s=]+([A-Za-z0-9_-]{6,})/)?.[1];
}

interface Failure {
  title: string;
  project: string;
  retry: number;
  cause: string;
  error_id?: string;
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
  const raw = error?.message ?? "";
  return {
    title: test.titlePath().filter(Boolean).join(" › "),
    project: test.parent.project()?.name ?? "?",
    retry: result.retry,
    cause: classify(result.status, raw),
    error_id: errorId(raw),
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
    `  cause: ${f.cause}${f.error_id ? ` (API error_id ${f.error_id})` : ""}`,
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
      console.log(`✘ [${f.cause}] [${f.project}] ${f.title}\n  ${f.message.split("\n")[0]}\n  at ${f.location}`);
    }
    if (this.flaky.length > 0) {
      console.log(`\nflaky (failed, then passed on retry): ${this.flaky.length} attempt(s)`);
      for (const f of this.flaky) {
        console.log(`~ [${f.project}] ${f.title}\n  ${f.message.split("\n")[0]}`);
      }
    }
    console.log(bar);
    this.writeSidecar();
  }

  // Appends one line per failed test to test-results/failures.jsonl. The runner
  // calls Playwright once per spec file, so each call appends; the runner
  // empties the file at the start of a run. It reads causes and error ids from
  // here rather than parsing the console output.
  private writeSidecar() {
    if (this.failed.length === 0) return;
    const dir = path.resolve(__dirname, "..", "test-results");
    fs.mkdirSync(dir, { recursive: true });
    const lines = this.failed.map((f) =>
      JSON.stringify({
        title: f.title,
        project: f.project,
        cause: f.cause,
        error_id: f.error_id ?? null,
      }),
    );
    fs.appendFileSync(path.join(dir, "failures.jsonl"), lines.join("\n") + "\n");
  }
}
