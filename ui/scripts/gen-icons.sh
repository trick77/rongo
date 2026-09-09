#!/usr/bin/env bash
# Regenerate every favicon / PWA raster from the three SVG sources in
# assets/icons/. Run it by hand after editing any of them, and commit what it
# writes:
#
#   ui/scripts/gen-icons.sh
#
# The outputs are COMMITTED rather than generated during the build, so neither
# `npm run build` nor CI needs an image toolchain. That is the whole reason this
# is not a package.json script.
#
# librsvg does the rasterising, not ImageMagick's own SVG support: IM's internal
# MSVG delegate renders stroked paths soft and distorted, and the mark inside
# every source here is strokes. ImageMagick is used only to read the results
# back and assert them.
#
# There is no favicon.ico, and no /favicon.svg either: the tab icon is
# /icon.svg, which matches the icon-*.png rasters beside it. backend/web answers
# both of those paths with a 404 and has a test that says so.
#
# Re-run whenever a source or a brand colour changes, then rebuild the UI so the
# new bytes land in backend/web/dist — that embedded copy is what the binary
# actually serves.
set -euo pipefail

DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)" # ui/
SRC="$DIR/assets/icons"
OUT="$DIR/public"
MASTER="$SRC/tile.svg" # the shape; ships as /icon.svg and renders the PWA rasters
DARK='#1f1f1e'         # app surface, OS-tile ground, and the glyph over the chip (index.css --color-bg)

# bc is in the list because the safe-circle check below compares a float. Without
# it the command substitution is empty, `(( ))` errors, the `if` reads false and
# the assertion passes silently — the one failure mode this script exists to
# prevent.
for tool in rsvg-convert magick bc; do
	if ! command -v "$tool" >/dev/null 2>&1; then
		echo "gen-icons: $tool not found — brew install librsvg imagemagick bc" >&2
		exit 1
	fi
done
mkdir -p "$OUT"
TMP="$(mktemp -d)"
trap 'rm -rf "$TMP"' EXIT

# --- icon.svg: served directly as the modern tab icon ------------------------
cp "$MASTER" "$OUT/icon.svg"

# --- from the master ---------------------------------------------------------
# -b none keeps the renderer from inventing a ground the master does not have.
# These stay TRANSPARENT — meaning the CANVAS is: the chip itself is opaque, and
# only the four corners outside its radius are clear. That is what lets a
# launcher render it as a chip rather than as a square of ours.
rsvg-convert -b none -w 192 -h 192 "$MASTER" -o "$OUT/icon-192.png"
rsvg-convert -b none -w 512 -h 512 "$MASTER" -o "$OUT/icon-512.png"

# --- from the tiled sources --------------------------------------------------
# These two carry a smaller chip and square corners because the OS crops them:
# iOS with its superellipse mask, Android with whatever the launcher picks. They
# also run edge to edge, because iOS flattens alpha onto black and Android fills
# it with the launcher's own colour — neither is ours to choose.
rsvg-convert -w 180 -h 180 "$SRC/tile-touch.svg" -o "$OUT/apple-touch-icon.png"
rsvg-convert -w 512 -h 512 "$SRC/tile-maskable.svg" -o "$OUT/icon-maskable-512.png"

# --- verify the grounds survived --------------------------------------------
# Rendering can succeed and still produce the wrong thing — most plausibly a
# source that lost its background rect, which yields a touch icon iOS flattens
# onto black. That fails silently in a viewer and only shows up on a real
# device, so assert every ground here instead.
#
# The split is the point, so both directions are checked. The master's rasters
# MUST keep a transparent canvas; the two OS tiles MUST stay opaque. Do not
# "fix" a failure here by relaxing the check.
fail=0
check_alpha() { # <file> <expected true|false>
	local got
	# ImageMagick 7 prints "True"/"False"; 6 printed "true"/"false". Fold the
	# case so this script is not pinned to one major version.
	got="$(magick identify -format '%[opaque]' "$1" | tr '[:upper:]' '[:lower:]')"
	if [[ "$got" != "$2" ]]; then
		echo "gen-icons: $(basename "$1") is opaque=$got, expected $2" >&2
		fail=1
	fi
}
check_alpha "$OUT/icon-192.png" false
check_alpha "$OUT/icon-512.png" false
check_alpha "$OUT/apple-touch-icon.png" true
check_alpha "$OUT/icon-maskable-512.png" true

# icon.svg ships as SVG, so there is no raster to read — render one here just to
# assert it. Four samples, one per way this file can quietly go wrong:
#
#   corner   must be clear, or the icon is a square and not a chip
#   stem     must be $DARK, or the glyph was lost into its own chip
#   tl, br   must both be opaque, warm, and DIFFERENT from each other
#
# The last pair is the gradient check. A chip whose fill was dropped, or whose
# url(#chip) resolved to nothing, renders flat or not at all — and either is
# invisible in a viewer that happens to sit on a dark page. Two points at
# opposite ends of the ramp, both clear of the glyph, prove the fill is there AND
# still ramping.
#
# All four coordinates are in the 512 render. The glyph's 0..100 box maps to
# 32..291 (translate(4 3) scale(0.56), times 8), so its vertical stem runs
# x=272..320 and the two ramp samples sit at x=400 and x=64,y=448 — both outside
# that box.
rsvg-convert -b none -w 512 -h 512 "$OUT/icon.svg" -o "$TMP/icon-favicon.png"
# -alpha on before every read. Without it an image that happens to be fully
# opaque carries no alpha channel, and then %[hex:...] returns six digits
# instead of eight and %[fx:...a] does not report 1 — the comparisons below
# would be measuring ImageMagick's channel bookkeeping rather than the icon.
fav_corner="$(magick "$TMP/icon-favicon.png" -alpha on -format '%[fx:p{0,0}.a]' info:)"
fav_stem="$(magick "$TMP/icon-favicon.png" -alpha on -format '%[hex:p{296,200}]' info: | tr '[:upper:]' '[:lower:]')"
fav_tl="$(magick "$TMP/icon-favicon.png" -alpha on -format '%[hex:p{400,64}]' info: | tr '[:upper:]' '[:lower:]')"
fav_br="$(magick "$TMP/icon-favicon.png" -alpha on -format '%[hex:p{64,448}]' info: | tr '[:upper:]' '[:lower:]')"
if [[ "$fav_corner" != "0" ]]; then
	echo "gen-icons: icon.svg's canvas corner has alpha $fav_corner, expected 0" >&2
	fail=1
fi
if [[ "$fav_stem" != "${DARK#\#}ff" ]]; then
	echo "gen-icons: icon.svg's stem is #$fav_stem, expected ${DARK}ff" >&2
	fail=1
fi
# Warm = opaque, and red > green > blue with red well up: the terracotta ramp
# and nothing else. Far less brittle than pinning the exact stop, which would
# have to be recomputed for every coverage or corner-radius change.
check_warm() { # <label> <x> <y>
	local ok
	ok="$(magick "$TMP/icon-favicon.png" -alpha on \
		-format "%[fx:p{$2,$3}.a==1 && p{$2,$3}.r>0.6 && p{$2,$3}.r>p{$2,$3}.g && p{$2,$3}.g>p{$2,$3}.b]" info:)"
	if [[ "$ok" != "1" ]]; then
		echo "gen-icons: icon.svg's chip at $1 is not the accent ramp" >&2
		fail=1
	fi
}
check_warm "top-right" 400 64
check_warm "bottom-left" 64 448
if [[ "$fav_tl" == "$fav_br" ]]; then
	echo "gen-icons: icon.svg's chip is flat #$fav_tl — the gradient did not resolve" >&2
	fail=1
fi

# The maskable icon has one more thing to prove: Android crops it to the
# launcher's own shape, and only the middle 80% — a circle of radius 204.8 at
# 512px — is guaranteed to survive. Assert the chip clears that circle rather
# than trusting the transform in tile-maskable.svg to still match its comment.
#
# Painting the safe circle over in the ground colour leaves nothing but ground IF
# the chip is fully inside it, so the whole image collapses to one flat colour
# and its standard deviation is 0. Anything left outside shows up as spread. The
# geometry caps coverage at 0.6488 (see tile-maskable.svg); its own 0.486 is well
# under that, so a non-zero reading here means a transform was edited without
# recomputing it.
CIRCLE_STDDEV="$(magick "$OUT/icon-maskable-512.png" -alpha off \
	-fill "$DARK" -draw 'circle 256,256 256,51.2' \
	-format '%[fx:standard_deviation]' info:)"
if (($(echo "$CIRCLE_STDDEV > 0.0005" | bc -l))); then
	echo "gen-icons: icon-maskable-512.png has the chip outside the 80% safe circle (stddev $CIRCLE_STDDEV)" >&2
	fail=1
fi

[[ "$fail" == 0 ]] || exit 1
echo "gen-icons: wrote icon.svg apple-touch-icon.png icon-192.png icon-512.png icon-maskable-512.png -> $OUT"
