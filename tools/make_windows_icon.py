#!/usr/bin/env python3
"""Build the multi-size Windows icon (16, 24, 32, 48, 256) from the colour artwork.

Usage: python3 tools/make_windows_icon.py
Writes coral-go/cmd/coral-tray/icon.ico and assets/icons/coral.ico. The colour artwork
reads on both light and dark taskbars (the macOS menu-bar glyph is white, so it must not
be used on Windows). Sizes up to 48 are stored as 32-bit BMP/DIB entries (what the
notification-area loader and old shells expect); 256 is a PNG entry.
"""
import io
import struct
from pathlib import Path

from PIL import Image

ROOT = Path(__file__).resolve().parent.parent
SRC = ROOT / "assets/icons/cora-icon-color-1024x1024.png"
OUTS = [ROOT / "coral-go/cmd/coral-tray/icon.ico", ROOT / "assets/icons/coral.ico"]
SIZES = [16, 24, 32, 48, 256]


def square_art():
    im = Image.open(SRC).convert("RGBA")
    box = im.getbbox()
    side = max(box[2] - box[0], box[3] - box[1])
    side += side // 25  # small margin so the ring is not clipped when scaled
    cx, cy = (box[0] + box[2]) // 2, (box[1] + box[3]) // 2
    canvas = Image.new("RGBA", (side, side), (0, 0, 0, 0))
    canvas.alpha_composite(im.crop(box), ((side - (box[2] - box[0])) // 2, (side - (box[3] - box[1])) // 2))
    return canvas


def dib(im):
    w, h = im.size
    px = im.load()
    rows = []
    for y in range(h - 1, -1, -1):  # bottom-up BGRA
        rows.append(b"".join(struct.pack("<BBBB", px[x, y][2], px[x, y][1], px[x, y][0], px[x, y][3]) for x in range(w)))
    mask_stride = ((w + 31) // 32) * 4
    mask = b"\x00" * (mask_stride * h)  # alpha channel carries transparency
    header = struct.pack("<IiiHHIIiiII", 40, w, h * 2, 1, 32, 0, w * h * 4, 0, 0, 0, 0)
    return header + b"".join(rows) + mask


def main():
    art = square_art()
    entries = []
    for s in SIZES:
        im = art.resize((s, s), Image.LANCZOS)
        if s >= 256:
            buf = io.BytesIO()
            im.save(buf, "PNG", optimize=True)
            entries.append((s, buf.getvalue()))
        else:
            entries.append((s, dib(im)))
    head = struct.pack("<HHH", 0, 1, len(entries))
    off = 6 + 16 * len(entries)
    table, blobs = b"", b""
    for s, data in entries:
        table += struct.pack("<BBBBHHII", s % 256, s % 256, 0, 0, 1, 32, len(data), off + len(blobs))
        blobs += data
    ico = head + table + blobs
    for out in OUTS:
        out.write_bytes(ico)
        print(f"wrote {out.relative_to(ROOT)} ({len(ico)} bytes)")


if __name__ == "__main__":
    main()
