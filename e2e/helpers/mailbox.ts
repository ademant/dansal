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
 * Poll the mbox file until a URL matching `pathPrefix` (a literal path
 * segment, e.g. "/events/suggest/manage/" or "/contact-posts/manage/")
 * appears, then return the token that follows it.
 *
 * Both the suggest wizard and the board-post flow send their manage-link
 * emails in a goroutine, so there is a short delay between form submission
 * and the file appearing. The default 15 s timeout gives ample margin even
 * under CI load.
 *
 * Throws if no token appears within `timeoutMs`.
 */
export async function waitForMailboxURL(
  pathPrefix: string,
  timeoutMs = 15_000
): Promise<string> {
  const escaped = pathPrefix.replace(/[.*+?^${}()|[\]\\]/g, "\\$&");
  const pattern = new RegExp(`${escaped}([A-Za-z0-9_-]{20,})`);
  const deadline = Date.now() + timeoutMs;
  while (Date.now() < deadline) {
    try {
      const content = fs.readFileSync(MAIL_FILE, "utf-8");
      const m = content.match(pattern);
      if (m) return m[1];
    } catch {
      // file not written yet — keep polling
    }
    await new Promise((r) => setTimeout(r, 250));
  }
  throw new Error(
    `waitForMailboxURL: no match for ${pathPrefix} in ${MAIL_FILE} after ${timeoutMs} ms`
  );
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
