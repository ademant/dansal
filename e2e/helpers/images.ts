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

export { API_BASE };
