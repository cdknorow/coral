#!/usr/bin/env bash
# A build that will not be signed must not ship while the Windows updater requires a signature (N4). Usage:
#   signature_required_check.sh <authenticode_require.go> <WINDOWS_SIGNING value> <build tags>
# Fails (exit 1) when RequireSignedWindowsPackage is true and the build will not be signed: signing is not switched on (WINDOWS_SIGNING is not exactly
# "true") or the build is the Dropbox tier (which is never signed or given an MSI). It passes while the switch is false, and when it is true on a build that is signed.
set -euo pipefail
file=${1:?authenticode_require.go}; signing=${2-}; tags=${3-}
[ -f "$file" ] || { echo "signature_required_check: no such file: $file" >&2; exit 2; }
n=$(grep -c '^const RequireSignedWindowsPackage = ' "$file" || true)
[ "$n" = 1 ] || { echo "signature_required_check: expected exactly one 'const RequireSignedWindowsPackage = ' line in $file, found $n" >&2; exit 2; }
if grep -q '^const RequireSignedWindowsPackage = true' "$file"; then
  if [ "$signing" != "true" ]; then
    echo "RequireSignedWindowsPackage is true, so the Windows updater refuses an unsigned package, but this release is not signed (WINDOWS_SIGNING is not 'true'): it would ship an update no Windows Coral can take." >&2
    exit 1
  fi
  case ",$tags," in
    *,dropboxers,*)
      echo "RequireSignedWindowsPackage is true, but the Dropbox build is never signed." >&2
      exit 1 ;;
  esac
fi
echo "signature requirement ok (switch: $(grep -o '^const RequireSignedWindowsPackage = [a-z]*' "$file" | awk '{print $NF}'), WINDOWS_SIGNING='${signing}', tags='${tags}')"
