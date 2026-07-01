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

# Sign with a stable identity so the macOS Accessibility grant survives rebuilds
# (Go's default ad-hoc signature changes hash every build, resetting the grant).
SIGN_ID="${SIGN_ID:-Developer ID Application: Jhionan Rian Lara dos Santos (5KNATBVY62)}"
if [ -n "$SIGN_ID" ]; then
  if codesign --force --sign "$SIGN_ID" --identifier com.rian.antitimely "$BIN" 2>/dev/null; then
    echo "Signed with stable identity — Accessibility grant persists across rebuilds."
  else
    echo "Warning: codesign with '$SIGN_ID' failed; kept ad-hoc signature (Accessibility grant will reset on rebuild)."
  fi
fi

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
