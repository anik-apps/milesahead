#!/usr/bin/env bash
# Renders the raster brand images from their HTML sources in scripts/brand/.
# Email clients (Outlook, Gmail) and social previews do not show SVG, so these
# are PNG copies of the SVG artwork in static/img/brand/.
#
#   scripts/brand-images.sh
#
# Needs Google Chrome. The web header and footer use the SVGs directly; re-run
# this after changing mark.svg, band-light.svg or plane.svg.
set -euo pipefail
cd "$(dirname "$0")/.."
CHROME="${CHROME:-/Applications/Google Chrome.app/Contents/MacOS/Google Chrome}"
OUT=static/img/brand
render() { # <source.html> <out.png> <width> <height> <scale>
  # Chrome writes the screenshot but does not always exit afterwards, so wait for
  # the file and then stop it. A fresh profile per render keeps runs independent.
  local profile pid i
  profile="$(mktemp -d)"
  rm -f "$2"
  "$CHROME" --headless=new --disable-gpu --hide-scrollbars --allow-file-access-from-files \
    --user-data-dir="$profile" --force-device-scale-factor="$5" --window-size="$3,$4" \
    --screenshot="$2" "file://$PWD/scripts/brand/$1" >/dev/null 2>&1 &
  pid=$!
  for i in $(seq 1 60); do
    [ -s "$2" ] && break
    sleep 0.5
  done
  sleep 1
  kill "$pid" 2>/dev/null || true
  wait "$pid" 2>/dev/null || true
  rm -rf "$profile"
  [ -s "$2" ] || { echo "failed to render $2" >&2; exit 1; }
  echo "$2 ($3x$4 @${5}x)"
}

render email-lockup.html "$OUT/email-lockup.png" 300 64 2
render email-band.html   "$OUT/email-band.png"   640 120 2
render og-default.html   "$OUT/og-default.png"   1200 630 1
# Headless Chrome has a minimum window width, so small icons are rendered large
# and scaled down.
ICON="$(mktemp -d)/icon.png"
render touch-icon.html "$ICON" 540 540 1
sips -z 180 180 "$ICON" --out "$OUT/apple-touch-icon.png" >/dev/null
sips -z 32 32 "$ICON" --out "$OUT/favicon-32.png" >/dev/null
rm -rf "$(dirname "$ICON")"
echo "$OUT/apple-touch-icon.png, $OUT/favicon-32.png (scaled from 540px)"

# Optional: shrink with pngquant if installed (lossy palette, visually identical here).
if command -v pngquant >/dev/null; then
  pngquant --force --skip-if-larger --quality 80-98 --ext .png "$OUT"/*.png || true
fi
