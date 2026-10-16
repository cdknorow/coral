#!/usr/bin/env bash
# install_cli_tools_test.sh: installers/install-cli.sh links exactly the programs the app ships
# (tests/release/commands.txt). It runs from inside Coral.app, so it keeps its own list; this keeps
# the two equal (it missed coral-hook-session-start until #3855's follow-up).
set -euo pipefail
root="$(cd "$(dirname "$0")/../.." && pwd)"
line=$(grep -E '^TOOLS=\(' "$root/installers/install-cli.sh")
[ "$(grep -cE '^TOOLS=\(' "$root/installers/install-cli.sh")" = 1 ] || { echo "FAIL: want exactly one TOOLS=( line in install-cli.sh"; exit 1; }
tools=$(sed -E 's/^TOOLS=\((.*)\)$/\1/' <<<"$line" | tr ' ' '\n' | grep . | sort)
want=$(grep -vE '^[[:space:]]*(#|$)' "$root/tests/release/commands.txt" | sort)
if [ "$tools" != "$want" ]; then
  echo "FAIL: install-cli.sh TOOLS and tests/release/commands.txt differ:"; diff <(echo "$want") <(echo "$tools") | sed 's/^/  /'; exit 1
fi
echo "PASS install-cli.sh links all $(wc -l <<<"$want" | tr -d ' ') shipped programs"
