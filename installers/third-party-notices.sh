#!/usr/bin/env bash
# third-party-notices.sh [--macos] OUT_DIR BINARY...
#
# Writes the third-party notices a Coral package ships with into OUT_DIR (the package's licenses/ folder):
#   THIRD_PARTY_NOTICES.md   index: every component, its version, license and where its source is
#   Coral-LICENSE.txt        Coral's own license
#   go/<module>@<version>/   the license, notice and patents files of every Go module linked into BINARY...
#                            (the modules `go version -m` lists, so each target gets exactly what it links)
#   vendor/, fonts/          the dashboard's vendored libraries and fonts, from licenses/ in the repo
#   THIRD_PARTY_NOTICES_MPL.md, HASHICORP-MPL-2.0.txt
#
# It makes no network request: module files come from the module cache (GOPROXY=off; run `go mod download`
# before it), after `go mod verify` has checked the cache against go.sum, and the vendor texts are checked
# against licenses/SHA256SUMS.vendor. It fails, never warns, when a linked module has no license file or a
# vendor text does not match. --macos adds the line for the bundled tmux licenses (Contents/Resources).
# Runs under bash 3.2 (macOS), Git Bash (Windows) and Linux bash.
set -euo pipefail

macos=0
if [ "${1:-}" = "--macos" ]; then macos=1; shift; fi
out=${1:?usage: third-party-notices.sh [--macos] OUT_DIR BINARY...}; shift
[ $# -gt 0 ] || { echo "third-party-notices: no binaries given" >&2; exit 2; }

root=$(cd "$(dirname "$0")/.." && pwd)
lic="$root/licenses"
export GOPROXY=off GOFLAGS=-mod=readonly

sha256check() { if command -v sha256sum >/dev/null 2>&1; then sha256sum -c --quiet "$@"; else shasum -a 256 -c --quiet "$@"; fi; }

# Seconds per phase, printed at the end: the Windows release job spends about two minutes here while the same
# script on the same runner image takes seconds (#4605), so the line shows where that time goes.
t0=$SECONDS; times=""
lap() { times="$times${times:+, }$1 $((SECONDS - t0))s"; t0=$SECONDS; }

# 1. The module cache is what go.sum pins.
(cd "$root/coral-go" && go mod verify) || { echo "third-party-notices: go mod verify failed" >&2; exit 1; }
lap verify

# 2. The modules the binaries link: "dep path version" lines, or the "=>" replacement that follows one.
mods=$(for b in "$@"; do
  [ -f "$b" ] || { echo "third-party-notices: no binary $b" >&2; exit 1; }
  go version -m "$b" | awk '
    $1 == "dep" { if (p != "") print p; p = $2 "@" $3 }
    $1 == "=>"  { p = $2 "@" $3 }
    END         { if (p != "") print p }'
done | LC_ALL=C sort -u)
lap binaries
n=$(printf '%s\n' "$mods" | grep -c . || true)
[ "$n" -gt 0 ] || { echo "third-party-notices: no modules found in the binaries" >&2; exit 1; }

rm -rf "$out"; mkdir -p "$out/go"
idx="$out/THIRD_PARTY_NOTICES.md"
{
  echo "# Third-party notices"
  echo
  echo "Coral is licensed under the terms in \`Coral-LICENSE.txt\`. It includes the components below, each under"
  echo "its own license. Their license texts are in this folder, next to this file."
  echo
  echo "## Go modules linked into the Coral programs"
  echo
  echo "Each module's license files are in \`go/<module>@<version>/\`. The exact source of each is recorded,"
  echo "with its checksum, in Coral's \`go.sum\` and is available from the Go module proxy"
  echo "(\`go mod download <module>@<version>\`)."
  echo
  echo "| Module | Version | License files |"
  echo "|---|---|---|"
} > "$idx"

# Every module's cache folder from one `go mod download -json` call, as "module@version<TAB>dir" lines: a
# call per module costs about 1.4 s each on the Windows runner (80 modules, 2 minutes of every release, #4605).
# Git Bash on Windows: the module cache path comes back as C:\...; use its /c/... form.
winpath=0; command -v cygpath >/dev/null 2>&1 && winpath=1
# shellcheck disable=SC2086 # $mods is one module@version per line, none with spaces
dirs=$(cd "$root/coral-go" && { go mod download -json $mods 2>/dev/null || true; } | awk -v win="$winpath" '
  function val(line) { sub(/^[^:]*: "/, "", line); sub(/",?[[:space:]]*$/, "", line); return line }
  /^[[:space:]]*"Path": /    { p = val($0) }
  /^[[:space:]]*"Version": / { v = val($0) }
  /^[[:space:]]*"Dir": /     { d = val($0); gsub(/\\\\/, "/", d)
                               if (win && d ~ /^[A-Za-z]:\//) d = "/" tolower(substr(d, 1, 1)) substr(d, 3)
                               print p "@" v "\t" d }')
lap lookup

missing=0
shopt -s nocasematch
while IFS= read -r mv; do
  [ -n "$mv" ] || continue
  m=${mv%@*}; v=${mv##*@}
  dir=""
  while IFS=$'\t' read -r k kd; do [ "$k" = "$mv" ] && { dir=$kd; break; }; done <<DIRS
$dirs
DIRS
  if [ -z "$dir" ] || [ ! -d "$dir" ]; then echo "third-party-notices: $mv is not in the module cache" >&2; missing=1; continue; fi
  dest="$out/go/$m@$v"; mkdir -p "$dest"
  files=""; has_license=0
  for f in "$dir"/*; do
    [ -f "$f" ] || continue
    base=${f##*/}
    case "$base" in
      license|license.*|licence|licence.*|license-*|copying|copying.*|unlicense|unlicense.*) has_license=1 ;;
      notice|notice.*|patents|patents.*) ;;
      *) continue ;;
    esac
    cp "$f" "$dest/$base"; files="$files${files:+, }$base"
  done
  if [ $has_license -eq 0 ]; then echo "third-party-notices: $mv has no license file" >&2; missing=1; continue; fi
  echo "| $m | $v | $files |" >> "$idx"
done <<EOF
$mods
EOF
shopt -u nocasematch
lap modules
[ $missing -eq 0 ] || { echo "third-party-notices: FAILED (see above)" >&2; exit 1; }

# 3. The vendored dashboard libraries and fonts, checked against the recorded sha256 first.
(cd "$lic" && sha256check SHA256SUMS.vendor) || { echo "third-party-notices: a vendor license text does not match SHA256SUMS.vendor" >&2; exit 1; }
cp -R "$lic/vendor" "$out/vendor"; cp -R "$lic/fonts" "$out/fonts"
{
  echo
  echo "## Dashboard libraries and fonts"
  echo
  echo "These are built into the Coral server's dashboard. Each one's license files are in the folder named."
  echo
  echo "| Component | Version | License | Folder | Source |"
  echo "|---|---|---|---|---|"
  awk -F'\t' 'NR > 1 { printf "| %s | %s | %s | `%s` | %s |\n", $1, $2, $3, $4, $5 }' "$lic/vendor/INDEX.tsv"
  echo
  echo "## MPL-2.0 components"
  echo
  echo "See \`THIRD_PARTY_NOTICES_MPL.md\` (Source Code Form locations) and \`HASHICORP-MPL-2.0.txt\`."
  echo
  echo "## Unicode data"
  echo
  echo "Tables derived from Unicode data files are linked into the Coral programs through golang.org/x/text and"
  echo "github.com/eskriett/confusables (Unicode 17.0.0 confusables). Their notice, the Unicode License v3, is"
  echo "\`UNICODE-LICENSE-V3.txt\`."
  if [ $macos -eq 1 ]; then
    echo
    echo "## Bundled tmux (macOS)"
    echo
    echo "Coral.app bundles tmux and the libraries it is built with (libevent, ncurses). Their licenses and sources"
    echo "are in \`Coral.app/Contents/Resources/tmux-licenses\`."
  fi
} >> "$idx"
cp "$root/THIRD_PARTY_NOTICES_MPL.md" "$out/THIRD_PARTY_NOTICES_MPL.md"
cp "$lic/HASHICORP-MPL-2.0.txt" "$out/HASHICORP-MPL-2.0.txt"
cp "$lic/UNICODE-LICENSE-V3.txt" "$out/UNICODE-LICENSE-V3.txt"
cp "$root/LICENSE" "$out/Coral-LICENSE.txt"

lap vendor
echo "third-party-notices: $n Go modules, $(($(wc -l < "$lic/vendor/INDEX.tsv") - 1)) vendor components -> $out ($times)"
