#!/usr/bin/env bash
# update_macos_local.sh: the macOS update path run for real on this Mac, with a person watching (#5581). NOT part of any release or CI: it is the operator's hands-on test.
#
# An OLDER test Coral (version 1.4.100, an app of its own in /tmp/coral-updtest, its own HOME and data folder, port 8474, --cloud-url off, started by a script,
# every PID recorded) is updated in place to the real, published, Approved v1.4.144 by the REAL stage and apply code of the updater (tools: the test program
# internal/updater/testrig/cmd/mac-update-test): the real Sigstore verification of the release's SHA256SUMS.txt, the real disk image, the pinned Developer ID team, notarization and
# version. The ONLY test-only trust is the promote manifest (no promote workflow exists yet): it is signed with a key that lives in memory in the test program, and the shipped
# coral-update and the release builds know nothing of it.
#
# NEVER touched: /Applications/Coral.app, the Coral on :8420, ~/.coral. Only recorded PIDs are killed; cleanup removes /tmp/coral-updtest by its literal path.
#
#   update_macos_local.sh prepare            build the old test app, fetch the real release's files from its release RUN (no download counted)
#   update_macos_local.sh start-old          start the old test Coral on 127.0.0.1:8474
#   update_macos_local.sh login-link         print a fresh sign-in link for the test dashboard (run it in YOUR terminal, not an agent's)
#   update_macos_local.sh versions           the version of the app on disk and of what is running
#   update_macos_local.sh shipped-updater    what the SHIPPED coral-update says: status, check, apply (inert: nothing is promoted, apply is not enabled)
#   update_macos_local.sh update SCENARIO    good | tamper-package | tamper-staged | wrong-identity | unhealthy
#   update_macos_local.sh evidence           after an update: the updater's own history, and the new app's signature, notarization and version, checked here again by hand
#   update_macos_local.sh cleanup            stop the recorded processes, remove the test folder
set -uo pipefail
export CORAL_TELEMETRY_DISABLED=1 # nothing here may send a usage event
here="$(cd "$(dirname "$0")" && pwd)"; ROOT="$(cd "$here/../.." && pwd)"
T=/tmp/coral-updtest
PORT=8474
OLDV=1.4.100   # a plain X.Y.Z (the updater refuses any other form of version), older than every real release in the test
TAG=${CORAL_UPDTEST_TAG:-v1.4.144}
RELEASE_RUN=${CORAL_UPDTEST_RUN:-39565430882}   # the release run of v1.4.144 (its artifacts hold the files; no download from the release)
OTHER_RUN=${CORAL_UPDTEST_OTHER_RUN:-39534127578} # v1.4.141's release run: another identity (cdknorow/coral), for the wrong-identity check
APP="$T/app/Coral.app"; MACOS="$APP/Contents/MacOS"
ENVI=(env -i PATH=/usr/bin:/bin:/usr/sbin:/sbin HOME="$T/home" CORAL_DATA_DIR="$T/data" CORAL_CLOUD_URL=off CORAL_TELEMETRY_DISABLED=1)
die() { echo "update_macos_local: $*" >&2; exit 1; }
say() { echo; echo "== $*"; }
need_t() { [ -f "$T/.coral-updtest" ] || die "run prepare first"; }

pid_alive() { [ -n "${1:-}" ] && kill -0 "$1" 2>/dev/null; }
listening() { lsof -nP -iTCP:"$PORT" -sTCP:LISTEN >/dev/null 2>&1; }

write_start_script() {
  cat > "$T/start.sh" <<EOF
#!/bin/bash
# start.sh INSTALL: start that install's Coral server with the isolated environment (never 'open', which would start it as the user's Coral); its pid is recorded
install="\$1"
cd "$T" || exit 1
/usr/bin/env -i PATH=/usr/bin:/bin:/usr/sbin:/sbin HOME="$T/home" CORAL_DATA_DIR="$T/data" CORAL_CLOUD_URL=off CORAL_TELEMETRY_DISABLED=1 \\
  "\$install/Contents/MacOS/coral" --home "$T/data" --no-browser --cloud-url off --host 127.0.0.1 --port $PORT --backend pty >> "$T/server.log" 2>&1 &
echo \$! > "$T/pids/server.pid"
disown
EOF
  chmod +x "$T/start.sh"
}

cmd_prepare() {
  [ -e "$T" ] && die "$T exists: run cleanup first"
  mkdir -p "$T/pids" "$T/home/.coral" "$T/home/.claude/projects" "$T/data" "$T/rel" "$T/other" "$MACOS" "$APP/Contents/Resources" || die "mkdir"
  : > "$T/.coral-updtest"; echo accepted > "$T/home/.coral/.eula-accepted"
  say "the real release's files, from its release run $RELEASE_RUN (artifacts, not a download of the release)"
  gh run download "$RELEASE_RUN" -R subgentic/coral -n release-sums -D "$T/rel" >/dev/null 2>&1 || die "gh run download release-sums"
  gh run download "$RELEASE_RUN" -R subgentic/coral -n coral-macos-dmg -D "$T/rel" >/dev/null 2>&1 || die "gh run download coral-macos-dmg"
  local v=${TAG#v}
  [ -f "$T/rel/Coral.v$v.dmg" ] && [ -f "$T/rel/SHA256SUMS.txt" ] && [ -f "$T/rel/SHA256SUMS.txt.sigstore.json" ] || die "the release run's files are not all there"
  [ "$(shasum -a 256 "$T/rel/Coral.v$v.dmg" | cut -d' ' -f1)" = "$(awk -v f="Coral.v$v.dmg" '$2==f{print $1}' "$T/rel/SHA256SUMS.txt")" ] || die "the disk image is not the one SHA256SUMS.txt lists"
  echo "ok: Coral.v$v.dmg equals its line of SHA256SUMS.txt"
  gh run download "$OTHER_RUN" -R subgentic/coral -n release-sums -D "$T/other" >/dev/null 2>&1 || die "gh run download of the other identity's sums"
  say "building the OLDER test app $OLDV (native arm64, prod tier, a version string only: no analytics key)"
  local LD="-s -w -X github.com/subgentic/coral/internal/config.Version=$OLDV"
  ( cd "$ROOT/coral-go" && go build -tags fts5 -ldflags "$LD" -o "$MACOS/coral" ./cmd/coral \
    && go build -tags fts5 -ldflags "$LD" -o "$MACOS/coral-update" ./cmd/coral-update \
    && go build -o "$MACOS/mac-update-test" ./internal/updater/testrig/cmd/mac-update-test ) || die "build"
  cat > "$APP/Contents/Info.plist" <<EOF
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0"><dict>
<key>CFBundleIdentifier</key><string>com.coral.updatetest</string>
<key>CFBundleName</key><string>Coral</string>
<key>CFBundleExecutable</key><string>coral</string>
<key>CFBundleShortVersionString</key><string>$OLDV</string>
<key>CFBundleVersion</key><string>$OLDV</string>
<key>CFBundlePackageType</key><string>APPL</string>
</dict></plist>
EOF
  /usr/bin/codesign --force --deep -s - "$APP" >/dev/null 2>&1 || die "ad hoc codesign"
  write_start_script
  echo "ok: $APP ($OLDV, signed ad hoc: it is the OLD app, nobody verifies it)"
  git -C "$ROOT" rev-parse "$TAG^{commit}" > "$T/source-sha" 2>/dev/null || die "no local tag $TAG (git fetch --tags)"
  echo "ok: $TAG was built from $(cat "$T/source-sha")"
}

cmd_start_old() {
  need_t
  pid_alive "$(cat "$T/pids/server.pid" 2>/dev/null)" && die "the test Coral is already running (pid $(cat "$T/pids/server.pid"))"
  listening && die "port $PORT is in use by something else: not touching it"
  "$T/start.sh" "$APP"
  local i; for i in $(seq 1 60); do listening && break; sleep 0.5; done
  listening || { tail -5 "$T/server.log"; die "the test Coral did not start"; }
  echo "the OLD test Coral is running: pid $(cat "$T/pids/server.pid"), http://127.0.0.1:$PORT (version $("$MACOS/coral" --version 2>&1 | head -1))"
}

cmd_login_link() {
  need_t
  "${ENVI[@]}" "$MACOS/coral" open --print --url "http://127.0.0.1:$PORT" --home "$T/data" 2>&1 | grep -E 'http://127.0.0.1:'
}

cmd_versions() {
  need_t
  echo "app on disk:      $(/usr/libexec/PlistBuddy -c 'Print :CFBundleShortVersionString' "$APP/Contents/Info.plist" 2>&1)   ($APP)"
  echo "its coral says:   $("$MACOS/coral" --version 2>&1 | head -1)"
  local p; p="$(cat "$T/pids/server.pid" 2>/dev/null)"
  if pid_alive "$p"; then echo "running:          pid $p  $(ps -o command= -p "$p" | cut -c1-90)"; else echo "running:          nothing (the test Coral is not running)"; fi
  echo "folder:           $(ls -A "$T/app" | tr '\n' ' ')"
}

cmd_shipped_updater() {
  need_t
  say "the SHIPPED coral-update (compiled-in pins, no test trust) inside the test app"
  echo "\$ coral-update status --json";  "${ENVI[@]}" "$MACOS/coral-update" status --json 2>&1 | head -3
  echo "\$ coral-update check   (one request to github.com: nothing is promoted yet, so it must say not_promoted)"; "${ENVI[@]}" "$MACOS/coral-update" check 2>&1 | head -3
  echo "\$ coral-update apply   (the apply switch is off in every release: not_enabled)";                          "${ENVI[@]}" "$MACOS/coral-update" apply 2>&1 | head -3
}

cmd_update() {
  need_t
  local sc=${1:-good} old extra=()
  case "$sc" in good|tamper-package|tamper-staged|wrong-identity|unhealthy) ;; *) die "unknown scenario $sc" ;; esac
  old="$(cat "$T/pids/server.pid" 2>/dev/null)"
  pid_alive "$old" || die "start-old first (the update replaces a RUNNING Coral)"
  [ "$sc" = wrong-identity ] && extra+=(--other-sums-dir "$T/other")
  [ "$sc" = good ] && extra+=(--hold 8s)
  [ "$sc" = unhealthy ] && extra+=(--health-for 12s)
  rm -rf "$T/data/updates"   # each scenario starts from a clean updater state (a refused check keeps a back-off for that version, which is its own behaviour)
  say "scenario $sc: the update program runs from INSIDE the old app, waiting for pid $old (the server it replaces) to end"
  : > "$T/update.log"
  ( "${ENVI[@]}" "$MACOS/mac-update-test" --data-dir "$T/data" --installed "$OLDV" --release-dir "$T/rel" --tag "$TAG" --source-sha "$(cat "$T/source-sha")" \
      --parent "$old" --start-script "$T/start.sh" --scenario "$sc" --wait 90s ${extra[@]+"${extra[@]}"} > "$T/update.log" 2>&1; echo "exit=$?" >> "$T/update.log" ) &
  local up=$! i
  for i in $(seq 1 240); do grep -qE '"step":"(ready)"|exit=' "$T/update.log" && break; sleep 1; done
  cat "$T/update.log" | cut -c1-230
  if grep -q '"step":"ready"' "$T/update.log"; then
    say "the staged, verified update is ready: the old server ends now (as it ends itself in the real flow), pid $old"
    kill "$old" 2>/dev/null
    for i in $(seq 1 15); do pid_alive "$old" || break; sleep 1; done
    wait "$up" 2>/dev/null
  else
    wait "$up" 2>/dev/null
  fi
  say "what the update program printed"
  cut -c1-260 "$T/update.log"
  echo; cmd_versions
  if [ "$sc" = good ]; then
    local i2; for i2 in $(seq 1 30); do listening && break; sleep 1; done
    listening && echo "the NEW Coral is listening on 127.0.0.1:$PORT" || echo "nothing is listening on $PORT"
  fi
  case "$sc" in
    tamper-staged|unhealthy) echo; echo "(after a refused or rolled-back update the old app is still the install: start-old again if it is not running)" ;;
  esac
}

cmd_evidence() {
  need_t
  say "the updater's own record (updates/state.json): what it accepted, when, from which version, started by whom"
  python3 -I -c 'import json,sys; d=json.load(open(sys.argv[1])); print(json.dumps({k:d.get(k) for k in ("version","sequence","last_check","history")}, indent=2))' "$T/data/updates/state.json" 2>&1 | head -24
  say "the app now on disk, checked by hand with the system's own tools"
  /usr/bin/codesign -dv --verbose=2 "$APP" 2>&1 | grep -E '^(Identifier|TeamIdentifier|Authority|Timestamp)=' | sed 's/Authority=Developer ID Application: .* (\(.*\))/Authority=Developer ID Application: <name> (\1)/'
  /usr/bin/codesign --verify --deep --strict "$APP" 2>&1 && echo "codesign --verify --deep --strict: ok"
  /usr/sbin/spctl --assess --type execute -vv "$APP" 2>&1 | grep -E 'accepted|source=' | head -2
  /usr/bin/xcrun stapler validate "$APP" 2>&1 | tail -1
  echo "version: $(/usr/libexec/PlistBuddy -c 'Print :CFBundleShortVersionString' "$APP/Contents/Info.plist")"
  local v=${TAG#v}
  echo "the package's sha256 as SHA256SUMS.txt lists it:  $(awk -v f="Coral.v$v.dmg" '$2==f{print $1}' "$T/rel/SHA256SUMS.txt")"
  echo "the disk image this test used:                   $(shasum -a 256 "$T/rel/Coral.v$v.dmg" | cut -d' ' -f1)"
}

cmd_cleanup() {
  [ -f "$T/.coral-updtest" ] || { echo "nothing to clean ($T has no marker)"; return 0; }
  local p; p="$(cat "$T/pids/server.pid" 2>/dev/null)"
  if pid_alive "$p"; then kill "$p" 2>/dev/null; sleep 1; pid_alive "$p" && kill -9 "$p" 2>/dev/null; echo "stopped the test Coral (pid $p)"; fi
  hdiutil info 2>/dev/null | grep -q "$T" && echo "WARNING: a disk image of the test is still mounted: detach it by hand (hdiutil info)"
  rm -rf /tmp/coral-updtest && echo "removed /tmp/coral-updtest"
}

case "${1:-}" in
  prepare) cmd_prepare ;; start-old) cmd_start_old ;; login-link) cmd_login_link ;; versions) cmd_versions ;;
  shipped-updater) cmd_shipped_updater ;; evidence) cmd_evidence ;; update) cmd_update "${2:-good}" ;; cleanup) cmd_cleanup ;;
  *) sed -n '2,24p' "$0"; exit 2 ;;
esac
