#!/bin/sh
# Isolation tests: starts lea under the lea-test prefix and a decoy published on the host,
# then checks from inside lea that neither the decoy nor the host can be reached.
# Linux hosts. Creates and removes only lea-test-*, lea-canary* resources.
# LEA_TEST_RUNTIME=runsc (or kata): the agent containers run on that runtime (LEA_RUNTIME, #106),
# which must be registered with dockerd (tests/integration/install-runtime.sh); the test then also
# checks that docker inspect shows it and that the agents see a kernel other than the host's.
set -eu
root=$(cd "$(dirname "$0")/../.." && pwd -P)

# The agents' image: lea:dev (make build), or LEA_TEST_IMAGE.
image=${LEA_TEST_IMAGE:-lea:dev}
# On hosts where Docker uses AppArmor, lea starts containers with apparmor=lea: it must be loaded.
if docker info --format '{{json .SecurityOptions}}' 2>/dev/null | grep -q 'name=apparmor'; then
  if ! docker run --rm --pull never --security-opt apparmor=lea --entrypoint true "$image" >/dev/null 2>&1; then
    echo "isolation: the AppArmor profile 'lea' is not usable (or lea:dev is not built): run 'lea init' (AppArmor) and 'make build' first" >&2
    exit 1
  fi
fi

work=$(mktemp -d) || exit 1
work=$(cd "$work" && pwd -P) || exit 1
projects="$work/projects"; mkdir -p "$projects"
# A second projects dir: its lea container is the sibling that the first one must not reach.
sibling="$work/sibling"; mkdir -p "$sibling"
export XDG_DATA_HOME="$work/data"
cat >"$work/lea.conf" <<CONF
LEA_PREFIX=lea-test
LEA_IMAGE=$image
LEA_PROXY_IMAGE=lea-proxy:dev
CONF
runtime=${LEA_TEST_RUNTIME:-}
case $runtime in *[!A-Za-z0-9._-]*) echo "isolation: LEA_TEST_RUNTIME '$runtime' is not a runtime name" >&2; exit 2 ;; esac
if [ -n "$runtime" ]; then printf 'LEA_RUNTIME=%s\n' "$runtime" >>"$work/lea.conf"; fi
export LEA_CONFIG="$work/lea.conf"
state="$XDG_DATA_HOME/lea/state"; mkdir -p "$state"; printf '{}\n' >"$state/claude.json"
# A GitHub login left in the agents' state by an earlier lea (#103): gh's config and git's store.
# lea moves it to the proxy at its first start (the proxy has no token yet). LEA_IT_GITHUB_TOKEN (CI: the workflow's token) proves that
# gh and git work with it; without one, a made-up token must reach GitHub (Bad credentials).
forge_token=${LEA_IT_GITHUB_TOKEN:-ghp_leaIsolationNotAToken}
forge_expect=bad
if [ -n "${LEA_IT_GITHUB_TOKEN:-}" ]; then
  forge_expect=auth
  # gh run view --log: GitHub redirects job logs to Azure blob storage, outside lea's default list.
  printf 'LEA_EGRESS_ALLOW=default,blob.core.windows.net\n' >>"$LEA_CONFIG"
fi
mkdir -p "$state/gh" "$state/git"
printf 'github.com:\n    users:\n        lea-it:\n            oauth_token: %s\n    git_protocol: https\n    oauth_token: %s\n    user: lea-it\n' \
  "$forge_token" "$forge_token" >"$state/gh/hosts.yml"
printf 'https://lea-it:%s@github.com\n' "$forge_token" >"$state/git/credentials"
chmod 0600 "$state/gh/hosts.yml" "$state/git/credentials"
# The first characters of the token: enough to find a copy of it, inside lea, without its value.
forge_needle=$(printf '%s' "$forge_token" | cut -c1-16)
slug=$(printf '%s' "$projects" | sed 's/[^A-Za-z0-9]/-/g')
id=$(printf '%s' "$projects" | sha256sum | cut -c1-10)
sid=$(printf '%s' "$sibling" | sha256sum | cut -c1-10)
# A third projects dir whose internal network exists without an isolated gateway (#105).
plain="$work/plain"; mkdir -p "$plain"
pid=$(printf '%s' "$plain" | sha256sum | cut -c1-10)
holders=
# The host's settings.json and plugins (#111): each lea container gets its own writable copy, and
# the host's must be byte-identical after the agents changed theirs. Made here when the host has
# none (CI), and removed at the end; a local marketplace in the projects dir for /plugin install.
made_settings=''; made_plugins=''
# A stand-in for the Lock Family hub's facade socket (I11): the host daemon is pointed at it, and
# no agent may see it (lea follows the hub by default). Outside the projects dir, as lockhub's is.
facade_dir="$work/run/lockhub"; facade="$facade_dir/facade.sock"; facade_pid=
export LEA_HUB_SOCKET="$facade"

# hold DIR: a background lea session on DIR, serving HTTP on port 8765 inside its container,
# that ends when DIR/.lea-release exists.
hold() {
  # shellcheck disable=SC2016 # expanded by the inner shell
  LEA_PROJECTS_DIR="$1" "$root/bin/lea" shell -- sh -c 'python3 -m http.server 8765 >/dev/null 2>&1 & p=$!
    while [ ! -e "$1" ]; do sleep 1; done; kill "$p"' sh "$1/.lea-release" >"$1.log" 2>&1 &
  holders="$holders $!"
}
# shellcheck disable=SC2317 # invoked from the cleanup trap
release_holders() {
  : >"$projects/.lea-release"; : >"$sibling/.lea-release"
  for h in $holders; do
    i=0
    while kill -0 "$h" 2>/dev/null && [ "$i" -lt 60 ]; do i=$((i + 1)); sleep 1; done
  done
  holders=
}
# wait_for WHAT CMD...: retries CMD (printing a non-empty value) for up to 180 s.
wait_for() {
  what=$1; shift; i=0
  until v=$("$@" 2>/dev/null) && [ -n "$v" ] && [ "$v" != "<no value>" ]; do
    i=$((i + 1))
    if [ "$i" -ge 180 ]; then
      echo "isolation: $what did not appear" >&2
      for l in "$projects.log" "$sibling.log"; do [ ! -f "$l" ] || { echo "--- output of lea ($l):" >&2; tail -40 "$l" >&2; }; done
      exit 1
    fi
    sleep 1
  done
  printf '%s\n' "$v"
}

# shellcheck disable=SC2317 # invoked via trap
cleanup() {
  release_holders
  if [ -n "$facade_pid" ]; then kill "$facade_pid" 2>/dev/null || true; fi
  docker rm -f lea-canary lea-canary-dns lea-canary-dns-host >/dev/null 2>&1 || true
  docker network rm lea-canary-net >/dev/null 2>&1 || true
  "$root/bin/lea" stop --all >/dev/null 2>&1 || true
  docker rm -f "lea-test-$id" "lea-test-$sid" "lea-test-$pid" lea-test-proxy >/dev/null 2>&1 || true
  docker network rm "lea-test-internal-$id" "lea-test-internal-$sid" "lea-test-internal-$pid" lea-test-egress >/dev/null 2>&1 || true
  docker volume rm "lea-test-podman-$id" "lea-test-podman-$sid" >/dev/null 2>&1 || true
  # The session created this per-run projects directory under the real ~/.claude.
  if [ -n "$slug" ]; then rm -r "${HOME:?}/.claude/projects/$slug" 2>/dev/null || true; fi
  sslug=$(printf '%s' "$sibling" | sed 's/[^A-Za-z0-9]/-/g')
  if [ -n "$sslug" ]; then rm -r "${HOME:?}/.claude/projects/$sslug" 2>/dev/null || true; fi
  if [ -n "$made_settings" ]; then rm -f "${HOME:?}/.claude/settings.json"; fi
  if [ -n "$made_plugins" ]; then rm -rf "${HOME:?}/.claude/plugins"; fi
  rm -rf "${work:?}"
}
trap cleanup EXIT

docker network create lea-canary-net >/dev/null
docker run -d --name lea-canary --network lea-canary-net -p 18080:80 busybox:1.37 \
  sh -c 'mkdir -p /w && echo canary >/w/index.html && httpd -f -p 80 -h /w' >/dev/null
canary_ip=$(docker inspect -f '{{range .NetworkSettings.Networks}}{{.IPAddress}}{{end}}' lea-canary)
# Positive control: the decoy must answer from the host, otherwise the "unreachable" checks prove nothing.
if [ -z "$canary_ip" ]; then echo "isolation: the decoy has no IP address" >&2; exit 1; fi
i=0
until curl -sf --max-time 2 http://127.0.0.1:18080/ 2>/dev/null | grep -qx canary; do
  i=$((i + 1))
  if [ "$i" -ge 10 ]; then echo "isolation: the decoy does not answer on 127.0.0.1:18080 (positive control failed)" >&2; exit 1; fi
  sleep 1
done
# A decoy DNS server (#98) that logs every query it gets: the DNS checks inside lea send lookups of
# names under DNS_MARK, which must never reach it. lea's image has the Python it runs on.
docker run -d --name lea-canary-dns --network lea-canary-net --user 0 --entrypoint python3 \
  -v "$root/tests/integration/dns-decoy.py:/dns-decoy.py:ro" "$image" -I /dns-decoy.py >/dev/null
dns_ip=$(docker inspect -f '{{range .NetworkSettings.Networks}}{{.IPAddress}}{{end}}' lea-canary-dns)
dns_mark="lea-it-$id.lea-decoy.test"
# Positive control: a container whose resolver is the decoy (as Docker's default is the host's)
# gets its lookups to it through Docker's embedded resolver, so the decoy's log would show a leak.
i=0
until docker logs lea-canary-dns 2>/dev/null | grep -q "^query ctl.$dns_mark 1$"; do
  docker run --rm --network lea-canary-net --dns "$dns_ip" busybox:1.37 nslookup "ctl.$dns_mark" >/dev/null 2>&1 || true
  i=$((i + 1))
  if [ "$i" -ge 10 ]; then
    echo "isolation: the decoy DNS server gets no query through Docker's embedded resolver (positive control failed)" >&2
    docker logs lea-canary-dns >&2 2>&1 || true; exit 1
  fi
  sleep 1
done
# A second decoy on the host's 127.0.0.1:53: Docker may query a loopback resolver from the host's
# namespace (its default, host(127.0.0.53)), so a loopback --dns would reach a resolver listening
# there (dnsmasq, unbound). Positive control: a query from the host reaches it.
docker run -d --name lea-canary-dns-host --network host --user 0 --entrypoint python3 \
  -v "$root/tests/integration/dns-decoy.py:/dns-decoy.py:ro" "$image" -I /dns-decoy.py 127.0.0.1 >/dev/null
i=0
until docker logs lea-canary-dns-host 2>/dev/null | grep -q "^query ctl-host.$dns_mark 1$"; do
  python3 -I -c 'import socket, struct, sys
q = struct.pack(">HHHHHH", 1, 0x0100, 1, 0, 0, 0) + b"".join(bytes([len(p)]) + p.encode() for p in sys.argv[1].split(".")) + b"\0\0\1\0\1"
c = socket.socket(socket.AF_INET, socket.SOCK_DGRAM); c.settimeout(2); c.sendto(q, ("127.0.0.1", 53))
try: c.recvfrom(512)
except OSError: pass' "ctl-host.$dns_mark" 2>/dev/null || true
  i=$((i + 1))
  if [ "$i" -ge 10 ]; then
    echo "note: the decoy DNS server on the host's 127.0.0.1:53 did not start (port in use?): the host-loopback DNS check did not run"
    docker logs lea-canary-dns-host 2>&1 | sed 's/^/     | /'; break
  fi
  sleep 1
done
# Local routing table lookup: no packet is sent.
host_ip=$(ip -4 route get 1.1.1.1 2>/dev/null | sed -n 's/.* src \([0-9.]*\).*/\1/p')
# Hashes only: host file contents are never passed into lea.
cred_sha=; hist_sha=
if [ -f "$HOME/.claude/.credentials.json" ]; then cred_sha=$(sha256sum <"$HOME/.claude/.credentials.json" | cut -d' ' -f1); fi
if [ -f "$HOME/.claude/history.jsonl" ]; then hist_sha=$(sha256sum <"$HOME/.claude/history.jsonl" | cut -d' ' -f1); fi
mkdir -p "$facade_dir"
python3 -I -c 'import socket, sys
s = socket.socket(socket.AF_UNIX); s.bind(sys.argv[1]); s.listen(8)
while True: s.accept()[0].close()' "$facade" &
facade_pid=$!
# Positive control: the stand-in facade socket answers on the host.
i=0
until python3 -I -c 'import socket, sys; socket.socket(socket.AF_UNIX).connect(sys.argv[1])' "$facade" 2>/dev/null; do
  i=$((i + 1))
  if [ "$i" -ge 10 ]; then echo "isolation: the stand-in facade socket does not answer (positive control failed)" >&2; exit 1; fi
  sleep 1
done
cp "$root/tests/integration/inside-isolation.sh" "$projects/"
mkdir -p "$HOME/.claude"
if [ ! -e "$HOME/.claude/settings.json" ]; then
  printf '{"permissions":{"allow":["Bash(lea-isolation-host *)"]}}\n' >"$HOME/.claude/settings.json"; made_settings=1
fi
if [ ! -e "$HOME/.claude/plugins" ]; then
  mkdir -p "$HOME/.claude/plugins"; printf '{"version":2,"plugins":{}}\n' >"$HOME/.claude/plugins/installed_plugins.json"; made_plugins=1
fi
# host_claude_sum: a digest of the host's settings.json and plugins: names, types, modes, link
# targets and contents.
host_claude_sum() {
  (cd "$HOME/.claude" && find settings.json plugins -printf '%p %y %m %l\n' 2>/dev/null | LC_ALL=C sort &&
    find settings.json plugins -type f -print0 2>/dev/null | LC_ALL=C sort -z | xargs -0 -r sha256sum) | sha256sum | cut -d' ' -f1
}
host_sum=$(host_claude_sum)
# lea copies a real file only (a symbolic link is skipped).
settings_sha='' host_rule=''
if [ -f "$HOME/.claude/settings.json" ] && [ ! -L "$HOME/.claude/settings.json" ]; then
  settings_sha=$(sha256sum <"$HOME/.claude/settings.json" | cut -d' ' -f1)
  # One of the host's own allow rules: the managed settings must carry it (#111).
  host_rule=$(jq -r '[.permissions.allow[]? | strings | select(length > 0 and length <= 1024)][0] // empty' "$HOME/.claude/settings.json" 2>/dev/null || true)
fi
# lea config claude (#334): the user's own base settings, kept in lea's configuration ($work, next
# to LEA_CONFIG), reach the managed settings of the containers started from now on.
"$root/bin/lea" config claude defaultMode auto >/dev/null
"$root/bin/lea" config claude allow add 'Bash(lea-config-claude *)' >/dev/null
# The default mode the host's settings.json gives once lea config claude's is unset (#333).
host_mode=''
[ -z "$settings_sha" ] || host_mode=$(jq -r '.permissions.defaultMode // empty | select(IN("default", "acceptEdits", "plan", "dontAsk", "auto"))' "$HOME/.claude/settings.json" 2>/dev/null || true)
mk="$projects/lea-it-market"
mkdir -p "$mk/.claude-plugin" "$mk/plugins/lea-it/.claude-plugin" "$mk/plugins/lea-it/commands"
printf '{"name":"lea-it-market","owner":{"name":"lea"},"plugins":[{"name":"lea-it","source":"./plugins/lea-it","description":"lea isolation test plugin"}]}\n' \
  >"$mk/.claude-plugin/marketplace.json"
printf '{"name":"lea-it","version":"1.0.0","description":"lea isolation test plugin"}\n' >"$mk/plugins/lea-it/.claude-plugin/plugin.json"
printf -- '---\ndescription: say ok\n---\nSay ok.\n' >"$mk/plugins/lea-it/commands/ok.md"
# The internal networks have Docker-allocated subnets: hold both sessions open to read the
# gateway of this one and the address of the sibling.
hold "$projects"
hold "$sibling"
# Recent Docker releases leave the gateway out of the IPAM config of an internal network: the
# bridge address is then the first host address of the subnet.
subnet=$(wait_for "the subnet of lea-test-internal-$id" \
  docker network inspect -f '{{range .IPAM.Config}}{{.Subnet}}{{end}}' "lea-test-internal-$id")
gw_ip=$(docker network inspect -f '{{range .IPAM.Config}}{{.Gateway}}{{end}}' "lea-test-internal-$id" 2>/dev/null || true)
if [ -z "$gw_ip" ] || [ "$gw_ip" = "<no value>" ]; then
  gw_ip=$(python3 -I -c 'import ipaddress, sys; print(next(ipaddress.ip_network(sys.argv[1], strict=False).hosts()))' "$subnet")
fi
sibling_ip=$(wait_for "the address of lea-test-$sid" \
  docker container inspect -f "{{with index .NetworkSettings.Networks \"lea-test-internal-$sid\"}}{{.IPAddress}}{{end}}" "lea-test-$sid")
wait_for "lea-test-$id running" sh -c "docker container inspect -f '{{.State.Running}}' lea-test-$id | grep -x true" >/dev/null
# The runtime both agent containers run on (#106), and the proxy's: always the engine's default.
default_runtime=$(docker info --format '{{.DefaultRuntime}}')
for c in "lea-test-$id" "lea-test-$sid"; do
  got=$(docker container inspect -f '{{.HostConfig.Runtime}}' "$c")
  if [ "$got" = "${runtime:-$default_runtime}" ]; then echo "ok   $c runs on the ${runtime:-$default_runtime} runtime"
  else echo "FAIL $c runs on the $got runtime, not ${runtime:-$default_runtime}" >&2; exit 1; fi
done
got=$(docker container inspect -f '{{.HostConfig.Runtime}}' lea-test-proxy)
if [ "$got" = "$default_runtime" ]; then echo "ok   lea-test-proxy keeps the engine's default runtime ($got)"
else echo "FAIL lea-test-proxy runs on the $got runtime, not the engine's default $default_runtime" >&2; exit 1; fi
# On a sandboxed runtime, lea doctor confirms it applies lea's seccomp profile (--oci-seccomp for
# runsc, disable_guest_seccomp = false for Kata): the AF_ALG refusal checked inside depends on it.
if [ -n "$runtime" ]; then
  doc=$(LEA_PROJECTS_DIR="$projects" timeout 120 "$root/bin/lea" doctor 2>&1 || true)
  if printf '%s\n' "$doc" | grep -q "^ok    $runtime seccomp: "; then echo "ok   lea doctor: $runtime applies lea's seccomp profile"
  else echo "FAIL lea doctor does not confirm that $runtime applies lea's seccomp profile:" >&2; printf '%s\n' "$doc" | grep -E 'runtime|seccomp' >&2; exit 1; fi
fi
# Positive control: the sibling's listener answers from inside the sibling.
i=0
until LEA_PROJECTS_DIR="$sibling" timeout 30 "$root/bin/lea" shell -- curl -s -o /dev/null --max-time 2 --noproxy '*' "http://$sibling_ip:8765/" >/dev/null 2>&1; do
  i=$((i + 1))
  if [ "$i" -ge 20 ]; then
    echo "isolation: the sibling's listener does not answer (positive control failed)" >&2
    for l in "$projects.log" "$sibling.log"; do [ ! -f "$l" ] || { echo "--- output of lea ($l):" >&2; tail -40 "$l" >&2; }; done
    echo "--- docker exec in lea-test-$sid, bypassing lea shell:" >&2
    timeout 30 docker exec "lea-test-$sid" sh -c "ps -eo pid,args | head -30; curl -s -o /dev/null -w 'HTTP %{http_code}\n' --max-time 5 --noproxy '*' http://$sibling_ip:8765/" >&2 || echo "docker exec: $?" >&2
    exit 1
  fi
  sleep 1
done
# #105: lea doctor reports the gateway mode of each internal network; lea refuses to start a
# container on an internal network the host keeps an address on, and creates no container.
"$root/bin/lea" doctor >"$work/doctor.out" 2>&1 || true
grep -qF "ok    lea-test-internal-$id gateway_mode_ipv4=isolated" "$work/doctor.out" ||
  { cat "$work/doctor.out" >&2; echo "isolation: lea doctor does not report lea-test-internal-$id as gateway_mode_ipv4=isolated" >&2; exit 1; }
docker network create --label lea.prefix=lea-test --label "lea.id=$pid" --internal "lea-test-internal-$pid" >/dev/null
if LEA_PROJECTS_DIR="$plain" "$root/bin/lea" shell -- true </dev/null >"$work/plain.out" 2>&1; then
  cat "$work/plain.out" >&2; echo "isolation: lea started on an internal network without gateway_mode_ipv4=isolated" >&2; exit 1
fi
grep -qF "lea-test-internal-$pid exists without gateway_mode_ipv4=isolated" "$work/plain.out" ||
  { cat "$work/plain.out" >&2; echo "isolation: lea's refusal does not name the non-isolated network" >&2; exit 1; }
if docker container inspect "lea-test-$pid" >/dev/null 2>&1; then
  echo "isolation: lea created lea-test-$pid on a non-isolated network" >&2; exit 1
fi
"$root/bin/lea" doctor >"$work/doctor.out" 2>&1 || true
grep -qF "FAIL  lea-test-internal-$pid gateway_mode_ipv4=unset" "$work/doctor.out" ||
  { cat "$work/doctor.out" >&2; echo "isolation: lea doctor does not fail on lea-test-internal-$pid" >&2; exit 1; }
docker network rm "lea-test-internal-$pid" >/dev/null
echo "isolation: ok    a non-isolated internal network is refused, and lea doctor fails on it"
# #105: lea checks the isolation itself, not the option string: an internal network that has
# gateway_mode_ipv4=isolated but whose bridge holds a host address is refused too. Adding the
# address needs root (sudo -n): without it the check is noted (a failure in CI).
docker network create --label lea.prefix=lea-test --label "lea.id=$pid" --internal \
  -o com.docker.network.bridge.gateway_mode_ipv4=isolated "lea-test-internal-$pid" >/dev/null
pbr="br-$(docker network inspect -f '{{.Id}}' "lea-test-internal-$pid" | cut -c1-12)"
if ip -4 addr show dev "$pbr" >/dev/null 2>&1 && sudo -n ip addr add 192.0.2.1/32 dev "$pbr" 2>/dev/null; then
  if LEA_PROJECTS_DIR="$plain" "$root/bin/lea" shell -- true </dev/null >"$work/plain.out" 2>&1; then
    cat "$work/plain.out" >&2; echo "isolation: lea started on an isolated network whose bridge holds a host address" >&2; exit 1
  fi
  grep -qF "lea-test-internal-$pid has gateway_mode_ipv4=isolated but the host holds 192.0.2.1/32 on its bridge $pbr" "$work/plain.out" ||
    { cat "$work/plain.out" >&2; echo "isolation: lea's refusal does not name the host address on the bridge" >&2; exit 1; }
  if docker container inspect "lea-test-$pid" >/dev/null 2>&1; then
    echo "isolation: lea created lea-test-$pid on a bridge with a host address" >&2; exit 1
  fi
  echo "isolation: ok    an isolated network whose bridge holds a host address is refused"
else
  echo "note: the bridge $pbr is not visible or sudo -n is unavailable: the host-address check did not run"
fi
docker network rm "lea-test-internal-$pid" >/dev/null
rc=0
# The GitHub token went to the proxy (0600, the user's), and out of the agents' state.
ft="$XDG_DATA_HOME/lea/proxy/forge/github.com"
if [ "$(cat "$ft" 2>/dev/null)" = "$forge_token" ] && [ "$(stat -c %a "$ft")" = 600 ]; then echo "ok   the GitHub token moved to the proxy"
else echo "FAIL the GitHub token is not in the proxy's key directory"; rc=1; fi
if grep -q oauth_token "$state/gh/hosts.yml" || grep -q github.com "$state/git/credentials"; then
  echo "FAIL the GitHub token is still in the agents' state"; rc=1
else echo "ok   the GitHub token left the agents' state"; fi
# Adopted once (#103): from now on, a token an agent plants in the state is discarded.
if [ -f "$XDG_DATA_HOME/lea/proxy/forge-migrated" ]; then echo "ok   the state's tokens are adopted only once"
else echo "FAIL no forge-migrated marker: a token planted in the state could replace the proxy's"; rc=1; fi
# The proxy lea started is confined (#102): capabilities, no-new-privileges, read-only root,
# limits, no root process but PID 1.
"$root/tests/proxy/check-confined.sh" lea-test-proxy || rc=1
echo "isolation: the checks inside lea"
LEA_PROJECTS_DIR="$projects" timeout 900 "$root/bin/lea" shell -- env FORGE_NEEDLE="$forge_needle" FORGE_EXPECT="$forge_expect" CANARY_IP="$canary_ip" GW_IP="$gw_ip" HOST_IP="$host_ip" \
  SIBLING_IP="$sibling_ip" DECOY_DNS_IP="$dns_ip" DNS_MARK="$dns_mark" HOST_KERNEL="$(uname -r)" SECOND_KERNEL="${runtime:+1}" HOST_FACADE="$facade" HOST_CRED_SHA="$cred_sha" HOST_HIST_SHA="$hist_sha" \
  HOST_SETTINGS_SHA="$settings_sha" HOST_ALLOW_RULE="$host_rule" LEA_IT_MARKET="$mk" CONFIG_CLAUDE=1 sh "$projects/inside-isolation.sh" || rc=$?
# No lookup made inside lea or its nested containers reached either decoy (#98): only the positive
# controls' names are in their logs.
dns_leaks() { for d in lea-canary-dns lea-canary-dns-host; do docker logs "$d" 2>&1 | grep "^query " | grep -v "^query ctl\(-host\)\?\.$dns_mark " | grep -F "$dns_mark" | sed "s/^/$d: /"; done; }
if [ -n "$(dns_leaks)" ]; then
  echo "FAIL no DNS query from inside lea reached the decoy:"; dns_leaks | sed 's/^/     | /'; rc=1
else echo "ok   no DNS query from inside lea reached the decoy"; fi
# What the agents did to their copies never reached the host (#111).
if [ "$(host_claude_sum)" = "$host_sum" ]; then echo "ok   host ~/.claude settings.json and plugins unchanged"
else echo "FAIL host ~/.claude settings.json and plugins unchanged"; rc=1; fi
# lea config claude (#334): its rule removed and its mode unset, a fresh container has neither.
release_holders
"$root/bin/lea" stop --all >/dev/null 2>&1 || true
"$root/bin/lea" config claude allow remove 'Bash(lea-config-claude *)' >/dev/null
"$root/bin/lea" config claude defaultMode --unset >/dev/null
got=$(LEA_PROJECTS_DIR="$projects" timeout 300 "$root/bin/lea" shell -- \
  jq -r '[(.permissions.defaultMode // "none"), (.permissions.allow | index("Bash(lea-config-claude *)") // "none")] | join(" ")' \
  /etc/claude-code/managed-settings.json </dev/null 2>/dev/null | tail -n 1 || true)
if [ "$got" = "${host_mode:-none} none" ]; then echo "ok   lea config claude: remove and --unset apply at the next start"
else echo "FAIL lea config claude: remove and --unset apply at the next start (got '$got', want '${host_mode:-none} none')"; rc=1; fi
exit "$rc"
