import * as fs from "fs";
import * as path from "path";
import { execFileSync } from "child_process";

// Per-run cleanup of the entities the suite creates (#1793, #1829).
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

export type CleanupKind =
  | "registration"
  | "event"
  | "location"
  | "musician"
  | "instructor"
  | "organisation"
  | "user";

export interface CleanupEntry {
  kind: CleanupKind;
  id: number;
  // users are removed by email (dansal_admin delete-user), so they carry it here.
  ref?: string;
}

export interface CleanupSummary {
  removed: number;
  failed: CleanupEntry[];
  // Entities the API refused to remove now but which go away on their own
  // (a registration already approved, whose record expires after a week).
  kept: CleanupEntry[];
  // Ledger files left alone, with the reason.
  skippedLedgers: { file: string; reason: string }[];
}

// Hosts the suite may delete on. Add a host here only when it is a test
// instance whose data may be removed freely.
export const CLEANUP_ALLOWED_HOSTS = ["localhost", "127.0.0.1", "dev.balfolk.jetzt"];

// Delete order: dependants first. Registrations and users hang off
// organisations and events; events reference locations, musicians and
// instructors; locations go last (rooms are deleted with their building).
const DELETE_ORDER: CleanupKind[] = [
  "registration",
  "event",
  "musician",
  "instructor",
  "organisation",
  "user",
  "location",
];

// Not under test-results/: Playwright empties that directory at the start of every run.
const LEDGER_DIR = path.resolve(__dirname, "..", ".run", "cleanup");
// A ledger named for a pid that no longer exists is from a crashed run. If
// the name has no pid, fall back to age.
const UNKNOWN_OWNER_STALE_MS = 60 * 60 * 1000;

// One run id for the whole suite. playwright.config.ts sets it in the main
// process before workers start, so every worker writes to the same ledger.
export const RUN_ID =
  process.env.E2E_RUN_ID ?? (process.env.E2E_RUN_ID = `${Date.now()}-${process.pid}`);

const ledgerPath = (runId: string) => path.join(LEDGER_DIR, `run-${runId}.jsonl`);

export const createdThisProcess: CleanupEntry[] = [];

// recordCreated notes an entity this run created. Call it right after a
// successful create; never for shared seed data (the seed org is reused, not created).
export function recordCreated(kind: CleanupKind, id: number, ref?: string): void {
  if (kind !== "user" && (!Number.isInteger(id) || id <= 0)) return;
  if (kind === "user" && !ref) return;
  const entry: CleanupEntry = ref !== undefined ? { kind, id, ref } : { kind, id };
  createdThisProcess.push(entry);
  try {
    fs.mkdirSync(LEDGER_DIR, { recursive: true });
    fs.appendFileSync(ledgerPath(RUN_ID), JSON.stringify(entry) + "\n");
  } catch (err) {
    console.warn(`[e2e cleanup] could not write ledger for ${kind} ${id}: ${err}`);
  }
}

function readLedger(file: string): CleanupEntry[] {
  if (!fs.existsSync(file)) return [];
  const out: CleanupEntry[] = [];
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

interface DeleteResult {
  ok: boolean;
  kept?: boolean;
  detail: string;
}

async function deleteOne(
  entry: CleanupEntry,
  apiBase: string,
  token: string,
): Promise<DeleteResult> {
  const { kind, id, ref } = entry;
  if (kind === "event" || kind === "registration") {
    const url =
      kind === "event"
        ? `${apiBase}/api/v1/events/${id}`
        : `${apiBase}/api/v1/pending-registrations/${id}`;
    const resp = await fetch(url, {
      method: "DELETE",
      headers: { Authorization: `Bearer ${token}` },
    });
    // 404: already gone (e.g. a test deleted it itself). That is success here.
    if (resp.ok || resp.status === 404) return { ok: true, detail: `HTTP ${resp.status}` };
    // 409 on a registration: it was approved. Its record stays for a week
    // and then expires; the user it made is removed by its own entry.
    if (kind === "registration" && resp.status === 409) {
      return { ok: true, kept: true, detail: "approved; the record expires on its own" };
    }
    return { ok: false, detail: `HTTP ${resp.status}: ${(await resp.text()).slice(0, 200)}` };
  }
  try {
    if (kind === "user") {
      adminCli(["delete-user", "--email", ref ?? ""]);
    } else {
      adminCli(["delete", "--type", kind, "--id", String(id), "--confirm"]);
    }
    return { ok: true, detail: "dansal_admin" };
  } catch (err: any) {
    const stderr = String(err?.stderr ?? err?.message ?? err).trim();
    if (/not found/i.test(stderr)) return { ok: true, detail: "already gone" };
    return { ok: false, detail: stderr.slice(0, 300) };
  }
}

function emptySummary(): CleanupSummary {
  return { removed: 0, failed: [], kept: [], skippedLedgers: [] };
}

function printSummary(label: string, s: CleanupSummary): void {
  const skipped = s.skippedLedgers.map((x) => `${path.basename(x.file)} (${x.reason})`);
  console.log(
    `[e2e cleanup] ${label}: removed ${s.removed}, failed ${s.failed.length}, ` +
      `kept ${s.kept.length}, ledgers skipped ${s.skippedLedgers.length}` +
      (skipped.length ? `: ${skipped.join("; ")}` : ""),
  );
  for (const e of s.failed) console.warn(`[e2e cleanup] left behind: ${e.kind} ${e.id}`);
}

// runCleanup deletes every entity recorded in one ledger file. Entities that
// fail stay in the ledger, so the next run's stale-ledger pass retries them.
// The ledger is removed only when everything was deleted.
export async function runCleanup(
  file: string,
  opts: { apiBase: string; token: string; urls: string[] },
): Promise<CleanupSummary> {
  const s = emptySummary();
  const entries = readLedger(file);
  if (entries.length === 0) {
    fs.rmSync(file, { force: true });
    return s;
  }
  const gate = cleanupAllowed([...opts.urls, opts.apiBase]);
  if (!gate.ok) {
    s.skippedLedgers.push({ file, reason: `refused: ${gate.reason}; ${entries.length} entities left` });
    return s;
  }
  if (!opts.token) {
    s.skippedLedgers.push({ file, reason: `no admin token; ${entries.length} entities left` });
    return s;
  }

  const failed: CleanupEntry[] = [];
  for (const kind of DELETE_ORDER) {
    for (const e of entries.filter((x) => x.kind === kind)) {
      const r = await deleteOne(e, opts.apiBase, opts.token);
      if (r.ok) {
        s.removed++;
        if (r.kept) s.kept.push(e);
        console.log(`[e2e cleanup] ${r.kept ? "kept" : "deleted"} ${kind} ${e.id}${e.ref ? ` (${e.ref})` : ""} (${r.detail})`);
      } else {
        console.warn(`[e2e cleanup] FAILED ${kind} ${e.id}: ${r.detail}`);
        failed.push(e);
      }
    }
  }
  s.failed = failed;
  // Kept entries are done too (they expire on their own), so only failures
  // stay in the ledger for the next run to retry.
  if (failed.length === 0) {
    fs.rmSync(file, { force: true });
  } else {
    fs.writeFileSync(file, failed.map((e) => JSON.stringify(e) + "\n").join(""));
  }
  return s;
}

// The pid in a ledger name (run-<timestamp>-<pid>.jsonl), or null.
function ownerPid(name: string): number | null {
  const m = name.match(/^run-\d+-(\d+)\.jsonl$/);
  return m ? parseInt(m[1], 10) : null;
}

// True when a process with this pid is alive. EPERM still means alive.
function pidAlive(pid: number): boolean {
  try {
    process.kill(pid, 0);
    return true;
  } catch (err: any) {
    return err?.code === "EPERM";
  }
}

// cleanupStaleLedgers clears ledgers left by earlier runs, at the start of
// this one (#1829). A ledger whose owning process is gone is cleaned now; one
// whose owner is still running is left alone; a ledger with no readable owner
// is cleaned once it is an hour old.
export async function cleanupStaleLedgers(opts: {
  apiBase: string;
  token: string;
  urls: string[];
}): Promise<CleanupSummary> {
  const total = emptySummary();
  if (!fs.existsSync(LEDGER_DIR)) return total;
  const now = Date.now();
  for (const name of fs.readdirSync(LEDGER_DIR)) {
    const file = path.join(LEDGER_DIR, name);
    if (file === ledgerPath(RUN_ID)) continue;
    const pid = ownerPid(name);
    if (pid !== null) {
      if (pidAlive(pid)) {
        total.skippedLedgers.push({ file, reason: `owner pid ${pid} is still running` });
        continue;
      }
    } else if (now - fs.statSync(file).mtimeMs < UNKNOWN_OWNER_STALE_MS) {
      total.skippedLedgers.push({ file, reason: "no owner pid and younger than an hour" });
      continue;
    }
    console.log(`[e2e cleanup] stale ledger from an earlier run: ${name}`);
    const s = await runCleanup(file, opts);
    total.removed += s.removed;
    total.failed.push(...s.failed);
    total.kept.push(...s.kept);
    total.skippedLedgers.push(...s.skippedLedgers);
  }
  printSummary("stale ledgers", total);
  return total;
}

// cleanupThisRun is used by globalTeardown and reports what it did.
export async function cleanupThisRun(opts: {
  apiBase: string;
  token: string;
  urls: string[];
}): Promise<CleanupSummary> {
  const s = await runCleanup(ledgerPath(RUN_ID), opts);
  printSummary("this run", s);
  return s;
}
