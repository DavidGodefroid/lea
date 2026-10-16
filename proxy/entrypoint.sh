#!/bin/sh
# lea-proxy: renders squid.conf from the mounted internal-hosts, starts the TCP forwarders, runs squid.
set -eu
/usr/local/bin/render-squid-conf.sh /etc/lea/internal-hosts >/etc/squid/squid.conf
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
exec squid -N -f /etc/squid/squid.conf
