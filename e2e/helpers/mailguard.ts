import * as fs from "fs";
import * as path from "path";
import { test } from "@playwright/test";
import { MAIL_FILE } from "./mailbox";

/**
 * skipWithoutMailbox (#1423) skips the calling describe/test when the
 * fake-sendmail mbox directory doesn't exist on this machine — e.g. a dev
 * instance whose e2e mail setup (DANSAL_MAIL_FILE drop-in, mbox dir) is
 * missing. Without it every mail-waiting spec ran into a 15 s
 * "no match … in mailbox" timeout with no hint that no mail could arrive.
 * Call at the top of a describe block.
 */
export function skipWithoutMailbox(): void {
  const dir = path.dirname(MAIL_FILE);
  test.skip(
    !fs.existsSync(dir),
    `mailbox directory ${dir} missing — set up fake-sendmail and DANSAL_MAIL_FILE for the target instance (see e2e/README.md)`
  );
}
