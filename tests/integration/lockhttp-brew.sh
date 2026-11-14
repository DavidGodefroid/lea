#!/bin/sh
# lea doctor with LEA_LOCKHTTP=1 on a brew-only install of lockhttp (#411), with the real launcher
# of a lockhttp release, the real cosign and the real registry:
# 1. The launcher where brew links it (<prefix>/bin/lockhttp into its Cellar, HOMEBREW_PREFIX),
#    stamped as the tap's formula stamps it (launcher_version, image_digest), of a release that is
#    not the one config/locks.lock pins: cosign verifies its digest, and lea doctor reports the
#    gate image as OK.
# 2. The same launcher in ~/.local/bin (lockfamily's install.sh), of the release and digest
#    config/locks.lock pins: OK without asking cosign.
# 3. Its digest swapped for another release's image: cosign does not verify it, lea doctor fails.
# Nothing in /usr/local/bin/lockhttp (the script stops if there is one). Linux hosts with cosign,
# curl, go and network access to GitHub, ghcr.io and Sigstore. Its own HOME, XDG_* and LEA_CONFIG.
set -eu
root=$(cd "$(dirname "$0")/../.." && pwd -P)
# A lockhttp release other than the set's, and its image digest (the tap's formula for it, which
# its bump checked with cosign).
REL=${LEA_LOCKHTTP_BREW_REL:-0.9.1}
DIGEST=${LEA_LOCKHTTP_BREW_DIGEST:-sha256:29a48e7a659bd1dc21bec36426e12933926f32b44a2b16a58fe509e510f7fd5a}
for c in cosign curl go; do command -v "$c" >/dev/null 2>&1 || { echo "lockhttp-brew: $c is missing" >&2; exit 1; }; done
if [ -e /usr/local/bin/lockhttp ]; then echo "lockhttp-brew: /usr/local/bin/lockhttp exists: this test needs a brew-only host" >&2; exit 1; fi

work=$(mktemp -d)
work=$(cd "$work" && pwd -P)
trap 'rm -rf "$work"' EXIT
trap 'exit 130' INT TERM
die() { printf 'FAIL: %s\n' "$*" >&2; exit 1; }

pin=$(sed -n 's/^LOCKS_LOCKHTTP_IMAGE=//p' "$root/config/locks.lock")
setrel=$(sed -n 's/^LOCKS_LOCKHTTP_VERSION=v//p' "$root/config/locks.lock")
[ "$REL" != "$setrel" ] || die "LEA_LOCKHTTP_BREW_REL is the set's release ($setrel): pick another"

(cd "$root" && CGO_ENABLED=0 go build -trimpath -o "$root/build/lea" ./cmd/lea)

mkdir -p "$work/home" "$work/run" "$work/state" "$work/data" "$work/projects"
chmod 700 "$work/run"
printf 'LEA_PREFIX=lea-lb-%s\nLEA_AUTOUPDATE=0\nLEA_LOCKHTTP=1\nLEA_PROJECTS_DIR=%s\n' "$$" "$work/projects" >"$work/lea.conf"
export LEA_CONFIG="$work/lea.conf" HOME="$work/home" XDG_RUNTIME_DIR="$work/run" \
  XDG_STATE_HOME="$work/state" XDG_DATA_HOME="$work/data" XDG_CONFIG_HOME="$work/config"
unset HOMEBREW_PREFIX

# stamp V DIGEST OUT: lockhttp's launcher of release V, stamped as the tap's formula does.
stamp() {
  curl -fsSL "https://raw.githubusercontent.com/DavidGodefroid/lockhttp/v$1/scripts/lockhttp" >"$work/launcher" ||
    die "cannot download lockhttp's launcher v$1"
  if ! grep -qx 'launcher_version=dev' "$work/launcher" || ! grep -qx 'image_digest=' "$work/launcher"; then
    die "lockhttp's launcher v$1 has no launcher_version=dev and image_digest= lines to stamp"
  fi
  mkdir -p "$(dirname "$3")"
  sed -e "s/^launcher_version=dev\$/launcher_version=$1/" -e "s/^image_digest=\$/image_digest=$2/" "$work/launcher" >"$3"
  chmod 0755 "$3"
}

# doctor WANT: lea doctor's lockhttp launcher line is WANT.
doctor() {
  out=$(timeout 300 "$root/bin/lea" doctor 2>&1 || true)
  line=$(printf '%s\n' "$out" | grep -E '^(ok|warn|FAIL) +lockhttp launcher' | head -n 1)
  case $line in
    "$1"*) echo "passed: $line" ;;
    *) printf '%s\n' "$out" | grep -i lockhttp >&2 || true; die "lea doctor: '$line', want '$1'" ;;
  esac
}

echo "=== 1. brew's launcher (HOMEBREW_PREFIX/bin/lockhttp, a link into its Cellar), lockhttp v$REL"
cellar=$work/brew/Cellar/lockhttp/$REL/bin/lockhttp
stamp "$REL" "$DIGEST" "$cellar"
mkdir -p "$work/brew/bin"
ln -s "../Cellar/lockhttp/$REL/bin/lockhttp" "$work/brew/bin/lockhttp"
export HOMEBREW_PREFIX="$work/brew"
doctor "ok    lockhttp launcher (gate image ghcr.io/davidgodefroid/lockhttp@$DIGEST)"

echo "=== 2. lockfamily's install.sh's (~/.local/bin/lockhttp), the set's lockhttp v$setrel"
unset HOMEBREW_PREFIX
stamp "$setrel" "${pin#*@}" "$HOME/.local/bin/lockhttp"
doctor "ok    lockhttp launcher (gate image $pin)"

echo "=== 3. the same launcher, its digest swapped for lockhttp v$REL's image"
stamp "$setrel" "$DIGEST" "$HOME/.local/bin/lockhttp"
doctor "FAIL  lockhttp launcher: $HOME/.local/bin/lockhttp pins ghcr.io/davidgodefroid/lockhttp@$DIGEST, which cosign does not verify as lockhttp's release v$setrel"

echo "lockhttp-brew: OK"
