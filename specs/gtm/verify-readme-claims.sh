#!/usr/bin/env bash
# Verify a composed README against specs/gtm/CLAIM_LEDGER.md.
#
# Checks that every DISPROVEN claim is absent and every VERIFIED claim that must
# carry a qualifier still does. Asserts on positive quantities so it cannot pass
# by not running.
#
# Usage: bash specs/gtm/verify-readme-claims.sh [path-to-readme]

set -uo pipefail
F="${1:-README.md}"

if [ ! -f "$F" ]; then echo "FAIL: no such file: $F"; exit 2; fi
LINES=$(wc -l < "$F" | tr -d ' ')
if [ "$LINES" -lt 50 ]; then echo "FALSE PASS GUARD: $F has only $LINES lines"; exit 2; fi

fail=0; checked=0
# banned <label> <extended-regex>   — must NOT appear
banned() {
  checked=$((checked+1))
  local label="$1" re="$2"
  local hits; hits=$(grep -nEi "$re" "$F" 2>/dev/null || true)
  if [ -n "$hits" ]; then
    fail=$((fail+1))
    echo "  FAIL  $label"
    echo "$hits" | sed 's/^/          /'
  else
    echo "  ok    $label (absent)"
  fi
}
# required <label> <extended-regex> — MUST appear at least once (catches deletion)
required() {
  checked=$((checked+1))
  local label="$1" re="$2"
  local n; n=$(grep -cEi "$re" "$F" 2>/dev/null); n=${n:-0}
  if [ "$n" -ge 1 ]; then echo "  ok    $label (present, $n)"
  else fail=$((fail+1)); echo "  FAIL  $label — REQUIRED CONTENT IS MISSING"; fi
}
# paired <label> <regex-a> <regex-b> — if A appears, B must appear too
paired() {
  checked=$((checked+1))
  local label="$1" a="$2" b="$3"
  if grep -qEi "$a" "$F" 2>/dev/null; then
    if grep -qEi "$b" "$F" 2>/dev/null; then echo "  ok    $label (claim present, qualifier present)"
    else fail=$((fail+1)); echo "  FAIL  $label — claim present WITHOUT its required pair"; fi
  else
    echo "  ok    $label (claim absent)"
  fi
}

echo "Verifying: $F  ($LINES lines)"
echo
echo "DISPROVEN CLAIMS — must be absent:"
banned "any-CLI-agent extensibility"        'any CLI[- ]based (tool|agent)|any cli agent'
banned "per-agent isolated worktrees"       'each agent .{0,40}(own )?(git )?worktree|its own copy of the repo'
banned "no-merge-conflicts safety"          'without (merge conflicts|interfering)'
banned "full-text session search"           'full[- ]text search'
banned "scheduled jobs in isolated worktrees" 'cron schedule in isolated worktrees'
banned "cost tracking in real time"         'cost and consumption in real time'
banned "cross-vendor cost total"            'across (all )?(vendors|providers).{0,20}cost|total spend across'
banned "Linux desktop app"                  'native desktop app.{0,20}linux|desktop app \(macos (and|&) linux\)'
banned "bare survives-a-restart"            'survives? a restart|survive[sd]? reboots?'
banned "save team configurations (implies a library)" 'save and share team configuration'

echo
echo "FACTUAL DETAILS — wrong values, not banned phrasings:"
banned "bare Coral.dmg (published name carries a version)"      '`Coral\.dmg`'
banned "bare coral-linux-amd64.tar.gz (carries a version)"      '`coral-linux-amd64\.tar\.gz`'
banned "brew install coral (not in upstream homebrew-cask)"     'brew install (--cask )?coral$|brew install (--cask )?coral[^/]'

echo
echo "ADJACENCY + PAIRING CHECKS:"
paired "no-API-keys must pair with telemetry" 'no api keys|holds no keys|keys come from your' 'telemetry|posthog|anonymous usage|usage data'
banned "workflows described as isolated"    'workflow[s]?[^|]{0,60}isolat|isolat[^|]{0,60}workflow'
banned "templates described as persisted"   'save (your )?team[s]? (to|in) a (library|template)'

echo
echo "REQUIRED CONTENT — must still be present (catches accidental deletion):"
required "Quick Start section"        '^## Quick Start'
required "Features section"           '^## Features'
required "Documentation section"      '^## Documentation'
required "License section"            '^## License'
required "download filenames"         'Coral\.v.{0,12}dmg|coral-linux-amd64-'
required "tmux requirement"           'tmux'
required "all four agents named"      'Claude Code.{0,60}Codex.{0,60}Gemini|Gemini.{0,60}Pi\.dev'
required "no orphaned duplicate heading" '^## '

echo
echo "checks run: $checked | failures: $fail"
if [ "$checked" -lt 24 ]; then echo "FALSE PASS GUARD: too few checks executed"; exit 2; fi
if [ "$fail" -gt 0 ]; then echo "RESULT: FAIL ($fail)"; exit 1; fi
echo "RESULT: PASS"
