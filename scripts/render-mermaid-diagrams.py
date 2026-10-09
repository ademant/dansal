#!/usr/bin/env python3
"""
render-mermaid-diagrams.py — pre-render ```mermaid fenced code blocks in
wiki/*.md to SVG, for dansal-doc (#1488).

Why build-time, not runtime: cmd/dansal_doc is a dependency-free Go binary
(goldmark + the Table extension, nothing else) matching the project's
self-hosted-with-no-external-runtime deployment model. Rendering Mermaid
client-side means every visitor downloads the ~500KB mermaid.js library;
rendering it server-side *per request* means spawning a headless Chromium
process per diagram per page view, which is both a new systemd-unit
dependency for every operator and a real resource/DoS concern on a public
docs page. Pre-rendering once, here, at authoring time, means dansal-doc
itself never changes at all -- the generated SVG is just another image
referenced from markdown, served by the exact same mechanism
cmd/dansal_doc/main.go already uses for screenshots (handleImage).

This script is NOT part of `make build` or `make deploy` -- it only needs
Node.js on whichever machine a diagram gets authored/edited on, never on a
deployed server. Run it manually after adding or changing a ```mermaid
block, commit the generated wiki/images/mermaid-*.svg alongside your
markdown change.

Usage:
    python3 scripts/render-mermaid-diagrams.py [--force] [FILE ...]

    --force   re-render every diagram found, even if its SVG already
              exists (useful after bumping MERMAID_CLI_VERSION below)
    FILE ...  specific wiki/*.md files to process (default: all of them)

Requires: Node.js + npx (only here, never on a dansal-doc server).
"""

import argparse
import hashlib
import json
import re
import subprocess
import sys
import tempfile
from pathlib import Path

REPO_ROOT = Path(__file__).resolve().parent.parent
WIKI_DIR = REPO_ROOT / "wiki"
IMAGES_DIR = WIKI_DIR / "images"

# Pinned rather than @latest: the output SVG is a committed artifact, not a
# live dependency, so reproducibility matters more than always getting the
# newest version. Bump deliberately (and re-run with --force) when needed.
MERMAID_CLI_VERSION = "12.0.0"

FENCE_RE = re.compile(
    r"(?P<fence>```mermaid\n(?P<source>.*?\n)```\n)",
    re.DOTALL,
)

PUPPETEER_CONFIG = {"args": ["--no-sandbox", "--disable-setuid-sandbox"]}


def render_diagram(source: str, out_path: Path) -> None:
    with tempfile.TemporaryDirectory() as tmp:
        tmp_path = Path(tmp)
        mmd_file = tmp_path / "diagram.mmd"
        mmd_file.write_text(source, encoding="utf-8")
        puppeteer_config = tmp_path / "puppeteer-config.json"
        puppeteer_config.write_text(json.dumps(PUPPETEER_CONFIG), encoding="utf-8")

        result = subprocess.run(
            [
                "npx", "-y", f"@mermaid-js/mermaid-cli@{MERMAID_CLI_VERSION}",
                "-i", str(mmd_file),
                "-o", str(out_path),
                "-p", str(puppeteer_config),
                "--no-font-embed",  # cuts ~200KB base64 font data down to ~1-30KB total (#1488)
                "-b", "white",      # diagrams always sit on their own light card regardless of
            ],              # the page's light/dark theme -- see the CSS rule in cmd/dansal_doc
            capture_output=True, text=True,
        )
        if result.returncode != 0:
            sys.exit(
                f"mermaid-cli failed (is Node.js installed?):\n{result.stdout}\n{result.stderr}"
            )


def process_file(path: Path, force: bool) -> int:
    path = path.resolve()
    text = path.read_text(encoding="utf-8")
    rendered = 0
    out_parts = []
    pos = 0
    changed = False

    for m in FENCE_RE.finditer(text):
        out_parts.append(text[pos:m.end("fence")])
        pos = m.end("fence")

        digest = hashlib.sha256(m.group("source").encode("utf-8")).hexdigest()[:12]
        svg_name = f"mermaid-{digest}.svg"
        svg_path = IMAGES_DIR / svg_name
        image_line = f"![Mermaid diagram](images/{svg_name})\n"

        if force or not svg_path.exists():
            IMAGES_DIR.mkdir(parents=True, exist_ok=True)
            render_diagram(m.group("source"), svg_path)
            rendered += 1
            print(f"  rendered {svg_name} ({path.relative_to(REPO_ROOT)})")

        # Idempotent: only insert the image reference if it isn't already
        # the next line (re-running this script must not keep stacking
        # duplicate image lines after the same fence).
        already_present = text[pos:pos + len(image_line)] == image_line
        if not already_present:
            out_parts.append(image_line)
            changed = True

    out_parts.append(text[pos:])
    if changed:
        path.write_text("".join(out_parts), encoding="utf-8")
    return rendered


def main() -> None:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--force", action="store_true")
    parser.add_argument("files", nargs="*", type=Path)
    args = parser.parse_args()

    targets = args.files or sorted(WIKI_DIR.glob("*.md"))
    total_rendered = 0
    for path in targets:
        total_rendered += process_file(path, args.force)

    if total_rendered == 0:
        print("No new diagrams to render (already up to date).")
    else:
        print(f"\nRendered {total_rendered} diagram(s). Review and commit "
              f"the generated files under {IMAGES_DIR.relative_to(REPO_ROOT)}/.")


if __name__ == "__main__":
    main()
