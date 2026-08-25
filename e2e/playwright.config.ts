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
  timeout: 60_000,
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
