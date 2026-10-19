# HTTP load tests (k6)

Load tests for the dansal REST API (#1544). They sit alongside the Go
benchmarks (in-process, `make bench`) and the Playwright e2e suite (browser,
single worker). Neither can measure throughput or concurrent-write behaviour;
these can.

- `read.js` — the filtered event list (`GET /api/v1/events?tag=…&start_time_after=…`)
  and the iCalendar form (`Accept: text/calendar`). Read-only.
- `write.js` — concurrent `POST /api/v1/events`, to probe SQLite write
  contention. Expect `database is locked` or 5xx to appear here first if the
  writer is the bottleneck. **Creates real events** with a `stress-test` title
  prefix.
- `lib.js` — shared guard and header helpers.

## Safety

The scripts refuse any host other than loopback unless `STRESS_ALLOW_REMOTE=1`
is set. Run them on the scratch instance's own host. Do not point them at dev
or prod. Load tests against a live instance can fill it with events and trip
its rate limits for real users.

## Running against a scratch instance

1. Set up a throwaway instance on the host, on its own port, with its own
   database. `scripts/install-instance` creates it. Give it a separate name
   (for example `scratch`) so it doesn't touch `dev`.
2. Raise or disable the per-IP limits so the test measures the server rather
   than the limiter. Either set `rate_limit` in `config.yaml` to a high value
   for the scratch instance, or set `internal_shared_secret` and pass the same
   value as `STRESS_INTERNAL_SECRET`. The secret exempts loopback callers from
   the rate limiter. nginx strips the header from outside traffic.
3. Create a publisher or admin token on the scratch instance for `write.js`.
4. Install k6 (<https://k6.io/docs/get-started/installation/>).

```bash
make stress STRESS_BASE_URL=http://127.0.0.1:18000 STRESS_TOKEN=… \
  STRESS_INTERNAL_SECRET=…
```

Or run one script on its own:

```bash
STRESS_BASE_URL=http://127.0.0.1:18000 k6 run stress/read.js
STRESS_BASE_URL=http://127.0.0.1:18000 STRESS_TOKEN=… k6 run stress/write.js
```

## Cleaning up after `write.js`

It leaves events titled `stress-test …` on the scratch instance. Delete them
there, or drop the scratch instance's database after the run.

## Thresholds

The thresholds are starting points, not measured limits. Set them from a
baseline run on the target hardware before using a failure as a regression
signal.

| Script | Threshold |
|---|---|
| `read.js` | error rate < 1%; list p95 < 500 ms; iCal p95 < 1 s |
| `write.js` | server-error rate < 1%; create p95 < 2 s |
