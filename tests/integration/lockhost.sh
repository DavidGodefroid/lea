#!/bin/sh
# End-to-end test of the lockmq / lockfile wiring (#149), without lea's image: the real lockmq
# client of the release lea pins (sha256 of image/Containerfile), the accounts, system.toml and
# socket directories image/Containerfile gives lea's image, and a stand-in console serving the
# socket as the tool's console account in a named volume, in containers sharing one kernel.
#   - an agent wired as lea wires it (the volume on /run/<tool>, --group-add of the tool's clients
#     group) reaches the socket, with the volume mounted read-write or read-only, and the lockmq
#     client accepts the console account behind it;
#   - an agent without the group is refused by the kernel;
#   - a socket served by another uid is refused by the client.
# Linux hosts with docker (or podman) and network access to GitHub. Creates lea-lockhost-* only.
set -eu
root=$(cd "$(dirname "$0")/../.." && pwd -P)
img=${LEA_LOCKHOST_TEST_IMAGE:-docker.io/library/python:3.12-slim}
case $(uname -m) in x86_64) arch=amd64 ;; aarch64) arch=arm64 ;; *) echo "lockhost: no release for $(uname -m)" >&2; exit 1 ;; esac
work=$(mktemp -d)
run=lea-lockhost-$$
# shellcheck disable=SC2317 # run by the trap
cleanup() {
  docker rm -f "$run-console" >/dev/null 2>&1 || true
  docker volume rm "$run-lockmq" "$run-lockfile" >/dev/null 2>&1 || true
  rm -rf "$work"
}
trap cleanup EXIT
fail=0
ok() { echo "ok    $1"; }
ko() { echo "FAIL  $1"; fail=1; }

# The lockmq client, checked against the pin of the image.
v=$(sed -n "s/.*MQ=\([0-9.]*\) LF=.*/\1/p" "$root/image/Containerfile")
pin=$(sed -n "s/.*$arch) mq=\([0-9a-f]*\) lf=.*/\1/p" "$root/image/Containerfile")
a=lockmq_${v}_linux_$arch.tar.gz
curl -fsSL -o "$work/$a" "https://github.com/DavidGodefroid/lockmq/releases/download/v$v/$a"
echo "$pin  $work/$a" | sha256sum -c >/dev/null || { echo "lockhost: $a does not match its pin" >&2; exit 1; }
tar -xzf "$work/$a" -C "$work" lockmq
ok "lockmq client of the pinned release (sha256 of image/Containerfile)"

# tool UID GID MODE: the ids and the socket directory's mode image/Containerfile gives the tool.
if ! { grep -q 'groupadd -g 10010 lockmq-clients && useradd -r -u 10011 ' "$root/image/Containerfile" &&
  grep -q 'install -d -m 2710 -o 10011 -g 10010 /run/lockmq' "$root/image/Containerfile" &&
  grep -q 'groupadd -g 10030 lockfile-clients && useradd -r -u 10031 ' "$root/image/Containerfile" &&
  grep -q 'install -d -m 2750 -o 10031 -g 10030 /run/lockfile' "$root/image/Containerfile"; }; then
  echo "lockhost: image/Containerfile changed the tools' ids or modes: update this test" >&2; exit 1
fi
SU=2000

# volume TOOL UID GID MODE: TOOL's socket volume with the console's directory.
volume() {
  docker volume create "$run-$1" >/dev/null
  docker run --rm -v "$run-$1:/d" "$img" sh -c "chown $2:$3 /d && chmod $4 /d"
}

# console TOOL UID GID: a stand-in console as UID, answering one line on /run/TOOL/user-p.sock.
console() {
  docker rm -f "$run-console" >/dev/null 2>&1 || true
  docker run -d --name "$run-console" --network none --user "$2:$3" -v "$run-$1:/run/$1" "$img" python3 -c '
import os, socket, sys
os.umask(0o007)
p = "/run/" + sys.argv[1] + "/user-p.sock"
if os.path.exists(p): os.unlink(p)
s = socket.socket(socket.AF_UNIX); s.bind(p); s.listen(4)
while True:
    c, _ = s.accept(); c.recv(65536); c.sendall(b"{\"jsonrpc\":\"2.0\",\"id\":1,\"error\":{\"code\":-1,\"message\":\"stand-in console\"}}\n"); c.close()
' "$1" >/dev/null
  i=0; until docker run --rm -v "$run-$1:/d" "$img" test -S /d/user-p.sock 2>/dev/null; do
    i=$((i + 1)); [ "$i" -lt 30 ] || { echo "lockhost: the stand-in console never listened" >&2; docker logs "$run-console" >&2; exit 1; }
    sleep 1
  done
}

# agent TOOL UID GID OPTS CMD: CMD as the session account (SU), in a container with the tool's
# accounts and system.toml as in lea's image, and the socket volume mounted with OPTS (",readonly"
# or ""); GID is the --group-add ("" for none).
agent() {
  docker run --rm --network none ${3:+--group-add} ${3:+"$3"} -e SU=$SU -e T="$1" -e C="$5" \
    -v "$work/lockmq:/usr/local/bin/lockmq:ro" --mount "type=volume,src=$run-$1,dst=/run/$1$4" "$img" sh -c '
      useradd -r -M -u '"$2"' "$T" && mkdir -p "/etc/$T" &&
      printf "%s\n" "service_user = \"$T\"" "client_group = \"$T-clients\"" "socket_dir   = \"/run/$T\"" >"/etc/$T/system.toml" &&
      mkdir -p /tmp/.config/lockmq && printf "[profiles.p]\nurl = \"http://127.0.0.1:15672\"\n" >/tmp/.config/lockmq/config.toml &&
      chown -R "$SU" /tmp/.config && cd / && HOME=/tmp exec setpriv --reuid "$SU" --regid "$SU" --keep-groups sh -c "$C" 2>&1' || true
}
# shellcheck disable=SC2016 # expanded in the agent's container
connect='python3 -c "import socket,sys; s=socket.socket(socket.AF_UNIX); s.connect(sys.argv[1]); s.sendall(b\"{}\"); print(s.recv(200).decode())" /run/$T/user-p.sock'

for t in lockmq:10011:10010:2710 lockfile:10031:10030:2750; do
  IFS=: read -r name uid gid mode <<EOF
$t
EOF
  volume "$name" "$uid" "$gid" "$mode"
  console "$name" "$uid" "$gid"
  for opts in "" ",readonly"; do
    out=$(agent "$name" "$uid" "$gid" "$opts" "$connect")
    case $out in *"stand-in console"*) ok "$name: a wired agent connects to the socket (volume${opts:-, read-write})" ;; *) ko "$name: wired agent, volume$opts: $out" ;; esac
  done
  out=$(agent "$name" "$uid" "" "" "$connect")
  case $out in *"ermission denied"*) ok "$name: an agent outside $name-clients is refused by the kernel" ;; *) ko "$name: an agent without the group: $out" ;; esac
done

# The lockmq client accepts the console account behind the socket, and refuses another uid.
console lockmq 10011 10010
out=$(agent lockmq 10011 10010 ",readonly" "lockmq status --profile p")
echo "      agent: $out"
case $out in
  *"permission denied"*|*"served by uid"*|*"no lockmq console"*) ko "the lockmq client reaches the console: $out" ;;
  *) ok "the lockmq client reaches the console's socket, served by the console account" ;;
esac
docker rm -f "$run-console" >/dev/null
docker run --rm -v "$run-lockmq:/d" "$img" sh -c 'rm -f /d/user-p.sock && chmod 2770 /d'
console lockmq 993 10010
out=$(agent lockmq 10011 10010 ",readonly" "lockmq status --profile p")
echo "      agent: $out"
case $out in *"served by uid 993"*) ok "a socket served by another uid is refused by the client" ;; *) ko "a socket of another uid: $out" ;; esac
exit "$fail"
