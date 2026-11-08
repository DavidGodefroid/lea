#!/bin/sh
# lea update on each install method (#354), with real release images:
# 1. A Homebrew-shaped install of lea v$OLD (this checkout's bin, libexec and config in
#    <prefix>/Cellar/lea/$OLD/libexec, linked as <prefix>/opt/lea and <prefix>/bin/lea, as the tap's
#    formula installs it) and a stand-in brew whose upgrade unpacks v$NEW beside it and moves both
#    links. `lea update` runs brew upgrade, then v$NEW pulls and verifies its images;
#    <prefix>/bin/lea reports v$NEW, ~/.local/bin/lea and ~/.local/share/lea-install do not exist,
#    and the next `lea` restarts the daemon on v$NEW.
# 2. The same install, `brew upgrade` alone, then `lea`: the images of v$NEW are pulled at the start
#    (ensure-images) and the daemon restarts on v$NEW (#283).
# 3. An install.sh install of the release v$OLD, its shell parts replaced by this checkout's:
#    `lea update` installs the latest release, linked at ~/.local/bin/lea, pulls its images and
#    names the Lock clients (no regression).
# For CI or a disposable host: it REMOVES the lea and lea-proxy images of v$NEW and of the latest
# release from this machine (docker image rm) and runs with a fresh HOME and XDG_RUNTIME_DIR.
# Linux hosts with docker, cosign, curl, go and network access to GitHub and ghcr.io. Its own
# HOME, XDG_*, TMUX_TMPDIR and LEA_CONFIG; creates only lea-ub-<pid>* resources.
set -eu
root=$(cd "$(dirname "$0")/../.." && pwd -P)
OLD=${LEA_UPDATE_OLD:-0.14.0}
NEW=${LEA_UPDATE_NEW:-0.14.1}
LEA_PREFIX=lea-ub-$$
for c in cosign curl docker go; do command -v "$c" >/dev/null 2>&1 || { echo "update-brew: $c is missing" >&2; exit 1; }; done

work=$(mktemp -d)
work=$(cd "$work" && pwd -P)
say() { printf '\n=== %s\n' "$*"; }
die() { printf 'FAIL: %s\n' "$*" >&2; exit 1; }
# shellcheck disable=SC2317 # run by the trap
cleanup() {
  [ -d "$work" ] || return 0
  for id in $(docker ps -aq --filter "name=^$LEA_PREFIX-"); do docker rm -f "$id" >/dev/null 2>&1 || true; done
  for n in $(docker network ls -q --filter "name=^$LEA_PREFIX-"); do docker network rm "$n" >/dev/null 2>&1 || true; done
  tmux -L lea kill-server 2>/dev/null || true
  rm -rf "$work"
}
trap cleanup EXIT
trap 'exit 130' INT TERM

say "lea v$OLD and v$NEW, built from this checkout"
for v in "$OLD" "$NEW"; do
  (cd "$root" && CGO_ENABLED=0 go build -trimpath -ldflags "-X main.version=v$v" -o "$work/lea-$v" ./cmd/lea)
done

# fresh: a fresh HOME, XDG_* and projects directory, the user configuration naming only this
# run's prefix; no lea of a previous step answers.
fresh() {
  tmux -L lea kill-server 2>/dev/null || true
  for id in $(docker ps -aq --filter "name=^$LEA_PREFIX-"); do docker rm -f "$id" >/dev/null 2>&1 || true; done
  rm -rf "$work/s"; mkdir -p "$work/s/home" "$work/s/run" "$work/s/state" "$work/s/data" "$work/s/projects"
  chmod 700 "$work/s/run"
  printf 'LEA_PREFIX=%s\nLEA_AUTOUPDATE=0\n' "$LEA_PREFIX" >"$work/s/lea.conf"
  export LEA_CONFIG="$work/s/lea.conf" HOME="$work/s/home" XDG_RUNTIME_DIR="$work/s/run" \
    XDG_STATE_HOME="$work/s/state" XDG_DATA_HOME="$work/s/data" TMUX_TMPDIR="$work/s"
  host="$XDG_DATA_HOME/lea/host"
}

# cellar V: lea vV in the Cellar of $P, as the formula installs a release (the directories of
# tools/dist.sh's archive, versioned images, the background update off).
cellar() {
  d="$P/Cellar/lea/$1/libexec"; mkdir -p "$d/build"
  cp -R "$root/bin" "$root/config" "$root/libexec" "$root/apparmor" "$root/image" "$root/proxy" "$d/"
  sed -i -e "s|^LEA_IMAGE=.*|LEA_IMAGE=ghcr.io/davidgodefroid/lea:$1|" \
    -e "s|^LEA_PROXY_IMAGE=.*|LEA_PROXY_IMAGE=ghcr.io/davidgodefroid/lea-proxy:$1|" \
    -e 's|^LEA_AUTOUPDATE=1$|LEA_AUTOUPDATE=0|' "$d/config/lea.conf"
  cp "$work/lea-$1" "$d/build/lea"
}

# brew_shaped: lea v$OLD under a fresh prefix $P, and the stand-in brew first on PATH.
brew_shaped() {
  P="$work/s/brew"; mkdir -p "$P/opt" "$P/bin" "$work/s/fakebin"
  cellar "$OLD"
  ln -s "../Cellar/lea/$OLD" "$P/opt/lea"
  ln -s "../Cellar/lea/$OLD/libexec/bin/lea" "$P/bin/lea"
  cat >"$work/s/fakebin/brew" <<EOT
#!/bin/sh
printf '%s\n' "\$*" >>"$work/s/brew.calls"
case \$1 in
  --prefix) echo "$P" ;;
  upgrade)
    d="$P/Cellar/lea/$NEW/libexec"; mkdir -p "\$d/build"
    cp -R "$root/bin" "$root/config" "$root/libexec" "$root/apparmor" "$root/image" "$root/proxy" "\$d/"
    sed -i -e "s|^LEA_IMAGE=.*|LEA_IMAGE=ghcr.io/davidgodefroid/lea:$NEW|" \\
      -e "s|^LEA_PROXY_IMAGE=.*|LEA_PROXY_IMAGE=ghcr.io/davidgodefroid/lea-proxy:$NEW|" \\
      -e 's|^LEA_AUTOUPDATE=1\$|LEA_AUTOUPDATE=0|' "\$d/config/lea.conf"
    cp "$work/lea-$NEW" "\$d/build/lea"
    ln -sfn "../Cellar/lea/$NEW" "$P/opt/lea"
    ln -sfn "../Cellar/lea/$NEW/libexec/bin/lea" "$P/bin/lea" ;;
esac
EOT
  chmod +x "$work/s/fakebin/brew"
  PATH="$work/s/fakebin:$base_path"; export PATH
}
base_path=$PATH

# no_images V: the lea and lea-proxy images of vV are gone from this machine.
no_images() { docker image rm "ghcr.io/davidgodefroid/lea:$1" "ghcr.io/davidgodefroid/lea-proxy:$1" >/dev/null 2>&1 || true; }
# has_images V: both images of vV are on this machine.
has_images() {
  docker image inspect "ghcr.io/davidgodefroid/lea:$1" >/dev/null 2>&1 || die "no image ghcr.io/davidgodefroid/lea:$1"
  docker image inspect "ghcr.io/davidgodefroid/lea-proxy:$1" >/dev/null 2>&1 || die "no image ghcr.io/davidgodefroid/lea-proxy:$1"
}
# no_parallel_install: lea's own installer left no trace beside Homebrew's.
no_parallel_install() {
  if [ -e "$HOME/.local/bin/lea" ] || [ -L "$HOME/.local/bin/lea" ]; then die "$HOME/.local/bin/lea exists beside Homebrew's lea"; fi
  [ ! -e "$HOME/.local/share/lea-install" ] || die "$HOME/.local/share/lea-install exists beside Homebrew's lea"
}
# shell_ok LEA OUT: LEA shell -- true succeeds in the projects directory, its output in OUT.
shell_ok() {
  (cd "$work/s/projects" && "$1" shell -- true </dev/null >"$2" 2>&1) || { cat "$2"; die "$1 shell -- true failed"; }
  cat "$2"
}

say "1. Homebrew-shaped v$OLD: lea update runs brew upgrade, then v$NEW pulls its images"
fresh; brew_shaped
shell_ok "$P/bin/lea" "$work/s/start.out"
[ "$(cat "$host/daemon.version")" = "v$OLD" ] || die "the daemon does not run v$OLD before the update"
no_images "$NEW"
"$P/bin/lea" update --deps none </dev/null >"$work/s/update.out" 2>&1 || { cat "$work/s/update.out"; die "lea update failed"; }
cat "$work/s/update.out"
grep -qx "upgrade davidgodefroid/tap/lea" "$work/s/brew.calls" || die "lea update did not run brew upgrade davidgodefroid/tap/lea"
grep -q "/install.sh" "$work/s/update.out" && die "lea update ran lea's installer on a Homebrew install"
[ "$("$P/bin/lea" version)" = "v$NEW" ] || die "$P/bin/lea is not v$NEW after lea update"
has_images "$NEW"
[ "$(grep -c "image .*: signature verified" "$work/s/update.out")" -eq 2 ] || die "both images of v$NEW were not signature-verified"
grep -q "Lock clients in lea's image" "$work/s/update.out" || die "lea update did not check the dependencies"
no_parallel_install
shell_ok "$P/bin/lea" "$work/s/next.out"
[ "$(cat "$host/daemon.version")" = "v$NEW" ] || die "the next lea did not restart the daemon on v$NEW"

say "2. Homebrew-shaped v$OLD: brew upgrade alone, then lea pulls the images of v$NEW and restarts the daemon"
fresh; brew_shaped
shell_ok "$P/bin/lea" "$work/s/start.out"
no_images "$NEW"
brew upgrade davidgodefroid/tap/lea
shell_ok "$P/bin/lea" "$work/s/next.out"
has_images "$NEW"
[ "$(cat "$host/daemon.version")" = "v$NEW" ] || die "lea did not restart the daemon on v$NEW after brew upgrade"
no_parallel_install

say "3. install.sh install of v$OLD with this checkout's shell parts: lea update installs the latest release"
fresh; PATH=$base_path; export PATH
curl -fsSL -o "$work/s/install.sh" "https://github.com/DavidGodefroid/lea/releases/download/v$OLD/install.sh"
LEA_REQUIRE_COSIGN=1 sh "$work/s/install.sh" --version "v$OLD" --no-init || die "install.sh v$OLD failed"
inst="$HOME/.local/share/lea-install/v$OLD"
[ -d "$inst" ] || die "install.sh left no $inst"
rm -rf "$inst/libexec"; cp -R "$root/libexec" "$inst/"
latest=$(curl -fsSLI -o /dev/null -w '%{url_effective}' https://github.com/DavidGodefroid/lea/releases/latest | sed 's|.*/tag/||')
no_images "${latest#v}"
"$HOME/.local/bin/lea" update --deps none </dev/null >"$work/s/update.out" 2>&1 || { cat "$work/s/update.out"; die "lea update failed"; }
cat "$work/s/update.out"
[ "$("$HOME/.local/bin/lea" version)" = "$latest" ] || die "$HOME/.local/bin/lea is not $latest after lea update"
has_images "${latest#v}"
grep -q "Lock clients in lea's image" "$work/s/update.out" || die "lea update did not check the dependencies"
echo "update-brew: OK"
