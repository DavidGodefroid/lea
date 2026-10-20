#!/bin/sh
# lea-proxy: renders squid.conf from the mounted internal-hosts, starts the TCP forwarders, runs squid.
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
# The peek squid (internal names only) listens before the main one starts, which probes it as a peer.
if [ -s /etc/squid/peek.conf ]; then
  # Its certificate only makes its port valid for ssl-bump: never presented, fresh per start.
  openssl req -x509 -newkey ec -pkeyopt ec_paramgen_curve:P-256 -nodes -days 3650 -subj /CN=lea-proxy \
    -keyout /etc/squid/lea-peek.pem -out /etc/squid/lea-peek.pem 2>/dev/null
  chown squid /etc/squid/lea-peek.pem
  chmod 0400 /etc/squid/lea-peek.pem
  squid -k parse -f /etc/squid/peek.conf
  squid -N -f /etc/squid/peek.conf &
  i=0
  until nc -z 127.0.0.1 3129 2>/dev/null; do
    i=$((i + 1))
    if [ "$i" -gt 100 ]; then echo "lea-proxy: the peek squid did not start" >&2; exit 1; fi
    sleep 0.1
  done
fi
exec squid -N -f /etc/squid/squid.conf
