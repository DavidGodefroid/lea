#!/bin/sh
# Assembles leadeck.dev into DIR, as .github/workflows/pages.yml publishes it: site/ as it is,
# plus the two files the site takes from the repository root, the demo (assets/demo.svg, which the
# README shows too) and lea's installer (install.sh). Nothing else of the root assets/: the site
# has its own assets/, and no root file may overwrite one of them.
# Usage: tools/site/assemble.sh DIR
set -eu
out=${1:?usage: tools/site/assemble.sh DIR}
root=$(cd "$(dirname "$0")/../.." && pwd)
for f in assets/demo.svg install.sh; do
  if [ -e "$root/site/$f" ]; then
    echo "assemble: site/$f exists: the root $f would overwrite it" >&2
    exit 1
  fi
done
mkdir -p "$out"
cp -R "$root/site/." "$out/"
cp "$root/assets/demo.svg" "$out/assets/demo.svg"
cp "$root/install.sh" "$out/install.sh"
