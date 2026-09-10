/**
 * Mailbox helpers for the fake-sendmail e2e infrastructure.
 *
 * The fake sendmail script (e2e/bin/fake-sendmail) appends raw email messages
 * to $DANSAL_MAIL_FILE (default /tmp/dansal-e2e-mail.mbox).  These helpers
 * let tests clear the file before a scenario and wait for a manage-link token
 * to appear after submission.
 *
 * Setup (one-time, per instance):
 *   config.yaml:    smtp.sendmail: /path/to/e2e/bin/fake-sendmail
 *                   smtp.from: noreply@example.com
 *   web.yaml:       smtp_sendmail: /path/to/e2e/bin/fake-sendmail
 */
import * as fs from "fs";

export const MAIL_FILE =
  process.env.DANSAL_MAIL_FILE ?? "/tmp/dansal-e2e-mail.mbox";

/** Delete the mbox file, discarding any accumulated messages. */
export function clearMailbox(): void {
  try {
    fs.unlinkSync(MAIL_FILE);
  } catch {
    // file doesn't exist yet — that's fine
  }
}

/**
 * Poll the mbox file until `pattern` matches, then return the match. Every
 * other wait* helper in this file is a thin wrapper around this — build a
 * new one for a link shape not already covered rather than re-rolling the
 * poll loop.
 *
 * Outgoing mail is always sent in a goroutine (see CLAUDE.md), so there is a
 * short delay between the triggering request and the file being written.
 * The default 15 s timeout gives ample margin even under CI load.
 *
 * Throws if no match appears within `timeoutMs`.
 */
export async function waitForMail(
  pattern: RegExp,
  timeoutMs = 15_000
): Promise<RegExpMatchArray> {
  const deadline = Date.now() + timeoutMs;
  while (Date.now() < deadline) {
    try {
      const content = fs.readFileSync(MAIL_FILE, "utf-8");
      const m = content.match(pattern);
      if (m) return m;
    } catch {
      // file not written yet — keep polling
    }
    await new Promise((r) => setTimeout(r, 250));
  }
  throw new Error(
    `waitForMail: no match for ${pattern} in ${MAIL_FILE} after ${timeoutMs} ms`
  );
}

/**
 * Poll the mbox file until a URL matching `pathPrefix` (a literal path
 * segment, e.g. "/events/suggest/manage/" or "/contact-posts/manage/")
 * appears, then return the token that follows it.
 */
export async function waitForMailboxURL(
  pathPrefix: string,
  timeoutMs = 15_000
): Promise<string> {
  const escaped = pathPrefix.replace(/[.*+?^${}()|[\]\\]/g, "\\$&");
  const pattern = new RegExp(`${escaped}([A-Za-z0-9_-]{20,})`);
  const m = await waitForMail(pattern, timeoutMs);
  return m[1];
}

/** The suggest wizard's manage-link token — e.g.
 *  http://localhost:8080/events/suggest/manage/<token> */
export async function waitForManageToken(timeoutMs = 15_000): Promise<string> {
  return waitForMailboxURL("/events/suggest/manage/", timeoutMs);
}

/** The bulletin board's combined verify+manage-link token — e.g.
 *  http://localhost:8080/contact-posts/manage/<token> */
export async function waitForBoardManageToken(timeoutMs = 15_000): Promise<string> {
  return waitForMailboxURL("/contact-posts/manage/", timeoutMs);
}

/** A generic account-verification link (POST /api/v1/users/{id}/verify,
 *  cmd/dansal/verify.go) — e.g. http://localhost:8080/verify/<token>.
 *  Distinct from the suggest wizard's own /register/verify/email/ links. */
export async function waitForVerifyToken(timeoutMs = 15_000): Promise<string> {
  return waitForMailboxURL("/verify/", timeoutMs);
}

/** A passwordless magic-login link (POST /api/v1/login/magic, magic.go) —
 *  e.g. http://localhost:8080/login/magic/<token>. */
export async function waitForMagicLoginToken(timeoutMs = 15_000): Promise<string> {
  return waitForMailboxURL("/login/magic/", timeoutMs);
}
