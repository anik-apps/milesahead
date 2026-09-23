#!/usr/bin/env bash
# Converts a card infographic PNG into the JPEGs the card page serves:
#
#   static/img/infographic-<slug>.jpg         full image, opened in the viewer
#   static/img/infographic-<slug>-teaser.jpg  top 1024x640 region at 256x160
#
# The teaser is the thumbnail in the compact "Infographic / Expand" bar under the
# card art, shown at 84 CSS px wide, so 256 stays sharp on 3x screens. The full
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

for tool in ffmpeg ffprobe; do
  if ! command -v "$tool" >/dev/null; then
    echo "$tool is required" >&2
    exit 1
  fi
done

# The teaser crop and the viewer both assume the 1024x1536 portrait layout.
dims=$(ffprobe -v error -select_streams v:0 -show_entries stream=width,height -of csv=s=x:p=0 "$src")
if [ "$dims" != "1024x1536" ]; then
  echo "expected a 1024x1536 image, got $dims" >&2
  exit 1
fi

out=$(cd "$(dirname "$0")/.." && pwd)/static/img

# JPEG has no alpha channel. Composite onto white first, or ffmpeg drops the
# alpha and transparent regions come out black. Opaque sources are unchanged.
encode() {
  local filter=$1 quality=$2 dest=$3
  ffmpeg -hide_banner -loglevel error -y \
    -f lavfi -i "color=c=white:s=1024x1536" -i "$src" \
    -filter_complex "[0:v][1:v]overlay=shortest=1:format=rgb${filter}" \
    -frames:v 1 -map_metadata -1 -q:v "$quality" "$dest"
}

encode "" 4 "$out/infographic-$slug.jpg"
encode ",crop=1024:640:0:0,scale=256:160:flags=lanczos" 5 "$out/infographic-$slug-teaser.jpg"

ls -l "$out/infographic-$slug.jpg" "$out/infographic-$slug-teaser.jpg"
