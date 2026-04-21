#!/bin/bash
set -uo pipefail

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"

printf '\nStarting Towel...\n\n'
/bin/bash "$SCRIPT_DIR/install_mac.sh"
STATUS=$?

printf '\n'
if [[ $STATUS -eq 0 ]]; then
  printf 'Towel finished startup. This window can be closed.\n'
else
  printf 'Towel startup failed with exit code %s.\n' "$STATUS"
fi

printf 'Press return to close this window... '
read -r _
exit $STATUS
