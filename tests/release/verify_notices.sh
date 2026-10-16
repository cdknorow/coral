#!/usr/bin/env bash
# verify_notices.sh LICENSES_DIR BINARY...: check a package's third-party notices (installers/third-party-notices.sh)
# against the binaries it ships. Every module `go version -m` lists for them must have a license file under
# go/<module>@<version>/ and a row in THIRD_PARTY_NOTICES.md; every vendor and font text must match
# licenses/SHA256SUMS.vendor; Coral's license and the MPL notices must be there. Offline; bash 3.2 compatible.
set -euo pipefail
d=${1:?usage: verify_notices.sh LICENSES_DIR BINARY...}; shift
[ $# -gt 0 ] || { echo "verify_notices: no binaries given" >&2; exit 2; }
root=$(cd "$(dirname "$0")/../.." && pwd)
fail=0; bad() { echo "verify_notices: $*" >&2; fail=1; }

for f in THIRD_PARTY_NOTICES.md Coral-LICENSE.txt THIRD_PARTY_NOTICES_MPL.md HASHICORP-MPL-2.0.txt; do
  [ -s "$d/$f" ] || bad "missing $f"
done
[ -s "$d/THIRD_PARTY_NOTICES.md" ] || { echo "verify_notices: FAILED" >&2; exit 1; }

mods=$(for b in "$@"; do
  [ -f "$b" ] || { echo "verify_notices: no binary $b" >&2; exit 1; }
  go version -m "$b" | awk '
    $1 == "dep" { if (p != "") print p; p = $2 "@" $3 }
    $1 == "=>"  { p = $2 "@" $3 }
    END         { if (p != "") print p }'
done | LC_ALL=C sort -u)
n=0
while IFS= read -r mv; do
  [ -n "$mv" ] || continue
  n=$((n + 1)); m=${mv%@*}; v=${mv##*@}
  found=0
  for f in "$d/go/$m@$v"/*; do
    [ -f "$f" ] || continue
    case "$(basename "$f" | tr '[:upper:]' '[:lower:]')" in
      license|license.*|licence|licence.*|license-*|copying|copying.*|unlicense|unlicense.*) [ -s "$f" ] && found=1 ;;
    esac
  done
  [ $found -eq 1 ] || bad "$mv: no license file in go/$m@$v"
  grep -qF "| $m | $v |" "$d/THIRD_PARTY_NOTICES.md" || bad "$mv: not listed in THIRD_PARTY_NOTICES.md"
done <<EOF
$mods
EOF
[ $n -gt 0 ] || bad "no modules found in the binaries"

if command -v sha256sum >/dev/null 2>&1; then sum() { sha256sum -c --quiet "$@"; }; else sum() { shasum -a 256 -c --quiet "$@"; }; fi
(cd "$d" && sum "$root/licenses/SHA256SUMS.vendor") || bad "vendor or font texts differ from licenses/SHA256SUMS.vendor"

[ $fail -eq 0 ] || { echo "verify_notices: FAILED" >&2; exit 1; }
echo "verify_notices: ok ($n Go modules, every vendor and font text) in $d"
