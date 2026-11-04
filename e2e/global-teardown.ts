import { FullConfig } from "@playwright/test";
import { AUTH_FILE } from "./helpers/auth";
import { cleanupThisRun, tokenFromAuthFile } from "./helpers/cleanup";

// Runs once after the whole suite, pass or fail (#1793). Deletes every
// entity this run recorded (helpers/cleanup.ts). Entities that fail to
// delete stay in the run's ledger and are retried by the next run's setup.
export default async function globalTeardown(config: FullConfig): Promise<void> {
  const baseURL =
    config.projects[0]?.use?.baseURL ??
    process.env.BASE_URL ??
    "http://localhost:8080";
  await cleanupThisRun({
    apiBase: process.env.API_URL ?? "http://localhost:8000",
    token: tokenFromAuthFile(AUTH_FILE),
    urls: [baseURL],
  });
}
