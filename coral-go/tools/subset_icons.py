#!/usr/bin/env python3
"""subset_icons.py: build the dashboard's Material Icons subset (#3973).

The dashboard ships Material Icons (Apache 2.0, Google Fonts) as vendor/fonts/material-icons.woff2. The full font is 129 KB for
about 2,200 icons and the dashboard names about 150 of them, so this keeps only the icons its own source can show
(tests/frontend/icon_names.mjs is the scan) and rewrites the ligature table to match.

    python3 tools/subset_icons.py --source /path/to/full/material-icons.woff2 [--dry-run]

  --source   the full upstream font (Google Fonts, fonts.gstatic.com/s/materialicons/v145/); it is not kept in the repository
  writes     pkg/frontend/static/vendor/fonts/material-icons.woff2 (the subset) and material-icons.names.txt (the icons in it)
  refreshes  tools/material-icons-all-names.txt (every ligature name of the full font, which the scan and its test compare against)

Needs: fonttools and brotli (pip install fonttools brotli; both MIT) and node. Run it again whenever a new icon is used: the node
test tests/frontend/icon_subset.test.mjs fails until you do.
"""
import argparse
import json
import os
import subprocess
import sys
import tempfile

from fontTools import subset
from fontTools.ttLib import TTFont

HERE = os.path.dirname(os.path.abspath(__file__))
ROOT = os.path.dirname(HERE)
FONTS = os.path.join(ROOT, 'pkg', 'frontend', 'static', 'vendor', 'fonts')
ALL_NAMES = os.path.join(HERE, 'material-icons-all-names.txt')
SCAN = os.path.join(os.path.dirname(ROOT), 'tests', 'frontend', 'icon_names.mjs')


def ligatures(font):
    """{ligature name: glyph name} from the font's ligature substitutions."""
    cmap = font.getBestCmap()
    rev = {g: chr(c) for c, g in cmap.items()}
    out = {}
    for lookup in font['GSUB'].table.LookupList.Lookup:
        for st in lookup.SubTable:
            st = getattr(st, 'ExtSubTable', st)
            for first, ligs in getattr(st, 'ligatures', {}).items():
                for lg in ligs:
                    out[''.join(rev[c] for c in [first] + list(lg.Component))] = lg.LigGlyph
    return out


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument('--source', required=True)
    ap.add_argument('--dry-run', action='store_true')
    a = ap.parse_args()

    full = TTFont(a.source)
    lig = ligatures(full)
    if not a.dry_run:
        with open(ALL_NAMES, 'w') as f:
            f.write('\n'.join(sorted(lig)) + '\n')
    scan = json.loads(subprocess.check_output(['node', SCAN, '--json'], text=True))
    names = [n for n in scan['names'] if n in lig]
    cmap = full.getBestCmap()
    points = [p for p in scan['codePoints'] if p in cmap]

    # Keep the letters, digits and underscore the ligatures are spelled with, the icons' own glyphs, and the code points the source writes.
    keep_glyphs = {lig[n] for n in names} | {cmap[p] for p in points}
    letters = [ord(c) for c in 'abcdefghijklmnopqrstuvwxyz0123456789_']
    opts = subset.Options()
    opts.layout_features = ['rlig']       # the font's ligature feature
    opts.layout_closure = False           # do not pull every ligature back in through the letters
    opts.flavor = 'woff2'
    opts.notdef_outline = True
    opts.glyph_names = False
    opts.name_IDs = [0, 1, 2, 3, 4, 5, 6]
    opts.drop_tables += ['DSIG']
    sub = subset.Subsetter(opts)
    sub.populate(unicodes=letters + points, glyphs=sorted(keep_glyphs))
    sub.subset(full)

    kept = ligatures(full)
    missing = [n for n in names if n not in kept]
    extra = sorted(set(kept) - set(names))
    if missing:
        sys.exit('lost icons in the subset: %s' % missing)
    out = os.path.join(FONTS, 'material-icons.woff2')
    outnames = os.path.join(FONTS, 'material-icons.names.txt')
    full.flavor = 'woff2'
    if a.dry_run:
        with tempfile.TemporaryDirectory() as d:
            probe = os.path.join(d, 'subset.woff2')
            full.save(probe)
            print('dry run: %d icons, %d bytes (%d more ligatures kept through code points: %s)' % (len(names), os.path.getsize(probe), len(extra), extra[:10]))
        return
    full.save(out)
    with open(outnames, 'w') as f:
        f.write('\n'.join(sorted(kept)) + '\n')
    print('%s: %d icons, %d bytes' % (out, len(kept), os.path.getsize(out)))


if __name__ == '__main__':
    main()
