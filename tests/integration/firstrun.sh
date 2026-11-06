#!/bin/sh
# lea's first run on a machine without images: a fresh HOME and XDG_* directories, an empty
# projects directory, the lea and lea-proxy release images absent, and the lox CLI of the
# checksum-verified lockhub release on PATH (behind a stand-in that records each call). `lea shell
# -- true` then pulls both release images (each cosign-verified against lea's release workflow),
# records the projects directory and runs the command; it runs no lox command and starts no hub
# (#300: approvals live in lox run, outside lea). `lea doctor` shows both images and the lox
# release ok. Once the workspace declares locks (.lockfamily/lockfamily.yaml), a second run pulls
# nothing, still runs no lox command, and says to run `lox run` in another terminal; so does
# `lea doctor`.
# For CI or a disposable host: it REMOVES the lea and lea-proxy release images of this machine
# (docker image rm) and runs with a fresh HOME and XDG_RUNTIME_DIR.
# Linux hosts with docker (compose), cosign, curl, the lea binary (build/lea, from
# `go build -o build/lea ./cmd/lea`) and network access to GitHub and ghcr.io. Its own HOME,
# XDG_*, TMUX_TMPDIR and LEA_CONFIG; creates only lea-fr-<pid>* resources (and removes, by that
# label, any compose project of its temporary projects directory, which lea must not make).
set -eu
root=$(cd "$(dirname "$0")/../.." && pwd -P)
LOCKHUB_VERSION=${LOCKHUB_VERSION:-0.9.0}
LEA_RELEASE=${LEA_RELEASE:-0.4.0}
LEA_IMAGE=${LEA_IMAGE:-ghcr.io/davidgodefroid/lea:$LEA_RELEASE}
LEA_PROXY_IMAGE=${LEA_PROXY_IMAGE:-ghcr.io/davidgodefroid/lea-proxy:$LEA_RELEASE}
LEA_PREFIX=lea-fr-$$
case $(uname -m) in x86_64) arch=amd64 ;; aarch64) arch=arm64 ;; *) echo "firstrun: no release for $(uname -m)" >&2; exit 1 ;; esac
[ -x "$root/build/lea" ] || { echo "firstrun: no build/lea: go build -o build/lea ./cmd/lea" >&2; exit 1; }
for c in cosign curl; do command -v "$c" >/dev/null 2>&1 || { echo "firstrun: $c is missing" >&2; exit 1; }; done
docker compose version >/dev/null 2>&1 || { echo "firstrun: docker compose is missing" >&2; exit 1; }

work=$(mktemp -d)
work=$(cd "$work" && pwd -P)
projects=$work/projects
mkdir -p "$projects" "$work/bin" "$work/home" "$work/run" "$work/state" "$work/data"
chmod 700 "$work/run"
# The images and prefix go in this run's own user configuration, not the environment: lea's
# configuration files beat the environment for these keys, and a source checkout's config/lea.conf
# names lea:dev, which lea would build instead of pulling. No LEA_PROJECTS_DIR here, so the first
# run still triggers; lea init --first-run records it in this same file.
cat >"$work/lea.conf" <<CONF
LEA_PREFIX=$LEA_PREFIX
LEA_IMAGE=$LEA_IMAGE
LEA_PROXY_IMAGE=$LEA_PROXY_IMAGE
CONF
export LEA_CONFIG="$work/lea.conf"
export HOME="$work/home" XDG_RUNTIME_DIR="$work/run" XDG_STATE_HOME="$work/state" XDG_DATA_HOME="$work/data"
# lea's tmux server (socket "lea" under TMUX_TMPDIR) is this run's own, never the user's.
export TMUX_TMPDIR="$work"
lid=$(printf '%s' "$projects" | sha256sum | cut -c1-10)
lfp=lockfamily-$(printf '%s' "$projects" | sha256sum | cut -c1-8)
say() { printf '\n=== %s\n' "$*"; }
die() { printf 'FAIL: %s\n' "$*" >&2; exit 1; }
# shellcheck disable=SC2317 # run by the trap
cleanup() {
  [ -d "$work" ] || return 0
  docker rm -f "$LEA_PREFIX-$lid" "$LEA_PREFIX-proxy" >/dev/null 2>&1 || true
  docker network rm "$LEA_PREFIX-egress" "$LEA_PREFIX-internal-$lid" >/dev/null 2>&1 || true
  # By label: a compose project of the projects directory, should lea have made one.
  docker ps -aq --filter "label=com.docker.compose.project=$lfp" | xargs -r docker rm -f >/dev/null 2>&1 || true
  docker network ls -q --filter "label=com.docker.compose.project=$lfp" | xargs -r docker network rm >/dev/null 2>&1 || true
  docker volume ls -q --filter "label=com.docker.compose.project=$lfp" | xargs -r docker volume rm >/dev/null 2>&1 || true
  tmux -L lea kill-server 2>/dev/null || true
  rm -rf "$work"
}
trap cleanup EXIT
trap 'exit 130' INT TERM

# A machine without images: both release images are gone before lea runs.
docker image rm "$LEA_IMAGE" "$LEA_PROXY_IMAGE" >/dev/null 2>&1 || true

say "the lox CLI of lockhub v$LOCKHUB_VERSION, its checksums signed by lockhub's release workflow"
rel=https://github.com/DavidGodefroid/lockhub/releases/download/v$LOCKHUB_VERSION
a=lox_${LOCKHUB_VERSION}_linux_$arch.tar.gz
for f in "$a" checksums.txt checksums.txt.sigstore.json; do curl -fsSL -o "$work/$f" "$rel/$f"; done
cosign verify-blob --bundle "$work/checksums.txt.sigstore.json" \
  --certificate-oidc-issuer https://token.actions.githubusercontent.com \
  --certificate-identity "https://github.com/DavidGodefroid/lockhub/.github/workflows/release.yml@refs/tags/v$LOCKHUB_VERSION" \
  "$work/checksums.txt" || die "lockhub's checksums are not signed by its release"
(cd "$work" && grep "  $a\$" checksums.txt | sha256sum -c) || die "$a does not match its signed checksum"
mkdir -p "$work/lox"
tar -xzf "$work/$a" -C "$work/lox" lox
# The stand-in on PATH records every call lea makes, then runs the real lox.
cat >"$work/bin/lox" <<STUB
#!/bin/sh
printf '%s\\n' "\$*" >>"$work/lox.calls"
exec "$work/lox/lox" "\$@"
STUB
chmod +x "$work/bin/lox"
PATH="$work/bin:$root/build:$PATH"; export PATH
"$work/lox/lox" version

# lox_calls: the lox commands lea ran, one per line ("" when none).
lox_calls() { cat "$work/lox.calls" 2>/dev/null || true; }

say "lea shell -- true: the first run pulls and verifies both images, and runs no lox command"
cd "$projects"
lea shell -- true </dev/null >"$work/first.out" 2>&1 || { cat "$work/first.out"; die "the first lea shell failed"; }
cat "$work/first.out"
grep -q "first run: pulling ghcr.io/davidgodefroid/lea:" "$work/first.out" ||
  die "the first run did not pull the lea image"
[ "$(grep -c "image .*: signature verified" "$work/first.out")" -eq 2 ] || die "both images were not signature-verified"
[ -z "$(lox_calls)" ] || die "lea ran lox: $(lox_calls)"
grep -q "lox init\|lox up" "$work/first.out" && die "lea started the hub"
[ -z "$(docker ps -aq --filter "label=com.docker.compose.project=$lfp")" ] || die "a container of the compose project $lfp runs: lea started the Lock Family"
grep -q "lox run" "$work/first.out" && die "lea asked for lox run in a workspace without locks"

say "lea doctor: both images ok, the lox release ok"
# What this machine is otherwise warned about (an old host tmux, say) is not this test's subject.
lea doctor >"$work/doctor.out" 2>&1 || true
cat "$work/doctor.out"
grep -qF "ok    image $LEA_IMAGE" "$work/doctor.out" || die "lea doctor: no ok line for $LEA_IMAGE"
grep -qF "ok    image $LEA_PROXY_IMAGE" "$work/doctor.out" || die "lea doctor: no ok line for $LEA_PROXY_IMAGE"
grep -qF "ok    lox $LOCKHUB_VERSION" "$work/doctor.out" || die "lea doctor: no ok line for lox $LOCKHUB_VERSION"
[ "$(lox_calls)" = version ] || die "lea doctor ran more than lox version: $(lox_calls)"
: >"$work/lox.calls"

say "a workspace with locks, no lox run: the second run pulls nothing, runs no lox, says to run lox run"
mkdir -p "$projects/.lockfamily"
printf 'hub: {}\nlocks: {}\n' >"$projects/.lockfamily/lockfamily.yaml"
lea shell -- true </dev/null >"$work/second.out" 2>&1 || { cat "$work/second.out"; die "the second lea shell failed: lea must start without lox run"; }
cat "$work/second.out"
grep -q "first run:" "$work/second.out" && die "the second run pulled or initialised like a first run"
grep -q "pulling" "$work/second.out" && die "the second run pulled an image"
[ -z "$(lox_calls)" ] || die "lea ran lox: $(lox_calls)"
# shellcheck disable=SC2016 # a fixed string: the backticks are literal
grep -qF 'Open another terminal (outside lea) and run `lox run` to approve the agents'"'"' connections and writes.' "$work/second.out" ||
  die "lea did not say to run lox run"
[ -z "$(docker ps -aq --filter "label=com.docker.compose.project=$lfp")" ] || die "lea started the Lock Family"
lea doctor >"$work/doctor2.out" 2>&1 || true
cat "$work/doctor2.out"
grep -q "^warn  lox run: .*run \`lox run\`" "$work/doctor2.out" || die "lea doctor did not say to run lox run"
echo "firstrun: OK"
