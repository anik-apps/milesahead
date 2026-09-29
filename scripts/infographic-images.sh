#!/usr/bin/env bash
# Converts a card infographic PNG into the JPEGs the card page serves:
#
#   static/img/infographic-<slug>.jpg         full image at 1024 px wide, opened in the viewer
#   static/img/infographic-<slug>-teaser.jpg  top 1024x640 region at 256x160
#
# The source may be any portrait size: it is scaled to 1024 wide with its aspect
# ratio kept, so a 1024x1536 (2:3) source stays 1024x1536 and a 1080x1917 one
# becomes 1024x1818. Nothing is cropped from the full image or stretched. The
# page reads the height from the JPEG at startup (infographicHeights), so no
# size needs to be declared anywhere.
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

width=1024
dims=$(ffprobe -v error -select_streams v:0 -show_entries stream=width,height -of csv=s=x:p=0 "$src")
src_w=${dims%x*}
src_h=${dims#*x}
if ! [[ $src_w =~ ^[0-9]+$ && $src_h =~ ^[0-9]+$ ]]; then
  echo "could not read the size of $src (got '$dims')" >&2
  exit 1
fi
# Height at 1024 wide, rounded to the nearest pixel (1080x1917 -> 1818).
height=$(( (src_h * width * 2 + src_w) / (src_w * 2) ))
# The teaser is the top 1024x640 and the viewer is laid out for a portrait
# image, so reject anything shorter than the original 2:3 layout.
if [ "$height" -lt 1536 ]; then
  echo "expected a portrait image at least 2:3 (1024x1536 or taller), got $dims" >&2
  exit 1
fi
if [ "$src_w" -lt "$width" ]; then
  echo "warning: $dims is narrower than $width px and will be upscaled" >&2
fi

out=$(cd "$(dirname "$0")/.." && pwd)/static/img

# JPEG has no alpha channel. Composite onto white first, or ffmpeg drops the
# alpha and transparent regions come out black. Opaque sources are unchanged.
# The source is scaled to the white canvas's exact size first, so the overlay
# covers it with nothing left over and nothing cut off.
encode() {
  local filter=$1 quality=$2 dest=$3
  ffmpeg -hide_banner -loglevel error -y \
    -f lavfi -i "color=c=white:s=${width}x${height}" -i "$src" \
    -filter_complex "[1:v]scale=${width}:${height}:flags=lanczos[fg];[0:v][fg]overlay=shortest=1:format=rgb${filter}" \
    -frames:v 1 -map_metadata -1 -q:v "$quality" "$dest"
}

encode "" 4 "$out/infographic-$slug.jpg"
encode ",crop=1024:640:0:0,scale=256:160:flags=lanczos" 5 "$out/infographic-$slug-teaser.jpg"

for f in "$out/infographic-$slug.jpg" "$out/infographic-$slug-teaser.jpg"; do
  echo "$(ffprobe -v error -select_streams v:0 -show_entries stream=width,height -of csv=s=x:p=0 "$f")  $(wc -c <"$f" | tr -d ' ') bytes  $f"
done
