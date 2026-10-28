#!/bin/sh
# lea-proxy: renders squid.conf from the mounted internal-hosts, starts the TCP forwarders, runs squid
# and, when the launcher mounted an endpoints file, the key forwarder lea-keyproxy (port 4400).
# LEA_LOCKHTTP=1: squid listens on the loopback only, behind a LockHTTP gate on :3130 (its own uid,
# never 0), which the agents and lea-keyproxy go through; no SSH forwarder.
set -eu
lockhttp=${LEA_LOCKHTTP:-0}
gate_uid=10003
if [ "$lockhttp" = 1 ]; then
  if [ -n "${LEA_GIT_SSH:-}" ]; then
    echo "lea-proxy: LEA_LOCKHTTP=1 refuses LEA_GIT_SSH: its SSH forwarders would bypass the LockHTTP gate" >&2
    exit 1
  fi
  if [ ! -x /usr/local/bin/lockhttp ] || [ ! -d /run/lockhttp ] || [ ! -d /etc/lockhttp-gate ]; then
    echo "lea-proxy: LEA_LOCKHTTP=1 needs the lockhttp binary, /run/lockhttp and /etc/lockhttp-gate mounted by lea" >&2
    exit 1
  fi
fi
/usr/local/bin/render-squid-conf.sh /etc/lea/internal-hosts >/etc/squid/squid.conf
/usr/local/bin/render-squid-conf.sh --peek /etc/lea/internal-hosts >/etc/squid/peek.conf
squid -k parse -f /etc/squid/squid.conf
# squid drops to user "squid", which must be able to open the log streams owned by root.
chmod a+w /dev/stdout /dev/stderr 2>/dev/null || true
# One SSH forwarder per git endpoint of LEA_GIT_SSH, in order: port 2222, 2223 ... (the lea
# container's ssh configuration sends each host to its port).
port=2222
for ep in ${LEA_GIT_SSH:-}; do
  socat TCP-LISTEN:$port,fork,reuseaddr "TCP:$ep" &
  port=$((port + 1))
done
if [ -n "${LEA_OTEL_UPSTREAM:-}" ]; then
  socat TCP-LISTEN:4318,fork,reuseaddr "TCP:${LEA_OTEL_UPSTREAM}" &
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
if [ -s /etc/squid/peek.conf ]; then
  # Its certificate only makes its port valid for ssl-bump: never presented, fresh per start.
  openssl req -x509 -newkey ec -pkeyopt ec_paramgen_curve:P-256 -nodes -days 3650 -subj /CN=lea-proxy \
    -keyout /etc/squid/lea-peek.pem -out /etc/squid/lea-peek.pem 2>/dev/null
  chown squid /etc/squid/lea-peek.pem
  chmod 0400 /etc/squid/lea-peek.pem
  squid -k parse -f /etc/squid/peek.conf
  squid -N -f /etc/squid/peek.conf &
  wait_port 3129 "the peek squid"
fi
if [ "$lockhttp" != 1 ] && [ ! -s /etc/lea/endpoints ]; then
  unset LEA_KEYPROXY_TOKEN
  exec squid -N -f /etc/squid/squid.conf
fi
# The gate and the key forwarder reach every upstream through the main squid: they start once it
# listens.
squid -N -f /etc/squid/squid.conf &
squid_pid=$!
wait_port 3128 "squid"
gate_pid='' keyproxy_pid=''
via=http://127.0.0.1:3128
if [ "$lockhttp" = 1 ]; then
  # The gate writes its audit and status as its own uid; the console authenticates it by that uid
  # on /run/lockhttp, which only the clients group may enter: Docker adds its gid
  # (LEA_LOCKHTTP_GID), rootless Podman keeps the user's groups (--group-add keep-groups).
  mkdir -p /var/log/lockhttp /run/lockhttp-gate
  chown "$gate_uid:$gate_uid" /var/log/lockhttp /run/lockhttp-gate
  if [ -n "${LEA_LOCKHTTP_GID:-}" ]; then groups="--groups $LEA_LOCKHTTP_GID"; else groups=--keep-groups; fi
  # shellcheck disable=SC2086 # groups, split on purpose
  env -u LEA_KEYPROXY_TOKEN LOCKHTTP_RUNTIME_DIR=/run \
    setpriv --reuid "$gate_uid" --regid "$gate_uid" $groups --inh-caps=-all --no-new-privs -- \
    lockhttp gate --listen 0.0.0.0:3130 --upstream http://127.0.0.1:3128 --profile "${LEA_LOCKHTTP_PROFILE:-default}" \
    --sources /etc/lockhttp-gate/sources.json --audit-dir /var/log/lockhttp --status-file /run/lockhttp-gate/status.json &
  gate_pid=$!
  wait_port 3130 "the LockHTTP gate"
  via=http://127.0.0.1:3130
fi
if [ -s /etc/lea/endpoints ]; then
  if [ "$lockhttp" = 1 ]; then
    lea-keyproxy -endpoints /etc/lea/endpoints -listen :4400 -via "$via" -sources /etc/lockhttp-gate/sources.json &
  else
    lea-keyproxy -endpoints /etc/lea/endpoints -listen :4400 -via "$via" &
  fi
  keyproxy_pid=$!
  # An invalid endpoints file fails the container: no agent is left with a silent port 4400.
  wait_port 4400 "lea-keyproxy"
fi
unset LEA_KEYPROXY_TOKEN
# shellcheck disable=SC2317,SC2329 # invoked via trap
stop() {
  # shellcheck disable=SC2086 # empty pids drop out
  kill "$squid_pid" $gate_pid $keyproxy_pid 2>/dev/null || true
}
trap stop TERM INT
# The container lives as long as squid: when it exits (or is stopped), so does the forwarder.
status=0
wait "$squid_pid" || status=$?
# A trapped signal interrupts wait: wait again for squid's own exit.
if kill -0 "$squid_pid" 2>/dev/null; then wait "$squid_pid" || status=$?; fi
# shellcheck disable=SC2086
kill $gate_pid $keyproxy_pid 2>/dev/null || true
exit "$status"
