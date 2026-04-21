#!/bin/bash
set -uo pipefail

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"

if ! command -v docker >/dev/null 2>&1; then
  printf 'Docker is not installed or not on PATH.\n'
  printf 'Press return to close this window... '
  read -r _
  exit 1
fi

printf '\nStopping Towel...\n\n'
docker compose -f "$SCRIPT_DIR/install.yml" down
STATUS=$?

printf '\n'
if [[ $STATUS -eq 0 ]]; then
  printf 'Towel is stopped.\n'
else
  printf 'Towel stop failed with exit code %s.\n' "$STATUS"
fi

printf 'Press return to close this window... '
read -r _
exit $STATUS
