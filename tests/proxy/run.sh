#!/bin/sh
# Proxy rule tests: fake DNS and decoy servers on internal test networks; nothing real is contacted.
set -eu
root=$(cd "$(dirname "$0")/../.." && pwd -P)
docker build -q -t lea-proxy:dev -f "$root/proxy/Containerfile" "$root" >/dev/null
cd "$(dirname "$0")"
dc() { docker compose -f compose.yaml "$@"; }
# shellcheck disable=SC2317 # invoked via trap
cleanup() { dc down -v --remove-orphans >/dev/null 2>&1 || true; }
trap cleanup EXIT
dc up -d --build --wait >/dev/null
fail=0
# check NAME EXPECTED -- curl args...  EXPECTED is "<CONNECT status>/<HTTP status>" as curl reports them.
check() {
  name=$1 expected=$2; shift 3
  got=$(dc exec -T client curl -s -o /dev/null --max-time 15 -x http://198.51.100.2:3128 \
        -w '%{http_connect}/%{http_code}' "$@" || true)
  if [ "$got" = "$expected" ]; then echo "ok   $name ($got)"; else echo "FAIL $name: expected $expected, got $got"; fail=1; fi
}
check "public HTTP allowed"              "000/200" -- http://public.test/
check "public CONNECT 443 allowed"        "200/200" -- -p http://public.test:443/
check "public other port refused"         "000/403" -- http://public.test:8080/
check "public CONNECT other port refused" "403/000" -- -p http://public.test:8443/
check "rebinding to private refused"      "403/000" -- -p http://rebind.test:443/
check "loopback name refused"             "000/403" -- http://loop.test/
check "rebinding with a mixed public/private answer refused" "403/000" -- -p http://mixed.test:443/
check "IPv6-only private answer refused" "403/000" -- -p http://v6.test:443/
check "private literal refused"           "000/403" -- http://10.99.0.81/
check "internal name allowed on 443 with its SNI" "200/200" -- -k https://issues.test/
check "internal name: SNI of another vhost on its ingress refused" "200/000" -- -k --connect-to admin.test:443:issues.test:443 https://admin.test/
check "internal name: TLS without SNI refused" "200/000" -- -k --connect-to 10.99.0.83:443:issues.test:443 https://10.99.0.83/
check "internal name: plain bytes in the tunnel refused" "200/000" -- -p http://gitlab.com:443/
check "internal host refused on 22"       "403/000" -- -p http://gitlab.com:22/
check "internal literal allowed on 443"   "200/200" -- -p http://10.99.0.82:443/
check "internal literal refused on 80"    "000/403" -- http://10.99.0.82/
got=$(dc exec -T client curl -s --max-time 10 --noproxy '*' http://198.51.100.2:2222/ || true)
if [ "$got" = "ok" ]; then echo "ok   SSH forwarder 2222 reaches gitlab.com:22"; else echo "FAIL SSH forwarder 2222: got '$got'"; fail=1; fi
# The second LEA_GIT_SSH endpoint gets the next port.
got=$(dc exec -T client curl -s --max-time 10 --noproxy '*' http://198.51.100.2:2223/ || true)
if [ "$got" = "ok" ]; then echo "ok   SSH forwarder 2223 reaches forge.test:443"; else echo "FAIL SSH forwarder 2223: got '$got'"; fail=1; fi
exit "$fail"
