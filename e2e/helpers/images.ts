/**
 * Image-upload test helpers.
 *
 * `makeImage` uses sharp to synthesise a solid-colour image in any format so
 * tests never rely on committed binary fixtures.  Each call returns a fresh
 * Buffer, ready to be passed to `uploadImageAPI`.
 *
 * `uploadImageAPI` wraps Playwright's multipart upload so every spec can
 * stay free of multipart boilerplate.
 */
import sharp from "sharp";
import { Page } from "@playwright/test";

const API_BASE = process.env.API_URL ?? "http://localhost:8000";

// WEB_BASE is the origin a browser actually visits. Image URLs the API hands
// out are root-relative (/api/v1/images/{id}) and are rendered verbatim by the
// web templates, so a browser resolves them against the web origin — which the
// web tier re-serves by proxying to the API (#1374). Tests that assert a real
// browser can render an image must therefore use this origin, not API_BASE.
const WEB_BASE = process.env.BASE_URL ?? "http://localhost:8080";

// Supported input formats the server accepts (decodeImageSafely in images.go
// uses the Go stdlib image decoders: PNG, JPEG, GIF + WebP/AVIF via the avif
// package).
export type ImageFormat = "png" | "jpeg" | "webp" | "avif";

const MIME: Record<ImageFormat, string> = {
  png:  "image/png",
  jpeg: "image/jpeg",
  webp: "image/webp",
  avif: "image/avif",
};

/**
 * Synthesise a solid-colour image of the given dimensions in the requested
 * format.  Background is a muted blue so AVIF/JPEG quality settings produce
 * a representative (non-trivial) compressed output.
 */
export async function makeImage(
  format: ImageFormat,
  width: number,
  height: number
): Promise<Buffer> {
  const s = sharp({
    create: {
      width,
      height,
      channels: 3,
      background: { r: 80, g: 120, b: 180 },
    },
  });
  switch (format) {
    case "png":  return s.png().toBuffer();
    case "jpeg": return s.jpeg({ quality: 85 }).toBuffer();
    case "webp": return s.webp({ quality: 85 }).toBuffer();
    case "avif": return s.avif({ quality: 50 }).toBuffer();
  }
}

/**
 * Upload `buf` as a multipart/form-data "image" field to `endpoint`
 * (relative path, e.g. "/api/v1/images/42") with Bearer auth.
 * Returns the raw Playwright APIResponse.
 */
export async function uploadImageAPI(
  page: Page,
  token: string,
  endpoint: string,
  buf: Buffer,
  format: ImageFormat,
  filename?: string
) {
  return page.request.fetch(`${API_BASE}${endpoint}`, {
    method: "POST",
    headers: { Authorization: `Bearer ${token}` },
    multipart: {
      image: {
        name: filename ?? `test.${format}`,
        mimeType: MIME[format],
        buffer: buf,
      },
    },
  });
}

/**
 * Fetch a public image URL (no auth needed) and return its sharp metadata.
 * Use this to assert that resize limits were respected.
 */
export async function fetchImageMeta(
  page: Page,
  url: string
): Promise<sharp.Metadata> {
  const resp = await page.request.fetch(url);
  const body = await resp.body();
  return sharp(body).metadata();
}

/**
 * Assert that an image really renders in a browser, by handing the URL to a
 * real Chromium `HTMLImageElement.decode()` and checking the result is
 * non-zero.
 *
 * This deliberately does not use sharp: sharp is a Node library with its own
 * codecs, so a payload it accepts can still be undecodable by the browser that
 * has to display it — exactly the failure #1374 reported (a valid AVIF
 * container that `img.decode()` rejects, naturalWidth 0). Only a browser
 * catches that.
 *
 * Pass a web-origin URL (see `webImageURL`), not an API_BASE one: the page's
 * Content-Security-Policy is `img-src 'self' data: https:`, so a cross-origin
 * plain-http image is blocked by CSP and would fail here for a reason that has
 * nothing to do with the image.
 */
export async function assertImageDecodes(page: Page, url: string): Promise<void> {
  const res = await page.evaluate(async (src) => {
    const img = new Image();
    img.src = src;
    try {
      await img.decode();
    } catch (e) {
      return { ok: false as const, err: String(e), w: 0, h: 0 };
    }
    return { ok: true as const, err: "", w: img.naturalWidth, h: img.naturalHeight };
  }, url);

  if (!res.ok) {
    throw new Error(`browser could not decode ${url}: ${res.err}`);
  }
  if (res.w === 0 || res.h === 0) {
    throw new Error(`browser decoded ${url} to a zero-sized image (${res.w}x${res.h})`);
  }
}

/**
 * webImageURL turns an API image path into the URL a browser on the site would
 * use, i.e. the same root-relative path served by the web tier's image proxy.
 */
export function webImageURL(apiPath: string): string {
  return `${WEB_BASE}${apiPath}`;
}

export { API_BASE, WEB_BASE };
