#!/usr/bin/env bash
# Regenerate the link-preview card, public/og.png, from assets/og/card.html.
# Run it by hand after editing that file, and commit what it writes:
#
#   ui/scripts/gen-og.sh
#
# The output is COMMITTED rather than generated during the build, for the same
# reason gen-icons.sh commits its rasters: neither `npm run build` nor CI then
# needs a browser or an image toolchain.
#
# Chrome does the rendering, not librsvg. The card is typeset in the two
# Anthropic variable faces that ui/src/fonts carries as woff2, and fontconfig -
# which is how librsvg resolves type - neither reads woff2 nor picks fonts up
# out of a repo directory. An SVG source would fall back to a system serif
# without erroring, and the card would already be cached in someone's chat
# before anyone noticed. Chrome loads @font-face over a relative path and is the
# same engine that renders the app.
#
# The card carries nothing that can go stale; see the comment at the top of
# card.html for why. Nothing here needs the backend, a database, or a running
# app.
set -euo pipefail

DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)" # ui/
SRC="$DIR/assets/og/card.html"
OUT="$DIR/public/og.png"
W=1200
H=630
GROUND='#1f1f1e' # index.css color-bg, the card's ground

CHROME="${CHROME:-/Applications/Google Chrome.app/Contents/MacOS/Google Chrome}"
if [[ ! -x "$CHROME" ]]; then
	echo "gen-og: Chrome not found at $CHROME — set CHROME=/path/to/chrome" >&2
	exit 1
fi
# bc is checked for the same reason gen-icons.sh checks it: the float
# comparison below is a command substitution, and without bc it comes back
# empty, the `if` reads false, and the assertion passes silently.
for tool in magick bc; do
	if ! command -v "$tool" >/dev/null 2>&1; then
		echo "gen-og: $tool not found — brew install imagemagick bc" >&2
		exit 1
	fi
done

TMP="$(mktemp -d)"
trap 'rm -rf "$TMP"' EXIT
SHOT="$TMP/og.png"

# --allow-file-access-from-files is what lets the file:// page fetch the woff2
# out of ../../src/fonts; without it Chrome blocks the font as a cross-origin
# read, renders in Times and exits 0. --virtual-time-budget makes it wait for
# the fonts to load and the paint to settle instead of shooting the first frame.
# --force-device-scale-factor pins the output to exactly WxH on a retina
# display, where the default would silently produce a 2x image.
"$CHROME" \
	--headless \
	--disable-gpu \
	--hide-scrollbars \
	--force-device-scale-factor=1 \
	--window-size="$W,$H" \
	--virtual-time-budget=4000 \
	--allow-file-access-from-files \
	--screenshot="$SHOT" \
	"file://$SRC" >/dev/null 2>&1 || true

if [[ ! -s "$SHOT" ]]; then
	echo "gen-og: Chrome wrote no screenshot" >&2
	exit 1
fi

# --- verify -----------------------------------------------------------------
# Chrome exits 0 on most of the ways this goes wrong — a blocked font, a blank
# paint, a doubled scale factor — so every property the card depends on is
# asserted here rather than trusted.
fail=0

geom="$(magick identify -format '%wx%h' "$SHOT")"
if [[ "$geom" != "${W}x${H}" ]]; then
	echo "gen-og: rendered ${geom}, expected ${W}x${H}" >&2
	fail=1
fi

# The ground, sampled at the bottom-LEFT: that is the one corner no aura reaches
# (they sit top-left and bottom-right). Catches a lost background rule, which
# would ship a white card into every dark chat client.
ground="$(magick "$SHOT" -alpha off -format "%[hex:p{8,$((H - 8))}]" info: | tr '[:upper:]' '[:lower:]')"
if [[ "$ground" != "${GROUND#\#}" ]]; then
	echo "gen-og: ground is #$ground, expected $GROUND" >&2
	fail=1
fi

# A blank canvas has a standard deviation of 0. This is the check that fires
# when the page fails to load at all and Chrome shoots an empty viewport.
SPREAD="$(magick "$SHOT" -alpha off -format '%[fx:standard_deviation]' info:)"
if (($(echo "$SPREAD < 0.02" | bc -l))); then
	echo "gen-og: image is nearly flat (stddev $SPREAD) — did the page load?" >&2
	fail=1
fi

# The mark-plus-wordmark row measured across, which is how a font fallback is
# caught. The brand serif sets it to 478px; with the woff2 unreachable Chrome
# falls back to a system serif and the same row measures 424px, so the band
# below rejects it. That was checked by breaking the @font-face URLs on purpose
# — do the same before widening this range, or it stops asserting anything.
#
# The band is narrow because the two renders are only 54px apart. Do not "round
# it out": the mark contributes a fixed 112px to both, so the gap the band has
# to resolve is smaller than the row it measures.
#
# Threshold, not -fuzz -trim. The ground is the aura gradient rather than a flat
# colour, so trim's corner-colour comparison walks out into the wash and reports
# most of the canvas as ink. Reducing to a black-and-white mask keeps the type
# and drops the gradient. 40% keeps the accent ramp the mark is stroked in
# (#c25f34 is about 45% grey) as well as the near-white wordmark.
#
# The crop window is inset 120px from each edge for the same reason
# ismimodown's is: at this threshold the aura's bright corner survives as a few
# stray pixels at the extreme left and -trim measures to those instead. Nothing
# legitimate goes near the edge; the composition is centred. The y band
# 170..300 sits on the row alone, clear of the eyebrow above and the lede below
# — keep the slack, because a band that clips the row measures a fragment and
# reports a fallback that is not there.
#
# This doubles as the square-crop guard. WhatsApp keeps only the middle 630px
# (see card.html), so a row wider than that is cut in half there and nowhere
# else — the one failure that never shows up in a 1.91:1 preview.
#
# If the band comes back all black — type gone, or the band moved off the row —
# -trim has nothing to trim. ImageMagick 7 treats that as a WARNING, not an
# error: it reports a width of 1 and still exits 0, so the range check below
# catches it. The `|| echo 0` is insurance against a version that exits
# non-zero instead, where `set -e` would otherwise kill the script. stderr is
# deliberately NOT silenced: on a real failure that warning names the cause.
MARK_W="$(magick "$SHOT" -crop "$((W - 240))x130+120+170" +repage -alpha off \
	-colorspace gray -threshold 40% -trim -format '%w' info: || echo 0)"
if ((MARK_W < 470 || MARK_W > 515)); then
	echo "gen-og: wordmark row measures ${MARK_W}px, expected 470-515 — font fallback, or type resized past the square crop" >&2
	fail=1
fi

[[ "$fail" == 0 ]] || exit 1

# oxipng/pngcrush are not assumed; Chrome's PNG is already reasonable and the
# file is served once per scrape, not once per visitor.
mkdir -p "$(dirname "$OUT")"
cp "$SHOT" "$OUT"
echo "gen-og: wrote og.png (${geom}, row ${MARK_W}px, $(wc -c <"$OUT" | tr -d ' ') bytes) -> $OUT"
