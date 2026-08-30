import { defineConfig, devices } from "@playwright/test";
import * as fs from "fs";
import * as path from "path";

const BASE_URL = process.env.BASE_URL ?? "http://localhost:8080";
const RESULTS_DIR = path.resolve(__dirname, "test-results");

// Clear previous run artifacts
for (const f of ["metrics.jsonl", "errors.jsonl", "analysis.json"]) {
  try {
    fs.unlinkSync(path.join(RESULTS_DIR, f));
  } catch {}
}

export default defineConfig({
  testDir: "./tests",
  // fullSeed()'s beforeAll hook is many sequential HTTP round-trips (3 CLI
  // user-creates, login, org/location lookups, 3 event creates, timetable,
  // musician, instructor) — 90s gives it real margin under load without
  // masking a genuinely hung step for minutes.
  timeout: 90_000,
  expect: { timeout: 10_000 },
  fullyParallel: false,
  retries: 1,
  reporter: [
    ["list"],
    ["json", { outputFile: "test-results/results.json" }],
    ["./helpers/reporter.ts"],
  ],
  use: {
    baseURL: BASE_URL,
    trace: "on-first-retry",
    screenshot: "only-on-failure",
    video: "retain-on-failure",
  },
  projects: [
    {
      name: "desktop",
      use: { ...devices["Desktop Chrome"] },
    },
    {
      name: "mobile",
      use: { ...devices["Pixel 7"] },
    },
  ],
});
