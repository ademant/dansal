/**
 * Minimal RFC 6238 TOTP code generator for e2e coverage (#1262) — no otplib
 * dependency, per design. Reimplements exactly what cmd/dansal/totp.go does
 * server-side (totpGenerateFromKey/totpGenerate): a base32 secret (no
 * padding), HMAC-SHA1, 30-second step, 6-digit code — so a code computed
 * here at time T validates against the server's own check at time T.
 */
import * as crypto from "crypto";

const BASE32_ALPHABET = "ABCDEFGHIJKLMNOPQRSTUVWXYZ234567";

/** Decode an RFC 4648 base32 string (no padding, matches totpNewSecret's encoding). */
function base32Decode(input: string): Buffer {
  const clean = input.trim().toUpperCase().replace(/=+$/, "");
  let bits = "";
  for (const char of clean) {
    const idx = BASE32_ALPHABET.indexOf(char);
    if (idx === -1) {
      throw new Error(`base32Decode: invalid character ${JSON.stringify(char)} in ${JSON.stringify(input)}`);
    }
    bits += idx.toString(2).padStart(5, "0");
  }
  const bytes: number[] = [];
  for (let i = 0; i + 8 <= bits.length; i += 8) {
    bytes.push(parseInt(bits.slice(i, i + 8), 2));
  }
  return Buffer.from(bytes);
}

/** The 30-second counter window a code belongs to — same value in means same code out. */
export function totpWindow(at: number = Date.now()): number {
  return Math.floor(at / 30_000);
}

/** Generate the 6-digit TOTP code for secretBase32 at the given time (default: now). */
export function generateTOTP(secretBase32: string, at: number = Date.now()): string {
  const key = base32Decode(secretBase32);
  const counter = totpWindow(at);
  const counterBuf = Buffer.alloc(8);
  counterBuf.writeBigUInt64BE(BigInt(counter));
  const hmac = crypto.createHmac("sha1", key).update(counterBuf).digest();
  const offset = hmac[hmac.length - 1] & 0x0f;
  const code =
    ((hmac[offset] & 0x7f) << 24) |
    ((hmac[offset + 1] & 0xff) << 16) |
    ((hmac[offset + 2] & 0xff) << 8) |
    (hmac[offset + 3] & 0xff);
  return String(code % 1_000_000).padStart(6, "0");
}

/**
 * Generate a code guaranteed to belong to a *different* 30s window than
 * `avoidWindow` (from a previous generateTOTP/freshTOTPCode call), waiting
 * out the rest of the current window if needed.
 *
 * Needed wherever a test uses two codes for the same secret close together:
 * totpCheckAndMark (auth.go login, settings/totp/disable) records each
 * accepted code and rejects a repeat within its window as a replay — two
 * codes generated moments apart are the *same string* whenever they land in
 * the same window, since the code is a pure function of the window number,
 * not wall-clock time. totpConfirmHandler (settings/totp/confirm) is the
 * one exception: it validates via totpValid, not totpCheckAndMark, so it
 * never marks a code used and never needs this.
 */
export async function freshTOTPCode(
  secretBase32: string,
  avoidWindow?: number
): Promise<{ code: string; window: number }> {
  let window = totpWindow();
  if (avoidWindow !== undefined && window === avoidWindow) {
    const waitMs = (window + 1) * 30_000 - Date.now() + 250;
    await new Promise((r) => setTimeout(r, waitMs));
    window = totpWindow();
  }
  return { code: generateTOTP(secretBase32, Date.now()), window };
}
