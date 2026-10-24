#!/bin/sh
# Renders the invented school papers in testdata/papers/html into
# photo-like JPEGs (testdata/papers/NN-name.jpg). macOS only: needs
# Google Chrome for the render and sips for the JPEG step.
set -eu

CHROME="${CHROME:-/Applications/Google Chrome.app/Contents/MacOS/Google Chrome}"
cd "$(dirname "$0")/../testdata/papers"
tmp=$(mktemp -d)
trap 'rm -r "$tmp"' EXIT

for src in html/[0-9]*.html; do
	name=$(basename "$src" .html)
	"$CHROME" --headless=new --disable-gpu --hide-scrollbars \
		--force-device-scale-factor=1 --window-size=1200,1600 \
		--default-background-color=00000000 \
		--screenshot="$tmp/$name.png" "file://$PWD/$src" >/dev/null 2>&1
	sips -s format jpeg -s formatOptions 80 "$tmp/$name.png" --out "$name.jpg" >/dev/null
	echo "$name.jpg"
done
