#!/usr/bin/env bash
set -euxo pipefail

# Requires npx

# ---------------------------------------------------------------------------
# Headless Chromium for browser tests
# Google Chrome ships no linux/arm64 build and Ubuntu's `chromium` is a snap
# stub (unusable in a container), so install Playwright's Chromium: it provides
# a binary for both arches AND `--with-deps` pulls the shared libraries
# (libnss3, libatk-1.0-0, libgbm1, …) that headless Chromium needs. Installs
# into a fixed, image-baked path so CHROME_BIN is stable.
# ---------------------------------------------------------------------------
export PLAYWRIGHT_BROWSERS_PATH=/opt/ms-playwright
npx --yes playwright install --with-deps chromium
CHROME_BIN_PATH="$(find "$PLAYWRIGHT_BROWSERS_PATH" -type f -name chrome -path '*chrome-linux*' | head -1)"
if [ -z "$CHROME_BIN_PATH" ]; then
  echo "Could not locate the Playwright Chromium binary under ${PLAYWRIGHT_BROWSERS_PATH}" >&2
  exit 1
fi
ln -sf "$CHROME_BIN_PATH" /usr/local/bin/chromium
chmod -R a+rx /opt/ms-playwright
rm -rf /var/lib/apt/lists/*

echo "Chromium version: $(/usr/local/bin/chromium --version || true)"
