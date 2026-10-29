#!/bin/sh
# Downloads the third-party assets embedded in the md2pdf binary.
# Run from the repository root: ./scripts/fetch-assets.sh
set -eu

DEST="internal/assets/vendor"
FONTS="https://raw.githubusercontent.com/google/fonts/main/ofl"
MATHJAX_VERSION="3.2.2"
MERMAID_VERSION="11.12.0"

mkdir -p "$DEST/fonts"

fetch() {
  if [ -s "$2" ]; then
    echo "skip $2 (already present)"
    return
  fi
  echo "fetch $2"
  curl -fsSL -o "$2.tmp" "$1"
  mv "$2.tmp" "$2"
}

fetch "$FONTS/notosansjp/NotoSansJP%5Bwght%5D.ttf" "$DEST/fonts/NotoSansJP.ttf"
fetch "$FONTS/notoserifjp/NotoSerifJP%5Bwght%5D.ttf" "$DEST/fonts/NotoSerifJP.ttf"
fetch "$FONTS/notosansmono/NotoSansMono%5Bwdth%2Cwght%5D.ttf" "$DEST/fonts/NotoSansMono.ttf"
fetch "$FONTS/notosansjp/OFL.txt" "$DEST/fonts/OFL.txt"
mkdir -p "$DEST/mathjax"
fetch "https://cdn.jsdelivr.net/npm/mathjax@$MATHJAX_VERSION/es5/tex-svg-full.js" "$DEST/mathjax/tex-svg-full.js"
mkdir -p "$DEST/mermaid"
fetch "https://cdn.jsdelivr.net/npm/mermaid@$MERMAID_VERSION/dist/mermaid.min.js" "$DEST/mermaid/mermaid.min.js"
mkdir -p "$DEST/hljs"
fetch "https://cdn.jsdelivr.net/npm/highlight.js@11.11.1/styles/github.css" "$DEST/hljs/github.css"
