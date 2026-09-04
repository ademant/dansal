import { defineConfig, devices } from "@playwright/test";
import * as fs from "fs";
import * as path from "path";
import { AUTH_FILE } from "./helpers/auth";

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
  // Runs once before any test file/worker starts: one real login, saved to
  // AUTH_FILE below — see global-setup.ts and #1252 for why this replaced
  // 13 real per-run logins with 1.
  globalSetup: require.resolve("./global-setup"),
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
    // Every test's `page` fixture loads pre-authenticated as admin from
    // global-setup.ts's one login, instead of each test logging in itself.
    storageState: AUTH_FILE,
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
