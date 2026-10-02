import { test, expect } from "../../helpers/fixtures";
import { Page } from "@playwright/test";
import {
  getTokenFromCookie,
  seedOrg,
  seedLocation,
  apiPost,
} from "../../helpers/seed";
import { AUTH_FILE } from "../../helpers/auth";

let seed: { token: string; orgId: number };
let dedupLocationId: number;

// Per-file nonce so this spec's titles/uids/urls can never collide with a
// previous run's rows (tiers 1/2/4 match on those strings), plus an origin
// ~10 days out so the tier-3 events can use a fresh location without ever
// tripping the index/render checks — the tier relationships only depend on
// relative ±3h offsets, never on absolute wall-clock.
const nonce = Math.random().toString(36).slice(2, 8);
const origin = new Date();
origin.setDate(origin.getDate() + 10);
origin.setHours(0, 0, 0, 0);

function isoDateTime(daysOffset: number, hour: number, minute = 0): string {
  const d = new Date(origin);
  d.setDate(d.getDate() + daysOffset);
  d.setHours(hour, minute, 0, 0);
  return d.toISOString().replace(".000Z", "");
}

function eventPayload(overrides: Record<string, unknown>): Record<string, unknown> {
  return {
    title: `Dedup ${nonce}`,
    description: "e2e dedup tier test",
    start_time: isoDateTime(0, 20, 0),
    end_time: isoDateTime(0, 21, 30),
    tags: ["bal-folk"],
    organization_id: seed.orgId,
    ...overrides,
  };
}

const API_BASE = process.env.API_URL ?? "http://localhost:8000";

async function authedJSON(page: Page, method: string, path: string): Promise<any> {
  const resp = await page.request.fetch(`${API_BASE}${path}`, {
    method,
    headers: { Authorization: `Bearer ${seed.token}` },
  });
  if (!resp.ok()) throw new Error(`${method} ${path} → ${resp.status()}`);
  return resp.status() === 204 ? null : resp.json();
}

async function postEvent(
  page: Page,
  payload: Record<string, unknown>
): Promise<number> {
  const data = await apiPost(page, "/api/v1/events", seed.token, payload);
  return data[0].id;
}

test.describe("Event dedup tiers (API)", () => {
  test.beforeAll(async ({ browser }) => {
    const context = await browser.newContext({ storageState: AUTH_FILE });
    const setupPage = await context.newPage();
    const token = await getTokenFromCookie(setupPage);
    seed = { token, orgId: await seedOrg(setupPage, token) };
    dedupLocationId = await seedLocation(setupPage, token);
    await context.close();
  });

  test("Tier 1: same uid merges regardless of title/time/location", async ({
    page,
  }) => {
    const uid = `dedup-t1-${nonce}`;

    const firstId = await postEvent(page, eventPayload({ uid }));
    const mergedId = await postEvent(
      page,
      eventPayload({
        uid,
        title: `Dedup T1 renamed ${nonce}`,
        start_time: isoDateTime(3, 12, 0),
        end_time: isoDateTime(3, 14, 0),
      })
    );
    expect(mergedId).toBe(firstId);

    const distinctId = await postEvent(
      page,
      eventPayload({
        uid: `dedup-t1-other-${nonce}`,
        title: `Dedup T1 other ${nonce}`,
      })
    );
    expect(distinctId).not.toBe(firstId);
  });

  test("Tier 2: same url within 3h merges; outside 3h is distinct", async ({
    page,
  }) => {
    const url = `https://dedup-t2-${nonce}.example.com/event`;

    const firstId = await postEvent(page, eventPayload({ url }));
    const mergedId = await postEvent(
      page,
      eventPayload({
        url,
        title: `Dedup T2 renamed ${nonce}`,
        start_time: isoDateTime(0, 21, 0),
        end_time: isoDateTime(0, 23, 0),
      })
    );
    expect(mergedId).toBe(firstId);

    const distinctId = await postEvent(
      page,
      eventPayload({
        url,
        title: `Dedup T2 later ${nonce}`,
        start_time: isoDateTime(1, 6, 0),
        end_time: isoDateTime(1, 8, 0),
      })
    );
    expect(distinctId).not.toBe(firstId);
  });

  // #1424: tier 3 (same venue, ±3h, no title check) only auto-merges when it
  // is clearly the same event — a manual creation is inserted and both are
  // flagged for review (resolved on /admin/duplicates/{id}, #1427).
  test("Tier 3: a manual event at the same venue within 3h is flagged, not merged", async ({
    page,
  }) => {
    const firstId = await postEvent(
      page,
      eventPayload({
        location_id: dedupLocationId,
        title: `Dedup T3 first ${nonce}`,
        start_time: isoDateTime(2, 20, 0),
        end_time: isoDateTime(2, 22, 0),
      })
    );
    const secondId = await postEvent(
      page,
      eventPayload({
        location_id: dedupLocationId,
        title: `Dedup T3 renamed ${nonce}`,
        start_time: isoDateTime(2, 21, 0),
        end_time: isoDateTime(2, 23, 0),
      })
    );
    try {
      expect(secondId).not.toBe(firstId);
      const check = await authedJSON(page, "GET", `/api/v1/events/${secondId}/duplicate-check`);
      expect(check.flagged).toBe(true);
      expect(check.partner_id).toBe(firstId);
      expect(check.reasons).toContain("venue");
    } finally {
      // Don't leave the pair in the admin's possible-duplicates list.
      await authedJSON(page, "DELETE", `/api/v1/events/${secondId}`).catch(() => {});
      await authedJSON(page, "DELETE", `/api/v1/events/${firstId}`).catch(() => {});
    }
  });

  test("Tier 3: the same feed re-sending its event with a new title merges", async ({
    page,
  }) => {
    const sources = await authedJSON(page, "GET", "/api/v1/feeds");
    test.skip(!Array.isArray(sources) || sources.length === 0, "needs a fetch source on the target");
    const fetch_source_id = sources[0].id;
    const firstId = await postEvent(
      page,
      eventPayload({
        location_id: dedupLocationId,
        fetch_source_id,
        title: `Dedup T3 feed placeholder ${nonce}`,
        start_time: isoDateTime(7, 20, 0),
        end_time: isoDateTime(7, 22, 0),
      })
    );
    const mergedId = await postEvent(
      page,
      eventPayload({
        location_id: dedupLocationId,
        fetch_source_id,
        title: `Dedup T3 feed lineup announced ${nonce}`,
        start_time: isoDateTime(7, 20, 30),
        end_time: isoDateTime(7, 23, 0),
      })
    );
    expect(mergedId).toBe(firstId);

    const distinctId = await postEvent(
      page,
      eventPayload({
        location_id: dedupLocationId,
        fetch_source_id,
        title: `Dedup T3 feed later ${nonce}`,
        start_time: isoDateTime(8, 6, 0),
        end_time: isoDateTime(8, 8, 0),
      })
    );
    expect(distinctId).not.toBe(firstId);
  });

  test("Tier 4: same title within 3h merges without a location", async ({
    page,
  }) => {
    const title = `Dedup T4 shared ${nonce}`;

    const firstId = await postEvent(
      page,
      eventPayload({
        title,
        start_time: isoDateTime(4, 20, 0),
        end_time: isoDateTime(4, 22, 0),
      })
    );
    const mergedId = await postEvent(
      page,
      eventPayload({
        title,
        start_time: isoDateTime(4, 21, 0),
        end_time: isoDateTime(4, 23, 0),
      })
    );
    expect(mergedId).toBe(firstId);

    const distinctId = await postEvent(
      page,
      eventPayload({
        title,
        start_time: isoDateTime(5, 6, 0),
        end_time: isoDateTime(5, 8, 0),
      })
    );
    expect(distinctId).not.toBe(firstId);
  });
});