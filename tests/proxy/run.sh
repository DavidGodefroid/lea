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
# PROXY: the proxy under test: 198.51.100.2 with LEA_EGRESS_ALLOW='*', 198.51.100.3 with an allowlist,
# 198.51.100.4 with lea's default one.
PROXY=198.51.100.2
check() {
  name=$1 expected=$2; shift 3
  got=$(dc exec -T client curl -s -o /dev/null --max-time 15 -x "http://$PROXY:3128" \
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
check "open: DoH resolver refused"         "403/000" -- -p http://dns.google:443/
check "open: DoH resolver subdomain refused" "403/000" -- -p http://mozilla.cloudflare-dns.com:443/
check "open: doh.* name refused"           "403/000" -- -p http://doh.example.test:443/
check "open: public IPv4 literal refused"  "403/000" -- -p http://198.51.100.80:443/
check "open: public IPv4 literal refused over HTTP" "000/403" -- http://198.51.100.80/
check "open: public IPv4 literal in decimal refused" "403/000" -- -p http://3325256784:443/
check "open: public IPv6 literal refused"  "403/000" -- -p "http://[2001:db8::80]:443/"
page=$(dc exec -T client curl -s --max-time 15 -x "http://$PROXY:3128" "http://dns.google/resolve?name=example.com" || true)
case $page in *DNS-over-HTTPS*) echo "ok   open: the DoH refusal page says why" ;;
  *) echo "FAIL open: DoH refusal page: $page"; fail=1 ;; esac
PROXY=198.51.100.4
check "default: the model API allowed"           "200/200" -- -p http://api.anthropic.com:443/
check "default: github.com allowed"              "200/200" -- -p http://github.com:443/
check "default: DoH resolver refused"            "403/000" -- -p http://dns.google:443/
check "default: DoH resolver literal refused"    "403/000" -- -p http://1.1.1.1:443/
check "default: public IPv4 literal refused"     "403/000" -- -p http://198.51.100.80:443/
check "default: unlisted domain refused"         "403/000" -- -p http://other.test:443/
check "default: internal host still allowed"     "200/200" -- -p http://10.99.0.82:443/
page=$(dc exec -T client curl -s --max-time 15 -x "http://$PROXY:3128" "http://dns.google/resolve?name=example.com" || true)
case $page in *LEA_EGRESS_ALLOW*) echo "ok   default: the DoH refusal page names LEA_EGRESS_ALLOW" ;;
  *) echo "FAIL default: DoH refusal page: $page"; fail=1 ;; esac
PROXY=198.51.100.3
check "allowlist: listed domain allowed"            "200/200" -- -p http://public.test:443/
check "allowlist: listed domain over HTTP allowed"  "000/200" -- http://public.test/
check "allowlist: subdomain allowed"                "200/200" -- -p http://sub.public.test:443/
check "allowlist: other domain refused"             "403/000" -- -p http://other.test:443/
check "allowlist: other domain over HTTP refused"   "000/403" -- http://other.test/
check "allowlist: public literal refused"           "403/000" -- -p http://198.51.100.80:443/
check "allowlist: internal host still allowed"      "200/200" -- -p http://10.99.0.82:443/
check "allowlist: private still refused"            "403/000" -- -p http://rebind.test:443/
page=$(dc exec -T client curl -s --max-time 15 -x http://198.51.100.3:3128 http://other.test/ || true)
case $page in *LEA_EGRESS_ALLOW*) echo "ok   allowlist: the refusal page names LEA_EGRESS_ALLOW" ;;
  *) echo "FAIL allowlist: refusal page: $page"; fail=1 ;; esac
# A refused plain HTTP request carries the live command in its body's first line (apt and wget log it).
first=$(printf '%s\n' "$page" | head -n1)
if [ "$first" = "lea-proxy refused other.test: not in the egress allowlist. To allow it, run on the host: lea egress allow other.test (applies live, no restart; there is no one-off form: it stays until lea egress deny other.test)." ]; then
  echo "ok   allowlist: the refusal's first line names lea egress allow"
else echo "FAIL allowlist: refusal first line: $first"; fail=1; fi
# lea egress denied reads the refusal's error name from the access log.
if dc logs proxy-allow 2>&1 | grep -Eq 'CONNECT other\.test:443 403 [^ ]+ ERR_LEA_EGRESS$'; then echo "ok   allowlist: the access log names ERR_LEA_EGRESS"
else echo "FAIL allowlist: no ERR_LEA_EGRESS log line:"; dc logs proxy-allow 2>&1 | grep other.test | tail -n 3; fail=1; fi
# lea egress allow|deny (#345): the host's live change, as lea runs it (docker exec as root,
# egress-apply.sh): no proxy restart (same container, same squid), the floor unchanged.
# The container, its start and squid's pid, which must be alive (a restarted squid gets another).
proxy_state() {
  id=$(dc ps -q proxy-allow)
  # shellcheck disable=SC2016 # expanded in the proxy
  printf '%s %s %s' "$id" "$(docker inspect -f '{{.State.StartedAt}}' "$id")" \
    "$(dc exec -T proxy-allow sh -c 'p=$(cat /run/lea-proxy/squid.pid) && [ -d "/proc/$p" ] && echo "$p"')"
}
before=$(proxy_state)
apply() { dc exec -T -u 0 -e "LEA_EGRESS_ALLOW=$1" proxy-allow /usr/local/bin/egress-apply.sh; }
if apply public.test,other.test,dns.google; then echo "ok   live allow: egress-apply.sh applied"; else echo "FAIL live allow: egress-apply.sh failed"; fail=1; fi
sleep 1
check "live allow: the domain is allowed"             "200/200" -- -p http://other.test:443/
check "live allow: over HTTP too"                     "000/200" -- http://other.test/
check "live allow: the listed domain still allowed"   "200/200" -- -p http://public.test:443/
check "live allow: a DoH resolver stays refused"      "403/000" -- -p http://dns.google:443/
check "live allow: a public literal stays refused"    "403/000" -- -p http://198.51.100.80:443/
check "live allow: private stays refused"             "403/000" -- -p http://rebind.test:443/
after=$(proxy_state)
if [ "$before" = "$after" ] && [ -n "${before#* }" ]; then echo "ok   live allow: same proxy container and squid ($after)"
else echo "FAIL live allow: the proxy restarted: '$before' then '$after'"; fail=1; fi
if dc exec -T proxy-allow cat /run/lea-proxy/egress-allow.src | grep -qx 'public.test,other.test,dns.google'; then echo "ok   live allow: the proxy records the value it applies"
else echo "FAIL live allow: egress-allow.src"; fail=1; fi
if apply 10.0.0.1 2>/dev/null; then echo "FAIL live allow: an IP literal was accepted"; fail=1; else echo "ok   live allow: an invalid list is refused"; fi
check "live allow: a refused change keeps the list"   "200/200" -- -p http://other.test:443/
if apply public.test; then echo "ok   live deny: egress-apply.sh applied"; else echo "FAIL live deny: egress-apply.sh failed"; fail=1; fi
sleep 1
check "live deny: the domain is refused again"        "403/000" -- -p http://other.test:443/
check "live deny: the listed domain still allowed"    "200/200" -- -p http://public.test:443/
if [ "$(proxy_state)" = "$before" ]; then echo "ok   live deny: same proxy container and squid"; else echo "FAIL live deny: the proxy restarted"; fail=1; fi
if dc exec -T -u 0 -e LEA_EGRESS_ALLOW=other.test proxy /usr/local/bin/egress-apply.sh 2>/dev/null; then
  echo "FAIL live allow on LEA_EGRESS_ALLOW='*': accepted"; fail=1
else echo "ok   live allow: refused by a proxy without an allowlist ('*')"; fi
got=$(dc exec -T client curl -s --max-time 10 --noproxy '*' http://198.51.100.2:2222/ || true)
if [ "$got" = "ok" ]; then echo "ok   SSH forwarder 2222 reaches gitlab.com:22"; else echo "FAIL SSH forwarder 2222: got '$got'"; fail=1; fi
# The second LEA_GIT_SSH endpoint gets the next port.
got=$(dc exec -T client curl -s --max-time 10 --noproxy '*' http://198.51.100.2:2223/ || true)
if [ "$got" = "ok" ]; then echo "ok   SSH forwarder 2223 reaches forge.test:443"; else echo "FAIL SSH forwarder 2223: got '$got'"; fail=1; fi
# The key forwarder (port 4400): the agent never holds the key, the upstream gets it.
# fwd NAME EXPECTED_STATUS PATTERN -- curl args...  PATTERN is a grep -x line of the body ("" for none).
fwd() {
  name=$1 expected=$2 pattern=$3; shift 4
  out=$(dc exec -T client curl -s --max-time 15 --noproxy '*' -w '\n%{http_code}' "$@" || true)
  code=$(printf '%s\n' "$out" | tail -n1)
  body=$(printf '%s\n' "$out" | sed '$d')
  if [ "$code" != "$expected" ]; then echo "FAIL $name: expected $expected, got $code"; fail=1; return; fi
  if [ -n "$pattern" ] && ! printf '%s\n' "$body" | grep -qx -- "$pattern"; then
    echo "FAIL $name: no line '$pattern' in the answer"; fail=1; return
  fi
  echo "ok   $name ($code)"
}
kp=http://198.51.100.2:4400
agent="-H Authorization:Bearer-agent-token -H X-Api-Key:agent -H Cookie:c=1 -H X-Forwarded-For:1.2.3.4"
# shellcheck disable=SC2086 # $agent is several curl arguments
{
  fwd "forwarder injects the key"          200 "authorization=Bearer test-key-123" -- $agent "$kp/llm/responses?stream=true"
  fwd "forwarder strips X-Api-Key"         200 "x-api-key="      -- $agent "$kp/llm/responses"
  fwd "forwarder strips Cookie"            200 "cookie="         -- $agent "$kp/llm/responses"
  fwd "forwarder strips X-Forwarded-For"   200 "x-forwarded-for=" -- $agent "$kp/llm/responses"
  fwd "forwarder joins the base path"      200 "path=/v1/responses?stream=true" -- -X POST -d '{}' "$kp/llm/responses?stream=true"
  fwd "forwarder sets the upstream Host"   200 "host=10.99.0.84:8000" -- "$kp/llm/models"
}
got=$(dc exec -T client curl -s -o /dev/null -D - --max-time 15 --noproxy '*' "$kp/llm/models" | grep -ci '^set-cookie' || true)
if [ "$got" = "0" ]; then echo "ok   forwarder drops Set-Cookie"; else echo "FAIL forwarder passed Set-Cookie"; fail=1; fi
fwd "forwarder: unknown name 404"        404 "" -- "$kp/nope/models"
fwd "forwarder: '..' path refused"       400 "" -- --path-as-is "$kp/llm/../rebind/x"
fwd "forwarder: encoded '..' refused"    400 "" -- --path-as-is "$kp/llm/%2e%2e/x"
fwd "forwarder: '..;' segment refused"    400 "" -- --path-as-is "$kp/llm/..;/x"
fwd "forwarder: PUT refused"             405 "" -- -X PUT "$kp/llm/models"
fwd "forwarder: private upstream not in internal-hosts refused by squid" 403 "" -- "$kp/rebind/models"
got=$(dc exec -T outsider curl -s -o /dev/null --max-time 15 --noproxy '*' -w '%{http_code}' http://203.0.113.2:4400/llm/models || true)
if [ "$got" = "403" ]; then echo "ok   forwarder refuses the egress network (403)"; else echo "FAIL forwarder on the egress network: expected 403, got $got"; fail=1; fi
# The forge forwarder (port 4401, issue #103): the GitHub token stays in the proxy.
fg=http://198.51.100.2:4401
fwd "forge: an SSH key on the account refused"  403 "" -- -X POST -H Host:api.github.com "$fg/user/keys"
fwd "forge: a webhook refused"                   403 "" -- -X POST -H Host:api.github.com "$fg/repos/o/r/hooks"
# An allowlist, not a denylist: the ID aliases, empty segments, access grants, Actions secrets and
# runners, and GraphQL mutations off the list are refused before they leave the proxy.
fwd "forge: an ID alias refused"                 403 "" -- -X POST -H Host:api.github.com "$fg/repositories/1/keys"
fwd "forge: an org ID alias refused"             403 "" -- -X POST -H Host:api.github.com "$fg/organizations/1/hooks"
fwd "forge: an empty segment refused"            400 "" -- --path-as-is -X POST -H Host:api.github.com "$fg//repos/o/r/keys"
fwd "forge: a collaborator refused"              403 "" -- -X PUT -H Host:api.github.com "$fg/repos/o/r/collaborators/evil"
fwd "forge: a transfer refused"                  403 "" -- -X POST -H Host:api.github.com "$fg/repos/o/r/transfer"
fwd "forge: a repository's settings refused"     403 "" -- -X PATCH -H Host:api.github.com "$fg/repos/o/r"
fwd "forge: an org membership refused"           403 "" -- -X PUT -H Host:api.github.com "$fg/orgs/o/memberships/evil"
fwd "forge: an Actions secret refused"           403 "" -- -X PUT -H Host:api.github.com "$fg/repos/o/r/actions/secrets/X"
fwd "forge: a runner token refused"              403 "" -- -X POST -H Host:api.github.com "$fg/repos/o/r/actions/runners/registration-token"
fwd "forge: a GraphQL mutation off the list refused" 403 "" -- -X POST -H Host:api.github.com -H Content-Type:application/json \
  --data '{"query":"mutation { a: transferRepository(input: {}) { clientMutationId } }"}' "$fg/graphql"
fwd "forge: github.com's web refused"            403 "" -- -X POST -H Host:lea-proxy:4401 "$fg/login/oauth/access_token"
# What lea's gh and git make goes out (502: api.github.com is a plain web server here).
fwd "forge: an issue comment goes out"           502 "" -- -X POST -H Host:api.github.com "$fg/repos/o/r/issues/1/comments"
# shellcheck disable=SC2016 # GraphQL variables, not shell ones
fwd "forge: a GraphQL mutation of gh pr goes out" 502 "" -- -X POST -H Host:api.github.com -H Content-Type:application/json \
  --data '{"query":"mutation($i: CreatePullRequestInput!) { createPullRequest(input: $i) { pullRequest { url } } }","variables":{"i":{}}}' "$fg/graphql"
fwd "forge: TRACE refused"                       405 "" -- -X TRACE -H Host:api.github.com "$fg/user"
fwd "forge: another port of the proxy's refused" 400 "" -- -H Host:lea-proxy:3128 "$fg/x"
# Another host (a redirect gh follows: job logs at *.blob.core.windows.net) goes to that host,
# without the token, under the egress rules: never to github.com.
fwd "forge: another host goes to that host"      502 "" -- -H Host:public.test -H "Authorization: token placeholder" "$fg/actions-results/x?sig=sas-not-logged"
# The egress rules apply to it: squid refuses the CONNECT to a DoH resolver (the forwarder answers
# 502; squid's log line below says 403), here on the open proxy (the other test proxies' client
# network is their default route, which the forwarder refuses).
fwd "forge: another host follows the egress rules" 502 "" -- -H Host:dns.google "$fg/actions-results/x"
fwd "forge: git at the proxy's address is github.com" 502 "" -- -H Host:198.51.100.2:4401 "$fg/o/r.git/info/refs"
fwd "forge: '..' path refused"                   400 "" -- --path-as-is -H Host:api.github.com "$fg/repos/../user"
# api.github.com resolves to a plain web server here: the forwarder's TLS to it fails (502).
fwd "forge: a call goes out over TLS through squid" 502 "" -- -H Host:api.github.com -H "Authorization: token placeholder" "$fg/user"
fwd "forge: runs without endpoints"              403 "" -- -X POST -H Host:api.github.com http://198.51.100.3:4401/user/keys
got=$(dc exec -T outsider curl -s -o /dev/null --max-time 15 --noproxy '*' -w '%{http_code}' -H Host:api.github.com http://203.0.113.2:4401/user || true)
if [ "$got" = "403" ]; then echo "ok   forge forwarder refuses the egress network (403)"; else echo "FAIL forge forwarder on the egress network: expected 403, got $got"; fail=1; fi
if dc logs proxy 2>&1 | grep -q 'forge api.github.com "GET" "/user" 502'; then echo "ok   forge: the forwarder logs host, method, path and status"
else echo "FAIL forge: no log line of the forwarded call"; fail=1; fi
for l in 'proxy forge public.test "GET" "/actions-results/x" 502' 'proxy forge github.com "GET" "/o/r.git/info/refs" 502' \
         'proxy forge dns.google "GET" "/actions-results/x" 502' 'proxy CONNECT public.test:443 200' 'proxy CONNECT dns.google:443 403'; do
  if dc logs "${l%% *}" 2>&1 | grep -qF -- "${l#* }"; then echo "ok   forge: log line ${l#* }"
  else echo "FAIL forge: no log line ${l#* } in ${l%% *}:"; dc logs "${l%% *}" 2>&1 | grep 'forge ' | tail -n 20; fail=1; fi
done
# LockHTTP: the agents go through the gate on :3130, squid is out of their reach, the allowlist is
# the gate's business (the squid floor stays), and lea-keyproxy reaches upstreams through the gate
# with its token and the calling container (sources.json names 198.51.100.5 lea-test).
PROXY=198.51.100.5
gate() {
  name=$1 expected=$2; shift 3
  got=$(dc exec -T client curl -s -o /dev/null --max-time 15 -x "http://dev-1:secret@$PROXY:${PORT:-3130}" \
        -w '%{http_connect}/%{http_code}' "$@" || true)
  if [ "$got" = "$expected" ]; then echo "ok   $name ($got)"; else echo "FAIL $name: expected $expected, got $got"; fail=1; fi
}
gate "lockhttp: CONNECT through the gate"             "200/200" -- -p http://public.test:443/
gate "lockhttp: no squid allowlist behind the gate"   "200/200" -- -p http://other.test:443/
gate "lockhttp: the squid floor stays (private)"      "403/000" -- -p http://rebind.test:443/
gate "lockhttp: the squid floor stays (DoH)"          "403/000" -- -p http://dns.google:443/
PORT=3128 gate "lockhttp: squid unreachable from the agents" "000/000" -- -p http://public.test:443/
fwd "lockhttp: forwarder through the gate injects the key" 200 "authorization=Bearer test-key-123" -- -H X-Lea-Source:lea-evil "http://$PROXY:4400/llm/models"
glog=$(dc exec -T lockhttp-gate cat /var/log/lockhttp/fake.log 2>/dev/null || true)
# Docker lists the primary group among the supplementary ones, Podman may not.
case $glog in "uid=10003 gid=10020 groups=[10020] runtime="*|"uid=10003 gid=10020 groups=[] runtime="*) echo "ok   lockhttp: the gate runs as 10003 with the clients group only" ;;
  *) echo "FAIL lockhttp: gate identity: $(printf '%s\n' "$glog" | head -n 1)"; fail=1 ;; esac
if printf '%s\n' "$glog" | grep -q "auth=Basic ZGV2LTE6c2VjcmV0 source=-"; then echo "ok   lockhttp: an agent's proxy password reaches the gate"
else echo "FAIL lockhttp: no agent request with its password in the gate log"; fail=1; fi
if printf '%s\n' "$glog" | grep -q '"GET http://10.99.0.84:8000/v1/models HTTP/1.1" auth=Basic a2V5cHJveHk6dG9r source=lea-test'; then
  echo "ok   lockhttp: the forwarder sends its token and the calling container, not the agent's"
else echo "FAIL lockhttp: forwarder request in the gate log:"; printf '%s\n' "$glog" | grep 10.99.0.84 || true; fail=1; fi
if dc exec -T lockhttp-gate grep -q '"console":"up"' /run/lockhttp-gate/status.json; then echo "ok   lockhttp: the gate writes its status"
else echo "FAIL lockhttp: no gate status"; fail=1; fi
# lea confines only an image that says it runs so (internal/launcher/net.go: proxyConfinable).
got=$(docker image inspect -f '{{index .Config.Labels "lea.confine"}}' lea-proxy:dev)
if [ "$got" = 1 ]; then echo "ok   the image is labelled lea.confine=1"; else echo "FAIL the image's lea.confine label: '$got'"; fail=1; fi
# Every proxy runs confined as lea starts it; check-confined.sh names each check.
for svc in proxy proxy-allow proxy-default proxy-lockhttp; do
  echo "-- $svc"
  "$root/tests/proxy/check-confined.sh" "$(dc ps -q "$svc")" || fail=1
done
# Nothing secret in the logs (the forwarder logs name, method, path and status only).
if dc logs proxy 2>&1 | grep -q -e test-key-123 -e agent-token -e stream=true -e lea-proxy-test-token -e sas-not-logged; then
  echo "FAIL the proxy logs hold a key, a client credential or a query"; fail=1
else
  echo "ok   the proxy logs hold no key, credential or query"
fi
exit "$fail"
