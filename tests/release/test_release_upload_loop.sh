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
if [[ "$1 $2" == "release upload" && -n "${FAIL_UPLOAD:-}" && "$4" == *"$FAIL_UPLOAD"* ]]; then
  echo "simulated upload failure for $4" >&2
  exit 1
fi
exit 0
GH
chmod +x "$WORK/bin/gh"

failures=0
# run_case NAME EXPECTED_EXIT EXPECTED_UPLOADS FILES... (FAIL_UPLOAD env optional)
run_case() {
  local name="$1" want_exit="$2" want_uploads="$3"; shift 3
  local dir="$WORK/case-$name"
  mkdir -p "$dir"
  ( cd "$dir"
    for f in "$@"; do mkdir -p "$(dirname "$f")"; : > "$f"; done )
  export GH_LOG="$dir/gh.log"; : > "$GH_LOG"
  local rc=0
  ( cd "$dir" && PATH="$WORK/bin:$PATH" GITHUB_REF_NAME=v0.0.0-test bash --noprofile --norc -eo pipefail "$WORK/step.sh" ) > "$dir/out.log" 2>&1 || rc=$?
  local uploads
  uploads="$(grep -c '^gh release upload' "$GH_LOG" || true)"
  local exit_ok=0
  if [[ "$want_exit" == "nonzero" ]]; then
    [[ "$rc" -ne 0 ]] && exit_ok=1
  else
    [[ "$rc" -eq "$want_exit" ]] && exit_ok=1
  fi
  if [[ "$exit_ok" -eq 1 && "$uploads" == "$want_uploads" ]]; then
    echo "PASS $name (exit=$rc uploads=$uploads)"
  else
    echo "FAIL $name: exit=$rc (want $want_exit) uploads=$uploads (want $want_uploads)" >&2
    sed 's/^/  | /' "$dir/out.log" >&2
    failures=$((failures + 1))
  fi
}

LINUX=artifacts/coral-linux-amd64/coral-linux-amd64-1.tar.gz
DMG=artifacts/coral-macos-dmg/Coral.dmg
MSI=artifacts/coral-windows-msi/Coral-1-x64.msi
ZIP=artifacts/coral-windows-portable/Coral-1-x64-portable.zip

# The v1.3.16 failure: Windows packages intentionally absent must succeed.
run_case windows-absent 0 2 "$LINUX" "$DMG"
run_case all-present 0 4 "$LINUX" "$DMG" "$MSI" "$ZIP"
run_case windows-partial 0 3 "$LINUX" "$DMG" "$MSI"
# Real upload failures must still fail the step, optional or required.
FAIL_UPLOAD=Coral.dmg run_case required-upload-fails nonzero 2 "$LINUX" "$DMG"
FAIL_UPLOAD=.msi run_case optional-upload-fails nonzero 3 "$LINUX" "$DMG" "$MSI"
# A missing required package is an error, not a skip.
run_case required-missing nonzero 1 "$LINUX"

if [[ "$failures" -ne 0 ]]; then
  echo "$failures case(s) failed" >&2
  exit 1
fi
echo "release upload loop regression: all cases passed"
