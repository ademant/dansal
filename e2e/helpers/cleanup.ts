import * as fs from "fs";
import * as path from "path";
import { execFileSync } from "child_process";

// Per-run cleanup of the entities the suite creates (#1793).
//
// Every entity a fixture creates is recorded in two places:
//   - memory: this process's list (createdThisProcess), for logging;
//   - file:   e2e/.run/cleanup/run-<RUN_ID>.jsonl, one JSON line per
//             entity, appended as soon as it is created. Workers are child
//             processes, so the file is what joins them: the teardown reads
//             it, and it survives a crashed run (see cleanupStaleLedgers).
//
// Cleanup runs once, after the whole run (globalTeardown), not after each
// test: specs share beforeAll seed data (fullSeed), so deleting after one
// test would break the next test in the same file.
//
// Deletion is refused unless every host the suite talks to is in
// CLEANUP_ALLOWED_HOSTS. This is an explicit list, not a domain check, so a
// new instance is never cleaned by accident.

export type CleanupKind = "event" | "location" | "musician" | "instructor";

// Hosts the suite may delete on. Add a host here only when it is a test
// instance whose data may be removed freely.
export const CLEANUP_ALLOWED_HOSTS = ["localhost", "127.0.0.1", "dev.balfolk.jetzt"];

// Delete order: children first, so parents never block their children's
// delete. Events go first (they reference locations, musicians and
// instructors); locations go last (rooms are deleted with their building).
const DELETE_ORDER: CleanupKind[] = ["event", "musician", "instructor", "location"];

// Not under test-results/: Playwright empties that directory at the start of every run.
const LEDGER_DIR = path.resolve(__dirname, "..", ".run", "cleanup");
const STALE_AFTER_MS = 60 * 60 * 1000; // a run never lasts an hour; older ledgers belong to a crashed run

// One run id for the whole suite. playwright.config.ts sets it in the main
// process before workers start, so every worker writes to the same ledger.
export const RUN_ID =
  process.env.E2E_RUN_ID ?? (process.env.E2E_RUN_ID = `${Date.now()}-${process.pid}`);

const ledgerPath = (runId: string) => path.join(LEDGER_DIR, `run-${runId}.jsonl`);

export const createdThisProcess: { kind: CleanupKind; id: number }[] = [];

// recordCreated notes an entity this run created. Call it right after a
// successful create; never for shared seed data (the seed org is reused, not created).
export function recordCreated(kind: CleanupKind, id: number): void {
  if (!Number.isInteger(id) || id <= 0) return;
  createdThisProcess.push({ kind, id });
  try {
    fs.mkdirSync(LEDGER_DIR, { recursive: true });
    fs.appendFileSync(ledgerPath(RUN_ID), JSON.stringify({ kind, id }) + "\n");
  } catch (err) {
    console.warn(`[e2e cleanup] could not write ledger for ${kind} ${id}: ${err}`);
  }
}

function readLedger(file: string): { kind: CleanupKind; id: number }[] {
  if (!fs.existsSync(file)) return [];
  const out: { kind: CleanupKind; id: number }[] = [];
  for (const line of fs.readFileSync(file, "utf-8").split("\n")) {
    if (line.trim() === "") continue;
    try {
      out.push(JSON.parse(line));
    } catch {
      // A line cut short by a crash: nothing usable in it.
      console.warn(`[e2e cleanup] skipping unreadable ledger line in ${path.basename(file)}`);
    }
  }
  return out;
}

// cleanupAllowed reports whether every URL points at an allowed host.
export function cleanupAllowed(urls: string[]): { ok: boolean; reason?: string } {
  for (const u of urls) {
    const host = new URL(u).hostname;
    if (!CLEANUP_ALLOWED_HOSTS.includes(host)) {
      return { ok: false, reason: `${host} is not in CLEANUP_ALLOWED_HOSTS` };
    }
  }
  return { ok: true };
}

// The admin token the global setup saved. Teardown uses it for event deletes.
export function tokenFromAuthFile(authFile: string): string {
  try {
    const state = JSON.parse(fs.readFileSync(authFile, "utf-8"));
    const cookie = (state.cookies ?? []).find((c: { name: string }) => c.name === "dsw_token");
    return cookie?.value ?? "";
  } catch {
    return "";
  }
}

// Runs the admin CLI with argv, not a shell string. Same `sudo -n` and socket
// handling as seed.ts's cli().
function adminCli(args: string[]): string {
  const socket = process.env.ADMIN_SOCKET ?? "/var/lib/dansal/dev/dansal.sock";
  const bin = process.env.ADMIN_CLI ?? "dansal_admin";
  const sudo = process.env.ADMIN_NO_SUDO === "1" ? [] : ["sudo", "-n"];
  const [cmd, ...rest] = [...sudo, bin, "--socket", socket, ...args];
  return execFileSync(cmd, rest, {
    encoding: "utf-8",
    timeout: 30_000,
    stdio: ["ignore", "pipe", "pipe"],
  }).trim();
}

async function deleteOne(
  kind: CleanupKind,
  id: number,
  apiBase: string,
  token: string,
): Promise<{ ok: boolean; detail: string }> {
  if (kind === "event") {
    const resp = await fetch(`${apiBase}/api/v1/events/${id}`, {
      method: "DELETE",
      headers: { Authorization: `Bearer ${token}` },
    });
    // 404: already gone (e.g. a test deleted it itself). That is success here.
    if (resp.ok || resp.status === 404) return { ok: true, detail: `HTTP ${resp.status}` };
    return { ok: false, detail: `HTTP ${resp.status}: ${(await resp.text()).slice(0, 200)}` };
  }
  try {
    adminCli(["delete", "--type", kind, "--id", String(id), "--confirm"]);
    return { ok: true, detail: "dansal_admin delete" };
  } catch (err: any) {
    const stderr = String(err?.stderr ?? err?.message ?? err).trim();
    if (/not found/i.test(stderr)) return { ok: true, detail: "already gone" };
    return { ok: false, detail: stderr.slice(0, 300) };
  }
}

// runCleanup deletes every entity recorded in one ledger file. Entities that
// fail stay in the ledger, so the next run's stale-ledger pass retries them.
// The ledger is removed only when everything was deleted.
export async function runCleanup(
  file: string,
  opts: { apiBase: string; token: string; urls: string[] },
): Promise<void> {
  const entries = readLedger(file);
  if (entries.length === 0) {
    fs.rmSync(file, { force: true });
    return;
  }
  const gate = cleanupAllowed([...opts.urls, opts.apiBase]);
  if (!gate.ok) {
    console.warn(`[e2e cleanup] refused: ${gate.reason}. ${entries.length} entities left in ${file}`);
    return;
  }
  if (!opts.token) {
    console.warn(`[e2e cleanup] no admin token: ${entries.length} entities left in ${file}`);
    return;
  }

  const failed: { kind: CleanupKind; id: number }[] = [];
  for (const kind of DELETE_ORDER) {
    for (const e of entries.filter((x) => x.kind === kind)) {
      const r = await deleteOne(kind, e.id, opts.apiBase, opts.token);
      if (r.ok) {
        console.log(`[e2e cleanup] deleted ${kind} ${e.id} (${r.detail})`);
      } else {
        console.warn(`[e2e cleanup] FAILED ${kind} ${e.id}: ${r.detail}`);
        failed.push(e);
      }
    }
  }
  if (failed.length === 0) {
    fs.rmSync(file, { force: true });
  } else {
    fs.writeFileSync(file, failed.map((e) => JSON.stringify(e) + "\n").join(""));
  }
}

// cleanupStaleLedgers cleans up after crashed runs: any ledger older than an
// hour that is not this run's. Called from global setup.
export async function cleanupStaleLedgers(opts: {
  apiBase: string;
  token: string;
  urls: string[];
}): Promise<void> {
  if (!fs.existsSync(LEDGER_DIR)) return;
  const now = Date.now();
  for (const name of fs.readdirSync(LEDGER_DIR)) {
    const file = path.join(LEDGER_DIR, name);
    if (file === ledgerPath(RUN_ID)) continue;
    if (now - fs.statSync(file).mtimeMs < STALE_AFTER_MS) continue;
    console.log(`[e2e cleanup] stale ledger from an earlier run: ${name}`);
    await runCleanup(file, opts);
  }
}

// cleanupThisRun is used by globalTeardown.
export async function cleanupThisRun(opts: {
  apiBase: string;
  token: string;
  urls: string[];
}): Promise<void> {
  await runCleanup(ledgerPath(RUN_ID), opts);
}
