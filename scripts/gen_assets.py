#!/usr/bin/env python3
"""Generate Dexaflow icon and favicon assets from logo.png.

Splits the lion/pinwheel mark (top cluster) from the "leoflow" wordmark
(bottom cluster), squares the mark, and emits PNG sizes plus favicon.ico.
"""
from __future__ import annotations

import pathlib

from PIL import Image

ROOT = pathlib.Path(__file__).resolve().parent.parent
SRC = ROOT / "logo.png"
OUT = ROOT / "docs" / "assets"
OUT.mkdir(parents=True, exist_ok=True)


def content_mask(img: Image.Image):
    """Return a per-pixel access object and a predicate for 'is content'."""
    px = img.load()
    w, h = img.size

    def is_content(x: int, y: int) -> bool:
        r, g, b, a = px[x, y]
        if a <= 32:
            return False
        return not (r > 245 and g > 245 and b > 245)

    return is_content, w, h


def row_has_content(is_content, w: int, y: int, step: int = 4) -> bool:
    return any(is_content(x, y) for x in range(0, w, step))


def find_clusters(is_content, w: int, h: int):
    """Find vertical content bands separated by empty rows."""
    rows = [row_has_content(is_content, w, y) for y in range(h)]
    clusters = []
    start = None
    for y, filled in enumerate(rows):
        if filled and start is None:
            start = y
        elif not filled and start is not None:
            clusters.append((start, y - 1))
            start = None
    if start is not None:
        clusters.append((start, h - 1))
    # Merge clusters separated by a tiny gap (< 1.5% of height).
    merged = []
    gap = int(h * 0.015)
    for c in clusters:
        if merged and c[0] - merged[-1][1] <= gap:
            merged[-1] = (merged[-1][0], c[1])
        else:
            merged.append(list(c))
    return [tuple(c) for c in merged]


def horizontal_bounds(is_content, top: int, bottom: int, w: int):
    left, right = w, 0
    for y in range(top, bottom + 1):
        for x in range(w):
            if is_content(x, y):
                left = min(left, x)
                right = max(right, x)
                break
        for x in range(w - 1, -1, -1):
            if is_content(x, y):
                right = max(right, x)
                break
    return left, right


def main() -> None:
    img = Image.open(SRC).convert("RGBA")
    is_content, w, h = content_mask(img)
    clusters = find_clusters(is_content, w, h)
    print(f"source {w}x{h}, vertical clusters: {clusters}")

    # The icon is the tallest top cluster; the wordmark is the wide short one below.
    icon_band = max(clusters, key=lambda c: c[1] - c[0])
    top, bottom = icon_band
    left, right = horizontal_bounds(is_content, top, bottom, w)
    print(f"icon band y[{top},{bottom}] x[{left},{right}]")

    box_w = right - left + 1
    box_h = bottom - top + 1
    side = max(box_w, box_h)
    margin = int(side * 0.06)
    side += margin * 2
    cx = (left + right) // 2
    cy = (top + bottom) // 2
    sq = (cx - side // 2, cy - side // 2, cx + side // 2, cy + side // 2)
    icon = img.crop(sq)
    print(f"icon square {icon.size}")

    master = OUT / "icon.png"
    icon.save(master)

    sizes = [512, 256, 192, 180, 128, 64, 48, 32, 16]
    for s in sizes:
        icon.resize((s, s), Image.LANCZOS).save(OUT / f"icon-{s}.png")

    ico_sizes = [(16, 16), (32, 32), (48, 48), (64, 64), (128, 128), (256, 256)]
    icon.resize((256, 256), Image.LANCZOS).save(OUT / "favicon.ico", sizes=ico_sizes)

    # Web-friendly full logo for the README (cap width at 640).
    full = img.copy()
    if full.width > 640:
        ratio = 640 / full.width
        full = full.resize((640, int(full.height * ratio)), Image.LANCZOS)
    full.save(OUT / "logo.png", optimize=True)
    print("done")


if __name__ == "__main__":
    main()
