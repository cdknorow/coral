#!/usr/bin/env bash
# Regenerate the Windows resources (rsrc_windows_amd64.syso) linked into the user-facing exes:
# the icon (assets/icons/coral.ico, see make_windows_icon.py), the manifest, and the version
# information Windows shows for the exe: the taskbar, Task Manager and the jump list name an app by
# its FileDescription (without one, by its file name: "coral-app.exe"). The GOOS/GOARCH suffix
# keeps the .syso out of every other platform's build.
#
# Usage: tools/gen_windows_resources.sh [version [product-name]]
#   No arguments: the committed files, with version 0.0.0.0 (a source build has no release
#   version). Commit the results.
#   A release build passes the release version (the tag without its "v", suffix included, e.g.
#   1.4.148 or 1.4.148-beta) and, for the Dropbox tier, "Coral For Dropbox". The numeric file and
#   product version are the leading X.Y.Z of it; the text version is all of it. Release builds
#   regenerate the files before compiling and do not commit them.
set -euo pipefail
ROOT="$(cd "$(dirname "$0")/.." && pwd)"
VERSION="${1:-}"
PRODUCT="${2:-Coral}"
COMPANY="Subgentic LLC" # the publisher (also the LICENSE copyright holder): the MSI's Manufacturer (tools/coral.wxs) and every exe's CompanyName
COPYRIGHT="$(sed -n 's/^ *Copyright \([0-9]\{4\} .*[^ ]\) *$/Copyright \1/p' "$ROOT/LICENSE" | head -1)"
[ -n "$COPYRIGHT" ] || { echo "no copyright line in LICENSE" >&2; exit 1; }

NUMERIC="0.0.0.0"
TEXT="0.0.0.0"
if [ -n "$VERSION" ]; then
  VERSION="${VERSION#v}"
  if [[ "$VERSION" =~ ^([0-9]+)\.([0-9]+)\.([0-9]+) ]]; then
    NUMERIC="${BASH_REMATCH[1]}.${BASH_REMATCH[2]}.${BASH_REMATCH[3]}.0"
  fi
  TEXT="$VERSION"
fi
# The values go into JSON by hand: refuse anything that would need escaping.
for v in "$PRODUCT" "$TEXT" "$COMPANY" "$COPYRIGHT"; do
  case "$v" in *\"* | *\\*) echo "unsupported character in: $v" >&2; exit 1 ;; esac
done

GOBIN="$(mktemp -d)"
WORK="$(mktemp -d)"
trap 'rm -rf "$GOBIN" "$WORK"' EXIT
GOBIN="$GOBIN" go install github.com/tc-hib/go-winres@v0.3.3
cp "$ROOT/assets/icons/coral.ico" "$WORK/coral.ico"

# cmd, file description (what the taskbar and Task Manager show), original file name
while IFS='|' read -r cmd description exe; do
  cat >"$WORK/winres.json" <<EOF
{
  "RT_GROUP_ICON": {"#1": {"0000": "coral.ico"}},
  "RT_MANIFEST": {
    "#1": {
      "0409": {
        "identity": {"name": "", "version": ""},
        "description": "",
        "minimum-os": "win7",
        "execution-level": "as invoker",
        "ui-access": false,
        "auto-elevate": false,
        "dpi-awareness": "system",
        "disable-theming": false,
        "disable-window-filtering": false,
        "high-resolution-scrolling-aware": false,
        "ultra-high-resolution-scrolling-aware": false,
        "long-path-aware": false,
        "printer-driver-isolation": false,
        "gdi-scaling": false,
        "segment-heap": false,
        "use-common-controls-v6": false
      }
    }
  },
  "RT_VERSION": {
    "#1": {
      "0000": {
        "fixed": {"file_version": "$NUMERIC", "product_version": "$NUMERIC"},
        "info": {
          "0409": {
            "CompanyName": "$COMPANY",
            "FileDescription": "$description",
            "FileVersion": "$TEXT",
            "InternalName": "$cmd",
            "LegalCopyright": "$COPYRIGHT",
            "OriginalFilename": "$exe",
            "ProductName": "$PRODUCT",
            "ProductVersion": "$TEXT"
          }
        }
      }
    }
  }
}
EOF
  (cd "$ROOT/coral-go/cmd/$cmd" && "$GOBIN/go-winres" make --in "$WORK/winres.json" --arch amd64 --out rsrc)
done <<EOF
coral|$PRODUCT server|coral.exe
launch-coral|$PRODUCT launcher|launch-coral.exe
coral-tray|$PRODUCT tray|coral-tray.exe
coral-app|$PRODUCT|coral-app.exe
EOF
