/**
 * The admin timetable grid UI itself (#1265) — drag-to-create, drag-to-move,
 * the difficulty widget (#1232), the building super-header (#1233), and
 * column drag-reorder (#1237/#1278) — driven directly at
 * /admin/events/{id}/timetable, not just through the PUT .../timetable API
 * timetable-management.spec.ts already covers.
 *
 * admin_timetable.html's drags are hand-rolled pointerdown/move/up on
 * document/element using clientX/Y (no pointerType branching) — real
 * page.mouse events exercise them faithfully. PX_PER_MIN/SNAP are read off
 * `window` rather than hardcoded, matching the constants the page itself
 * computes pixel offsets from.
 *
 * There is no GET .../timetable route — every assertion of persisted state
 * reads the `timetable` field embedded in GET /api/v1/events/{id} (same
 * approach as timetable-management.spec.ts), or, for room order, the
 * `timetable_room_order` field PATCHed by the dedicated
 * PUT .../timetable/room-order endpoint (#1278 — persistence gap #1265
 * originally scoped around; closed since, so the column-reorder test here
 * also asserts the order survives a reload, not just the in-session render).
 *
 * The building-super-header and column-reorder scenarios avoid seeding any
 * location directly through the raw API — dansal_web's picker cache
 * (#1276) is only invalidated by the web-layer's own create/edit handlers,
 * not a raw API POST. Their rooms come from the timetable editor's own
 * "+ Create room" quick-create flow (already built into #tt-room-add,
 * itself a cache-invalidating web-layer handler); their event's own venue —
 * the "building" the rooms group under — is created through the real
 * admin location form for the same reason, rather than reusing the
 * shared seed's location (seedLocation() creates that via the raw API, so
 * adminTimetablePageHandler's own building-name lookup against the
 * (possibly still-stale) cached location list could otherwise silently
 * come back empty).
 *
 * Per #1265's agreed scope, drags (drag-to-create, drag-to-move, column
 * drag-reorder) stay desktop-first (test.skip on other projects) — the
 * popup and difficulty-widget clicks, and the building super-header
 * (no drag needed to assert the grouping itself), run on both.
 */
import { test, expect } from "@playwright/test";
import { Page } from "@playwright/test";
import { fullSeed, SeedResult, getTokenFromCookie, apiPost } from "../../helpers/seed";
import { randomFutureDate } from "../../fixtures/data";
import { AUTH_FILE } from "../../helpers/auth";

const WEB_BASE = process.env.BASE_URL ?? "http://localhost:8080";
const API_BASE = process.env.API_URL ?? "http://localhost:8000";

let seed: SeedResult;
let adminToken: string;

function unique(name: string): string {
  return `${name} ${Date.now()}`;
}

// Local calendar date/time strings (no toISOString/UTC conversion) — the
// server (Europe/Berlin here) echoes start_time back with an explicit
// offset, and the grid's own computeDays() truncates that to a UTC
// calendar date; hour 13 keeps enough margin either side of midnight that
// the two never land on different days regardless of the server's offset.
function isoDateTimeFor(d: Date, hour: number, minute = 0): string {
  const pad = (n: number) => String(n).padStart(2, "0");
  return `${d.getFullYear()}-${pad(d.getMonth() + 1)}-${pad(d.getDate())}T${pad(hour)}:${pad(minute)}:00`;
}
function isoDateFor(d: Date): string {
  const pad = (n: number) => String(n).padStart(2, "0");
  return `${d.getFullYear()}-${pad(d.getMonth() + 1)}-${pad(d.getDate())}`;
}

async function authedGet(page: Page, path: string) {
  return page.request.fetch(`${API_BASE}${path}`, {
    headers: { Authorization: `Bearer ${adminToken}` },
  });
}
async function authedJSON(page: Page, path: string): Promise<any> {
  return (await authedGet(page, path)).json();
}

// A single-calendar-day event, far enough out (and at a distinctive hour)
// to dodge dedup against the shared seed events (event-import skill:
// same-location fixtures need >3h separation or a distinct location; this
// one has no location at all unless withLocation is set, so title alone
// already disambiguates it from every other fixture).
async function createGridEvent(
  page: Page,
  title: string,
  opts: { locationId?: number } = {}
): Promise<{ id: number; day: string }> {
  const d = randomFutureDate(60, 90);
  const day = isoDateFor(d);
  const body: any = {
    title,
    start_time: isoDateTimeFor(d, 13, 0),
    end_time: isoDateTimeFor(d, 16, 0),
    organization_id: seed.orgId,
  };
  if (opts.locationId) body.location_id = opts.locationId;
  const data = await apiPost(page, "/api/v1/events", adminToken, body);
  const created = Array.isArray(data) ? data[0] : data;
  return { id: created.id, day };
}

async function putTimetable(page: Page, eventId: number, entries: any[]) {
  return page.request.fetch(`${API_BASE}/api/v1/events/${eventId}/timetable`, {
    method: "PUT",
    headers: { "Content-Type": "application/json", Authorization: `Bearer ${adminToken}` },
    data: JSON.stringify(entries),
  });
}

async function saveAndWait(page: Page) {
  await page.locator("#save-btn").click();
  await expect(page.locator("#save-status")).toHaveText("Saved");
}

// dansal_web caches GetLocations for admin pickers, including
// adminTimetablePageHandler's own TopLocationName lookup (#1276) — a
// location created by POSTing straight to the raw API (seedLocation(), as
// seed.locationId is) never invalidates that cache, so the timetable
// page's own building-name lookup can silently come back empty for up to
// a minute. Creating it through the real admin form instead invalidates
// the cache on save, same as createOrg's pattern elsewhere in the suite.
// The location edit/create form collapses every section except #sec-base
// (locations.spec.ts's own openSection() helper, reproduced here) — #town
// lives under #sec-address, so it's display:none and unfillable until its
// nav item is clicked open.
async function openLocationSection(page: Page, target: string): Promise<void> {
  const btn = page.locator(`.loc-nav-item[data-target="${target}"]`);
  if (!(await btn.isVisible())) {
    await page.locator("#loc-nav-toggle").click();
  }
  await btn.click();
}

async function createBuildingLocation(page: Page, name: string): Promise<number> {
  await page.goto(`${WEB_BASE}/admin/locations/new`);
  await page.fill("#location", name);
  await page.fill("#short_name", name);
  await openLocationSection(page, "sec-address");
  await page.fill("#town", "Testville");
  await page.locator("#save-btn").click();
  await page.waitForURL("**/admin/locations");
  const resp = await page.request.fetch(
    `${API_BASE}/api/v1/locations?name=${encodeURIComponent(name)}&limit=100`
  );
  const list = await resp.json();
  const match = Array.isArray(list) ? list.find((l: any) => l.location === name) : null;
  if (!match) throw new Error(`location not found: ${name}`);
  return match.id;
}

test.describe("Admin timetable grid UI", () => {
  test.beforeAll(async ({ browser }) => {
    const context = await browser.newContext({ storageState: AUTH_FILE });
    const setupPage = await context.newPage();
    seed = await fullSeed(setupPage);
    adminToken = await getTokenFromCookie(setupPage);
    await context.close();
  });

  test("drag-to-create opens a prefilled popup and persists the new entry", async ({ page }, testInfo) => {
    // Drags stay desktop-first per #1265's agreed scope — mobile touch
    // emulation makes precise pointerdown/move/up pixel targeting (SNAP
    // grid, resize-handle strips, exact column edges) considerably less
    // reliable than on desktop, for coverage this test already gets there.
    test.skip(testInfo.project.name !== "desktop", "drag interactions are desktop-first (#1265)");
    const { id: eventId, day } = await createGridEvent(page, unique("TT Grid Create"));
    await page.goto(`${WEB_BASE}/admin/events/${eventId}/timetable`);

    const { pxPerMin, snap } = await page.evaluate(() => ({
      pxPerMin: (window as any).PX_PER_MIN,
      snap: (window as any).SNAP,
    }));
    expect(pxPerMin).toBeTruthy();
    expect(snap).toBeTruthy();

    // Fresh event, no entries yet: the only column is the "Other" sentinel
    // (id -1), and the grid defaults to 18:00–04:00 (no entries to derive a
    // range from) — well clear of the 13:00 event time, so any slot inside
    // it is a safe, predictable drag target regardless of the event's own
    // start/end.
    const body = page.locator(`.tt-room-body[data-day="${day}"][data-room-id="-1"]`);
    const box = (await body.boundingBox())!;
    const gridMin = await page.evaluate(() => (window as any)._gridMin);
    const startOffMin = 20 * 60 - gridMin; // 20:00
    const endOffMin = 21 * 60 - gridMin; // 21:00
    const x = box.x + box.width / 2;
    const yStart = box.y + startOffMin * pxPerMin + 2;
    const yEnd = box.y + endOffMin * pxPerMin;

    await page.mouse.move(x, yStart);
    await page.mouse.down();
    await page.mouse.move(x, yEnd, { steps: 5 });
    await page.mouse.up();

    const popup = page.locator("#tt-popup");
    await expect(popup).toBeVisible();
    await expect(page.locator("#pp-day")).toHaveValue(day);
    await expect(page.locator("#pp-start")).toHaveValue("20:00");
    await expect(page.locator("#pp-end")).toHaveValue("21:00");
    await expect(page.locator("#pp-room-sel")).toHaveValue("");

    const title = unique("Dragged entry");
    await page.fill("#pp-title", title);
    await page.locator('[data-fn="ppSave"]').click();
    await expect(popup).toBeHidden();
    await expect(page.locator(".tt-panel").filter({ hasText: title })).toBeVisible();

    await saveAndWait(page);

    const event = await authedJSON(page, `/api/v1/events/${eventId}`);
    const entry = event.timetable.find((e: any) => e.title === title);
    expect(entry, "created entry must be persisted").toBeTruthy();
    expect(entry.entry_date).toBe(day);
    expect(entry.start_time.slice(0, 5)).toBe("20:00");
    expect(entry.end_time.slice(0, 5)).toBe("21:00");
    expect(entry.location_id ?? null).toBeNull();
  });

  test("drag-to-move carries an entry into a different room, preserving its time", async (
    { page },
    testInfo
  ) => {
    test.skip(testInfo.project.name !== "desktop", "drag interactions are desktop-first (#1265)");
    const title = unique("TT Grid Move");
    const { id: eventId, day } = await createGridEvent(page, title);
    const seedEntryTitle = unique("Ouverture");
    await putTimetable(page, eventId, [
      { start_time: "20:00", end_time: "20:30", title: seedEntryTitle, entry_type: "bal", entry_date: day },
    ]);

    await page.goto(`${WEB_BASE}/admin/events/${eventId}/timetable`);

    // Add a free-text room column — no server round trip (the event has no
    // location, so _topLocID is 0 and the "+Add" path is purely client-side
    // state, unlike the quick-create path the building-header test below
    // uses), just enough to give drag-to-move a second column to land in.
    const roomName = "Grid Room";
    await page.fill("#tt-room-add", roomName);
    await page.locator("#tt-room-suggest li", { hasText: `Add "${roomName}"` }).click();

    const tile = page.locator(".tt-panel").filter({ hasText: seedEntryTitle });
    await expect(tile).toBeVisible();
    const tileBox = (await tile.boundingBox())!;
    const targetBody = page.locator(`.tt-room-body[data-day="${day}"][data-room-label="${roomName}"]`);
    const targetBox = (await targetBody.boundingBox())!;

    // Grab well below the top resize handle (.tt-rh-top, 8px tall) and drop
    // at the same Y in the target column — same day, so the target body's
    // top aligns exactly with the source body's, and holding Y constant
    // reproduces the same snapped start time in the new room.
    const grabY = tileBox.y + Math.min(20, tileBox.height / 2);
    await page.mouse.move(tileBox.x + tileBox.width / 2, grabY);
    await page.mouse.down();
    await page.mouse.move(targetBox.x + targetBox.width / 2, grabY, { steps: 5 });
    await page.mouse.up();

    await expect(targetBody.locator(".tt-panel").filter({ hasText: seedEntryTitle })).toBeVisible();

    await saveAndWait(page);

    const event = await authedJSON(page, `/api/v1/events/${eventId}`);
    const entry = event.timetable.find((e: any) => e.title === seedEntryTitle);
    expect(entry, "moved entry must still be persisted").toBeTruthy();
    expect(entry.room).toBe(roomName);
    expect(entry.location_id ?? null).toBeNull();
    expect(entry.entry_date).toBe(day);
    expect(entry.start_time.slice(0, 5)).toBe("20:00");
    expect(entry.end_time.slice(0, 5)).toBe("20:30");
  });

  test("difficulty widget: traffic-light fill persists, re-clicking the top slot clears it", async ({
    page,
  }) => {
    const seedEntryTitle = unique("TT Grid Difficulty");
    const { id: eventId, day } = await createGridEvent(page, unique("TT Grid Diff Event"));
    await putTimetable(page, eventId, [
      { start_time: "20:00", end_time: "20:30", title: seedEntryTitle, entry_type: "bal", entry_date: day },
    ]);

    await page.goto(`${WEB_BASE}/admin/events/${eventId}/timetable`);
    await page.locator(".tt-panel").filter({ hasText: seedEntryTitle }).click();
    const popup = page.locator("#tt-popup");
    await expect(popup).toBeVisible();

    const beginner = page.locator('.tt-diff-slot[data-level="beginner"]');
    const advanced = page.locator('.tt-diff-slot[data-level="advanced"]');
    const profi = page.locator('.tt-diff-slot[data-level="profi"]');

    // Clicking "advanced" lights it and everything to its left (beginner)
    // — the traffic-light fill — but not profi, to its right.
    await advanced.click();
    await expect(beginner).toHaveClass(/active/);
    await expect(advanced).toHaveClass(/active/);
    await expect(profi).not.toHaveClass(/active/);

    await page.locator('[data-fn="ppSave"]').click();
    await saveAndWait(page);
    let event = await authedJSON(page, `/api/v1/events/${eventId}`);
    let entry = event.timetable.find((e: any) => e.title === seedEntryTitle);
    expect(entry.difficulty).toBe("advanced");

    // Re-opening and re-clicking the already-active top slot clears it back
    // to "not set", rather than just re-selecting it.
    await page.locator(".tt-panel").filter({ hasText: seedEntryTitle }).click();
    await expect(popup).toBeVisible();
    await expect(advanced).toHaveClass(/active/);
    await advanced.click();
    await expect(beginner).not.toHaveClass(/active/);
    await expect(advanced).not.toHaveClass(/active/);

    await page.locator('[data-fn="ppSave"]').click();
    await saveAndWait(page);
    event = await authedJSON(page, `/api/v1/events/${eventId}`);
    entry = event.timetable.find((e: any) => e.title === seedEntryTitle);
    expect(entry.difficulty ?? "").toBe("");
  });

  // Quick-create two rooms under an event's own venue (acting as the
  // "building" — per adminTimetablePageHandler's topLocID derivation, a
  // location with no parent of its own is treated as its own building for
  // room quick-create purposes). Shared by both tests below.
  async function quickCreateRoom(page: Page, name: string): Promise<number> {
    await page.fill("#tt-room-add", name);
    await page
      .locator("#tt-room-suggest li", { hasText: `Create room "${name}"` })
      .click();
    await expect(page.locator(".tt-room-col-header", { hasText: name })).toBeVisible();
    return page.evaluate(
      (label) => (window as any)._rooms.find((r: any) => r.label === label).id,
      name
    );
  }

  test("building super-header groups quick-created rooms", async ({ page }) => {
    const buildingName = unique("TT Grid Venue");
    const buildingId = await createBuildingLocation(page, buildingName);
    const { id: eventId } = await createGridEvent(page, unique("TT Grid Building"), {
      locationId: buildingId,
    });
    await page.goto(`${WEB_BASE}/admin/events/${eventId}/timetable`);

    await quickCreateRoom(page, "Room A");
    await quickCreateRoom(page, "Room B");

    // One spanning header per contiguous same-building run — Room A+B form
    // one group (flex:2), and the trailing "Other" sentinel column (no
    // building) gets its own empty placeholder cell, per the render loop
    // creating one .tt-building-hdr per group unconditionally, text only
    // when the group actually has a buildingId. Two cells total, not one.
    const buildingHdrs = page.locator(".tt-building-hdr");
    await expect(buildingHdrs).toHaveCount(2);
    const groupedHdr = page.locator(".tt-building-hdr", { hasText: buildingName });
    await expect(groupedHdr).toHaveCount(1);
    // el.style.flex (the shorthand) reads back browser-normalized/expanded
    // (e.g. "2 1 0%") — check the flex-grow longhand the code actually sets
    // a plain integer string into, not the shorthand's serialized form.
    const flexGrow = await groupedHdr.evaluate((el) => (el as HTMLElement).style.flexGrow);
    expect(flexGrow).toBe("2");
    // Grouped rooms show just their own short name, not "Room — Building"
    // (the building name already appears once, in the super-header above).
    await expect(page.locator(".tt-room-col-header", { hasText: "Room A" })).toHaveText("Room A");
    await expect(page.locator(".tt-room-col-header", { hasText: "Room B" })).toHaveText("Room B");
  });

  test("column drag-reorder swaps room columns and persists across reload", async (
    { page },
    testInfo
  ) => {
    test.skip(testInfo.project.name !== "desktop", "drag interactions are desktop-first (#1265)");

    const buildingId = await createBuildingLocation(page, unique("TT Grid Venue"));
    const { id: eventId, day } = await createGridEvent(page, unique("TT Grid Reorder"), {
      locationId: buildingId,
    });
    await page.goto(`${WEB_BASE}/admin/events/${eventId}/timetable`);

    const roomAId = await quickCreateRoom(page, "Room A");
    const roomBId = await quickCreateRoom(page, "Room B");

    // ── Column drag-reorder (#1237): drag Room B before Room A ────────────
    const hdrA = page.locator(".tt-room-col-header", { hasText: "Room A" });
    const hdrB = page.locator(".tt-room-col-header", { hasText: "Room B" });
    const boxA = (await hdrA.boundingBox())!;
    const boxB = (await hdrB.boundingBox())!;

    await page.mouse.move(boxB.x + boxB.width / 2, boxB.y + boxB.height / 2);
    await page.mouse.down();
    // Land in Room A's *left* half — setupDragReorder's axis:'x' drop rule
    // inserts before the target there, after past its midpoint.
    const dropX = boxA.x + boxA.width * 0.25;
    await page.mouse.move(dropX, boxA.y + boxA.height / 2, { steps: 5 });
    const [roomOrderResp] = await Promise.all([
      page.waitForResponse(
        (r) => r.url().includes("/timetable/room-order") && r.request().method() === "PUT"
      ),
      page.mouse.up(),
    ]);
    expect(roomOrderResp.ok()).toBe(true);

    // In-session render: columns swapped (Room B now left of Room A).
    const headerTexts = await page.locator(".tt-room-col-header").allTextContents();
    const idxA = headerTexts.indexOf("Room A");
    const idxB = headerTexts.indexOf("Room B");
    expect(idxB).toBeLessThan(idxA);

    // Persistence (#1278): survives a reload, not just the in-session render.
    // Room *columns* only reappear on reload if something actually ties an
    // entry to them — recomputeRooms() (admin_timetable.html) derives the
    // active column set from entries' location_ids, not from
    // timetable_room_order alone (that only reorders whatever set of rooms
    // recomputeRooms() and .Rooms already produce). Neither room has an
    // entry yet (this test only exercised layout so far), so seed one in
    // each — otherwise both columns would simply vanish on reload,
    // independent of whether the order itself persisted correctly.
    await putTimetable(page, eventId, [
      { start_time: "20:00", end_time: "20:30", title: unique("Room A slot"), entry_type: "bal", entry_date: day, location_id: roomAId },
      { start_time: "20:00", end_time: "20:30", title: unique("Room B slot"), entry_type: "bal", entry_date: day, location_id: roomBId },
    ]);
    await page.reload();
    await expect(page.locator(".tt-room-col-header", { hasText: "Room A" })).toBeVisible();
    const headerTextsAfterReload = await page.locator(".tt-room-col-header").allTextContents();
    const idxA2 = headerTextsAfterReload.indexOf("Room A");
    const idxB2 = headerTextsAfterReload.indexOf("Room B");
    expect(idxB2).toBeLessThan(idxA2);

    const event = await authedJSON(page, `/api/v1/events/${eventId}`);
    expect(Array.isArray(event.timetable_room_order)).toBe(true);
    const order: number[] = event.timetable_room_order;
    expect(order.indexOf(roomBId)).toBeGreaterThanOrEqual(0);
    expect(order.indexOf(roomAId)).toBeGreaterThanOrEqual(0);
    expect(order.indexOf(roomBId)).toBeLessThan(order.indexOf(roomAId));
  });
});
