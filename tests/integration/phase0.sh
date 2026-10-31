#!/bin/sh
# The phase-0 exit demo with real consoles, under lox up: an agent in a real lea (lea:dev, `lea
# shell`) calls the MCP tools locksql_status, lockmq_status and lockfile_status of the clients lea's
# image pins, and each reaches a running console of that release that lockhub's `lox up` runs for
# the projects directory, wired as lea wires it: the socket volume of each lox up lock
# (<project>_<lock>-run, .lockfamily/up.json) on /run/<tool>, read-only, the --group-add of each
# clients group (10000, 10010, 10030), no host account and no /etc/<tool>/system.toml on the host.
#   - each release is the one image/Containerfile pins: its archive matches the pinned sha256 and
#     the checksums.txt that cosign proves its release workflow signed at that tag; the consoles
#     run that release's image, by digest, cosign-verified, as do the hub (lockhub
#     LOCKHUB_VERSION) and the lox CLI of that lockhub release;
#   - lox add declares the three locks (allowed_uids: this account's uid, as lea's hints say: lea's
#     agents run as the host user's uid, which has no account in the consoles' images), lox up
#     starts them with the hub, and each console's start-up questions (its policy, lockmq's
#     password) are answered at its terminal through lox attach;
#   - locksql on a SQLite database of its profile, lockmq on a real RabbitMQ (account tagged
#     monitoring, reached on the host as the locks' profiles name a broker), lockfile on a local
#     directory of its profile copy;
#   - each call goes through the tool's MCP server (<tool> mcp) over stdio in lea's container:
#     initialize, notifications/initialized, tools/call; no model is involved.
# The Locks run hub-attached only (lockkit v0.9.0): a console started without lox up's hub flags
# exits 3. That check needs released hub-only images; for a release that still has a standalone
# console it is reported as a note, not run (see "hub-only" below).
# Negative control: lea's image without the clients groups is refused by the kernel.
# Linux hosts with docker (compose), cosign, curl, jq, python3, script (util-linux), the images
# lea:dev and lea-proxy:dev (make build), build/lea, and network access to GitHub and ghcr.io. Its
# own XDG_RUNTIME_DIR, XDG_STATE_HOME and XDG_DATA_HOME; creates only lea-phase0-<pid>* resources
# and the compose project lox up makes for its temporary projects directory, and removes them.
set -eu
root=$(cd "$(dirname "$0")/../.." && pwd -P)
LOCKHUB_VERSION=${LOCKHUB_VERSION:-0.3.0}
case $(uname -m) in x86_64) arch=amd64 ;; aarch64) arch=arm64 ;; *) echo "phase0: no release for $(uname -m)" >&2; exit 1 ;; esac
id=lea-phase0-$$
uid=$(id -u)
cf=$root/image/Containerfile
rabbit_image=docker.io/library/rabbitmq:3.13-management-alpine@sha256:0c64ec4fc5596406d755dcca75e8a3b618192cc5fb69f8f0340f06290b1cc08c
fail=0
ok() { echo "ok    $1"; }
ko() { echo "FAIL  $1"; fail=1; }
note() { echo "note: $1"; }
die() { echo "phase0: $*" >&2; exit 1; }
say() { printf '\n=== %s\n' "$*"; }

for i in lea:dev lea-proxy:dev; do docker image inspect "$i" >/dev/null 2>&1 || die "no image $i: make build"; done
[ -x "$root/build/lea" ] || die "no build/lea: go build -o build/lea ./cmd/lea"
for c in cosign curl jq python3 script; do command -v "$c" >/dev/null 2>&1 || die "$c is missing"; done
docker compose version >/dev/null 2>&1 || die "docker compose is missing"

work=$(mktemp -d)
work=$(cd "$work" && pwd -P)
projects=$work/projects
mkdir -p "$projects/.phase0" "$work/run" "$work/state" "$work/bin"
chmod 700 "$work/run"
export XDG_DATA_HOME="$work/data" XDG_RUNTIME_DIR="$work/run" XDG_STATE_HOME="$work/state"
# lea's tmux server (socket "lea" under TMUX_TMPDIR) is this run's own: lea stop --all ends it.
export TMUX_TMPDIR="$work"
# lea_up: LEA_CONFIG names this run's prefix; until then lea stop --all would stop the user's lea.
lea_up=
lid=$(printf '%s' "$projects" | sha256sum | cut -c1-10)
slug=$(printf '%s' "$projects" | sed 's/[^A-Za-z0-9]/-/g')
# shellcheck disable=SC2317 # run by the trap
cleanup() {
  [ -d "$work" ] || return 0
  if [ -n "$lea_up" ]; then "$root/bin/lea" stop --all >/dev/null 2>&1 || true; fi
  docker rm -fv "$id-$lid" "$id-proxy" "$id-rabbit" "$id-hubonly" >/dev/null 2>&1 || true
  docker network rm "$id-internal-$lid" "$id-egress" >/dev/null 2>&1 || true
  docker volume rm "$id-podman-$lid" >/dev/null 2>&1 || true
  # Every compose project of this run's own state directory, a lox up that failed half-way too.
  for c in "$XDG_STATE_HOME"/lockfamily/*/compose.yaml; do
    [ -f "$c" ] || continue
    d=$(dirname "$c")
    docker compose -p "$(basename "$d")" --project-directory "$d" -f "$c" down -v --remove-orphans >/dev/null 2>&1 || true
  done
  if [ -n "$slug" ]; then rm -r "${HOME:?}/.claude/projects/$slug" 2>/dev/null || true; fi
  # The hub's and the consoles' files may belong to their containers' accounts.
  docker run --rm --network none -v "$work:/w" docker.io/library/alpine:3.22 rm -rf /w/projects /w/data /w/state /w/run >/dev/null 2>&1 || true
  rm -rf "$work"
}
trap cleanup EXIT
trap 'exit 130' INT TERM
# tty INPUT CMD...: CMD at a pseudo-terminal (lox up refuses a pipe), INPUT typed in.
tty() { in=$1; shift; printf '%b' "$in" | script -qefc "$*" /dev/null; }

# attach.py LOG UNTIL TIMEOUT [EXPECT SEND]... -- CMD...: CMD (lox attach) at a pseudo-terminal,
# its output in LOG. Attaching replays nothing: the console's pending question on a first start is
# its policy review, so y is typed once attached, and again at each later [y/N]; each SEND once,
# when its EXPECT shows. Detaches (Ctrl-P Ctrl-Q) once UNTIL shows (exit 0), else exit 1 after
# TIMEOUT seconds.
cat >"$work/attach.py" <<'PY'
import os, pty, select, sys, time
a = sys.argv[1:]
i = a.index("--")
log, until, timeout = open(a[0], "a", buffering=1), a[1], float(a[2])
todo, cmd = list(zip(a[3:i:2], a[4:i:2])), a[i + 1:]
pid, fd = pty.fork()
if pid == 0:
    os.execvp(cmd[0], cmd)
time.sleep(3)
os.write(fd, b"y\r")
seen, yn, ok, end = "", 0, False, time.time() + timeout
while time.time() < end and not ok:
    r, _, _ = select.select([fd], [], [], 0.5)
    if not r:
        continue
    try:
        b = os.read(fd, 4096)
    except OSError:
        break
    if not b:
        break
    s = b.decode(errors="replace")
    log.write(s)
    seen += s
    for e, t in list(todo):
        if e in seen:
            os.write(fd, t.encode())
            todo.remove((e, t))
    if seen.count("[y/N]") > yn:
        yn = seen.count("[y/N]")
        os.write(fd, b"y\r")
    ok = until in seen
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
# mcpcall.py TOOL NAME: TOOL's MCP server (TOOL mcp) on stdio: initialize, initialized, then
# tools/call NAME with no argument; prints the server's name and the call's answer, as JSON.
cat >"$projects/.phase0/mcpcall.py" <<'PY'
import json, select, subprocess, sys, time
tool, name = sys.argv[1], sys.argv[2]
p = subprocess.Popen([tool, "mcp"], stdin=subprocess.PIPE, stdout=subprocess.PIPE)
def send(m):
    p.stdin.write((json.dumps(m) + "\n").encode())
    p.stdin.flush()
def answer(i):
    end = time.time() + 60
    while time.time() < end:
        if not select.select([p.stdout], [], [], 1)[0]:
            continue
        line = p.stdout.readline()
        if not line:
            sys.exit("mcpcall: %s mcp ended" % tool)
        m = json.loads(line)
        if m.get("id") == i:
            return m
    sys.exit("mcpcall: no answer from %s mcp" % tool)
send({"jsonrpc": "2.0", "id": 1, "method": "initialize", "params": {"protocolVersion": "2025-06-18",
      "capabilities": {}, "clientInfo": {"name": "lea-phase0", "version": "0"}}})
init = answer(1)
send({"jsonrpc": "2.0", "method": "notifications/initialized"})
send({"jsonrpc": "2.0", "id": 2, "method": "tools/call", "params": {"name": name, "arguments": {}}})
res = answer(2)
p.stdin.close()
p.wait(10)
print(json.dumps({"server": init.get("result", {}).get("serverInfo"), "call": res}))
PY
# inside.sh TOOL...: run by lea shell in lea's container, as the session account: its ids, then
# one line "call TOOL <answer>" per TOOL, from the projects directory.
cat >"$projects/.phase0/inside.sh" <<'SH'
#!/bin/sh
d=$(cd "$(dirname "$0")" && pwd -P)
cd "$d/.."
echo "id: $(id)"
for t in "$@"; do
  printf 'call %s %s\n' "$t" "$(python3 -I "$d/mcpcall.py" "$t" "${t}_status" 2>&1 | tr '\n' ' ')"
done
SH

say "no host account and no /etc/<tool>/system.toml on this host"
h=
for t in locksql lockmq lockfile; do
  if getent passwd "$t" >/dev/null 2>&1 || getent group "$t-clients" >/dev/null 2>&1 || [ -e "/etc/$t" ]; then h="$h $t"; fi
done
if [ -z "$h" ]; then ok "no host account, clients group or /etc/<tool> for locksql, lockmq, lockfile"; else ko "the host has accounts or /etc entries for:$h"; fi

say "the releases image/Containerfile pins"
sq_v=$(sed -n 's/.* V=\([0-9][0-9.]*\) .*/\1/p' "$cf")
sq_sum=$(sed -n "s/^ *$arch) sq=\([0-9a-f]\{64\}\) ;;.*/\1/p" "$cf")
mq_v=$(sed -n 's/.* MQ=\([0-9][0-9.]*\) LF=.*/\1/p' "$cf")
lf_v=$(sed -n 's/.* MQ=[0-9.]* LF=\([0-9][0-9.]*\) .*/\1/p' "$cf")
mq_sum=$(sed -n "s/^ *$arch) mq=\([0-9a-f]\{64\}\) lf=.*/\1/p" "$cf")
lf_sum=$(sed -n "s/^ *$arch) mq=[0-9a-f]* lf=\([0-9a-f]\{64\}\) ;;.*/\1/p" "$cf")
for x in "$sq_v" "$sq_sum" "$mq_v" "$mq_sum" "$lf_v" "$lf_sum"; do
  [ -n "$x" ] || die "image/Containerfile no longer pins the clients as this test reads them: update it"
done
echo "locksql $sq_v, lockmq $mq_v, lockfile $lf_v ($arch)"
c=$root/config/lea.conf
if grep -qx "LEA_LOCKSQL_REF=v$sq_v" "$c" && grep -qx "LEA_LOCKMQ_REF=v$mq_v" "$c" && grep -qx "LEA_LOCKFILE_REF=v$lf_v" "$c"; then
  ok "config/lea.conf pins the same releases (LEA_LOCKSQL_REF, LEA_LOCKMQ_REF, LEA_LOCKFILE_REF)"
else ko "config/lea.conf and image/Containerfile pin different releases"; fi

# release TOOL VERSION SHA256: the release archive, its sha256 the pin and in the checksums.txt its
# release workflow signed at that tag (cosign).
release() {
  d=$work/rel-$1 a=${1}_${2}_linux_$arch.tar.gz u=https://github.com/DavidGodefroid/$1/releases/download/v$2
  mkdir -p "$d"
  for f in "$a" checksums.txt checksums.txt.sigstore.json; do curl -fsSL -o "$d/$f" "$u/$f" || die "cannot download $u/$f"; done
  if cosign verify-blob --bundle "$d/checksums.txt.sigstore.json" \
    --certificate-oidc-issuer https://token.actions.githubusercontent.com \
    --certificate-identity "https://github.com/DavidGodefroid/$1/.github/workflows/release.yml@refs/tags/v$2" \
    "$d/checksums.txt" >/dev/null 2>&1 &&
    (cd "$d" && grep "  $a\$" checksums.txt | sha256sum -c - >/dev/null) &&
    echo "$3  $d/$a" | sha256sum -c - >/dev/null; then
    ok "$1 v$2: the archive is the pinned one, its checksum signed by the release workflow at v$2"
  else
    die "$1 v$2: the archive does not match the pin, or its checksums are not signed by its release"
  fi
}
release locksql "$sq_v" "$sq_sum"
release lockmq "$mq_v" "$mq_sum"
release lockfile "$lf_v" "$lf_sum"

# digest REPO VERSION: the digest of ghcr.io/davidgodefroid/REPO:VERSION, once cosign verifies its
# signature by the release workflow at vVERSION (what lox add pins).
digest() {
  tok=$(curl -fsS "https://ghcr.io/token?scope=repository:davidgodefroid/$1:pull" | jq -r .token)
  dg=$(curl -fsSI -H "Authorization: Bearer $tok" \
    -H 'Accept: application/vnd.oci.image.index.v1+json, application/vnd.docker.distribution.manifest.list.v2+json' \
    "https://ghcr.io/v2/davidgodefroid/$1/manifests/$2" | tr -d '\r' | sed -n 's/^[Dd]ocker-[Cc]ontent-[Dd]igest: *//p')
  printf '%s\n' "$dg" | grep -Eqx 'sha256:[0-9a-f]{64}' || die "no digest for ghcr.io/davidgodefroid/$1:$2"
  cosign verify --certificate-oidc-issuer https://token.actions.githubusercontent.com \
    --certificate-identity "https://github.com/DavidGodefroid/$1/.github/workflows/release.yml@refs/tags/v$2" \
    "ghcr.io/davidgodefroid/$1@$dg" >/dev/null 2>&1 || die "ghcr.io/davidgodefroid/$1@$dg is not signed by $1's release v$2"
  printf 'ghcr.io/davidgodefroid/%s@%s' "$1" "$dg"
}
sq_img=$(digest locksql "$sq_v")
ok "locksql image $sq_img, signed by its release workflow at v$sq_v"
mq_img=$(digest lockmq "$mq_v")
ok "lockmq image $mq_img, signed by its release workflow at v$mq_v"
lf_img=$(digest lockfile "$lf_v")
ok "lockfile image $lf_img, signed by its release workflow at v$lf_v"
hub=$(digest lockhub "$LOCKHUB_VERSION")
ok "hub image $hub, signed by lockhub's release workflow at v$LOCKHUB_VERSION"

say "the lox CLI of lockhub v$LOCKHUB_VERSION, its checksums signed by lockhub's release workflow"
rel=https://github.com/DavidGodefroid/lockhub/releases/download/v$LOCKHUB_VERSION
a=lox_${LOCKHUB_VERSION}_linux_$arch.tar.gz
mkdir -p "$work/rel-lockhub"
for f in "$a" checksums.txt checksums.txt.sigstore.json; do curl -fsSL -o "$work/rel-lockhub/$f" "$rel/$f" || die "cannot download $rel/$f"; done
if cosign verify-blob --bundle "$work/rel-lockhub/checksums.txt.sigstore.json" \
  --certificate-oidc-issuer https://token.actions.githubusercontent.com \
  --certificate-identity "https://github.com/DavidGodefroid/lockhub/.github/workflows/release.yml@refs/tags/v$LOCKHUB_VERSION" \
  "$work/rel-lockhub/checksums.txt" >/dev/null 2>&1 &&
  (cd "$work/rel-lockhub" && grep "  $a\$" checksums.txt | sha256sum -c - >/dev/null); then
  ok "lox v$LOCKHUB_VERSION: the archive's checksum signed by lockhub's release workflow"
else die "$a does not match lockhub's signed checksums"; fi
tar -xzf "$work/rel-lockhub/$a" -C "$work/bin" lox
lox=$work/bin/lox
"$lox" version

say "hub-only: a console started without lox up's hub flags refuses to run (exit 3)"
# lockkit v0.9.0's consoles run hub-attached only. A release built on an earlier lockkit still has
# a standalone console: that check waits for its first hub-only release (a note, not a failure).
for x in "locksql $sq_v $sq_img" "lockmq $mq_v $mq_img" "lockfile $lf_v $lf_img"; do
  # shellcheck disable=SC2086 # three words
  set -- $x
  docker pull -q "$3" >/dev/null
  rc=0
  out=$(timeout 60 docker run --rm --name "$id-hubonly" --network none "$3" console </dev/null 2>&1) || rc=$?
  docker rm -f "$id-hubonly" >/dev/null 2>&1 || true
  # shellcheck disable=SC2016 # the console's words, backquotes included
  if [ "$rc" = 3 ] && printf '%s' "$out" | grep -q 'launched by `lox up` only'; then
    ok "$1 v$2: its console refuses to start without lox up (exit 3)"
  else
    note "$1 v$2 has no hub-only console yet (exit $rc): the hub-only check is not run for it"
  fi
done

say "the backends: a RabbitMQ on the host, a SQLite database and a local directory in the profiles"
# A bounded nofile: Erlang sizes its tables on it, and starts very slowly under a huge one. Its
# management port published on the host: the locks reach the host as host.docker.internal, as
# their profile wizards name a broker there.
docker run -d --name "$id-rabbit" -p 15672 --ulimit nofile=65536:65536 -e RABBITMQ_DEFAULT_USER=admin -e RABBITMQ_DEFAULT_PASS="$id-admin" \
  "$rabbit_image" >/dev/null
i=0
# No rabbitmqctl before the server is up: run as root before the server, it writes the Erlang
# cookie as root and the server (as rabbitmq) can no longer read it.
until docker logs "$id-rabbit" 2>&1 | grep -q 'Server startup complete'; do
  i=$((i + 1))
  if [ "$i" -ge 90 ]; then docker logs --tail 40 "$id-rabbit" >&2; die "RabbitMQ did not start"; fi
  sleep 2
done
rport=$(docker port "$id-rabbit" 15672/tcp | sed -n '1s/.*://p')
[ -n "$rport" ] || die "no host port for RabbitMQ's management API"
# The read-only account lockmq's docs make (docs/docker.md, A broker in a container).
docker exec -u rabbitmq "$id-rabbit" rabbitmqctl add_user lockmq-ro "$id-ro" >/dev/null
docker exec -u rabbitmq "$id-rabbit" rabbitmqctl set_user_tags lockmq-ro monitoring >/dev/null
docker exec -u rabbitmq "$id-rabbit" rabbitmqctl set_permissions -p / lockmq-ro '^$' '^$' '^$' >/dev/null
# The profiles lox up reviews and copies into each lock (read-only there): locksql's SQLite
# database in its copy, lockfile's local source the copy's own directory.
mkdir -m 0755 "$projects/.locksql" "$projects/.lockmq" "$projects/.lockfile"
python3 -I -c 'import sqlite3, sys; c = sqlite3.connect(sys.argv[1]); c.execute("create table t (x integer)"); c.commit()' "$projects/.locksql/demo.db"
printf '[profiles.dev]\nengine = "sqlite"\npath   = ".locksql/demo.db"\n' >"$projects/.locksql/config.toml"
printf '[profiles.p]\nurl         = "http://host.docker.internal:%s"\nuser        = "lockmq-ro"\ncredentials = "ask"\n' "$rport" >"$projects/.lockmq/config.toml"
printf '[profiles.samples]\nsource      = "local"\npath        = "/home/lockfile/.config/lockfile"\ncredentials = "none"\n' >"$projects/.lockfile/config.toml"
chmod 0644 "$projects/.locksql/config.toml" "$projects/.locksql/demo.db" "$projects/.lockmq/config.toml" "$projects/.lockfile/config.toml"

say "lox add: the three locks of the pinned releases, and the hub"
cd "$projects"
"$lox" add locksql crm --image "$sq_img" --profile dev --hub-image "$hub"
"$lox" add lockmq events --image "$mq_img" --profile p
"$lox" add lockfile docs --image "$lf_img" --profile samples
# allowed_uids: the uid lea's agents run as (lea's hints say to add it; lox up reviews it).
python3 -I - "$projects/.lockfamily/lockfamily.yaml" "$uid" <<'PY'
import sys
p, uid = sys.argv[1], sys.argv[2]
out = []
for line in open(p):
    out.append(line)
    if line.rstrip("\n") in ("  crm:", "  events:", "  docs:"):
        out.append("    allowed_uids: [%s]\n" % uid)
open(p, "w").write("".join(out))
PY
cat .lockfamily/lockfamily.yaml
if [ "$(grep -c "allowed_uids: \[$uid\]" .lockfamily/lockfamily.yaml)" = 3 ]; then ok "lockfamily.yaml: three locks, each with allowed_uids [$uid]"
else die "lockfamily.yaml lacks the locks' allowed_uids"; fi

say "lox up: the hub and the three consoles"
tty 'y\n' "$lox" up >"$work/up.out" 2>&1 || { cat "$work/up.out"; die "lox up failed"; }
cat "$work/up.out"
grep -q "up: project " "$work/up.out" || die "lox up did not finish"
p=$(jq -r .project .lockfamily/up.json)
if [ -z "$p" ] || [ "$p" = null ]; then die "no project in up.json"; fi
for x in "crm locksql" "events lockmq" "docs lockfile"; do
  # shellcheck disable=SC2086 # two words
  set -- $x
  v=$(jq -r ".locks[\"$1\"].socket_volume" .lockfamily/up.json)
  if [ "$v" = "${p}_$1-run" ] && [ "$(docker volume inspect -f '{{index .Labels "dev.lockfamily.type"}}' "$v" 2>/dev/null)" = "$2" ]; then
    ok "up.json: lock $1 ($2), its socket volume $v"
  else ko "up.json: lock $1: socket volume '$v'"; fi
done

say "the consoles' start-up questions, at their terminals: lox attach"
# attach NAME LOG [EXPECT SEND]...: lox attach NAME until its console listens.
attach() {
  _n=$1 _l=$work/attach-$1.log
  shift
  if python3 -I "$work/attach.py" "$_l" Listening 120 "$@" -- "$lox" attach "$_n" >/dev/null 2>&1; then
    ok "lox attach $_n: the console listens on $(sed 's/\x1b\[[0-9;?]*[A-Za-z]//g' "$_l" | grep -o '/run/[a-z]*/[0-9a-z._-]*\.sock' | tail -1)"
  else
    sed 's/\x1b\[[0-9;?]*[A-Za-z]//g' "$_l" | tail -30 >&2
    ko "lox attach $_n: its console never listened"
  fi
}
cr=$(printf '\r')
attach crm
attach events 'Password for' "$id-ro$cr"
attach docs

# plain USER GROUP TOOL: TOOL's status call from lea's image, without lea (no entrypoint), as USER
# with the supplementary GROUP ("" for none), lox up's socket volumes mounted as lea mounts them.
plain() {
  docker run --rm --network none --user "$1" ${2:+--group-add} ${2:+"$2"} -e HOME=/tmp -w "$projects" \
    --mount "type=bind,src=$projects,dst=$projects,readonly" \
    --mount "type=volume,src=${p}_crm-run,dst=/run/locksql,readonly" \
    --mount "type=volume,src=${p}_events-run,dst=/run/lockmq,readonly" \
    --mount "type=volume,src=${p}_docs-run,dst=/run/lockfile,readonly" \
    --entrypoint python3 lea:dev -I "$projects/.phase0/mcpcall.py" "$3" "${3}_status" 2>&1 | tr '\n' ' '
}
# answered TEXT: whether TEXT is an answer of an MCP server to the call (a result or an error).
answered() { case $1 in *'"call": {"jsonrpc": "2.0", "id": 2, '*) return 0 ;; esac; return 1; }
# running TOOL TEXT: whether TEXT, an answer of TOOL's status tool, says its console runs and
# gave its status (locksql says "console running, status failed" for a socket it cannot connect to).
running() {
  case $1 in
    locksql) case $2 in *'"text": "dev: console running\nprofile      dev\n'*) return 0 ;; esac ;;
    lockmq) case $2 in *'"text": "profile p: console running'*) return 0 ;; esac ;;
    lockfile) case $2 in *'"text": "samples: console up'*) return 0 ;; esac ;;
  esac
  return 1
}
out=$(plain 10012:10012 10010 lockmq)
if running lockmq "$out"; then ok "control: lox up's lockmq console serves the image's own agent account (uid 10012, lockmq-clients)"; else ko "control: lockmq's agent account: $out"; fi
out=$(plain 10032:10032 10030 lockfile)
if running lockfile "$out"; then ok "control: lox up's lockfile console serves the image's own agent account (uid 10032, lockfile-clients)"; else ko "control: lockfile's agent account: $out"; fi

say "lea: a session wired for locksql, lockmq and lockfile (lea shell)"
cat >"$work/lea.conf" <<CONF
LEA_PREFIX=$id
LEA_IMAGE=lea:dev
LEA_PROXY_IMAGE=lea-proxy:dev
LEA_LOCKSQL=1
LEA_LOCKSQL_REF=v$sq_v
LEA_LOCKMQ=1
LEA_LOCKFILE=1
LEA_HUB=0
CONF
export LEA_CONFIG="$work/lea.conf"
lea_up=1
state=$XDG_DATA_HOME/lea/state
mkdir -p "$state"
printf '{}\n' >"$state/claude.json"
LEA_PROJECTS_DIR="$projects" "$root/bin/lea" shell -- sh "$projects/.phase0/inside.sh" locksql lockmq lockfile \
  </dev/null >"$work/agent.out" 2>&1 || true
cat "$work/agent.out"
call() { sed -n "s/^call $1 //p" "$work/agent.out"; }
case $(cat "$work/agent.out") in
  *"no locksql socket"* | *"no lockmq socket"* | *"no lockfile socket"*) ko "lea warned that a tool's agents get no socket of lox up" ;;
  *) ok "lea gave every tool's agents lox up's socket (no warning)" ;;
esac
c=$id-$lid
w=$(docker inspect -f '{{json .HostConfig.GroupAdd}} {{range .Mounts}}{{if eq .Type "volume"}}{{.Name}}:{{.Destination}}:{{.RW}} {{end}}{{end}}' "$c" 2>/dev/null) || w=
echo "      $c: $w"
miss=
for x in '"10000"' '"10010"' '"10030"' " ${p}_crm-run:/run/locksql:false " " ${p}_events-run:/run/lockmq:false " " ${p}_docs-run:/run/lockfile:false "; do
  case " $w " in *"$x"*) ;; *) miss="$miss $x" ;; esac
done
case " $w " in *" locksql-run:"* | *" lockmq-run:"* | *" lockfile-run:"* | *"-home:"*) miss="$miss (a fixed or home volume)" ;; esac
if [ -z "$miss" ]; then ok "lea's container: --group-add 10000 10010 10030, lox up's socket volumes on /run/<tool>, read-only"; else ko "lea's container lacks:$miss"; fi
case $(sed -n 's/^id: //p' "$work/agent.out") in
  *"10000(locksql-clients)"*"10010(lockmq-clients)"*"10030(lockfile-clients)"*) ok "the session account (uid $uid) is in locksql-clients, lockmq-clients, lockfile-clients" ;;
  *) ko "the session account's groups: $(sed -n 's/^id: //p' "$work/agent.out")" ;;
esac
for t in locksql lockmq lockfile; do
  out=$(call "$t")
  if running "$t" "$out"; then ok "lea's agent: ${t}_status reaches the running $t console"; else ko "lea's agent: ${t}_status: $out"; fi
done

say "negative control: lea's image as the session account, without the clients groups"
for t in locksql lockmq lockfile; do
  out=$(plain "$uid:$(id -g)" "" "$t")
  if answered "$out" && ! running "$t" "$out"; then
    ok "$t: without $t-clients, no console: $(printf '%s' "$out" | grep -o '"text": "[^"]*' | head -1 | cut -c10-110)"
  else ko "$t without its group: $out"; fi
done
exit "$fail"
