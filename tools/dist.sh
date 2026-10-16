#!/bin/sh
# tools/dist.sh VERSION: the release archives of lea VERSION (vX.Y.Z) in dist/, one per platform,
# and dist/checksums.txt. Each archive holds lea-VERSION/ with the binary (build/lea), the files it
# runs with (bin, config, libexec, apparmor, image, proxy: image and proxy let lea init build the
# images when they cannot be pulled), config/lea.conf pointing at the images of VERSION and, on
# Linux, a static tmux (build/tmux) when dist-tmux/tmux-<arch> exists.
set -eu
v=${1:?usage: tools/dist.sh vX.Y.Z}
case $v in v[0-9]*.[0-9]*.[0-9]*) ;; *) echo "dist: bad version '$v'" >&2; exit 1 ;; esac
root=$(cd "$(dirname "$0")/.." && pwd -P)
registry=${LEA_DIST_REGISTRY:-ghcr.io/davidgodefroid}
platforms=${LEA_DIST_PLATFORMS:-linux/amd64 linux/arm64 darwin/amd64 darwin/arm64}
out=$root/dist
rm -rf "$out"
mkdir -p "$out/stage"
for p in $platforms; do
  os=${p%/*} arch=${p#*/}
  name=lea_${v#v}_${os}_${arch}
  d=$out/stage/$name/lea-$v
  mkdir -p "$d/build"
  (cd "$root" && CGO_ENABLED=0 GOOS=$os GOARCH=$arch go build -trimpath \
    -ldflags "-s -w -X main.version=$v" -o "$d/build/lea" ./cmd/lea)
  (cd "$root" && tar -cf - bin config libexec apparmor image proxy LICENSE README.md) | tar -xf - -C "$d"
  sed -i.bak -e "s|^LEA_IMAGE=.*|LEA_IMAGE=$registry/lea:${v#v}|" \
    -e "s|^LEA_PROXY_IMAGE=.*|LEA_PROXY_IMAGE=$registry/lea-proxy:${v#v}|" "$d/config/lea.conf"
  rm -f "$d/config/lea.conf.bak"
  # A static tmux for Linux (tools/tmux, built per architecture into dist-tmux/ by the release
  # workflow): the deck then needs no tmux on the host.
  if [ "$os" = linux ] && [ -x "$root/dist-tmux/tmux-$arch" ]; then cp "$root/dist-tmux/tmux-$arch" "$d/build/tmux"; fi
  printf '%s\n' "$v" >"$d/VERSION"
  tar -C "$out/stage/$name" -czf "$out/$name.tar.gz" "lea-$v"
done
rm -rf "$out/stage"
cp "$root/install.sh" "$out/install.sh"
(cd "$out" && { sha256sum ./*.tar.gz install.sh 2>/dev/null || shasum -a 256 ./*.tar.gz install.sh; } |
  sed 's| \./| |' >checksums.txt)
ls -l "$out"
