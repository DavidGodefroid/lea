#!/bin/sh
# End-to-end test of the locksql console's confinement (#101), without the locksql image: the real
# lea locksql (libexec/lib/locksql.sh) starts a stand-in console image that checks, from inside,
# what the console may do, while this script inspects the running container.
#   - docker inspect: CapDrop ALL, no-new-privileges, read-only root, pids and memory limits;
#   - inside: no effective capability, NoNewPrivs, the root and the projects directory read-only
#     (a write gets EROFS), the console's own .locksql directory mounted read-write;
#   - a .locksql that is a symbolic link is refused before any container starts.
# Linux hosts with docker (or podman). Creates lea-locksql-confine-* and locksql-console only.
set -eu
root=$(cd "$(dirname "$0")/../.." && pwd -P)
base=${LEA_LOCKSQL_CONFINE_IMAGE:-docker.io/library/alpine:3.20}
img=lea-locksql-confine:test
work=$(mktemp -d)
# shellcheck disable=SC2317 # run by the trap
cleanup() {
  docker rm -f locksql-console >/dev/null 2>&1 || true
  docker volume rm lea-locksql-confine-run lea-locksql-confine-home >/dev/null 2>&1 || true
  rm -rf "$work"
}
trap cleanup EXIT
fail=0
ok() { echo "ok    $1"; }
ko() { echo "FAIL  $1"; fail=1; }

# The stand-in console: the image's account, and a check script as its entrypoint (it gets
# "console --project DIR", as the locksql console does).
mkdir "$work/ctx"
cat >"$work/ctx/check" <<'EOF'
#!/bin/sh
p=$3
r() { if "$@" 2>/dev/null; then echo yes; else echo no; fi; }
echo "capeff=$(sed -n 's/^CapEff:[[:space:]]*//p' /proc/self/status)"
echo "nnp=$(sed -n 's/^NoNewPrivs:[[:space:]]*//p' /proc/self/status)"
echo "root-write=$(r touch /lea-confine)"
echo "projects-write=$(touch "$p/lea-confine" 2>&1)"
echo "projects-mount=$(awk -v d="$p" '$2 == d { print $4 }' /proc/mounts | cut -d, -f1)"
echo "locksql-mount=$(awk -v d="$p/.locksql" '$2 == d { print $4 }' /proc/mounts | cut -d, -f1)"
echo "tmp-write=$(r touch /tmp/lea-confine)"
echo "done"
sleep 30
EOF
# No RUN step: the account comes as files (nested builders may not run one).
printf 'root:x:0:0:root:/root:/bin/sh\nlocksql:x:10001:10001::/home/locksql:/bin/sh\n' >"$work/ctx/passwd"
printf 'root:x:0:\nlocksql:x:10001:\n' >"$work/ctx/group"
cat >"$work/ctx/Containerfile" <<EOF
FROM $base
COPY passwd group /etc/
COPY --chmod=0755 check /check
ENTRYPOINT ["/check"]
EOF
docker build -q -t "$img" -f "$work/ctx/Containerfile" "$work/ctx" >/dev/null

P=$work/projects
mkdir "$P"
# lea locksql as lea runs it, on this docker, with the stand-in image; the volumes of the real
# console are not touched: the stand-in gets its own.
sed 's/locksql-run:/lea-locksql-confine-run:/; s/locksql-home:/lea-locksql-confine-home:/' \
  "$root/libexec/lib/locksql.sh" >"$work/locksql.sh"
locksql_test() {
  # shellcheck disable=SC2016 # expanded by the inner shell
  env LEA_ROOT="$root" W="$work" LEA_PROJECTS_DIR="$P" LEA_LOCKSQL=1 LEA_LOCKSQL_IMAGE="$img" sh -c '
    for f in "$LEA_ROOT"/libexec/lib/*.sh; do . "$f"; done
    . "$W/locksql.sh"
    lea_locksql' locksql
}

ln -s "$work" "$P/.locksql"
if locksql_test </dev/null >"$work/out" 2>&1; then ko "a symbolic link as .locksql is refused"
elif grep -q "is not a directory" "$work/out" && ! docker inspect locksql-console >/dev/null 2>&1; then
  ok "a symbolic link as .locksql is refused, no console started"
else ko "a symbolic link as .locksql is refused: $(cat "$work/out")"; fi
rm "$P/.locksql"

locksql_test </dev/null >"$work/out" 2>&1 &
i=0
until grep -q '^done' "$work/out" 2>/dev/null; do
  i=$((i + 1))
  if [ "$i" -gt 120 ]; then echo "locksql-confine: the stand-in console never reported:" >&2; cat "$work/out" >&2; exit 1; fi
  sleep 0.5
done

got=$(docker inspect -f '{{json .HostConfig.CapDrop}}|{{json .HostConfig.SecurityOpt}}|{{.HostConfig.ReadonlyRootfs}}|{{.HostConfig.PidsLimit}}|{{.HostConfig.Memory}}' locksql-console)
# Podman lists the capabilities --cap-drop ALL dropped instead of ALL.
case $got in
  *'"ALL"'*'|'*no-new-privileges*'|true|256|1073741824' | *'"CAP_CHOWN"'*'"CAP_SETUID"'*'|'*no-new-privileges*'|true|256|1073741824')
    ok "docker inspect: CapDrop ALL, no-new-privileges, read-only root, pids 256, memory 1g" ;;
  *) ko "docker inspect: $got" ;;
esac
docker rm -f locksql-console >/dev/null 2>&1 || true
wait || true

v() { sed -n "s/^$1=//p" "$work/out"; }
# is KEY WANT WHAT: ok WHAT when the stand-in reported WANT for KEY.
is() { if [ "$(v "$1")" = "$2" ]; then ok "$3"; else ko "$3 (got '$(v "$1")')"; fi; }
is capeff 0000000000000000 "no effective capability"
is nnp 1 "no new privileges"
is root-write no "the root is read-only"
is tmp-write yes "/tmp is writable"
case $(v projects-write) in
  *"Read-only file system"*) ok "a write to the projects directory gets EROFS" ;;
  *) ko "a write to the projects directory: '$(v projects-write)'" ;;
esac
is projects-mount ro "the projects directory is mounted read-only"
is locksql-mount rw ".locksql is mounted read-write"
[ ! -e "$P/lea-confine" ] || ko "a file reached the projects directory"
exit "$fail"
