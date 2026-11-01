#!/bin/sh
# The phase-2 exit demo, lea's side: a ticket approved from lea's deck in a real lockhub.
#   - the lox CLI of lockhub's release LOCKHUB_VERSION (cosign-verified checksums), lox up on a
#     lockfamily.yaml with a LockHTTP lock (release LOCKHTTP_VERSION) and the released hub image
#     (cosign-verified, pinned by digest), the console's policy approved at its terminal;
#   - LockHTTP's gate, as lox up's docs and lea run it, holds an agent's write and asks the hub;
#   - lea's deck (TestHubDeckApprovesARealLockhubTicket, internal/daemon) reads the queue from the
#     real facade socket, shows the ticket on its agent and approves it (facade "deck");
#   - the gate then lets the write out, and the hub's audit holds that one signed decision.
# Linux hosts with docker (compose), cosign, curl, jq, python3, script (util-linux), Go, and
# network access to GitHub, ghcr.io and example.com. Its own XDG_RUNTIME_DIR and XDG_STATE_HOME;
# creates only lea-hubdeck-<pid>* resources and the compose project lox up makes for its
# temporary workspace, and removes them.
set -eu
root=$(cd "$(dirname "$0")/../.." && pwd -P)
LOCKHUB_VERSION=${LOCKHUB_VERSION:-0.3.0}
LOCKHTTP_VERSION=${LOCKHTTP_VERSION:-0.7.0}
case $(uname -m) in x86_64) arch=amd64 ;; aarch64) arch=arm64 ;; *) echo "hubdeck: no release for $(uname -m)" >&2; exit 1 ;; esac
id=lea-hubdeck-$$
work=$(mktemp -d)
work=$(cd "$work" && pwd -P)
ws=$work/projects
mkdir -p "$ws" "$work/run" "$work/state" "$work/bin"
chmod 700 "$work/run"
export XDG_RUNTIME_DIR="$work/run" XDG_STATE_HOME="$work/state"
p=
cpid=
say() { printf '\n=== %s\n' "$*"; }
die() { printf 'FAIL: %s\n' "$*" >&2; exit 1; }
# shellcheck disable=SC2317 # run by the trap
cleanup() {
  [ -d "$work" ] || return 0
  if [ -n "$cpid" ]; then kill "$cpid" 2>/dev/null || true; fi
  docker rm -f "$id-agent" "$id-gate" >/dev/null 2>&1 || true
  # Every compose project of this run's own state directory, a lox up that failed half-way too.
  for c in "$XDG_STATE_HOME"/lockfamily/*/compose.yaml; do
    [ -f "$c" ] || continue
    d=$(dirname "$c")
    docker compose -p "$(basename "$d")" --project-directory "$d" -f "$c" down -v --remove-orphans >/dev/null 2>&1 || true
  done
  docker network rm "$id-agents" >/dev/null 2>&1 || true
  # The hub's and the consoles' files may belong to their containers' accounts.
  docker run --rm -v "$work:/w" alpine:3.22 rm -rf /w/state /w/run /w/projects >/dev/null 2>&1 || true
  rm -rf "$work"
}
trap cleanup EXIT
trap 'exit 130' INT TERM
# tty INPUT CMD...: CMD at a pseudo-terminal (lox up refuses a pipe), INPUT typed in.
tty() { in=$1; shift; printf '%b' "$in" | script -qefc "$*" /dev/null; }

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
lox=$work/bin/lox
"$lox" version

say "the hub image of lockhub v$LOCKHUB_VERSION, pinned by digest, signed by its release workflow"
tok=$(curl -fsS "https://ghcr.io/token?scope=repository:davidgodefroid/lockhub:pull" | jq -r .token)
digest=$(curl -fsSI -H "Authorization: Bearer $tok" \
  -H 'Accept: application/vnd.oci.image.index.v1+json, application/vnd.docker.distribution.manifest.list.v2+json' \
  "https://ghcr.io/v2/davidgodefroid/lockhub/manifests/$LOCKHUB_VERSION" | tr -d '\r' | sed -n 's/^[Dd]ocker-[Cc]ontent-[Dd]igest: *//p')
printf '%s\n' "$digest" | grep -Eqx 'sha256:[0-9a-f]{64}' || die "no digest for ghcr.io/davidgodefroid/lockhub:$LOCKHUB_VERSION"
hub=ghcr.io/davidgodefroid/lockhub@$digest
cosign verify --certificate-oidc-issuer https://token.actions.githubusercontent.com \
  --certificate-identity "https://github.com/DavidGodefroid/lockhub/.github/workflows/release.yml@refs/tags/v$LOCKHUB_VERSION" \
  "$hub" >/dev/null || die "$hub is not signed by lockhub's release v$LOCKHUB_VERSION"
echo "$hub"

say "lea's deck, built before any ticket waits on it"
(cd "$root" && go test -c -o "$work/daemon.test" ./internal/daemon)

say "lox up: a LockHTTP lock (v$LOCKHTTP_VERSION) and the hub"
cd "$ws"
"$lox" add lockhttp web --version "v$LOCKHTTP_VERSION" --hub-image "$hub"
mkdir -p .lockhttp
cat >.lockhttp/config.toml <<'TOML'
[profiles.default]
mode = "enforce"
heuristics = "off"
buffer_limit = "1MiB"
hold = "240s"
connect_grant = "1m"
trusted = []
splice = []
deny = []
TOML
chmod 0644 .lockhttp/config.toml
cat .lockfamily/lockfamily.yaml
grep -qxF "  image: $hub" .lockfamily/lockfamily.yaml || die "the hub of lockfamily.yaml is not $hub"
tty 'y\n' "$lox" up >"$work/up.out" 2>&1 || { cat "$work/up.out"; die "lox up failed"; }
cat "$work/up.out"
grep -q "up: project " "$work/up.out" || die "lox up did not finish"
p=$(jq -r .project .lockfamily/up.json)
if [ -z "$p" ] || [ "$p" = null ]; then die "no project in up.json"; fi
got=$(docker inspect -f '{{.Config.Image}}' "$p-lockhub-1") || die "no hub container $p-lockhub-1"
[ "$got" = "$hub" ] || die "the hub runs $got, not $hub"
facade=$XDG_RUNTIME_DIR/lockhub/facade.sock
[ -S "$facade" ] || die "no facade socket at $facade"

say "the console asks its policy at its terminal: lox attach web"
cat >"$work/attach.py" <<'PY'
import os, pty, select, sys, time
# attach.py TIMEOUT UNTIL CMD...: CMD at a pseudo-terminal, y typed once attached; detaches
# (Ctrl-P Ctrl-Q) once UNTIL is seen (exit 0), else exit 1 after TIMEOUT seconds.
timeout, until, cmd = float(sys.argv[1]), sys.argv[2], sys.argv[3:]
pid, fd = pty.fork()
if pid == 0:
    os.execvp(cmd[0], cmd)
time.sleep(3)
os.write(fd, b"y\n")
out, ok, end = "", False, time.time() + timeout
while time.time() < end and not ok:
    r, _, _ = select.select([fd], [], [], 0.5)
    if r:
        try:
            b = os.read(fd, 4096)
        except OSError:
            break
        if not b:
            break
        s = b.decode(errors="replace")
        sys.stdout.write(s)
        sys.stdout.flush()
        out += s
    ok = until in out
time.sleep(1)
try:
    os.write(fd, b"\x10\x11")
except OSError:
    pass
time.sleep(2)
try:
    os.kill(pid, 9)
except OSError:
    pass
sys.exit(0 if ok else 1)
PY
python3 -I "$work/attach.py" 60 '(approved policy)' "$lox" attach web >"$work/attach.out" 2>&1 ||
  { cat "$work/attach.out"; die "the console of web did not approve its policy"; }

say "the gate, as lox up's docs run it: on the hub's network, beside the agents"
docker network create "$id-agents" >/dev/null
hubnet=$(jq -r .hub_network .lockfamily/up.json)
subnet=$(docker network inspect -f '{{(index .IPAM.Config 0).Subnet}}' "$hubnet")
img=$(sed -n '/^  web:/,/image:/s/^ *image: *//p' .lockfamily/lockfamily.yaml)
docker run -d --name "$id-gate" --network "$hubnet" --user 10003:10020 \
  -v "${p}_web-run:/run/lockhttp" -v "${p}_web-gate:/g" -v "${p}_hubkey:/run/lockfamily:ro" \
  "$img" gate --listen 0.0.0.0:3128 --profile default \
  --hub "$(jq -r .hub_url .lockfamily/up.json)" --hub-plain-http --hub-key-file /run/lockfamily/hub.pub \
  --instance web --home /g --hub-net "$subnet" >/dev/null
docker network connect "$id-agents" "$id-gate"
sleep 5
docker logs "$id-gate" 2>&1 | tail -5

say "an agent's write, held by the gate until a decision"
# Pulled first: the check below that the write is held must not be the pull's time.
docker pull -q curlimages/curl:8.11.1 >/dev/null
docker pull -q alpine:3.22 >/dev/null
docker run --rm --name "$id-agent" --network "$id-agents" curlimages/curl:8.11.1 -sS -m 280 -o /dev/null \
  -w '%{http_code}\n' -x "http://$id-gate:3128" -d probe=1 "http://example.com/" >"$work/curl.out" 2>&1 &
cpid=$!
sleep 3
if ! kill -0 "$cpid" 2>/dev/null; then cat "$work/curl.out"; die "the write was not held (it ended before any decision)"; fi

say "lea's deck approves it on the real hub"
rc=0
(cd "$root/internal/daemon" && LEA_E2E_HUB_SOCKET="$facade" LEA_E2E_HUB_INSTANCE=web \
  "$work/daemon.test" -test.count=1 -test.v -test.run '^TestHubDeckApprovesARealLockhubTicket$') >"$work/deck.out" 2>&1 || rc=$?
cat "$work/deck.out"
grep -q -- '--- SKIP' "$work/deck.out" && die "the deck's test was skipped"
if [ "$rc" != 0 ] || ! grep -q -- '^--- PASS: TestHubDeckApprovesARealLockhubTicket ' "$work/deck.out"; then
  docker logs "$id-gate" 2>&1 | tail -20; docker logs "$p-lockhub-1" 2>&1 | tail -20; die "the deck did not approve the ticket"
fi

say "the gate let the approved write out"
wait "$cpid" || true
cpid=
cat "$work/curl.out"
code=$(tr -d '\r' <"$work/curl.out" | tail -1)
# example.com answers a POST with 200 or an error of its own: what counts is that the gate let it
# out (not its 403 refusal, nor a 5xx of its own, nor a timeout).
case "$code" in 2??|3??|404|405|411) ;; *) docker logs "$id-gate" 2>&1 | tail -20; die "the approved write did not pass ($code)" ;; esac
docker logs "$id-gate" 2>&1 | grep -q '"decision":"approved"' || { docker logs "$id-gate" 2>&1 | tail -20; die "the gate logged no approval"; }

say "the hub's audit holds the deck's one decision"
state=$XDG_STATE_HOME/lockfamily/$p
"$lox" audit verify --data "$state/hub" --hub-key-file "$state/hub.pub" >"$work/audit.out" 2>&1 || { cat "$work/audit.out"; die "lox audit verify failed"; }
cat "$work/audit.out"
grep -q " 1 decisions signed" "$work/audit.out" || die "the deck's approval is not in the hub's audit"
echo "hubdeck: OK"
