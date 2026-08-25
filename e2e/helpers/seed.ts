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
const INSTANCE = process.env.INSTANCE ?? "dev";

function cli(args: string): string {
  return execSync(`${ADMIN_CLI} --instance ${INSTANCE} ${args}`, {
    encoding: "utf-8",
    timeout: 30_000,
  }).trim();
}

function extractUserId(output: string): number {
  const m = output.match(/id=(\d+)/);
  if (!m) throw new Error(`Cannot extract user ID from: ${output}`);
  return parseInt(m[1], 10);
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
  const adminOut = cli(
    `create-user --email ${ADMIN.email} --password "${ADMIN.password}" --role admin`
  );
  const editorOut = cli(
    `create-user --email ${EDITOR.email} --password "${EDITOR.password}" --role publisher`
  );
  const viewerOut = cli(
    `create-user --email ${VIEWER.email} --password "${VIEWER.password}" --role user`
  );
  return {
    adminId: extractUserId(adminOut),
    editorId: extractUserId(editorOut),
    viewerId: extractUserId(viewerOut),
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
  await page.waitForURL("**/", { timeout: 15_000 });
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
