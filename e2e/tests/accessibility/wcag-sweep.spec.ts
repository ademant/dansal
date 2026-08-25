import { test, expect } from "../../helpers/fixtures";
import { fullSeed, SeedResult } from "../../helpers/seed";
import AxeBuilder from "@axe-core/playwright";
import * as fs from "fs";
import * as path from "path";

const ALLOWLIST_FILE = path.resolve(__dirname, "../../allowlist.json");

interface AllowlistEntry {
  ruleId: string;
  url: string;
  description?: string;
}

function loadAllowlist(): AllowlistEntry[] {
  try {
    return JSON.parse(fs.readFileSync(ALLOWLIST_FILE, "utf-8"));
  } catch {
    return [];
  }
}

function isAllowlisted(
  allowlist: AllowlistEntry[],
  ruleId: string,
  url: string
): boolean {
  return allowlist.some(
    (e) => e.ruleId === ruleId && (e.url === "*" || url.includes(e.url))
  );
}

async function runAxe(
  page: import("@playwright/test").Page,
  url: string,
  routeLabel: string,
  allowlist: AllowlistEntry[]
): Promise<void> {
  await page.goto(url, { waitUntil: "networkidle" });

  const results = await new AxeBuilder({ page })
    .withTags(["wcag2a", "wcag2aa", "wcag21a", "wcag21aa"])
    .analyze();

  const violations = results.violations.filter(
    (v) => !isAllowlisted(allowlist, v.id, url)
  );
  const critical = violations.filter(
    (v) => v.impact === "critical" || v.impact === "serious"
  );

  if (critical.length > 0) {
    const report = critical
      .map(
        (v) =>
          `[${v.impact}] ${v.id}: ${v.description}\n  Nodes: ${v.nodes
            .slice(0, 3)
            .map((n) => n.html.slice(0, 120))
            .join("\n    ")}`
      )
      .join("\n\n");
    expect(
      critical.length,
      `WCAG violations on ${routeLabel} (${url}):\n${report}`
    ).toBe(0);
  }

  if (violations.length > 0) {
    console.log(
      `[a11y] ${routeLabel}: ${violations.length} violations (${critical.length} critical): ${violations.map((v) => v.id).join(", ")}`
    );
  }
}

const allowlist = loadAllowlist();
let seed: SeedResult;

test.describe("WCAG 2.1 AA accessibility sweep", () => {
  test.beforeAll(async ({ browser }) => {
    const setupPage = await browser.newPage();
    seed = await fullSeed(setupPage);
    await setupPage.context().close();
  });

  test.describe("public routes", () => {
    test("index (/)", async ({ page, metrics }) => {
      await runAxe(page, "/", "index", allowlist);
      await metrics.collect("a11y_public_index");
    });

    test("event detail", async ({ page, metrics }) => {
      await runAxe(
        page,
        `/events/${seed.eventIds[0]}`,
        "event-detail",
        allowlist
      );
      await metrics.collect("a11y_public_event-detail");
    });

    test("location detail", async ({ page, metrics }) => {
      await runAxe(
        page,
        `/location/${seed.locationId}`,
        "location-detail",
        allowlist
      );
      await metrics.collect("a11y_public_location-detail");
    });

    test("org detail", async ({ page, metrics }) => {
      await runAxe(
        page,
        "/org/bal-test-association",
        "org-detail",
        allowlist
      );
      await metrics.collect("a11y_public_org-detail");
    });

    test("musician detail", async ({ page, metrics }) => {
      await runAxe(
        page,
        `/musicians/${seed.musicianId}`,
        "musician-detail",
        allowlist
      );
      await metrics.collect("a11y_public_musician-detail");
    });

    test("instructor detail", async ({ page, metrics }) => {
      await runAxe(
        page,
        `/instructors/${seed.instructorId}`,
        "instructor-detail",
        allowlist
      );
      await metrics.collect("a11y_public_instructor-detail");
    });

    test("city hub", async ({ page, metrics }) => {
      await runAxe(page, "/city/testville", "city-hub", allowlist);
      await metrics.collect("a11y_public_city-hub");
    });

    test("tag page", async ({ page, metrics }) => {
      await runAxe(page, "/tags/bal-folk", "tag-page", allowlist);
      await metrics.collect("a11y_public_tag-page");
    });

    test("search", async ({ page, metrics }) => {
      await runAxe(page, "/search", "search", allowlist);
      await metrics.collect("a11y_public_search");
    });

    test("festivals", async ({ page, metrics }) => {
      await runAxe(page, "/festivals", "festivals", allowlist);
      await metrics.collect("a11y_public_festivals");
    });

    test("board", async ({ page, metrics }) => {
      await runAxe(page, "/board", "board", allowlist);
      await metrics.collect("a11y_public_board");
    });

    test("register", async ({ page, metrics }) => {
      await runAxe(page, "/register", "register", allowlist);
      await metrics.collect("a11y_public_register");
    });

    test("login", async ({ page, metrics }) => {
      await runAxe(page, "/login", "login", allowlist);
      await metrics.collect("a11y_public_login");
    });

    test("embed/events", async ({ page, metrics }) => {
      await runAxe(page, "/embed/events", "embed-events", allowlist);
      await metrics.collect("a11y_public_embed-events");
    });

    test("embed/next", async ({ page, metrics }) => {
      await runAxe(page, "/embed/next", "embed-next", allowlist);
      await metrics.collect("a11y_public_embed-next");
    });

    test("embed/calendar", async ({ page, metrics }) => {
      await runAxe(page, "/embed/calendar", "embed-calendar", allowlist);
      await metrics.collect("a11y_public_embed-calendar");
    });

    test("embed/locations", async ({ page, metrics }) => {
      await runAxe(page, "/embed/locations", "embed-locations", allowlist);
      await metrics.collect("a11y_public_embed-locations");
    });
  });

  test.describe("admin routes", () => {
    test("dashboard", async ({ page, metrics }) => {
      await runAxe(page, "/dashboard", "dashboard", allowlist);
      await metrics.collect("a11y_admin_dashboard");
    });

    test("admin event edit", async ({ page, metrics }) => {
      await runAxe(
        page,
        `/admin/events/${seed.eventIds[0]}/edit`,
        "admin-event-edit",
        allowlist
      );
      await metrics.collect("a11y_admin_event-edit");
    });

    test("admin timetable edit", async ({ page, metrics }) => {
      await runAxe(
        page,
        `/admin/events/${seed.eventIds[0]}/timetable`,
        "admin-timetable-edit",
        allowlist
      );
      await metrics.collect("a11y_admin_timetable-edit");
    });

    test("admin org edit", async ({ page, metrics }) => {
      await runAxe(
        page,
        `/admin/organizations/${seed.orgId}/edit`,
        "admin-org-edit",
        allowlist
      );
      await metrics.collect("a11y_admin_org-edit");
    });

    test("admin location edit", async ({ page, metrics }) => {
      await runAxe(
        page,
        `/admin/locations/${seed.locationId}/edit`,
        "admin-location-edit",
        allowlist
      );
      await metrics.collect("a11y_admin_location-edit");
    });

    test("admin users", async ({ page, metrics }) => {
      await runAxe(page, "/admin/users", "admin-users", allowlist);
      await metrics.collect("a11y_admin_users");
    });
  });
});
