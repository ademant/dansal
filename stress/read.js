// Read-path load test for the public API (#1544): the filtered event list and
// its iCalendar form, the two endpoints the public site and feed subscribers
// hit hardest. Read-only, so it is safe against any instance it is allowed to
// reach; the loopback guard in lib.js still applies.
//
//   STRESS_BASE_URL=http://127.0.0.1:18000 k6 run stress/read.js
import http from "k6/http";
import { check } from "k6";
import { baseURL, internalHeaders } from "./lib.js";

const BASE = baseURL();

export const options = {
  scenarios: {
    list: {
      executor: "ramping-vus",
      startVUs: 1,
      stages: [
        { duration: "30s", target: 10 },
        { duration: "1m", target: 50 },
        { duration: "30s", target: 0 },
      ],
      gracefulRampDown: "10s",
      exec: "list",
    },
    ical: {
      executor: "ramping-vus",
      startVUs: 1,
      stages: [
        { duration: "30s", target: 5 },
        { duration: "1m", target: 20 },
        { duration: "30s", target: 0 },
      ],
      gracefulRampDown: "10s",
      exec: "ical",
    },
  },
  thresholds: {
    http_req_failed: ["rate<0.01"],
    "http_req_duration{name:list}": ["p(95)<500"],
    "http_req_duration{name:ical}": ["p(95)<1000"],
  },
};

// Filters are re-evaluated per request so the server does real work
// (tag + time-window + pagination), not just a cached whole-table scan.
export function list() {
  const after = Math.floor(Date.now() / 1000);
  const url =
    `${BASE}/api/v1/events?tag=workshop&start_time_after=${after}&limit=20&offset=0`;
  const res = http.get(url, {
    headers: { Accept: "application/json", ...internalHeaders() },
    tags: { name: "list" },
  });
  check(res, { "list 200": (r) => r.status === 200 });
}

export function ical() {
  const res = http.get(`${BASE}/api/v1/events`, {
    headers: { Accept: "text/calendar", ...internalHeaders() },
    tags: { name: "ical" },
  });
  check(res, { "ical 200": (r) => r.status === 200 });
}
