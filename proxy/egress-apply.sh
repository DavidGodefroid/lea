#!/bin/sh
# Replaces the egress allowlist of a running lea-proxy and reconfigures squid, without a restart
# (lea egress allow|deny, issue #345). Only the host runs it: lea's launcher, through
# `docker exec -u 0 -e LEA_EGRESS_ALLOW=<the new value> <proxy> egress-apply.sh`. Nothing in the
# agents' reach can: they have no engine socket of the host, and the allowlist lives on the
# proxy's /run tmpfs, which only root in this container writes.
#
# The list is rendered and checked as at start (render-squid-conf.sh --egress), squid -k parse
# checks the whole configuration with it, and the previous list comes back on any failure. squid
# then rereads its configuration on SIGHUP: open connections stay.
set -eu
conf=/run/lea-proxy
list=$conf/egress-allow
if [ ! -f "$list" ] || ! grep -q "dstdomain -n \"$list\"" "$conf/squid.conf"; then
  echo "lea-proxy: this proxy has no egress allowlist to change (LEA_EGRESS_ALLOW='*' or LockHTTP at its start)" >&2
  exit 3
fi
umask 022
new=$conf/egress-allow.new
/usr/local/bin/render-squid-conf.sh --egress >"$new" || { rm -f "$new"; exit 1; }
cp "$list" "$conf/egress-allow.old"
mv "$new" "$list"
if ! su-exec squid:squid squid -k parse -f "$conf/squid.conf" >/dev/null 2>&1; then
  mv "$conf/egress-allow.old" "$list"
  echo "lea-proxy: squid refused the new egress allowlist; the previous one stays" >&2
  exit 1
fi
rm -f "$conf/egress-allow.old"
printf '%s\n' "${LEA_EGRESS_ALLOW:-}" >"$conf/egress-allow.src.new"
mv "$conf/egress-allow.src.new" "$conf/egress-allow.src"
# Root holds no CAP_KILL: squid's own user signals it. Only the main squid reads the allowlist:
# the entrypoint wrote its pid.
pid=$(head -c 16 "$conf/squid.pid" | tr -cd 0-9)
if [ -z "$pid" ]; then echo "lea-proxy: no pid for squid in $conf/squid.pid" >&2; exit 1; fi
su-exec squid:squid kill -HUP "$pid"
