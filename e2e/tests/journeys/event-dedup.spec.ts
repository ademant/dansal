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

  test("Tier 3: same location within 3h merges with no title check", async ({
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
    const mergedId = await postEvent(
      page,
      eventPayload({
        location_id: dedupLocationId,
        title: `Dedup T3 renamed ${nonce}`,
        start_time: isoDateTime(2, 21, 0),
        end_time: isoDateTime(2, 23, 0),
      })
    );
    expect(mergedId).toBe(firstId);

    const distinctId = await postEvent(
      page,
      eventPayload({
        location_id: dedupLocationId,
        title: `Dedup T3 later ${nonce}`,
        start_time: isoDateTime(3, 6, 0),
        end_time: isoDateTime(3, 8, 0),
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