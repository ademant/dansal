import { execSync } from "child_process";
import { Page } from "@playwright/test";
import {
  ADMIN,
  EDITOR,
  VIEWER,
  ORG,
  LOCATION,
  MUSICIAN,
  INSTRUCTOR,
  EVENT_DATE_MIN_DAYS,
  EVENT_DATE_MAX_DAYS,
  seedEvents,
  seedTimetable,
} from "../fixtures/data";

// The index page renders only the soonest ~100 published future events
// (cmd/dansal_web/frontend.go's indexHandler). A static day-range guess for
// "future" would silently go stale as a shared/pre-filled database (e.g.
// the persistent dev instance) accumulates more events over time — so this
// looks at what's actually there and picks a day count comfortably inside
// however many of the *next* INDEX_EVENT_CAP events are already scheduled.
const INDEX_EVENT_CAP = 100;
const INDEX_SAFETY_MARGIN = 40; // stay clear of the cap even mid-run

const API_BASE = process.env.API_URL ?? "http://localhost:8000";
const ADMIN_CLI = process.env.ADMIN_CLI ?? "dansal_admin";
const ADMIN_SOCKET =
  process.env.ADMIN_SOCKET ?? "/var/lib/dansal/dev/dansal.sock";
const SUDO = process.env.ADMIN_NO_SUDO === "1" ? "" : "sudo -n ";

function cli(args: string): string {
  return execSync(`${SUDO}${ADMIN_CLI} --socket ${ADMIN_SOCKET} ${args}`, {
    encoding: "utf-8",
    timeout: 30_000,
  }).trim();
}

function extractUserId(output: string): number {
  const m = output.match(/id=(\d+)/);
  if (!m) throw new Error(`Cannot extract user ID from: ${output}`);
  return parseInt(m[1], 10);
}

// Look up an existing user's ID from `list-users` table output; -1 if absent.
function findExistingUserId(email: string): number {
  const out = cli("list-users");
  for (const line of out.split("\n")) {
    const cols = line.trim().split(/\s+/);
    if (cols.includes(email) && /^\d+$/.test(cols[0])) {
      return parseInt(cols[0], 10);
    }
  }
  return -1;
}

function createUser(email: string, password: string, role: string): number {
  try {
    const out = cli(
      `create-user --email ${email} --password "${password}" --role ${role}`
    );
    return extractUserId(out);
  } catch (e) {
    const msg = String(e);
    if (msg.includes("already exists")) {
      const id = findExistingUserId(email);
      if (id !== -1) return id;
    }
    throw e;
  }
}

export interface SeedResult {
  adminId: number;
  editorId: number;
  viewerId: number;
  orgId: number;
  locationId: number;
  eventIds: number[];
  eventTitles: string[];
  musicianId: number;
  instructorId: number;
}

export function createUsers(): {
  adminId: number;
  editorId: number;
  viewerId: number;
} {
  return {
    adminId: createUser(ADMIN.email, ADMIN.password, "admin"),
    editorId: createUser(EDITOR.email, EDITOR.password, "publisher"),
    viewerId: createUser(VIEWER.email, VIEWER.password, "user"),
  };
}

export async function loginAs(
  page: Page,
  email: string,
  password: string
): Promise<void> {
  await page.goto("/login");
  await page.fill("#identifier", email);
  await page.fill("#password", password);
  await page.waitForTimeout(3500);
  await page.click("#btn-login");
  // A generous margin: under a heavily loaded dev instance (many
  // concurrent test/build processes) the native form POST + 303 redirect
  // can take noticeably longer than a quiet instance would need.
  await page.waitForURL((url) => !url.pathname.startsWith("/login"), {
    timeout: 30_000,
  });
}

export async function getTokenFromCookie(page: Page): Promise<string> {
  const cookies = await page.context().cookies();
  const tok = cookies.find((c) => c.name === "dsw_token");
  return tok?.value ?? "";
}

async function apiPost(
  page: Page,
  path: string,
  token: string,
  body: unknown
): Promise<any> {
  const resp = await page.request.fetch(`${API_BASE}${path}`, {
    method: "POST",
    headers: {
      "Content-Type": "application/json",
      Authorization: `Bearer ${token}`,
    },
    data: JSON.stringify(body),
  });
  return resp.json();
}

async function apiGet(page: Page, path: string): Promise<any> {
  const resp = await page.request.fetch(`${API_BASE}${path}`, {
    method: "GET",
  });
  return resp.json();
}

// seedOrg reuses an existing "Bal Test Association" org rather than creating
// a fresh one every run — organizations aren't deduped server-side, so
// against a pre-filled/persistent database (e.g. the shared dev instance)
// repeated runs would otherwise pile up same-named orgs and make
// create-event.spec.ts's label-based <select> pick ambiguous.
export async function seedOrg(page: Page, token: string): Promise<number> {
  const existing = await apiGet(page, "/api/v1/organizations");
  const found = Array.isArray(existing)
    ? existing.find((o: any) => o.name === ORG.name)
    : undefined;
  if (found) return found.id;
  const data = await apiPost(page, "/api/v1/organizations", token, ORG);
  return data.id;
}

// seedLocation creates a fresh location every run, with the fixture's
// lat/lon jittered by a small random offset. Two things need this to NOT
// just reuse one shared row (unlike seedOrg/createUser):
//  1. locations.geohash carries a UNIQUE index (cmd/dansal/locations.go) —
//     POSTing the fixture's exact fixed coordinates twice 500s once a prior
//     run has already created that geohash.
//  2. A shared location_id would defeat the whole point of seedEvents'
//     randomized dates: dansal's dedup tier 3 matches on
//     location_id + start_time ±3h with NO title check (see
//     cmd/dansal/dedup.go), so two runs landing on the same day at the same
//     *shared* location would silently get merged into one event — which is
//     exactly what happened before this fix (timetable.spec.ts read back
//     timetable-management.spec.ts's replaced entries because both runs'
//     "Bal de Testville" picked the same location + a close enough time).
// ~0.01° (~1km) reliably lands in a different geohash cell while still
// reading as "the same test venue" for anything the specs assert on.
function jitteredLocation() {
  const jitter = () => (Math.random() - 0.5) * 0.02;
  return {
    ...LOCATION,
    latitude: LOCATION.latitude + jitter(),
    longitude: LOCATION.longitude + jitter(),
  };
}

export async function seedLocation(
  page: Page,
  token: string
): Promise<number> {
  const data = await apiPost(page, "/api/v1/locations", token, jitteredLocation());
  return data.id;
}

// safeMaxDaysOut looks at the database's actual published-future-event
// density and returns a days-out bound that keeps a newly seeded event
// inside the index page's top-INDEX_EVENT_CAP soonest-events window, so
// discover.spec.ts's event-table assertions find it regardless of how much
// other data the (shared/pre-filled) instance already has scheduled soon.
async function safeMaxDaysOut(page: Page): Promise<number> {
  const events = await apiGet(page, "/api/v1/events?limit=1000");
  if (!Array.isArray(events)) return EVENT_DATE_MAX_DAYS;
  const now = Date.now();
  const futureMs = events
    .map((e: any) => (e.start_time ? new Date(e.start_time).getTime() : NaN))
    .filter((t: number) => !isNaN(t) && t > now)
    .sort((a: number, b: number) => a - b);
  const idx = Math.max(0, INDEX_EVENT_CAP - INDEX_SAFETY_MARGIN - 1);
  const cutoffMs =
    futureMs.length > idx
      ? futureMs[idx]
      : now + EVENT_DATE_MAX_DAYS * 86_400_000;
  const days = Math.floor((cutoffMs - now) / 86_400_000);
  return Math.max(EVENT_DATE_MIN_DAYS + 2, Math.min(days, EVENT_DATE_MAX_DAYS));
}

export interface SeededEvent {
  id: number;
  title: string;
}

export async function seedEventsAndTimetable(
  page: Page,
  token: string,
  orgId: number,
  locationId: number
): Promise<SeededEvent[]> {
  const maxDays = await safeMaxDaysOut(page);
  const payloads = seedEvents(orgId, locationId, maxDays);
  const events: SeededEvent[] = [];
  for (const payload of payloads) {
    const data = await apiPost(page, "/api/v1/events", token, payload);
    const created = Array.isArray(data) ? data[0] : data;
    events.push({ id: created?.id, title: created?.title ?? payload.title });
  }
  if (events.length > 0) {
    const tt = seedTimetable(events[0].id);
    await page.request.fetch(
      `${API_BASE}/api/v1/events/${events[0].id}/timetable`,
      {
        method: "POST",
        headers: {
          "Content-Type": "application/json",
          Authorization: `Bearer ${token}`,
        },
        data: JSON.stringify(tt),
      }
    );
  }
  return events;
}

export async function seedMusician(
  page: Page,
  token: string
): Promise<number> {
  const data = await apiPost(page, "/api/v1/musicians", token, MUSICIAN);
  return data.id;
}

export async function seedInstructor(
  page: Page,
  token: string
): Promise<number> {
  const data = await apiPost(page, "/api/v1/instructors", token, INSTRUCTOR);
  return data.id;
}

export async function fullSeed(page: Page): Promise<SeedResult> {
  const users = createUsers();
  await loginAs(page, ADMIN.email, ADMIN.password);
  const token = await getTokenFromCookie(page);
  const orgId = await seedOrg(page, token);
  const locationId = await seedLocation(page, token);
  const events = await seedEventsAndTimetable(page, token, orgId, locationId);
  const musicianId = await seedMusician(page, token);
  const instructorId = await seedInstructor(page, token);
  return {
    adminId: users.adminId,
    editorId: users.editorId,
    viewerId: users.viewerId,
    orgId,
    locationId,
    eventIds: events.map((e) => e.id),
    eventTitles: events.map((e) => e.title),
    musicianId,
    instructorId,
  };
}
