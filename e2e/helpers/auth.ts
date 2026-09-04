import * as path from "path";

// Shared path for the admin session saved by global-setup.ts's one real
// login-form submission. Every spec file's setupPage (via
// browser.newContext({ storageState: AUTH_FILE })) and every test's `page`
// fixture (via playwright.config.ts's use.storageState) load this instead
// of re-submitting the login form — see #1252.
export const AUTH_FILE = path.resolve(__dirname, "../.auth/admin.json");
