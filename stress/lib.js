// Shared guard for the k6 scripts (#1544). Load tests write to and hammer the
// instance they point at, so they refuse anything but loopback unless the
// operator opts in explicitly with STRESS_ALLOW_REMOTE=1. Run them on the
// scratch instance's host, not against dev or prod.
export function baseURL() {
  const base = __ENV.STRESS_BASE_URL || "";
  if (!base) {
    throw new Error("STRESS_BASE_URL is required, e.g. http://127.0.0.1:18000");
  }
  const host = base.replace(/^https?:\/\//, "").split(/[/:]/)[0];
  const loopback = ["127.0.0.1", "localhost", "::1"].includes(host);
  if (!loopback && __ENV.STRESS_ALLOW_REMOTE !== "1") {
    throw new Error(
      `refusing to load-test non-loopback host ${host}; set STRESS_ALLOW_REMOTE=1 to override`
    );
  }
  return base.replace(/\/+$/, "");
}

// Headers that identify the caller as dansal-web's loopback backend, which is
// exempt from the per-IP rate limiter (see isInternalCaller in
// cmd/dansal/main.go). Only sent when STRESS_INTERNAL_SECRET is set, and only
// honoured when the request actually comes from loopback.
export function internalHeaders() {
  const secret = __ENV.STRESS_INTERNAL_SECRET || "";
  return secret ? { "X-Dansal-Internal": secret } : {};
}
