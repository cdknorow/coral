#!/usr/bin/env python3
"""Build the Windows installer's dialog bitmaps from the colour artwork.

Usage: pip install -r tools/requirements-installer-art.txt && python3 tools/make_installer_art.py
Writes assets/installer/WixUIBannerBmp.bmp and assets/installer/WixUIDialogBmp.bmp, which
tools/coral.wxs uses in place of WiX's stock art. With the pinned Pillow the output is the same
bytes on every machine, so the committed files can be checked against a fresh run.

WiX draws its own text over both, in black, so the art keeps the text areas plain:
  WixUIBannerBmp (493x58): the top banner of the inner dialogs. The title and description sit
    on the left, so the left is plain white and the icon sits at the right edge.
  WixUIDialogBmp (493x312): the welcome/license and finish dialogs. WiX draws its text from
    x=164 rightwards, so the brand panel is the left 164 px and the rest is white.
The icon is the same artwork as the app's icon (make_windows_icon.py). No text is rendered, so
nothing depends on the fonts a machine has.
"""
from pathlib import Path

from PIL import Image

ROOT = Path(__file__).resolve().parent.parent
SRC = ROOT / "assets/icons/cora-icon-color-1024x1024.png"
OUT = ROOT / "assets/installer"

WHITE = (255, 255, 255)
SKY = (0x38, 0xBE, 0xF9)  # the icon's two colours
TINT_TOP = (0xEC, 0xF8, 0xFF)  # very light tints of them, for the panel
TINT_BOTTOM = (0xD6, 0xE8, 0xFF)
PANEL = 164


def square_art():
    """The artwork cropped to its drawing and centred in a square, as make_windows_icon.py does."""
    im = Image.open(SRC).convert("RGBA")
    box = im.getbbox()
    side = max(box[2] - box[0], box[3] - box[1])
    side += side // 25
    canvas = Image.new("RGBA", (side, side), (0, 0, 0, 0))
    canvas.alpha_composite(im.crop(box), ((side - (box[2] - box[0])) // 2, (side - (box[3] - box[1])) // 2))
    return canvas


def icon(art, size):
    return art.resize((size, size), Image.LANCZOS)


def banner(art):
    im = Image.new("RGBA", (493, 58), WHITE + (255,))
    size = 44
    im.alpha_composite(icon(art, size), (493 - size - 10, (58 - size) // 2))
    return im.convert("RGB")


def dialog(art):
    im = Image.new("RGBA", (493, 312), WHITE + (255,))
    px = im.load()
    for y in range(312):  # the panel: a soft vertical gradient between the two tints
        t = y / 311
        c = tuple(round(a + (b - a) * t) for a, b in zip(TINT_TOP, TINT_BOTTOM)) + (255,)
        for x in range(PANEL - 2):
            px[x, y] = c
    for y in range(312):  # a thin edge in the brand colour between the panel and the text area
        for x in range(PANEL - 2, PANEL):
            px[x, y] = SKY + (255,)
    size = 112
    im.alpha_composite(icon(art, size), ((PANEL - 2 - size) // 2, 84))
    return im.convert("RGB")


def main():
    OUT.mkdir(parents=True, exist_ok=True)
    art = square_art()
    for name, im in (("WixUIBannerBmp.bmp", banner(art)), ("WixUIDialogBmp.bmp", dialog(art))):
        path = OUT / name
        im.save(path, "BMP")  # 24-bit, what WiX's dialogs take
        print(f"wrote {path.relative_to(ROOT).as_posix()} {im.size[0]}x{im.size[1]}")


if __name__ == "__main__":
    main()
