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
 * Poll the mbox file until a manage-link URL appears, then return the token.
 *
 * The suggest API sends the email in a goroutine, so there is a short delay
 * between form submission and the file appearing.  The default 15 s timeout
 * gives ample margin even under CI load.
 *
 * Throws if no token appears within `timeoutMs`.
 */
export async function waitForManageToken(timeoutMs = 15_000): Promise<string> {
  const deadline = Date.now() + timeoutMs;
  while (Date.now() < deadline) {
    try {
      const content = fs.readFileSync(MAIL_FILE, "utf-8");
      // The email body contains the manage URL:
      //   http://localhost:8080/events/suggest/manage/<token>
      const m = content.match(/\/events\/suggest\/manage\/([A-Za-z0-9_-]{20,})/);
      if (m) return m[1];
    } catch {
      // file not written yet — keep polling
    }
    await new Promise((r) => setTimeout(r, 250));
  }
  throw new Error(
    `waitForManageToken: no token found in ${MAIL_FILE} after ${timeoutMs} ms`
  );
}
