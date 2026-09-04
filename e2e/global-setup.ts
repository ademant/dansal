import { chromium, FullConfig } from "@playwright/test";
import * as fs from "fs";
import * as path from "path";
import { createUsers, loginAs } from "./helpers/seed";
import { ADMIN } from "./fixtures/data";
import { AUTH_FILE } from "./helpers/auth";

// Runs once for the whole suite, before any test file or worker starts —
// regardless of how many spec files call fullSeed() or how many workers run
// them in parallel. Performs exactly one real login-form submission and
// persists the resulting session cookies to AUTH_FILE. Every spec file's
// setupPage (browser.newContext({ storageState: AUTH_FILE })) and every
// test's `page` fixture (playwright.config.ts's use.storageState) then load
// pre-authenticated instead of re-submitting the login form.
//
// This collapses what used to be 13 real logins per full run (9 implicit,
// one per fullSeed()-calling file's beforeAll, plus 4 explicit per-test
// calls) down to 1 — see #1252. Beyond the ~45s of dead anti-bot wait time
// that saved, it also stops the suite from tripping its own target's
// per-IP login throttle (cmd/dansal_web/auth.go's newLoginThrottle) against
// itself: 9 files' beforeAll hooks hitting /login concurrently under the
// default 8-worker parallelism, on a stale seed-user password, is exactly
// what produced unrelated-looking page.waitForURL timeouts before.
export default async function globalSetup(config: FullConfig): Promise<void> {
  // Idempotent — safe even if the fixture users already exist from a prior
  // run (createUsers() looks up the existing ID rather than failing).
  createUsers();

  const baseURL =
    config.projects[0]?.use?.baseURL ??
    process.env.BASE_URL ??
    "http://localhost:8080";

  const browser = await chromium.launch();
  const page = await browser.newPage({ baseURL });
  await loginAs(page, ADMIN.email, ADMIN.password);

  fs.mkdirSync(path.dirname(AUTH_FILE), { recursive: true });
  await page.context().storageState({ path: AUTH_FILE });

  await browser.close();
}
