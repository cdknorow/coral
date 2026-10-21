#!/usr/bin/env bash
# tools/devops/release-cadence.sh against a stand-in gh and a fixed clock (release channels, #5559): no network.
set -uo pipefail
export CORAL_TELEMETRY_DISABLED=1 # nothing here runs Coral, but the telemetry census wants it on every script that names the repository
here="$(cd "$(dirname "$0")" && pwd)"; SCRIPT="$here/../../tools/devops/release-cadence.sh"
T="$(mktemp -d /tmp/rcd.XXXX)"; [ -d "$T" ] || exit 1
trap 'rm -rf "$T"' EXIT
mkdir -p "$T/bin"
cat > "$T/bin/gh" <<'EOF'
#!/usr/bin/env bash
cat "$FIX"
EOF
chmod +x "$T/bin/gh"
export PATH="$T/bin:$PATH" FIX="$T/rel.json" GITHUB_REPOSITORY=subgentic/coral NOW=2026-10-30T00:00:00Z
fail=0
ok() { echo "PASS $1"; }
bad() { echo "FAIL $1" >&2; fail=1; }
cat > "$FIX" <<'EOF'
[
 {"tag_name":"v1.4.152","draft":false,"prerelease":true, "published_at":"2026-10-29T00:00:00Z","body":"notes"},
 {"tag_name":"v1.4.151","draft":false,"prerelease":true, "published_at":"2026-10-21T00:00:00Z","body":"notes"},
 {"tag_name":"v1.4.150","draft":false,"prerelease":false,"published_at":"2026-10-20T00:00:00Z","body":"notes\n\nApproved: 2026-10-22T10:30:00Z by someone"},
 {"tag_name":"v1.4.149","draft":false,"prerelease":true, "published_at":"2026-10-19T00:00:00Z","body":"notes"},
 {"tag_name":"v1.4.148","draft":false,"prerelease":false,"published_at":"2026-10-18T00:00:00Z","body":"notes\n\nRevoked: 2026-10-19T00:00:00Z by someone"},
 {"tag_name":"v1.4.9","draft":false,"prerelease":false,"published_at":"2026-09-01T00:00:00Z","body":"notes"},
 {"tag_name":"v1.4.153","draft":true,"prerelease":true, "published_at":"2026-10-29T12:00:00Z","body":"draft"},
 {"tag_name":"v1.4.150-rc1","draft":false,"prerelease":true, "published_at":"2026-10-17T00:00:00Z","body":"rc"}
]
EOF
out="$(bash "$SCRIPT" 2>&1)"; rc=$?
[ $rc = 0 ] && ok "the table prints" || bad "rc=$rc: $out"
row() { grep -E "^$1 " <<<"$out"; }
row v1.4.152 | grep -q 'WAITING.*1 days$' && ok "a newer pre-release waits, 1 day" || bad "152: $(row v1.4.152)"
row v1.4.151 | grep -q 'WAITING.*9 days (RED: over a week)' && ok "9 days is RED (a week or more)" || bad "151: $(row v1.4.151)"
row v1.4.150 | grep -q 'approved.*2026-10-22T10:30:00Z' && ok "approved shows the time of the display-only marker" || bad "150: $(row v1.4.150)"
row v1.4.149 | grep -q 'superseded' && ok "an older pre-release is superseded, not waiting" || bad "149: $(row v1.4.149)"
row v1.4.148 | grep -q 'revoked' && ok "a Revoked line shows revoked" || bad "148: $(row v1.4.148)"
row v1.4.9 | grep -q 'approved.*(no marker)' && ok "an approved release without a marker says so" || bad "9: $(row v1.4.9)"
grep -q 'v1.4.153' <<<"$out" && bad "a draft is listed" || ok "drafts are not listed"
grep -q 'rc1' <<<"$out" && bad "a suffix tag is listed" || ok "suffix tags are not listed"
[ "$(grep -n '^v1.4.152' <<<"$out" | cut -d: -f1)" -lt "$(grep -n '^v1.4.9 ' <<<"$out" | cut -d: -f1)" ] && ok "newest version first, numerically (1.4.152 before 1.4.9)" || bad "order"
[ "$(bash "$SCRIPT" 2 | grep -c '^v1')" = 2 ] && ok "COUNT limits the rows" || bad "count"
bash "$SCRIPT" abc >/dev/null 2>&1; [ $? = 2 ] && ok "a bad COUNT is a usage error" || bad "bad count accepted"
exit "$fail"
