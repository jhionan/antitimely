#!/usr/bin/env bash
# rebuild.sh — rebuild antitimely and (if running) restart its launch agent.
#
# Resolves the project root from this script's location, so it works no
# matter where you invoke it from.

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PROJECT_ROOT="$(cd "$SCRIPT_DIR/.." && pwd)"
BIN="$PROJECT_ROOT/antitimely"

cd "$PROJECT_ROOT"

echo "Building → $BIN"
go build -o "$BIN" .

if "$BIN" status > /dev/null 2>&1; then
  echo "Daemon is running — restarting via launchctl…"
  "$BIN" uninstall-launch-agent
  "$BIN" install-launch-agent
else
  echo "Daemon not running — installing fresh…"
  "$BIN" install-launch-agent
fi

sleep 1
"$BIN" status | head -3
