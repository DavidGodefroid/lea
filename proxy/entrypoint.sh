#!/bin/sh
# lea-proxy: renders squid.conf from the mounted internal-hosts, starts the TCP forwarders, runs squid
# and, when the launcher mounted an endpoints file, the key forwarder lea-keyproxy (port 4400).
set -eu
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
if [ ! -s /etc/lea/endpoints ]; then
  exec squid -N -f /etc/squid/squid.conf
fi
# The key forwarder reaches every upstream through the main squid: it starts once squid listens.
squid -N -f /etc/squid/squid.conf &
squid_pid=$!
wait_port 3128 "squid"
lea-keyproxy -endpoints /etc/lea/endpoints -listen :4400 -via http://127.0.0.1:3128 &
keyproxy_pid=$!
# An invalid endpoints file fails the container: no agent is left with a silent port 4400.
wait_port 4400 "lea-keyproxy"
# shellcheck disable=SC2317,SC2329 # invoked via trap
stop() { kill "$squid_pid" "$keyproxy_pid" 2>/dev/null || true; }
trap stop TERM INT
# The container lives as long as squid: when it exits (or is stopped), so does the forwarder.
status=0
wait "$squid_pid" || status=$?
# A trapped signal interrupts wait: wait again for squid's own exit.
if kill -0 "$squid_pid" 2>/dev/null; then wait "$squid_pid" || status=$?; fi
kill "$keyproxy_pid" 2>/dev/null || true
exit "$status"
