#!/bin/sh
# lea installer: curl -fsSL https://raw.githubusercontent.com/DavidGodefroid/lea/main/install.sh | sh
#
# Checks the requirements, downloads the release archive for this platform, verifies it against
# the release's checksums.txt (whose Sigstore signature cosign checks, when cosign is installed),
# unpacks it into ~/.local/share/lea-install/<version>, links ~/.local/bin/lea, then runs lea init
# (which pulls the images and, unless --no-locksql, builds the locksql console image). Never runs
# sudo itself: lea init asks once, only for the AppArmor profile on hosts that need it.
#
#   sh install.sh [--version vX.Y.Z] [--no-locksql] [--no-init]
#
# LEA_INSTALL_DIR (default ~/.local/share/lea-install) and LEA_BIN_DIR (default ~/.local/bin)
# change where lea goes. LEA_REQUIRE_COSIGN=1 refuses to install without cosign, instead of
# warning and trusting the checksum alone.
set -eu
repo=DavidGodefroid/lea
version=${LEA_VERSION:-}
init=1
locksql=
while [ $# -gt 0 ]; do
  case $1 in
    --version) version=${2:?--version needs a value}; shift ;;
    --version=*) version=${1#*=} ;;
    --no-locksql) locksql=--no-locksql ;;
    --locksql) locksql=--locksql ;;
    --no-init) init= ;;
    -h|--help) sed -n '2,14p' "$0" 2>/dev/null | sed 's/^# \{0,1\}//'; exit 0 ;;
    *) echo "lea install: unknown option $1" >&2; exit 2 ;;
  esac
  shift
done

say() { printf 'lea install: %s\n' "$*" >&2; }
die() { say "error: $*"; exit 1; }
have() { command -v "$1" >/dev/null 2>&1; }

# Requirements.
case $(uname -s) in
  Linux) os=linux ;;
  Darwin) os=darwin ;;
  *) die "unsupported system $(uname -s): lea runs on Linux, macOS, and Windows through WSL2" ;;
esac
case $(uname -m) in
  x86_64|amd64) arch=amd64 ;;
  aarch64|arm64) arch=arm64 ;;
  *) die "unsupported architecture $(uname -m)" ;;
esac
have curl || die "curl is missing"
have tar || die "tar is missing"
have docker || die "Docker is missing: install Docker Engine (Linux) or Docker Desktop (macOS, Windows), then rerun"
docker info >/dev/null 2>&1 || die "Docker does not answer (docker info failed): start it, or add yourself to the docker group, then rerun"
have ssh-keygen || die "ssh-keygen is missing (package openssh-client)"
if have sha256sum; then sum() { sha256sum "$1" | cut -d' ' -f1; }
elif have shasum; then sum() { shasum -a 256 "$1" | cut -d' ' -f1; }
else die "neither sha256sum nor shasum is installed"; fi
if [ "${LEA_REQUIRE_COSIGN:-}" = 1 ] && ! have cosign; then
  die "cosign is missing and LEA_REQUIRE_COSIGN=1: install cosign (https://docs.sigstore.dev), then rerun"
fi

tmux_ok=
if have tmux; then
  tv=$(tmux -V 2>/dev/null | sed -n 's/^tmux [^0-9]*\([0-9][0-9]*\)\.\([0-9][0-9]*\).*/\1 \2/p')
  if [ -n "$tv" ] && { [ "${tv% *}" -gt 3 ] || { [ "${tv% *}" -eq 3 ] && [ "${tv#* }" -ge 2 ]; }; }; then tmux_ok=1; fi
fi

# The release.
if [ -z "$version" ]; then
  version=$(curl -fsSLI -o /dev/null -w '%{url_effective}' "https://github.com/$repo/releases/latest" | sed 's|.*/tag/||')
  case $version in v*) ;; *) die "could not find the latest release of $repo" ;; esac
fi
asset=lea_${version#v}_${os}_${arch}.tar.gz
# LEA_RELEASE_BASE: another place holding the release files (a mirror, a local test).
base=${LEA_RELEASE_BASE:-https://github.com/$repo/releases/download/$version}
tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT
say "downloading lea $version for $os/$arch"
curl -fsSL -o "$tmp/$asset" "$base/$asset" || die "cannot download $base/$asset"
curl -fsSL -o "$tmp/checksums.txt" "$base/checksums.txt" || die "cannot download $base/checksums.txt"
# checksums.txt is signed keyless by the release workflow, on a v* tag (Sigstore, GitHub OIDC).
bundle=checksums.txt.sigstore.json
if have cosign && ! curl -fsSL -o "$tmp/$bundle" "$base/$bundle"; then
  if [ "${LEA_REQUIRE_COSIGN:-}" = 1 ]; then
    die "cannot download the Sigstore bundle $base/$bundle and LEA_REQUIRE_COSIGN=1"
  fi
  case $version in
    # The releases made before signing: no bundle, checksum only. Any later release is signed,
    # so a missing bundle there is suspicious.
    v0.1.0|v0.1.1) say "warning: no Sigstore bundle for this release (pre-signing version), checksum only" ;;
    *) die "cannot download the Sigstore bundle $base/$bundle" ;;
  esac
elif have cosign; then
  cosign verify-blob --bundle "$tmp/$bundle" \
    --certificate-identity-regexp '^https://github\.com/DavidGodefroid/lea/\.github/workflows/release\.yml@refs/tags/v[0-9]' \
    --certificate-oidc-issuer https://token.actions.githubusercontent.com \
    "$tmp/checksums.txt" >&2 || die "checksums.txt does not match its Sigstore signature"
  say "checksums.txt signature verified (cosign)"
else
  say "warning: cosign is missing, checksums.txt signature not verified (checksum only)"
fi
want=$(awk -v f="$asset" '$2 == f { print $1 }' "$tmp/checksums.txt")
[ -n "$want" ] || die "$asset is not in checksums.txt"
[ "$(sum "$tmp/$asset")" = "$want" ] || die "$asset does not match its checksum"

dir=${LEA_INSTALL_DIR:-${XDG_DATA_HOME:-$HOME/.local/share}/lea-install}
mkdir -p "$dir"
tar -xzf "$tmp/$asset" -C "$tmp"
rm -rf "${dir:?}/${version:?}"
mv "$tmp/lea-$version" "$dir/$version"
bindir=${LEA_BIN_DIR:-$HOME/.local/bin}
mkdir -p "$bindir"
if [ -e "$bindir/lea" ] && [ ! -L "$bindir/lea" ]; then
  die "$bindir/lea exists and is not a link: move it away, then rerun"
fi
ln -sfn "$dir/$version/bin/lea" "$bindir/lea"
say "installed lea $version in $dir/$version, linked as $bindir/lea"
case ":$PATH:" in *":$bindir:"*) ;; *) say "add $bindir to your PATH (in your shell profile) to run lea by name" ;; esac
if [ -x "$dir/$version/build/tmux" ]; then tmux_ok=1; fi # the archive's own static tmux
if [ -z "$tmux_ok" ]; then
  if [ "$os" = darwin ]; then hint="brew install tmux"
  elif have apt-get; then hint="sudo apt-get install tmux"
  elif have dnf; then hint="sudo dnf install tmux"
  elif have pacman; then hint="sudo pacman -S tmux"
  else hint="your package manager"; fi
  say "lea needs tmux 3.2 or later for its deck: install it ($hint)"
fi

if [ -n "$init" ]; then
  say "running lea init"
  if [ -r /dev/tty ] && (: </dev/tty) 2>/dev/null; then
    # shellcheck disable=SC2086
    "$bindir/lea" init $locksql </dev/tty
  else
    # shellcheck disable=SC2086
    LEA_INIT_NONINTERACTIVE=1 "$bindir/lea" init $locksql
  fi
fi
