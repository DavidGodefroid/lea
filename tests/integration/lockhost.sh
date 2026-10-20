#!/bin/sh
# End-to-end test of the lockmq / lockfile wiring (#26), without lea's image: the real clients of
# the releases lea pins (sha256 of image/Containerfile), the real image/lock-host, and a stand-in
# console serving the socket as the console account, in containers sharing one kernel.
#   - an agent wired by lock_host (account + clients group, read-only mounts) reaches the socket
#     and the client accepts the console account behind it;
#   - an agent without the group is refused by the kernel;
#   - a socket served by another uid is refused by the client;
#   - lock_host refuses the session's own uid, root, and ids taken in the image.
# Linux hosts with docker (or podman) and network access to GitHub. Creates lea-lockhost-* only.
set -eu
root=$(cd "$(dirname "$0")/../.." && pwd -P)
img=${LEA_LOCKHOST_TEST_IMAGE:-docker.io/library/python:3.12-slim}
case $(uname -m) in x86_64) arch=amd64 ;; aarch64) arch=arm64 ;; *) echo "lockhost: no release for $(uname -m)" >&2; exit 1 ;; esac
work=$(mktemp -d)
# shellcheck disable=SC2317 # run by the trap
cleanup() {
  docker rm -f lea-lockhost-console >/dev/null 2>&1 || true
  # Files the containers' accounts own: removed from a container, as their root.
  docker run --rm -v "$work:/w" "$img" rm -rf /w/host >/dev/null 2>&1 || true
  rm -rf "$work"
}
trap cleanup EXIT
fail=0
ok() { echo "ok    $1"; }
ko() { echo "FAIL  $1"; fail=1; }

# The clients, checked against the pins of the image.
for t in lockmq lockfile; do
  v=$(sed -n "s/.*MQ=\([0-9.]*\) LF=\([0-9.]*\).*/\1 \2/p" "$root/image/Containerfile")
  case $t in lockmq) v=${v% *} ;; lockfile) v=${v#* } ;; esac
  pin=$(sed -n "s/.*$arch) mq=\([0-9a-f]*\) lf=\([0-9a-f]*\).*/\1 \2/p" "$root/image/Containerfile")
  case $t in lockmq) pin=${pin% *} ;; lockfile) pin=${pin#* } ;; esac
  a=${t}_${v}_linux_$arch.tar.gz
  curl -fsSL -o "$work/$a" "https://github.com/DavidGodefroid/$t/releases/download/v$v/$a"
  echo "$pin  $work/$a" | sha256sum -c >/dev/null || { echo "lockhost: $a does not match its pin" >&2; exit 1; }
  tar -xzf "$work/$a" -C "$work" "$t"
done
ok "clients of the pinned releases (sha256 of image/Containerfile)"

# The host side: system.toml (root-owned in the containers: this account maps to their root),
# the socket directory of the console account (991) and its clients group (990), mode 2710.
CU=991 CG=990 SU=2000
mkdir -p "$work/host/etc/lockmq" "$work/host/run/lockmq"
printf '%s\n' 'service_user = "lockmq"' 'client_group = "lockmq-clients"' 'socket_dir   = "/run/lockmq"' >"$work/host/etc/lockmq/system.toml"
chmod 0644 "$work/host/etc/lockmq/system.toml"
docker run --rm -v "$work/host/run/lockmq:/d" "$img" sh -c "chown $CU:$CG /d && chmod 2710 /d"

# console UID: a stand-in console as UID, answering one line on /run/lockmq/user-p.sock.
console() {
  docker rm -f lea-lockhost-console >/dev/null 2>&1 || true
  docker run -d --name lea-lockhost-console --network none --user "$1:$CG" -v "$work/host/run/lockmq:/run/lockmq" "$img" \
    python3 -c '
import os, socket
os.umask(0o007)
p = "/run/lockmq/user-p.sock"
if os.path.exists(p): os.unlink(p)
s = socket.socket(socket.AF_UNIX); s.bind(p); s.listen(4)
while True:
    c, _ = s.accept(); c.recv(65536); c.sendall(b"{\"jsonrpc\":\"2.0\",\"id\":1,\"error\":{\"code\":-1,\"message\":\"stand-in console\"}}\n"); c.close()
' >/dev/null
  i=0; until docker run --rm -v "$work/host/run/lockmq:/d" "$img" test -S /d/user-p.sock 2>/dev/null; do
    i=$((i + 1)); [ "$i" -lt 30 ] || { echo "lockhost: the stand-in console never listened" >&2; docker logs lea-lockhost-console >&2; exit 1; }
    sleep 1
  done
}

# agent SETUP: the client as the session account (SU), after SETUP (shell, as root) ran.
agent() {
  docker run --rm --network none -e LEA_UID=$SU -e LEA_GID=$SU \
    -v "$work/lockmq:/usr/local/bin/lockmq:ro" -v "$root/image/lock-host:/lock-host:ro" \
    -v "$work/host/etc/lockmq/system.toml:/etc/lockmq/system.toml:ro" -v "$work/host/run/lockmq:/run/lockmq:ro" \
    "$img" sh -c 'groupadd -g "$LEA_GID" agent && useradd -M -u "$LEA_UID" -g "$LEA_GID" agent && user=agent && . /lock-host && '"$1"' &&
      mkdir -p /tmp/.config/lockmq && printf "[profiles.p]\nurl = \"http://127.0.0.1:15672\"\n" >/tmp/.config/lockmq/config.toml &&
      chown -R "$LEA_UID" /tmp/.config && cd / && HOME=/tmp exec setpriv --reuid "$LEA_UID" --regid "$LEA_GID" --init-groups lockmq status --profile p 2>&1' || true
}

console $CU
out=$(agent "lock_host lockmq $CU $CG")
echo "      agent: $out"
case $out in
  *"permission denied"*|*"served by uid"*|*"no lockmq console"*) ko "a wired agent reaches the console: $out" ;;
  *) ok "a wired agent reaches the console's socket (read-only mount), served by the console account" ;;
esac
out=$(agent "useradd -r -M -u $CU lockmq")
echo "      agent: $out"
case $out in *"permission denied"*) ok "an agent outside lockmq-clients is refused by the kernel" ;; *) ko "an agent without the group: $out" ;; esac

# An impostor (uid 993, in the group) that could write to the directory serves the socket.
docker rm -f lea-lockhost-console >/dev/null
docker run --rm -v "$work/host/run/lockmq:/d" "$img" sh -c 'rm -f /d/user-p.sock && chmod 2770 /d'
console 993
out=$(agent "lock_host lockmq $CU $CG")
echo "      agent: $out"
case $out in *"served by uid 993"*) ok "a socket served by another uid is refused by the client" ;; *) ko "a socket of another uid: $out" ;; esac
docker rm -f lea-lockhost-console >/dev/null

# lock_host's refusals: the session's uid, root, a gid taken in the image.
for c in "$SU $CG" "0 $CG" "$CU 0" "$CU $SU" "$CU 100"; do
  out=$(docker run --rm --network none -e LEA_UID=$SU -e LEA_GID=$SU -v "$root/image/lock-host:/lock-host:ro" "$img" \
    sh -c 'groupadd -g "$LEA_GID" agent && useradd -M -u "$LEA_UID" -g "$LEA_GID" agent && user=agent && . /lock-host && lock_host lockmq '"$c"' 2>&1; id -nG agent')
  case $out in *lockmq-clients*) ko "lock_host lockmq $c wired the agent: $out" ;; *"left out"*) ok "lock_host lockmq $c: left out" ;; *) ko "lock_host lockmq $c: $out" ;; esac
done
exit "$fail"
