// Write-path load test (#1544): concurrent POST /api/v1/events to probe SQLite
// write contention. Writes serialize on the DB (WAL + busy_timeout), so the
// failure mode to look for is 5xx / "database is locked" under concurrency.
//
// Creates real events, so it needs a token for a scratch instance and must
// never be pointed at dev or prod. Created titles carry a "stress-test" prefix
// so they can be deleted afterwards.
//
//   STRESS_BASE_URL=http://127.0.0.1:18000 STRESS_TOKEN=... k6 run stress/write.js
import http from "k6/http";
import { check } from "k6";
import { Counter, Rate, Trend } from "k6/metrics";
import { baseURL, internalHeaders } from "./lib.js";

const BASE = baseURL();
const TOKEN = __ENV.STRESS_TOKEN || "";
if (!TOKEN) {
  throw new Error("STRESS_TOKEN is required (admin or publisher token for the scratch instance)");
}

const serverErrors = new Rate("server_errors");
const locked = new Counter("database_locked");
const createMs = new Trend("create_ms", true);

export const options = {
  scenarios: {
    create: {
      executor: "ramping-vus",
      startVUs: 1,
      stages: [
        { duration: "30s", target: 5 },
        { duration: "1m", target: 20 },
        { duration: "30s", target: 0 },
      ],
      gracefulRampDown: "10s",
    },
  },
  thresholds: {
    server_errors: ["rate<0.01"],
    create_ms: ["p(95)<2000"],
  },
};

export default function () {
  const start = Date.parse("2030-01-01T19:00:00Z") + (__VU * 1000 + __ITER) * 60000;
  const body = JSON.stringify({
    title: `stress-test ${__VU}-${__ITER}-${Date.now()}`,
    start_time: new Date(start).toISOString(),
    end_time: new Date(start + 3 * 3600 * 1000).toISOString(),
  });
  const res = http.post(`${BASE}/api/v1/events`, body, {
    headers: {
      Authorization: `Bearer ${TOKEN}`,
      "Content-Type": "application/json",
      ...internalHeaders(),
    },
    tags: { name: "create" },
  });
  createMs.add(res.timings.duration);
  serverErrors.add(res.status >= 500);
  if (res.body && String(res.body).includes("database is locked")) {
    locked.add(1);
  }
  check(res, { "create 201": (r) => r.status === 201 });
}
