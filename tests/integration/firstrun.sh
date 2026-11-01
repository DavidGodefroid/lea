#!/bin/sh
# lea's first run on a machine without images: a fresh HOME and XDG_* directories, an empty
# projects directory, the lea and lea-proxy release images absent, and the lox CLI of the
# checksum-verified lockhub release on PATH. `lea shell -- true` then pulls both release images
# (each cosign-verified against lea's release workflow), records the projects directory, starts
# the Lock Family hub with `lox init` (its Ready line, the hub container of the projects
# directory's compose project), and runs the command; `lea doctor` shows both images ok; a
# second run pulls nothing and starts no hub.
# For CI or a disposable host: it REMOVES the lea and lea-proxy release images of this machine
# (docker image rm) and runs with a fresh HOME and XDG_RUNTIME_DIR.
# Linux hosts with docker (compose), cosign, curl, the lea binary (build/lea, from
# `go build -o build/lea ./cmd/lea`) and network access to GitHub and ghcr.io. Its own HOME,
# XDG_* and TMUX_TMPDIR; creates only lea-fr-<pid>* resources and the compose project lox init
# makes for its temporary projects directory, and removes them (by name, then by that compose
# project's label).
set -eu
root=$(cd "$(dirname "$0")/../.." && pwd -P)
LOCKHUB_VERSION=${LOCKHUB_VERSION:-0.5.0}
LEA_RELEASE=${LEA_RELEASE:-0.4.0}
LEA_IMAGE=${LEA_IMAGE:-ghcr.io/davidgodefroid/lea:$LEA_RELEASE}
LEA_PROXY_IMAGE=${LEA_PROXY_IMAGE:-ghcr.io/davidgodefroid/lea-proxy:$LEA_RELEASE}
LEA_PREFIX=lea-fr-$$
export LEA_IMAGE LEA_PROXY_IMAGE LEA_PREFIX
case $(uname -m) in x86_64) arch=amd64 ;; aarch64) arch=arm64 ;; *) echo "firstrun: no release for $(uname -m)" >&2; exit 1 ;; esac
[ -x "$root/build/lea" ] || { echo "firstrun: no build/lea: go build -o build/lea ./cmd/lea" >&2; exit 1; }
for c in cosign curl; do command -v "$c" >/dev/null 2>&1 || { echo "firstrun: $c is missing" >&2; exit 1; }; done
docker compose version >/dev/null 2>&1 || { echo "firstrun: docker compose is missing" >&2; exit 1; }

work=$(mktemp -d)
work=$(cd "$work" && pwd -P)
projects=$work/projects
mkdir -p "$projects" "$work/bin" "$work/home" "$work/run" "$work/state" "$work/data"
chmod 700 "$work/run"
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
  # Every compose project of this run's own state directory, a lox init that failed half-way too.
  for c in "$XDG_STATE_HOME"/lockfamily/*/compose.yaml; do
    [ -f "$c" ] || continue
    d=$(dirname "$c")
    docker compose -p "$(basename "$d")" --project-directory "$d" -f "$c" down -v --remove-orphans >/dev/null 2>&1 || true
  done
  # By label, whatever the state directory held: the compose project of the projects directory.
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
tar -xzf "$work/$a" -C "$work/bin" lox
PATH="$work/bin:$root/build:$PATH"; export PATH
lox version

say "lea shell -- true: the first run pulls and verifies both images, then starts the hub"
cd "$projects"
lea shell -- true </dev/null >"$work/first.out" 2>&1 || { cat "$work/first.out"; die "the first lea shell failed"; }
cat "$work/first.out"
grep -q "first run: pulling ghcr.io/davidgodefroid/lea:" "$work/first.out" ||
  die "the first run did not pull the lea image"
[ "$(grep -c "image .*: signature verified" "$work/first.out")" -eq 2 ] || die "both images were not signature-verified"
grep -q "first run: starting the Lock Family hub (lox init)" "$work/first.out" ||
  die "lea did not start the hub with lox init"
# shellcheck disable=SC2016 # a fixed string: the backticks are literal
grep -qF 'Ready. Run `lea` (or `claude` + `lox approve`). Add a source: `lox add locksql|lockmq|lockfile|lockhttp`.' "$work/first.out" ||
  die "lox init did not end with its Ready line"
docker ps --filter "label=com.docker.compose.project=$lfp" --format '{{.Names}}' >"$work/ps.out"
grep -q -- "-lockhub-" "$work/ps.out" || die "no lockhub container in the compose project $lfp"
hub1=$(docker ps -q --filter "label=com.docker.compose.project=$lfp" --filter "name=-lockhub-")
[ -n "$hub1" ] || die "no running lockhub container id in the compose project $lfp"

say "lea doctor: both images ok"
# What this machine is otherwise warned about (an old host tmux, say) is not this test's subject.
lea doctor >"$work/doctor.out" 2>&1 || true
cat "$work/doctor.out"
grep -qF "ok    image $LEA_IMAGE" "$work/doctor.out" || die "lea doctor: no ok line for $LEA_IMAGE"
grep -qF "ok    image $LEA_PROXY_IMAGE" "$work/doctor.out" || die "lea doctor: no ok line for $LEA_PROXY_IMAGE"

say "a second run: no pull, no hub start"
lea shell -- true </dev/null >"$work/second.out" 2>&1 || { cat "$work/second.out"; die "the second lea shell failed"; }
cat "$work/second.out"
grep -q "first run:" "$work/second.out" && die "the second run pulled or initialised like a first run"
grep -q "lox init" "$work/second.out" && die "the second run started the hub again"
grep -q "pulling" "$work/second.out" && die "the second run pulled an image"
hub2=$(docker ps -q --filter "label=com.docker.compose.project=$lfp" --filter "name=-lockhub-")
[ "$hub2" = "$hub1" ] || die "the second run did not keep the first run's hub container ($hub1 became ${hub2:-none})"
echo "firstrun: OK"
