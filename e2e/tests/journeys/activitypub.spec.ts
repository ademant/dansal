import { execSync } from "child_process";
import { test, expect } from "../../helpers/fixtures";
import { Page } from "@playwright/test";
import { getTokenFromCookie, seedOrg } from "../../helpers/seed";
import { AUTH_FILE } from "../../helpers/auth";
import { ORG } from "../../fixtures/data";

// ActivityPub actor/discovery and IndexNow tests. Unlike most journey specs,
// these endpoints live on the *web* frontend (dansal_web, BASE_URL) rather
// than the REST API (API_URL), so fetches use site-relative paths that
// resolve against the Playwright baseURL.
//
// The actor docs are asserted for internal consistency (id ↔ inbox/outbox/
// followers ↔ publicKey ↔ sharedInbox hosts) instead of absolute domains so
// they pass on every instance regardless of the configured web.yaml `domain`
// (localhost on the throwaway runs, events.example.com in CI, the real
// public domain in production).

const ORG_SLUG = "bal-test-association"; // orgSlug("Bal Test Association")
const RELAY_SLUG = "relay";

// siteSettingsCache reloads site_settings at most every 10 s (cmd/dansal_web/
// sitecache.go), so a freshly seeded indexnow_key needs a full window before
// the key-file endpoint reflects it.
const SITE_SETTINGS_TTL_MS = 11_000;

function apHeaders(): Record<string, string> {
  return { Accept: "application/activity+json" };
}

// actorService fetches /org/{slug} as an ActivityPub client would and
// asserts the transport contracts (status + content negotiation).
async function actorService(page: Page, slug: string): Promise<any> {
  const resp = await page.request.fetch(`/org/${slug}`, { headers: apHeaders() });
  expect(resp.status()).toBe(200);
  expect(resp.headers()["content-type"]).toContain("application/activity+json");
  return resp.json();
}

function urlParts(id: string): { host: string; pathname: string } {
  const u = new URL(id);
  return { host: u.host, pathname: u.pathname };
}

// setIndexNowKey upserts the indexnow_key site setting directly into web.db.
// The web frontend has no endpoint that writes site_settings (only the
// separate webmin binary does), so the e2e suite seeds the value itself when
// the instance orchestration exposes a writable web.db (throwaway runs and
// CI). Python ships a sqlite3 module in its stdlib, avoiding a native npm
// dependency (Node 20 has no node:sqlite); values go through env vars so
// execSync never has to quote them into the shell command.
function setIndexNowKey(webDbPath: string, key: string): void {
  const sql = `INSERT INTO site_settings(key, value) VALUES('indexnow_key', '${key}') ON CONFLICT(key) DO UPDATE SET value = excluded.value`;
  execSync(
    `python3 -c "import os, sqlite3; db = sqlite3.connect(os.environ['WEBDB']); db.execute(os.environ['SQL']); db.commit(); db.close()"`,
    { env: { ...process.env, WEBDB: webDbPath, SQL: sql }, timeout: 10_000 }
  );
}

function randomKey(): string {
  return `e2eidx${Math.random().toString(36).slice(2, 12)}`;
}

test.describe("ActivityPub + IndexNow (web)", () => {
  // The org actor is resolved lazily against the API's organization list
  // (frontend.go's apActorHandler), so the fixture org must exist first.
  test.beforeAll(async ({ browser }) => {
    const context = await browser.newContext({ storageState: AUTH_FILE });
    const setupPage = await context.newPage();
    const token = await getTokenFromCookie(setupPage);
    await seedOrg(setupPage, token);
    await context.close();
  });

  test("org actor document is well-formed ActivityPub", async ({ page }) => {
    const first = await actorService(page, ORG_SLUG);
    const second = await actorService(page, ORG_SLUG);

    expect(first.type).toBe("Service");
    expect(first.preferredUsername).toBe(ORG_SLUG);
    expect(first.name).toBe(ORG.name);
    expect(first.manuallyApprovesFollowers).toBe(false);

    const { host, pathname } = urlParts(first.id);
    expect(pathname).toBe(`/org/${ORG_SLUG}`);
    expect(first.url).toBe(first.id);
    expect(first.inbox).toBe(`${first.id}/inbox`);
    expect(first.outbox).toBe(`${first.id}/outbox`);
    expect(first.followers).toBe(`${first.id}/followers`);
    expect(first.endpoints.sharedInbox).toContain("/inbox");
    expect(urlParts(first.endpoints.sharedInbox).host).toBe(host);
    expect(first.publicKey.id).toBe(`${first.id}#main-key`);
    expect(first.publicKey.owner).toBe(first.id);
    expect(first.publicKey.publicKeyPem).toContain("BEGIN PUBLIC KEY");

    // ensureActor is lazy and idempotent: a second fetch must reuse the
    // stored actor record, so both the id and the signing key are identical.
    expect(second.id).toBe(first.id);
    expect(second.publicKey.publicKeyPem).toBe(first.publicKey.publicKeyPem);
  });

  test("relay actor document is the synthetic Application", async ({ page }) => {
    const relay = await actorService(page, RELAY_SLUG);

    expect(relay.type).toBe("Application");
    expect(relay.preferredUsername).toBe(RELAY_SLUG);

    const { host, pathname } = urlParts(relay.id);
    expect(pathname).toBe(`/org/${RELAY_SLUG}`);
    expect(relay.inbox).toBe(`${relay.id}/inbox`);
    expect(relay.outbox).toBe(`${relay.id}/outbox`);
    expect(relay.followers).toBe(`${relay.id}/followers`);
    expect(urlParts(relay.endpoints.sharedInbox).host).toBe(host);
    expect(relay.publicKey.id).toBe(`${relay.id}#main-key`);
    expect(relay.publicKey.owner).toBe(relay.id);
    expect(relay.publicKey.publicKeyPem).toContain("BEGIN PUBLIC KEY");
  });

  test("webfinger resolves org and relay to their AP self links", async ({
    page,
  }) => {
    const orgActor = await actorService(page, ORG_SLUG);
    const relayActor = await actorService(page, RELAY_SLUG);

    for (const actor of [orgActor, relayActor]) {
      const host = urlParts(actor.id).host;
      const resource = `acct:${actor.preferredUsername}@${host}`;

      const resp = await page.request.fetch(
        `/.well-known/webfinger?resource=${encodeURIComponent(resource)}`
      );
      expect(resp.status()).toBe(200);
      expect(resp.headers()["content-type"]).toContain("application/jrd+json");

      const jrd = await resp.json();
      expect(jrd.subject).toBe(resource);
      const self = jrd.links.find(
        (l: any) => l.rel === "self" && l.type === "application/activity+json"
      );
      expect(self).toBeTruthy();
      expect(self.href).toBe(actor.id);
    }

    // A well-formed-but-unknown account must 404, not leak a page.
    const host = urlParts(orgActor.id).host;
    const unknown = await page.request.fetch(
      `/.well-known/webfinger?resource=${encodeURIComponent(
        `acct:definitely-not-a-real-actor-${Math.random().toString(36).slice(2, 8)}@${host}`
      )}`
    );
    expect(unknown.status()).toBe(404);
  });

  test("indexnow key file serves only the configured key", async ({ page }) => {
    const webDbPath = process.env.WEB_DB_PATH;
    if (!webDbPath) {
      // No writable web.db here (e.g. the shared dev instance): the key is
      // unset, so the endpoint must refuse every request. This still covers
      // the indexNowKeyFileHandler wiring.
      expect((await page.request.fetch("/nobody-knows-this-key.txt")).status()).toBe(404);
      expect((await page.request.fetch("/indexnow.txt")).status()).toBe(404);
      return;
    }

    const key = randomKey();
    setIndexNowKey(webDbPath, key);
    // Give siteSettingsCache (TTL 10 s) a full window to pick up the seed.
    await page.waitForTimeout(SITE_SETTINGS_TTL_MS);

    const ok = await page.request.fetch(`/${key}.txt`);
    expect(ok.status()).toBe(200);
    expect(ok.headers()["content-type"]).toContain("text/plain");
    expect(await ok.text()).toBe(key);

    // The key file is exactly /{key}.txt — similar names must 404.
    expect((await page.request.fetch(`/${key}`)).status()).toBe(404);
    expect((await page.request.fetch(`/${key}.txt-evil`)).status()).toBe(404);

    // Restore the unset state so later specs in this run (and any other
    // instance sharing the DB) don't start firing notifyIndexNow POSTs.
    setIndexNowKey(webDbPath, "");
  });
});