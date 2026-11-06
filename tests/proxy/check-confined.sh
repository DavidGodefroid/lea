#!/bin/sh
# check-confined.sh CONTAINER: checks that a running lea-proxy is confined as lea starts it
# (internal/launcher/net.go: proxyConfine): no capability but SETUID and SETGID, no-new-privileges,
# a read-only root, pids and memory limits, and no root process but, at most, PID 1.
# Prints one "ok"/"FAIL" line per check; exits 1 when one fails.
set -eu
name=$1
fail=0
# check WHAT GOT WANT
check() {
  if [ "$2" = "$3" ]; then echo "ok   proxy confined: $1"; else echo "FAIL proxy confined: $1: expected '$3', got '$2'"; fail=1; fi
}
f=$(docker inspect -f '{{json .HostConfig.CapDrop}}|{{json .HostConfig.CapAdd}}|{{json .HostConfig.SecurityOpt}}|{{.HostConfig.ReadonlyRootfs}}|{{.HostConfig.PidsLimit}}|{{.HostConfig.Memory}}' "$name")
IFS='|' read -r capdrop capadd secopt ro pids mem <<EOF
$f
EOF
# Docker reports the names as given or with the CAP_ prefix, depending on its version.
capdrop=$(printf '%s' "$capdrop" | sed 's/CAP_//g')
capadd=$(printf '%s' "$capadd" | tr -d '[]"' | tr ',' '\n' | sed 's/^CAP_//' | sort | tr '\n' ' ')
check "CapDrop" "$capdrop" '["ALL"]'
check "CapAdd" "$capadd" 'SETGID SETUID '
case $secopt in *'"no-new-privileges'*) secopt=yes ;; esac
check "no-new-privileges" "$secopt" yes
check "ReadonlyRootfs" "$ro" true
case $pids in ''|0|-1|'<nil>') pids=none ;; *) pids='set' ;; esac
check "pids limit" "$pids" set
case $mem in ''|0) mem=none ;; *) mem='set' ;; esac
check "memory limit" "$mem" set
# No root process but PID 1 (the entrypoint, which only waits). ps runs as root: left out.
roots=$(docker exec "$name" ps -o user=,pid=,args= | awk '$1 == "root" && $2 != 1 && $3 != "ps" { print }')
check "no root process but PID 1" "$roots" ""
exit "$fail"
