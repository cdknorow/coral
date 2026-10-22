#!/usr/bin/env bash
# Regression for the release job's upload loop. It extracts the real run block
# of the "Create release and upload assets" step from release.yml and runs it
# under the same shell options GitHub Actions uses (bash -eo pipefail), with a
# fake `gh` on PATH and fixture artifacts.
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/../.." && pwd)"
WORKFLOW="$ROOT/.github/workflows/release.yml"
WORK="$(mktemp -d)"
trap 'rm -rf "$WORK"' EXIT

# Pull the run block out of the workflow step, removing its indentation.
awk '
  /- name: Create release and upload assets/ { in_step = 1; next }
  in_step && /^ *run: \|/ { in_run = 1; next }
  in_run {
    if ($0 ~ /^ *$/) { print ""; next }
    match($0, /^ */)
    if (indent == 0) indent = RLENGTH
    if (RLENGTH < indent) exit
    print substr($0, indent + 1)
  }
' "$WORKFLOW" > "$WORK/step.sh"
grep -q 'gh release upload' "$WORK/step.sh" || { echo "could not extract upload step" >&2; exit 2; }

mkdir -p "$WORK/bin"
cat > "$WORK/bin/gh" <<'GH'
#!/usr/bin/env bash
echo "gh $*" >> "$GH_LOG"
# release view: the release exists unless NO_RELEASE is set; release create fails if FAIL_CREATE is.
if [[ "$1 $2" == "release view" && -n "${NO_RELEASE:-}" ]]; then exit 1; fi
if [[ "$1 $2" == "release create" && -n "${FAIL_CREATE:-}" ]]; then echo "simulated create failure" >&2; exit 1; fi
if [[ "$1 $2" == "release upload" && -n "${FAIL_UPLOAD:-}" && "$4" == *"$FAIL_UPLOAD"* ]]; then
  echo "simulated upload failure for $4" >&2
  exit 1
fi
exit 0
GH
chmod +x "$WORK/bin/gh"
# Fake cosign: sign-blob writes the bundle it is asked for; verify-blob succeeds.
cat > "$WORK/bin/cosign" <<'COSIGN'
#!/usr/bin/env bash
echo "cosign $*" >> "$COSIGN_LOG"
if [[ "$1" == "sign-blob" ]]; then
  while [[ $# -gt 0 ]]; do [[ "$1" == "--bundle" ]] && { : > "$2"; break; }; shift; done
fi
exit 0
COSIGN
chmod +x "$WORK/bin/cosign"
# macOS has shasum, not sha256sum.
if ! command -v sha256sum >/dev/null; then
  printf '#!/usr/bin/env bash\nexec shasum -a 256 "$@"\n' > "$WORK/bin/sha256sum"
  chmod +x "$WORK/bin/sha256sum"
fi

failures=0
# run_case NAME EXPECTED_EXIT EXPECTED_UPLOADS EXPECTED_SIGNATURES FILES... (FAIL_UPLOAD env optional)
run_case() {
  local name="$1" want_exit="$2" want_uploads="$3" want_signs="$4"; shift 4
  local dir="$WORK/case-$name"
  mkdir -p "$dir"
  ( cd "$dir"
    for f in "$@"; do mkdir -p "$(dirname "$f")"; : > "$f"; done )
  export GH_LOG="$dir/gh.log"; : > "$GH_LOG"
  export COSIGN_LOG="$dir/cosign.log"; : > "$COSIGN_LOG"
  local rc=0
  ( cd "$dir" && PATH="$WORK/bin:$PATH" PRERELEASE="${PRERELEASE:-false}" BUILD_TAGS="${BUILD_TAGS:-fts5}" GITHUB_REF_NAME=v0.0.0-test GITHUB_SERVER_URL=https://github.com \
      GITHUB_WORKFLOW_REF=subgentic/coral/.github/workflows/release.yml@refs/tags/v0.0.0-test \
      bash --noprofile --norc -eo pipefail "$WORK/step.sh" ) > "$dir/out.log" 2>&1 || rc=$?
  local uploads signs
  uploads="$(grep -c '^gh release upload' "$GH_LOG" || true)"
  signs="$(grep -c '^cosign sign-blob' "$COSIGN_LOG" || true)"
  local exit_ok=0
  if [[ "$want_exit" == "nonzero" ]]; then
    [[ "$rc" -ne 0 ]] && exit_ok=1
  else
    [[ "$rc" -eq "$want_exit" ]] && exit_ok=1
  fi
  if [[ "$exit_ok" -eq 1 && "$uploads" == "$want_uploads" && "$signs" == "$want_signs" ]]; then
    echo "PASS $name (exit=$rc uploads=$uploads signatures=$signs)"
  else
    echo "FAIL $name: exit=$rc (want $want_exit) uploads=$uploads (want $want_uploads) signatures=$signs (want $want_signs)" >&2
    sed 's/^/  | /' "$dir/out.log" >&2
    failures=$((failures + 1))
  fi
}

LINUX=artifacts/coral-linux-amd64/coral-linux-amd64-1.tar.gz
LINUX_ARM=artifacts/coral-linux-arm64/coral-linux-arm64-1.tar.gz
DMG=artifacts/coral-macos-dmg/Coral.dmg
MSI=artifacts/coral-windows-msi/Coral-1-x64.msi
ZIP=artifacts/coral-windows-portable/Coral-1-x64-portable.zip
ALL=("$LINUX" "$LINUX_ARM" "$DMG" "$MSI" "$ZIP")

# Every package present: 5 package uploads + one call with the 5 stable-name copies (#5333) + SHA256SUMS.txt
# + one call with the 5 bundles (3, and the 2 copied for the stable Linux tarballs); 3 signatures (the checksums
# and both Linux tarballs: the copies reuse the bundles and sign nothing).
run_case all-present 0 8 3 "${ALL[@]}"
# Every package is required (Windows is built on every tag; both Linux arches):
# a missing one stops the step before anything is signed.
run_case arm64-missing nonzero 1 0 "$LINUX" "$DMG" "$MSI" "$ZIP"
run_case amd64-missing nonzero 0 0 "$LINUX_ARM" "$DMG" "$MSI" "$ZIP"
run_case windows-missing nonzero 3 0 "$LINUX" "$LINUX_ARM" "$DMG"
# A failed upload fails the step, before signing.
FAIL_UPLOAD=Coral.dmg run_case upload-fails nonzero 3 0 "${ALL[@]}"

# #3855: the release is created only when gh release view says it doesn't exist; a re-run keeps it.
# A failed create fails the step at once (it used to be "|| true", and the upload failed later).
creates() { grep -c '^gh release create' "$WORK/case-$1/gh.log" || true; }
[[ "$(creates all-present)" == 0 ]] || { echo "FAIL all-present: an existing release was created again" >&2; failures=$((failures + 1)); }
NO_RELEASE=1 run_case new-release 0 8 3 "${ALL[@]}"
# Every release is created as a pre-release that is not "latest" (#5551: the operator approves it later), whatever PRERELEASE says.
[[ "$(creates new-release)" == 1 ]] && grep -q -- '^gh release create .*--prerelease --latest=false' "$WORK/case-new-release/gh.log" || { echo "FAIL new-release: want one create, as a pre-release that is not latest" >&2; failures=$((failures + 1)); }
NO_RELEASE=1 PRERELEASE=true run_case new-prerelease 0 8 3 "${ALL[@]}"
grep -q -- '^gh release create .*--prerelease --latest=false' "$WORK/case-new-prerelease/gh.log" || { echo "FAIL new-prerelease: created without --prerelease --latest=false" >&2; failures=$((failures + 1)); }
NO_RELEASE=1 FAIL_CREATE=1 run_case create-fails nonzero 0 0 "${ALL[@]}"

# The Coral For Dropbox tier ships no MSI (operator decision, 2026-10-21): with no MSI in the artifacts the step still succeeds
# (4 packages + 1 stable-name call + SHA256SUMS.txt + 1 bundle call = 7 uploads, 3 signatures), and its checksums name no MSI. The
# same tier with an MSI present would still not publish it.
BUILD_TAGS=fts5,dropboxers NO_RELEASE=1 PRERELEASE=true run_case dropbox-no-msi 0 7 3 "$LINUX" "$LINUX_ARM" "$DMG" "$ZIP"
grep -q 'msi' "$WORK/case-dropbox-no-msi/SHA256SUMS.txt" && { echo "FAIL dropbox-no-msi: SHA256SUMS.txt names an MSI" >&2; failures=$((failures + 1)); }
grep -q 'Coral-x64-portable.zip' "$WORK/case-dropbox-no-msi/SHA256SUMS.txt" || { echo "FAIL dropbox-no-msi: SHA256SUMS.txt lacks the portable zip" >&2; failures=$((failures + 1)); }
BUILD_TAGS=fts5,dropboxers PRERELEASE=true run_case dropbox-msi-present 0 7 3 "${ALL[@]}"
grep -q 'msi' "$WORK/case-dropbox-msi-present/gh.log" && { echo "FAIL dropbox-msi-present: an MSI was uploaded" >&2; failures=$((failures + 1)); }

if [[ "$failures" -ne 0 ]]; then
  echo "$failures case(s) failed" >&2
  exit 1
fi
echo "release upload loop regression: all cases passed"
