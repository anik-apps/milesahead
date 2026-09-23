#!/usr/bin/env bash
# Converts a card infographic PNG into the two JPEGs the card page serves:
#
#   static/img/infographic-<slug>.jpg         full image, opened in the viewer
#   static/img/infographic-<slug>-teaser.jpg  top 1024x640 region at 480x300,
#                                             shown in the card-art slot
#
# The teaser is 480 wide so it stays sharp at the sidebar's 220 CSS px and the
# mobile header's 320 CSS px on high-density screens, at about 45 KB. The full
# image loads only when the viewer opens.
#
# Usage: scripts/infographic-images.sh <slug> <source.png>
# Then add the slug to cardInfographics in internal/web/cardinfographics.go.
set -euo pipefail

if [ $# -ne 2 ]; then
  echo "usage: $0 <slug> <source.png>" >&2
  exit 2
fi
slug=$1
src=$2

if ! command -v ffmpeg >/dev/null; then
  echo "ffmpeg is required" >&2
  exit 1
fi

# The teaser crop and the viewer both assume the 1024x1536 portrait layout.
dims=$(ffprobe -v error -select_streams v:0 -show_entries stream=width,height -of csv=s=x:p=0 "$src")
if [ "$dims" != "1024x1536" ]; then
  echo "expected a 1024x1536 image, got $dims" >&2
  exit 1
fi

out=$(cd "$(dirname "$0")/.." && pwd)/static/img
ffmpeg -hide_banner -loglevel error -y -i "$src" -map_metadata -1 -q:v 4 \
  "$out/infographic-$slug.jpg"
ffmpeg -hide_banner -loglevel error -y -i "$src" -map_metadata -1 \
  -vf "crop=1024:640:0:0,scale=480:300:flags=lanczos" -q:v 5 \
  "$out/infographic-$slug-teaser.jpg"

ls -l "$out/infographic-$slug.jpg" "$out/infographic-$slug-teaser.jpg"
