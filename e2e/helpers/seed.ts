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
  seedEvents,
  seedTimetable,
} from "../fixtures/data";

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
  await page.waitForURL((url) => !url.pathname.startsWith("/login"), {
    timeout: 15_000,
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

export async function seedOrg(page: Page, token: string): Promise<number> {
  const data = await apiPost(page, "/api/v1/organizations", token, ORG);
  return data.id;
}

export async function seedLocation(
  page: Page,
  token: string
): Promise<number> {
  const data = await apiPost(page, "/api/v1/locations", token, LOCATION);
  return data.id;
}

export async function seedEventsAndTimetable(
  page: Page,
  token: string,
  orgId: number,
  locationId: number
): Promise<number[]> {
  const payloads = seedEvents(orgId, locationId);
  const eventIds: number[] = [];
  for (const payload of payloads) {
    const data = await apiPost(page, "/api/v1/events", token, payload);
    const id = Array.isArray(data) ? data[0]?.id : data.id;
    eventIds.push(id);
  }
  if (eventIds.length > 0) {
    const tt = seedTimetable(eventIds[0]);
    await page.request.fetch(
      `${API_BASE}/api/v1/events/${eventIds[0]}/timetable`,
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
  return eventIds;
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
  const eventIds = await seedEventsAndTimetable(page, token, orgId, locationId);
  const musicianId = await seedMusician(page, token);
  const instructorId = await seedInstructor(page, token);
  return {
    adminId: users.adminId,
    editorId: users.editorId,
    viewerId: users.viewerId,
    orgId,
    locationId,
    eventIds,
    musicianId,
    instructorId,
  };
}
