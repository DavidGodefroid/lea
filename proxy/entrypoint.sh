#!/bin/sh
# lea-proxy: renders squid.conf from the mounted internal-hosts, starts the TCP forwarders, runs squid
# and, when the launcher mounted an endpoints file, the key forwarder lea-keyproxy (port 4400).
# LEA_LOCKHTTP=1: squid listens on the loopback only, behind a LockHTTP gate on :3130, which the
# agents and lea-keyproxy go through; no SSH or OTel forwarder. The gate is a container of its own
# that lea runs in this container's network namespace (lockhttp's docs/lea.md).
#
# Only this script runs as root (PID 1, which then only waits). lea starts the container with every
# capability dropped but SETUID and SETGID (for su-exec), no-new-privileges, a read-only root (tmpfs
# on /run, /tmp and /var/cache/squid) and pids and memory limits. Each service starts under a user
# of its own: squid as squid (never root: a squid started as root keeps a way back to it), the TCP
# forwarders as nobody, lea-keyproxy as the owner of the endpoints file (lea's user, who owns the
# key files).
set -eu
lockhttp=${LEA_LOCKHTTP:-0}
if [ "$lockhttp" = 1 ]; then
  # Raw TCP forwarders would be a way out the gate never sees.
  if [ -n "${LEA_GIT_SSH:-}" ]; then
    echo "lea-proxy: LEA_LOCKHTTP=1 refuses LEA_GIT_SSH: its SSH forwarders would bypass the LockHTTP gate" >&2
    exit 1
  fi
  if [ -n "${LEA_OTEL_UPSTREAM:-}" ]; then
    echo "lea-proxy: LEA_LOCKHTTP=1 refuses LEA_OTEL_UPSTREAM: its TCP forwarder would bypass the LockHTTP gate" >&2
    exit 1
  fi
  if [ ! -d /etc/lockhttp-gate ]; then
    echo "lea-proxy: LEA_LOCKHTTP=1 needs /etc/lockhttp-gate mounted by lea" >&2
    exit 1
  fi
  if [ "${LEA_LOCKHTTP_MITM:-0}" = 1 ] && [ ! -s /etc/lockhttp-gate/ca.pem ]; then
    echo "lea-proxy: LEA_LOCKHTTP_MITM=1 needs the CA's certificate in /etc/lockhttp-gate/ca.pem (lea writes it)" >&2
    exit 1
  fi
fi
# The gate's password of lea-keyproxy reaches lea-keyproxy only, not the other services.
kp_token=${LEA_KEYPROXY_TOKEN:-}
unset LEA_KEYPROXY_TOKEN
# The rendered configuration: on the /run tmpfs (the root is read-only).
conf=/run/lea-proxy
mkdir -p "$conf"
# owner FILE: the uid:gid of FILE.
owner() { stat -c %u:%g "$1"; }
# Root without CAP_DAC_OVERRIDE may not read a private file of lea's user: the rules are rendered
# as the owner of internal-hosts.
hosts_owner=$(owner /etc/lea/internal-hosts)
# The egress allowlist squid.conf reads (none under LockHTTP or with LEA_EGRESS_ALLOW='*'), and the
# value it comes from: lea egress allow|deny replaces both live (egress-apply.sh).
if [ "$lockhttp" != 1 ] && [ "${LEA_EGRESS_ALLOW:-}" != '*' ]; then
  /usr/local/bin/render-squid-conf.sh --egress >"$conf/egress-allow"
  printf '%s\n' "${LEA_EGRESS_ALLOW:-}" >"$conf/egress-allow.src"
  chmod 0644 "$conf/egress-allow" "$conf/egress-allow.src"
fi
su-exec "$hosts_owner" /usr/local/bin/render-squid-conf.sh /etc/lea/internal-hosts >"$conf/squid.conf"
su-exec "$hosts_owner" /usr/local/bin/render-squid-conf.sh --peek /etc/lea/internal-hosts >"$conf/peek.conf"
chmod 0644 "$conf/squid.conf" "$conf/peek.conf"
su-exec squid:squid squid -k parse -f "$conf/squid.conf"
# The services, none of them root, must be able to open the log streams owned by root.
chmod a+w /dev/stdout /dev/stderr 2>/dev/null || true
# One SSH forwarder per git endpoint of LEA_GIT_SSH, in order: port 2222, 2223 ... (the lea
# container's ssh configuration sends each host to its port).
port=2222
for ep in ${LEA_GIT_SSH:-}; do
  su-exec nobody:nobody socat TCP-LISTEN:$port,fork,reuseaddr "TCP:$ep" &
  port=$((port + 1))
done
if [ -n "${LEA_OTEL_UPSTREAM:-}" ]; then
  su-exec nobody:nobody socat TCP-LISTEN:4318,fork,reuseaddr "TCP:${LEA_OTEL_UPSTREAM}" &
fi
# wait_port PORT WHAT: wait until 127.0.0.1:PORT accepts connections, or fail the container.
wait_port() {
  i=0
  until nc -z 127.0.0.1 "$1" 2>/dev/null; do
    i=$((i + 1))
    if [ "$i" -gt 100 ]; then echo "lea-proxy: $2 did not start" >&2; exit 1; fi
    sleep 0.1
  done
}
# The peek squid (internal names only) listens before the main one starts, which probes it as a peer.
if [ -s "$conf/peek.conf" ]; then
  # Its certificate only makes its port valid for ssl-bump: never presented, fresh per start, made
  # by squid in a directory of its own on the /tmp tmpfs (no root-owned key to hand over).
  su-exec squid:squid sh -c 'umask 077 && mkdir /tmp/lea-peek && exec openssl req -x509 -newkey ec \
    -pkeyopt ec_paramgen_curve:P-256 -nodes -days 3650 -subj /CN=lea-proxy \
    -keyout /tmp/lea-peek/lea-peek.pem -out /tmp/lea-peek/lea-peek.pem 2>/dev/null'
  su-exec squid:squid squid -k parse -f "$conf/peek.conf"
  su-exec squid:squid squid -N -f "$conf/peek.conf" &
  wait_port 3129 "the peek squid"
fi
# The forge forwarder (port 4401, issue #103) holds the GitHub token of lea github login: the
# launcher always mounts its key directory, so lea-keyproxy runs even without endpoints.
forge=0
if [ -d /etc/lea/forge ]; then forge=1; fi
if [ ! -s /etc/lea/endpoints ] && [ "$forge" = 0 ]; then
  # squid becomes PID 1: no root process is left in the container. Its pid is the one
  # egress-apply.sh signals.
  echo "$$" >"$conf/squid.pid"
  exec su-exec squid:squid squid -N -f "$conf/squid.conf"
fi
# lea-keyproxy runs as the owner of the endpoints file lea wrote: lea's user, who owns the key
# files (which root without CAP_DAC_OVERRIDE could not read either). Under rootless Podman, lea's
# user is the container's root: lea-keyproxy then runs as that root, lea's user on the host. Each
# key file must be readable by it: a proxy that could not forward a key fails at start. Without
# endpoints, the owner of the forge key directory (lea's user too, who wrote the GitHub token).
keys=''
if [ -s /etc/lea/endpoints ]; then
  kp_user=$(owner /etc/lea/endpoints)
  # shellcheck disable=SC2016 # awk's fields
  keys=$(su-exec "$kp_user" awk '!/^[[:space:]]*(#|$)/ && $3 != "" { print $3 }' /etc/lea/endpoints)
else
  kp_user=$(owner /etc/lea/forge)
fi
# (Tested as that user: root without CAP_DAC_READ_SEARCH may not even look into the directory.)
if [ "$forge" = 1 ] && ! su-exec "$kp_user" sh -c '[ ! -e /etc/lea/forge/github.com ] || [ -r /etc/lea/forge/github.com ]'; then
  echo "lea-proxy: the GitHub token /etc/lea/forge/github.com is not readable by $kp_user" >&2
  exit 1
fi
for key in $keys; do
  if ! su-exec "$kp_user" test -r "$key"; then
    echo "lea-proxy: the key file $key is not readable by the owner ($kp_user) of the endpoints file" >&2
    exit 1
  fi
done
# The key forwarder reaches every upstream through the main squid, or through the gate, which
# reaches squid: it starts once squid listens. The gate may start later: until then, the key
# forwarder's requests fail (closed).
su-exec squid:squid squid -N -f "$conf/squid.conf" &
squid_pid=$!
echo "$squid_pid" >"$conf/squid.pid"
wait_port 3128 "squid"
via=http://127.0.0.1:3128
if [ "$lockhttp" = 1 ]; then via=http://127.0.0.1:3130; fi
set -- -endpoints /etc/lea/endpoints -listen :4400 -via "$via"
if [ "$forge" = 1 ]; then set -- "$@" -forge-listen :4401 -forge-keys /etc/lea/forge; fi
if [ "$lockhttp" = 1 ]; then set -- "$@" -sources /etc/lockhttp-gate/sources.json; fi
# The gate decrypts and re-signs with lea's CA: keyproxy trusts its certificate (never the key).
if [ "${LEA_LOCKHTTP_MITM:-0}" = 1 ]; then set -- "$@" -ca-cert /etc/lockhttp-gate/ca.pem; fi
if [ "$lockhttp" = 1 ]; then
  LEA_KEYPROXY_TOKEN=$kp_token su-exec "$kp_user" lea-keyproxy "$@" &
else
  su-exec "$kp_user" lea-keyproxy "$@" &
fi
# An invalid endpoints file fails the container: no agent is left with a silent port 4400 (or 4401).
if [ -s /etc/lea/endpoints ]; then wait_port 4400 "lea-keyproxy"; fi
if [ "$forge" = 1 ]; then wait_port 4401 "lea-keyproxy's forge forwarder"; fi
unset kp_token
# Root without CAP_KILL may not signal the services (other users): on a stop, PID 1 exits, and the
# kernel ends every other process of the container with it.
trap 'exit 143' TERM
trap 'exit 130' INT
# The container lives as long as squid: when it exits, PID 1 exits, and the other services with it.
status=0
wait "$squid_pid" || status=$?
exit "$status"
