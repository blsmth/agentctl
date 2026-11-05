#!/bin/bash
# Builds AgentctlFleet.app: a release SwiftPM build assembled into a minimal
# macOS app bundle, ad-hoc signed so it can launch outside a terminal.
#
# No App Sandbox: the app execs `tmux` and must reach its control socket at
# /private/tmp/tmux-<uid>/... — a sandboxed child inherits the sandbox and
# cannot.
set -euo pipefail

cd "$(dirname "${BASH_SOURCE[0]}")"

APP_NAME="AgentctlFleet"
BUILD_DIR=".build/release"
APP_BUNDLE="build/${APP_NAME}.app"

echo "==> swift build -c release"
swift build -c release

echo "==> assembling ${APP_BUNDLE}"
rm -rf "${APP_BUNDLE}"
mkdir -p "${APP_BUNDLE}/Contents/MacOS"
mkdir -p "${APP_BUNDLE}/Contents/Resources"

cp "${BUILD_DIR}/${APP_NAME}" "${APP_BUNDLE}/Contents/MacOS/${APP_NAME}"
cp "Resources/Info.plist" "${APP_BUNDLE}/Contents/Info.plist"

# SwiftTerm declares a resource bundle (it ships a Metal shader), which
# SwiftPM emits alongside the executable — it must travel with the app.
SWIFTTERM_BUNDLE="${BUILD_DIR}/SwiftTerm_SwiftTerm.bundle"
if [ -d "${SWIFTTERM_BUNDLE}" ]; then
  cp -R "${SWIFTTERM_BUNDLE}" "${APP_BUNDLE}/Contents/Resources/"
else
  echo "warning: ${SWIFTTERM_BUNDLE} not found; SwiftTerm resources may be missing" >&2
fi

echo "==> codesign (ad-hoc)"
codesign --force --deep -s - "${APP_BUNDLE}"

echo "==> built ${APP_BUNDLE}"
