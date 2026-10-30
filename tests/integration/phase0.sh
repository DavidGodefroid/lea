#!/bin/sh
# The phase-0 exit demo with real consoles: an agent in a real lea (lea:dev, `lea shell`) calls the
# MCP tools locksql_status, lockmq_status and lockfile_status of the clients lea's image pins, and
# each reaches a running console of that release, wired as lea wires it: the socket volumes
# locksql-run, lockmq-run and lockfile-run on /run/<tool>, the --group-add of each clients group
# (10000, 10010, 10030), no host account and no /etc/<tool>/system.toml on the host.
#   - each release is the one image/Containerfile pins: its archive matches the pinned sha256 and
#     the checksums.txt that cosign proves its release workflow signed at that tag;
#   - locksql: lea's own path. The console image is built by lea__locksql_build (libexec/lib/
#     init.sh: the tag's source, cosign-checked, ALLOWED_UIDS of this account) and the console is
#     started by lea_locksql (lea locksql) through locksql's launcher of that release, on a SQLite
#     database of the projects directory (no server needed);
#   - lockmq and lockfile: the console started by the tool's own launcher of the release, as its
#     install.sh writes it (the release image by digest, cosign-verified), lockmq on a real
#     RabbitMQ (account tagged monitoring), lockfile on a local directory;
#   - each call goes through the tool's MCP server (<tool> mcp) over stdio in lea's container:
#     initialize, notifications/initialized, tools/call; no model is involved.
# The one stand-in left, and why: lea's agents run as the host user's uid, which the consoles'
# release images of lockmq and lockfile do not allow (their /etc/<tool>/system.toml has no
# allowed_uids, and the uid has no account in the console's container, so the console closes every
# connection of lea's agents). lea builds locksql's image with ALLOWED_UIDS, but nothing does it
# for lockmq and lockfile. The test proves that first (with the release images unchanged, lea's
# agent gets no console while the image's own agent account does), then runs those two consoles
# from the release image plus one layer that appends `allowed_uids = [<uid>]` to the image's
# system.toml: exactly what the tools' own ALLOWED_UIDS build argument writes; binary unchanged.
# Negative control: lea's image without the clients groups is refused by the kernel.
# Linux hosts with docker, cosign, curl, jq, python3, the images lea:dev and lea-proxy:dev (make
# build), build/lea, and network access to GitHub and ghcr.io. The tools' launchers use fixed
# volume names (<tool>-run, <tool>-home) and lea_locksql the container name locksql-console: the
# test refuses to run when any of them exists, creates them, and removes them; everything else it
# creates is named lea-phase0-<pid>*.
set -eu
root=$(cd "$(dirname "$0")/../.." && pwd -P)
case $(uname -m) in x86_64) arch=amd64 ;; aarch64) arch=arm64 ;; *) echo "phase0: no release for $(uname -m)" >&2; exit 1 ;; esac
id=lea-phase0-$$
uid=$(id -u)
cf=$root/image/Containerfile
rabbit_image=docker.io/library/rabbitmq:3.13-management-alpine@sha256:0c64ec4fc5596406d755dcca75e8a3b618192cc5fb69f8f0340f06290b1cc08c
fixed_volumes="locksql-run locksql-home lockmq-run lockmq-home lockfile-run lockfile-home"
fail=0
ok() { echo "ok    $1"; }
ko() { echo "FAIL  $1"; fail=1; }
die() { echo "phase0: $*" >&2; exit 1; }
say() { printf '\n=== %s\n' "$*"; }

for v in $fixed_volumes; do
  if docker volume inspect "$v" >/dev/null 2>&1; then die "the volume $v exists: this test creates and removes the launchers' fixed volumes; run it where none exists (CI)"; fi
done
if docker container inspect locksql-console >/dev/null 2>&1; then die "a container locksql-console exists: run the test where none does (CI)"; fi
for i in lea:dev lea-proxy:dev; do docker image inspect "$i" >/dev/null 2>&1 || die "no image $i: make build"; done
[ -x "$root/build/lea" ] || die "no build/lea: go build -o build/lea ./cmd/lea"

work=$(mktemp -d)
work=$(cd "$work" && pwd -P)
projects=$work/projects
lbin=$work/lbin
drivers=
mkdir -p "$projects/.phase0" "$work/samples"
mkdir -m 0755 "$lbin"
export XDG_DATA_HOME="$work/data"
lid=$(printf '%s' "$projects" | sha256sum | cut -c1-10)
slug=$(printf '%s' "$projects" | sed 's/[^A-Za-z0-9]/-/g')
# shellcheck disable=SC2317 # run by the trap
cleanup() {
  for c in "$id-lockmq" "$id-lockfile" "$id-rabbit"; do docker rm -f "$c" >/dev/null 2>&1 || true; done
  docker rm -f locksql-console >/dev/null 2>&1 || true
  for p in $drivers; do kill "$p" 2>/dev/null || true; done
  "$root/bin/lea" stop --all >/dev/null 2>&1 || true
  docker rm -f "$id-$lid" "$id-proxy" >/dev/null 2>&1 || true
  docker network rm "$id-internal-$lid" "$id-egress" "$id-net" >/dev/null 2>&1 || true
  docker volume rm "$id-podman-$lid" >/dev/null 2>&1 || true
  # Checked absent above: this run made them.
  # shellcheck disable=SC2086 # names
  docker volume rm $fixed_volumes >/dev/null 2>&1 || true
  docker image rm "$id-locksql:test" "$id-lockmq:allow" "$id-lockfile:allow" >/dev/null 2>&1 || true
  if [ -n "$slug" ]; then rm -r "${HOME:?}/.claude/projects/$slug" 2>/dev/null || true; fi
  # .locksql and .lockmq belong to the consoles' accounts after doctor --fix.
  docker run --rm --network none -v "$work:/w" docker.io/library/alpine:3.22 rm -rf /w/projects /w/data >/dev/null 2>&1 || true
  rm -rf "$work"
}
trap cleanup EXIT
trap 'exit 130' INT TERM

# drive.py LOG EXPECT SEND [EXPECT SEND...] -- CMD...: CMD at a pseudo-terminal (the consoles
# refuse a pipe), its output in LOG; each SEND typed once, when its EXPECT shows; until CMD ends.
cat >"$work/drive.py" <<'PY'
import os, pty, sys
a = sys.argv[1:]
log, i = open(a[0], "a", buffering=1), a.index("--")
todo, cmd = list(zip(a[1:i:2], a[2:i:2])), a[i + 1:]
pid, fd = pty.fork()
if pid == 0:
    os.execvp(cmd[0], cmd)
seen = ""
while True:
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
_, st = os.waitpid(pid, 0)
sys.exit(os.waitstatus_to_exitcode(st))
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
# release workflow signed at that tag (cosign); its files in $work/rel-TOOL.
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
  tar -xzf "$d/$a" -C "$d"
}
release locksql "$sq_v" "$sq_sum"
release lockmq "$mq_v" "$mq_sum"
release lockfile "$lf_v" "$lf_sum"

# digest TOOL VERSION: the digest of ghcr.io/davidgodefroid/TOOL:VERSION, once cosign verifies its
# signature by the release workflow at vVERSION (what the tool's install.sh pins in its launcher).
digest() {
  tok=$(curl -fsS "https://ghcr.io/token?scope=repository:davidgodefroid/$1:pull" | jq -r .token)
  dg=$(curl -fsSI -H "Authorization: Bearer $tok" \
    -H 'Accept: application/vnd.oci.image.index.v1+json, application/vnd.docker.distribution.manifest.list.v2+json' \
    "https://ghcr.io/v2/davidgodefroid/$1/manifests/$2" | tr -d '\r' | sed -n 's/^[Dd]ocker-[Cc]ontent-[Dd]igest: *//p')
  printf '%s\n' "$dg" | grep -Eqx 'sha256:[0-9a-f]{64}' || die "no digest for ghcr.io/davidgodefroid/$1:$2"
  cosign verify --certificate-oidc-issuer https://token.actions.githubusercontent.com \
    --certificate-identity "https://github.com/DavidGodefroid/$1/.github/workflows/release.yml@refs/tags/v$2" \
    "ghcr.io/davidgodefroid/$1@$dg" >/dev/null 2>&1 || die "ghcr.io/davidgodefroid/$1@$dg is not signed by $1's release v$2"
  printf '%s' "$dg"
}
mq_dg=$(digest lockmq "$mq_v")
ok "lockmq image ghcr.io/davidgodefroid/lockmq@$mq_dg, signed by its release workflow at v$mq_v"
lf_dg=$(digest lockfile "$lf_v")
ok "lockfile image ghcr.io/davidgodefroid/lockfile@$lf_dg, signed by its release workflow at v$lf_v"
mq_img=ghcr.io/davidgodefroid/lockmq@$mq_dg
lf_img=ghcr.io/davidgodefroid/lockfile@$lf_dg
docker pull -q "$mq_img" >/dev/null
docker pull -q "$lf_img" >/dev/null

say "the launchers, as the tools' install.sh writes them (outside the projects directory)"
sed -e "s/^launcher_version=dev\$/launcher_version=$sq_v/" "$work/rel-locksql/scripts/locksql" >"$lbin/locksql"
sed -e "s/^launcher_version=dev\$/launcher_version=$mq_v/" -e "s/^image_digest=\$/image_digest=$mq_dg/" \
  "$work/rel-lockmq/scripts/lockmq" >"$lbin/lockmq"
sed -e "s/^launcher_version=dev\$/launcher_version=$lf_v/" -e "s/^image_digest=\$/image_digest=$lf_dg/" \
  -e "s|^seccomp_profile=\$|seccomp_profile=$work/rel-lockfile/seccomp.json|" "$work/rel-lockfile/scripts/lockfile" >"$lbin/lockfile"
chmod 0755 "$lbin/locksql" "$lbin/lockmq" "$lbin/lockfile"
if ! grep -qx "image_digest=$mq_dg" "$lbin/lockmq" || ! grep -qx "image_digest=$lf_dg" "$lbin/lockfile" ||
  ! grep -qx "launcher_version=$sq_v" "$lbin/locksql"; then die "a launcher does not take its version or digest"; fi

say "the locksql console image, built by lea (lea__locksql_build)"
sq_img=$id-locksql:test
# shellcheck disable=SC2016 # expanded by the inner shell
if env LEA_ROOT="$root" sh -c '
  for f in "$LEA_ROOT"/libexec/lib/*.sh; do . "$f"; done
  # Only the image: the launcher is the release archive'"'"'s ($lbin), not installed with sudo.
  lea__locksql_install_launcher() { :; }
  lea__locksql_build "$1" "$2"' sh "v$sq_v" "$sq_img" >"$work/sqbuild.log" 2>&1 &&
  grep -q "locksql v$sq_v: signature verified" "$work/sqbuild.log" &&
  docker run --rm --entrypoint cat "$sq_img" /etc/locksql/system.toml | grep -qx "allowed_uids = \[$uid\]"; then
  ok "locksql console image built by lea__locksql_build from v$sq_v (signature verified, allowed_uids = [$uid])"
else
  tail -40 "$work/sqbuild.log" >&2
  die "lea__locksql_build did not build the locksql console image"
fi

# The release images of lockmq and lockfile with the one line lea does not give them (see above).
for t in lockmq:"$mq_img" lockfile:"$lf_img"; do
  printf 'FROM %s\nUSER root\nRUN printf "allowed_uids = [%s]\\n" >>/etc/%s/system.toml\nUSER agent\n' "${t#*:}" "$uid" "${t%%:*}" |
    docker build -q -t "$id-${t%%:*}:allow" - >/dev/null
done

say "the backends: a RabbitMQ, a SQLite database, a local directory"
docker network create "$id-net" >/dev/null
# A bounded nofile: Erlang sizes its tables on it, and starts very slowly under a huge one.
docker run -d --name "$id-rabbit" --network "$id-net" --ulimit nofile=65536:65536 -e RABBITMQ_DEFAULT_USER=admin -e RABBITMQ_DEFAULT_PASS="$id-admin" \
  "$rabbit_image" >/dev/null
i=0
until docker exec "$id-rabbit" rabbitmqctl await_startup >"$work/rabbit.out" 2>&1; do
  i=$((i + 1))
  if [ "$i" -ge 90 ]; then cat "$work/rabbit.out" >&2; docker logs --tail 40 "$id-rabbit" >&2; die "RabbitMQ did not start"; fi
  sleep 2
done
# The read-only account lockmq's docs make (docs/docker.md, A broker in a container).
docker exec "$id-rabbit" rabbitmqctl add_user lockmq-ro "$id-ro" >/dev/null
docker exec "$id-rabbit" rabbitmqctl set_user_tags lockmq-ro monitoring >/dev/null
docker exec "$id-rabbit" rabbitmqctl set_permissions -p / lockmq-ro '^$' '^$' '^$' >/dev/null
python3 -I -c 'import sqlite3, sys; c = sqlite3.connect(sys.argv[1]); c.execute("create table t (x integer)"); c.commit()' "$projects/demo.db"
mkdir -m 0755 "$projects/.locksql" "$projects/.lockmq"
printf '[profiles.dev]\nengine = "sqlite"\npath   = "demo.db"\n' >"$projects/.locksql/config.toml"
printf '[profiles.p]\nurl         = "http://%s-rabbit:15672"\nuser        = "lockmq-ro"\ncredentials = "ask"\n' "$id" >"$projects/.lockmq/config.toml"
chmod 0644 "$projects/.locksql/config.toml" "$projects/.lockmq/config.toml" "$projects/demo.db"
echo sample >"$work/samples/a.txt"
chmod -R a+rX "$work/samples"
# The console accounts' own directories, given by the launchers' doctor --fix (as root, in the image).
(cd "$projects" && LOCKSQL_IMAGE=$sq_img "$lbin/locksql" doctor --fix --yes) >"$work/doctor.log" 2>&1 || true
(cd "$projects" && "$lbin/lockmq" doctor --fix --yes) >>"$work/doctor.log" 2>&1 || true
if [ "$(stat -c %u "$projects/.locksql")" != 10001 ] || [ "$(stat -c %u "$projects/.lockmq")" != 10011 ]; then
  cat "$work/doctor.log" >&2; die "doctor --fix did not give .locksql and .lockmq to the consoles"
fi
"$lbin/lockfile" profile add samples --source local --path /srv/samples </dev/null >"$work/profile.log" 2>&1 ||
  { cat "$work/profile.log" >&2; die "lockfile profile add failed"; }

# console NAME LOG EXPECT SEND... -- CMD...: CMD under drive.py in the background, from the
# projects directory (the launchers' project), until LOG says the console listens.
console() {
  n=$1 log=$2
  shift 2
  : >"$log"
  (cd "$projects" && exec python3 -I "$work/drive.py" "$log" "$@") </dev/null >/dev/null 2>&1 &
  drivers="$drivers $!"
  i=0
  until grep -q 'Listening' "$log"; do
    i=$((i + 1))
    if [ "$i" -ge 180 ]; then sed 's/\x1b\[[0-9;?]*[A-Za-z]//g' "$log" | tail -30 >&2; ko "$n: the console never listened"; return 1; fi
    sleep 1
  done
  ok "$n: the console listens: $(sed 's/\x1b\[[0-9;?]*[A-Za-z]//g' "$log" | grep -o 'Listening on [^[:space:]]*' | tail -1)"
}
cr=$(printf '\r')
# stop NAME: the console container NAME, removed; its driver ends with it.
stop() {
  docker rm -f "$1" >/dev/null 2>&1 || true
  i=0
  while docker container inspect "$1" >/dev/null 2>&1 && [ "$i" -lt 30 ]; do i=$((i + 1)); sleep 1; done
}
mq_console() {
  console "lockmq ($1)" "$work/lockmq-$2.log" '[y/N]' "y$cr" 'Password for' "$id-ro$cr" -- \
    env LOCKMQ_IMAGE="$3" LOCKMQ_CONSOLE_NAME="$id-lockmq" LOCKMQ_DOCKER_ARGS="--network $id-net" "$lbin/lockmq" console --profile p
}
lf_console() {
  console "lockfile ($1)" "$work/lockfile-$2.log" '[y/N]' "y$cr" -- \
    env LOCKFILE_IMAGE="$3" LOCKFILE_CONSOLE_NAME="$id-lockfile" LOCKFILE_DOCKER_ARGS="-v $work/samples:/srv/samples:ro" \
    "$lbin/lockfile" console --profile samples
}

say "the consoles: locksql by lea locksql, lockmq and lockfile by their launchers (release images)"
cat >"$work/lea-locksql.sh" <<'SH'
for f in "$LEA_ROOT"/libexec/lib/*.sh; do
  # shellcheck source=/dev/null
  . "$f"
done
lea_locksql "$@"
SH
console "locksql (lea locksql)" "$work/locksql.log" '[y/N]' "y$cr" -- \
  env PATH="$lbin:$PATH" LEA_ROOT="$root" LEA_PROJECTS_DIR="$projects" LEA_LOCKSQL=1 LEA_LOCKSQL_IMAGE="$sq_img" \
  sh "$work/lea-locksql.sh" --profile dev || true
mq_console "release image $mq_v" release "$mq_img" || true
lf_console "release image $lf_v" release "$lf_img" || true

# plain USER GROUP TOOL: TOOL's status call from lea's image, without lea (no entrypoint), as USER
# with the supplementary GROUP ("" for none), the socket volumes mounted as lea mounts them.
plain() {
  docker run --rm --network none --user "$1" ${2:+--group-add} ${2:+"$2"} -e HOME=/tmp -w "$projects" \
    --mount "type=bind,src=$projects,dst=$projects,readonly" -v locksql-run:/run/locksql -v lockmq-run:/run/lockmq \
    -v lockfile-run:/run/lockfile --entrypoint python3 lea:dev -I "$projects/.phase0/mcpcall.py" "$3" "${3}_status" 2>&1 | tr '\n' ' '
}
# answered TEXT: whether TEXT is an answer of an MCP server to the call (a result or an error).
answered() { case $1 in *'"call": {"jsonrpc": "2.0", "id": 2, '*) return 0 ;; esac; return 1; }
# running TOOL TEXT: whether TEXT, an answer of TOOL's status tool, says its console runs.
running() {
  case $1 in
    locksql) case $2 in *'"text": "dev: console running'*) return 0 ;; esac ;;
    lockmq) case $2 in *'"text": "profile p: console running'*) return 0 ;; esac ;;
    lockfile) case $2 in *'"text": "samples: console up'*) return 0 ;; esac ;;
  esac
  return 1
}
out=$(plain 10012:10012 10010 lockmq)
if running lockmq "$out"; then ok "control: the lockmq release image serves its own agent account (uid 10012, lockmq-clients)"; else ko "control: lockmq's agent account: $out"; fi
out=$(plain 10032:10032 10030 lockfile)
if running lockfile "$out"; then ok "control: the lockfile release image serves its own agent account (uid 10032, lockfile-clients)"; else ko "control: lockfile's agent account: $out"; fi

say "lea: a session wired for locksql, lockmq and lockfile (lea shell)"
cat >"$work/lea.conf" <<CONF
LEA_PREFIX=$id
LEA_IMAGE=lea:dev
LEA_PROXY_IMAGE=lea-proxy:dev
LEA_LOCKSQL=1
LEA_LOCKSQL_IMAGE=$sq_img
LEA_LOCKSQL_REF=v$sq_v
LEA_LOCKMQ=1
LEA_LOCKFILE=1
LEA_HUB=0
CONF
export LEA_CONFIG="$work/lea.conf"
state=$XDG_DATA_HOME/lea/state
mkdir -p "$state"
printf '{}\n' >"$state/claude.json"
# agent PHASE: inside.sh in lea's container (lea shell), its output in $work/agent-PHASE.out.
agent() {
  LEA_PROJECTS_DIR="$projects" "$root/bin/lea" shell -- sh "$projects/.phase0/inside.sh" locksql lockmq lockfile \
    </dev/null >"$work/agent-$1.out" 2>&1 || true
  cat "$work/agent-$1.out"
}
call() { sed -n "s/^call $1 //p" "$work/agent-$2.out"; }
agent release
c=$id-$lid
w=$(docker inspect -f '{{json .HostConfig.GroupAdd}} {{range .Mounts}}{{if eq .Type "volume"}}{{.Name}}:{{.Destination}}:{{.RW}} {{end}}{{end}}' "$c" 2>/dev/null) || w=
echo "      $c: $w"
miss=
for x in '"10000"' '"10010"' '"10030"' ' locksql-run:/run/locksql:true ' ' lockmq-run:/run/lockmq:true ' ' lockfile-run:/run/lockfile:true '; do
  case " $w " in *"$x"*) ;; *) miss="$miss $x" ;; esac
done
if [ -z "$miss" ]; then ok "lea's container: --group-add 10000 10010 10030, the volumes <tool>-run on /run/<tool>"; else ko "lea's container lacks:$miss"; fi
case $(sed -n 's/^id: //p' "$work/agent-release.out") in
  *"10000(locksql-clients)"*"10010(lockmq-clients)"*"10030(lockfile-clients)"*) ok "the session account (uid $uid) is in locksql-clients, lockmq-clients, lockfile-clients" ;;
  *) ko "the session account's groups: $(sed -n 's/^id: //p' "$work/agent-release.out")" ;;
esac
sq_ok=0
out=$(call locksql release)
if running locksql "$out"; then sq_ok=1; ok "lea's agent: locksql_status reaches the running locksql console"; else ko "lea's agent: locksql_status: $out"; fi
# The bug this test works around (see the header): the release images refuse lea's agents. Only
# meaningful when the same session reached locksql's console and the image's agent account
# reached the tool's (the controls above).
for t in lockmq lockfile; do
  out=$(call "$t" release)
  if [ "$sq_ok" = 1 ] && answered "$out" && ! running "$t" "$out"; then
    ok "known lea bug: the $t release image (no allowed_uids) refuses lea's agent (uid $uid): ${t}_status gets no console"
  else ko "the $t release image and lea's agent (uid $uid): $out"; fi
done

say "lockmq and lockfile again, from the release image plus allowed_uids = [$uid]"
stop "$id-lockmq"
stop "$id-lockfile"
mq_console "release image $mq_v, allowed_uids" allow "$id-lockmq:allow" || true
lf_console "release image $lf_v, allowed_uids" allow "$id-lockfile:allow" || true
agent allow
for t in locksql lockmq lockfile; do
  out=$(call "$t" allow)
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
