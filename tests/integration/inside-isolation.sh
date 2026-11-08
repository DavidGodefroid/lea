#!/bin/sh
# Runs inside lea. Prints ok/FAIL per check; exits non-zero if any check fails.
# SC2016: the sh -c scripts below are deliberately single-quoted and expanded by the inner shell.
# shellcheck disable=SC2016
set -u
fail=0
ok() { echo "ok   $1"; }
ko() { echo "FAIL $1"; fail=1; }
must() { n=$1; shift; if "$@" >/dev/null 2>&1; then ok "$n"; else ko "$n"; fi; }
mustnt() { n=$1; shift; if "$@" >/dev/null 2>&1; then ko "$n"; else ok "$n"; fi; }

if [ -z "${CANARY_IP:-}" ] || [ -z "${GW_IP:-}" ]; then
  echo "inside-isolation: CANARY_IP and GW_IP must be set (run through isolation.sh)" >&2
  exit 2
fi
[ -n "${HOST_IP:-}" ] || echo "note: HOST_IP is empty, the host-IP checks are skipped"
[ -n "${SIBLING_IP:-}" ] || echo "note: SIBLING_IP is empty, the sibling lea checks are skipped"

# Hash of a file, or "absent".
sha_of() { if [ -f "$1" ]; then sha256sum <"$1" | cut -d' ' -f1; else echo absent; fi; }

# A sandboxed runtime (LEA_RUNTIME=runsc or kata, #106): the agents run on a kernel of their own
# (gVisor's Sentry, Kata's guest), not the host's.
if [ "${SECOND_KERNEL:-}" = 1 ] && [ -n "${HOST_KERNEL:-}" ]; then
  must   "a kernel other than the host's ($(uname -r), host $HOST_KERNEL)" test "$(uname -r)" != "$HOST_KERNEL"
fi
# lea's bus (messaging between agents): a host Unix socket the daemon binds in the bus directory.
# Not a containment check: says whether the runtime lets a host socket through (#106).
if [ -n "${LEA_BUS_SOCK:-}" ]; then
  if python3 -I -c 'import socket, sys; socket.socket(socket.AF_UNIX).connect(sys.argv[1])' "$LEA_BUS_SOCK" 2>/dev/null; then
    echo "info lea's bus socket is reachable"
  else
    echo "info lea's bus socket is not reachable"
  fi
fi
mustnt "no host Docker socket"                 test -S /var/run/docker.sock
mustnt "no host Docker socket (/run)"          test -S /run/docker.sock
must   "docker is the nested Podman"            sh -c 'docker version 2>/dev/null | grep -qi podman'
# Its engine's output goes to a log, shown when it fails.
runlog=$(mktemp)
if docker run --rm busybox:1.37 true >"$runlog" 2>&1; then ok "a nested container runs"; else ko "a nested container runs"; sed 's/^/     | /' "$runlog"; fi
rm -f "$runlog"
mustnt "decoy unreachable directly"             curl -s --max-time 5 --noproxy '*' "http://$CANARY_IP/"
mustnt "decoy unreachable through the proxy"    curl -sf --max-time 5 "http://$CANARY_IP/"
mustnt "host port unreachable via the bridge"   curl -s --max-time 5 --noproxy '*' "http://$GW_IP:18080/"
mustnt "host port refused by the proxy (bridge)" curl -sf --max-time 5 "http://$GW_IP:18080/"
if [ -n "${HOST_IP:-}" ]; then
  mustnt "host port unreachable via host IP"    curl -s --max-time 5 --noproxy '*' "http://$HOST_IP:18080/"
  mustnt "host port refused by the proxy (host IP)" curl -sf --max-time 5 "http://$HOST_IP:18080/"
fi
if [ -n "${SIBLING_IP:-}" ]; then
  mustnt "sibling lea unreachable directly"    curl -s --max-time 5 --noproxy '*' "http://$SIBLING_IP:8765/"
  mustnt "sibling lea refused by the proxy"    curl -sf --max-time 5 "http://$SIBLING_IP:8765/"
fi
mustnt "AF_ALG socket refused (seccomp)"       python3 -c 'import socket; socket.socket(38, 5, 0)'
# NETLINK_AUDIT: EINVAL from seccomp (image/seccomp/lea.json), where the kernel would open the
# socket in lea's network namespace; other netlink sockets (NETLINK_ROUTE: ip, pasta) stay open.
must   "NETLINK_AUDIT socket refused (seccomp)" python3 -I -c 'import errno, socket, sys
socket.socket(16, 3, 0).close()
try: socket.socket(16, 3, 9)
except OSError as e: sys.exit(e.errno != errno.EINVAL)
sys.exit(1)'
# socket(2) through a raw syscall, each argument a full 64-bit register: seccomp compares all 64
# bits, the kernel reads the low 32 of these ints. Protocol 0x100000009 is NETLINK_AUDIT to the
# kernel, family 0x100000026 AF_ALG: both refused (image/seccomp/generate.py, ARG_BITS).
# shellcheck disable=SC2317,SC2329 # run by must
socket_raw_refused() {
  python3 -I -c 'import ctypes, platform, sys
nr = {"x86_64": 41, "aarch64": 198}[platform.machine()]
libc = ctypes.CDLL(None, use_errno=True); libc.syscall.restype = ctypes.c_long
fd = libc.syscall(ctypes.c_long(nr), *(ctypes.c_long(int(a, 0)) for a in sys.argv[1:]))
sys.exit(0 if fd < 0 and ctypes.get_errno() in (1, 22, 97) else 1)' "$@"
}
must   "NETLINK_AUDIT socket with high protocol bits refused (seccomp)" socket_raw_refused 16 3 0x100000009
must   "AF_ALG socket with high family bits refused (seccomp)" socket_raw_refused 0x100000026 5 0
# The sandbox (#100): the nested rootless Podman builds and runs over pasta under lea's seccomp
# profile and AppArmor; what it does not need stays refused, and nothing gets root back.
# Its engine's output goes to a log, shown when it fails.
buildlog=$(mktemp)
if sh -c 'd=$(mktemp -d) && printf "FROM busybox:1.37\nRUN echo built >/lea-built\nRUN test -s /lea-built\n" >"$d/Containerfile" && docker build -t lea-it-build "$d" && docker rmi lea-it-build; rc=$?; rm -rf "$d"; exit $rc' >"$buildlog" 2>&1; then
  ok "a nested image builds"
else
  ko "a nested image builds"; sed 's/^/     | /' "$buildlog"
fi
rm -f "$buildlog"
# Plain HTTP through the proxy from a nested container: any answer of the proxy (an error page for
# this name) proves pasta's way out of the nested network namespace.
must   "a nested container reaches the proxy over pasta" sh -c 'docker run --rm busybox:1.37 sh -c "http_proxy=http://\$1:3128 wget -Y on -T 10 -O /dev/null http://lea-isolation.invalid/ 2>&1" sh "$LEA_PROXY_IP" | grep -q "HTTP/1"'
# Rootless Podman's own user namespace: required (README, Security), so it is a must.
must   "unprivileged user namespace (rootless Podman needs it)" unshare -U -r true
# keyctl(KEYCTL_GET_KEYRING_ID, KEY_SPEC_SESSION_KEYRING, 0) and perf_event_open of a software
# counter: the kernel keyring and perf events are not namespaced (seccomp refuses both). vmsplice on
# fd -1: EPERM from seccomp, where the kernel itself would answer EBADF (image/seccomp/lea.json).
# shellcheck disable=SC2317,SC2329 # run by must
sys_refused() {
  python3 -I -c 'import ctypes, platform, sys
nr = {"x86_64": {"keyctl": 250, "perf_event_open": 298, "reboot": 169, "vmsplice": 278}, "aarch64": {"keyctl": 219, "perf_event_open": 241, "reboot": 142, "vmsplice": 75}}[platform.machine()][sys.argv[1]]
attr = (ctypes.c_uint32 * 32)(); attr[0] = 1; attr[1] = 128  # PERF_TYPE_SOFTWARE, size; config 0: cpu-clock
args = {"keyctl": (0, -3, 0), "perf_event_open": (attr, 0, -1, -1, 0), "reboot": (0, 0, 0, 0), "vmsplice": (-1, 0, 0, 0)}[sys.argv[1]]
libc = ctypes.CDLL(None, use_errno=True)
sys.exit(0 if libc.syscall(nr, *args) < 0 and ctypes.get_errno() == 1 else 1)' "$1"
}
must   "keyctl refused (seccomp)"               sys_refused keyctl
must   "perf_event_open refused (seccomp)"      sys_refused perf_event_open
must   "reboot refused (seccomp)"               sys_refused reboot
must   "vmsplice refused (seccomp)"             sys_refused vmsplice
mustnt "sudo absent"                            command -v sudo
must   "no setuid or setgid file"               sh -c '[ -z "$(find / -xdev -type f \( -perm -4000 -o -perm -2000 \) -print 2>/dev/null)" ]'
# The locksql console's own directory: read-only, and a mount point no agent can replace by a
# symbolic link that the console's bind mount would follow on the host (#121).
if [ "${LEA_LOCKSQL:-}" = 1 ]; then
  lk=$LEA_PROJECTS/.locksql
  must   ".locksql is a directory"                test -d "$lk" -a ! -L "$lk"
  mustnt ".locksql is not writable"               touch "$lk/lea-isolation"
  mustnt ".locksql cannot be removed"             rm -rf "$lk"
  mustnt ".locksql cannot be renamed"             mv "$lk" "$LEA_PROJECTS/.locksql-moved"
  mustnt ".locksql cannot be replaced by a link"  sh -c 'ln -sfn /tmp "$1.new" && mv -T "$1.new" "$1"' sh "$lk"
  rm -f "$lk.new"
  must   ".locksql is still a directory"          test -d "$lk" -a ! -L "$lk"
fi
# The Lock Family's directory (hub config, keys, routing): read-only, whatever Lock is on, so that
# no agent rewrites what the locks and the hub trust (a write gets EROFS).
lf=$LEA_PROJECTS/.lockfamily
must   ".lockfamily is a directory"             test -d "$lf" -a ! -L "$lf"
must   ".lockfamily write gets EROFS"           sh -c 'touch "$1/lea-isolation" 2>&1 | grep -q "Read-only file system"' sh "$lf"
mustnt ".lockfamily cannot be removed"          rm -rf "$lf"
mustnt ".lockfamily cannot be renamed"          mv "$lf" "$LEA_PROJECTS/.lockfamily-moved"
mustnt ".lockfamily cannot be replaced by a link" sh -c 'ln -sfn /tmp "$1.new" && mv -T "$1.new" "$1"' sh "$lf"
rm -f "$lf.new"
must   ".lockfamily is still a directory"       test -d "$lf" -a ! -L "$lf"
# The hub's facade socket (I11): the host's is not visible, nor any lockhub runtime directory nor
# any facade socket anywhere in the container.
if [ -n "${HOST_FACADE:-}" ]; then
  mustnt "host facade socket not visible"       test -e "$HOST_FACADE"
  mustnt "host facade directory not visible"    test -e "$(dirname "$HOST_FACADE")"
fi
if [ -n "${XDG_RUNTIME_DIR:-}" ]; then
  mustnt "no lockhub runtime directory"         test -e "$XDG_RUNTIME_DIR/lockhub"
fi
must   "no facade socket in the container"      sh -c '[ -z "$(find / \( -path /proc -o -path /sys -o -path /dev \) -prune -o -name facade.sock -print 2>/dev/null)" ]'
mustnt "host ~/.ssh absent"                     test -e "$HOME/.ssh"
mustnt "host ~/.docker absent"                  test -e "$HOME/.docker"
mustnt "host ~/.config/glab-cli absent"         test -e "$HOME/.config/glab-cli"
mustnt "host kubeconfig absent"                 test -e "$HOME/.kube/config"
# settings.json and plugins are this container's own copies of the host's (#111): writable, so
# /permissions, /plugin install and the marketplaces' auto-update work; the host's are never
# mounted (isolation.sh checks they are byte-identical afterwards). lea's hooks are in the
# managed settings, root's.
c=$HOME/.claude
mustnt "no host bind on ~/.claude/settings.json"  grep -q " $c/settings.json " /proc/self/mountinfo
mustnt "no host bind on ~/.claude/plugins"        grep -q " $c/plugins " /proc/self/mountinfo
if [ -n "${HOST_SETTINGS_SHA:-}" ]; then
  must   "settings.json is a copy of the host's" test "$(sha_of "$c/settings.json")" = "$HOST_SETTINGS_SHA"
fi
# What /permissions does: a rule in the user settings, written by a rename next to the file.
must   "/permissions adds a rule"                sh -c 'f=$1/settings.json; { [ -f "$f" ] || echo "{}" >"$f"; } &&
  jq ".permissions.allow += [\"Bash(lea-isolation *)\"]" "$f" >"$f.new" && mv "$f.new" "$f" &&
  jq -e ".permissions.allow | index(\"Bash(lea-isolation *)\")" "$f"' sh "$c"
must   "plugins/data is writable"                sh -c 'mkdir -p "$1/plugins/data/lea-inline" && touch "$1/plugins/data/lea-inline/.lea-write-test"' sh "$c"
if [ -n "${LEA_IT_MARKET:-}" ]; then
  # A local marketplace (a directory of the projects dir): added, its plugin installed and
  # enabled, the marketplace updated as the auto-update does.
  must   "/plugin marketplace add works"         claude plugin marketplace add "$LEA_IT_MARKET"
  must   "/plugin install works"                 claude plugin install lea-it@lea-it-market
  must   "the installed plugin is enabled"       sh -c 'claude plugin list 2>&1 | grep -A4 "lea-it@lea-it-market" | grep -q enabled'
  must   "the installed plugin is recorded"      jq -e '.plugins["lea-it@lea-it-market"]' "$c/plugins/installed_plugins.json"
  must   "marketplace update works"              claude plugin marketplace update lea-it-market
fi
m=/etc/claude-code/managed-settings.json
must   "managed settings carry lea's hooks"      /usr/local/libexec/lea/managed-check
# The permission rules are the managed ones only (#111): the host's joined lea's at start, and
# what the agents write into their copy (the /permissions check above) is not among them.
must   "permission rules are managed only"        jq -e '.allowManagedPermissionRulesOnly == true and .permissions.disableBypassPermissionsMode == "disable"' "$m"
if [ -n "${HOST_ALLOW_RULE:-}" ]; then
  must "the host's permission rules are managed"  jq -e --arg r "$HOST_ALLOW_RULE" '.permissions.allow | index($r)' "$m"
fi
mustnt "an agent's rule is not managed"           jq -e '.permissions.allow | index("Bash(lea-isolation *)")' "$m"
mustnt "the host's rules are not writable"        sh -c 'echo "{}" >/etc/lea/claude-permissions.json'
must   "managed settings are root's"             test "$(stat -c %u "$m")" = 0
mustnt "managed settings are not writable"       touch "$m"
mustnt "managed settings cannot be replaced"     sh -c 'echo "{}" >"$1.new" && mv "$1.new" "$1"' sh "$m"
mustnt "no managed-settings.d drop-in"           mkdir /etc/claude-code/managed-settings.d
if [ -n "${HOST_CRED_SHA:-}" ]; then
  mustnt "host Claude credentials not visible"  test "$(sha_of "$HOME/.claude/.credentials.json")" = "$HOST_CRED_SHA"
fi
if [ -n "${HOST_HIST_SHA:-}" ]; then
  mustnt "host Claude history not visible"      test "$(sha_of "$HOME/.claude/history.jsonl")" = "$HOST_HIST_SHA"
fi
must   "only this projects dir under ~/.claude/projects" sh -c '[ "$(ls -A "$HOME/.claude/projects" | wc -l)" -eq 1 ]'
# lea's gh config of pinned calls is root's, read-only to the agent: gh must find it in its current
# format, or it migrates it and fails writing it back (every granted gh call failed so).
must   "lea's pinned gh config loads read-only"  sh -c '[ ! -w /usr/local/share/lea/forge-gh/config.yml ] && [ "$(GH_CONFIG_DIR=/usr/local/share/lea/forge-gh gh config get http_unix_socket)" = /run/lea-forge/gh.sock ]'
# GitHub (#103): no token where an agent reads it (the shared, read-only entries of ~/.claude are
# the user's own configuration), and gh and git still work: the proxy adds it.
if [ -n "${FORGE_NEEDLE:-}" ]; then
  ex="--exclude-dir=plugins --exclude-dir=skills --exclude-dir=hooks --exclude-dir=agents --exclude-dir=commands"
  # shellcheck disable=SC2086
  must   "no GitHub token readable in /lea/state or ~" sh -c '[ -z "$(grep -rlE "oauth_token|ghp_|github_pat_" '"$ex"' /lea/state "$HOME" 2>/dev/null)" ]'
  # shellcheck disable=SC2086
  must   "no copy of the GitHub token readable"   sh -c '[ -z "$(grep -rlF "$1" '"$ex"' /lea/state "$HOME" 2>/dev/null)" ]' sh "$FORGE_NEEDLE"
  must   "the GitHub relay socket is nobody's"     sh -c '[ "$(stat -c %U /run/lea-forge/gh.sock)" = nobody ] && [ "$(stat -c %U /run/lea-forge)" = nobody ]'
  must   "the GitHub relay runs as nobody"         sh -c 'p=$(pgrep -f "forge-rela[y] /run" | head -n 1) && [ "$(stat -c %u "/proc/$p")" = 65534 ]'
  mustnt "the GitHub relay cannot be stopped"      sh -c 'kill -0 "$(pgrep -f "forge-rela[y] /run" | head -n 1)"'
  must   "gh is pointed at the relay"             sh -c '[ "$(gh config get http_unix_socket)" = /run/lea-forge/gh.sock ]'
  # The proxy's allowlist (#103): a call that would add access is refused before it reaches GitHub.
  must   "gh: a call off lea's allowlist is refused" sh -c 'gh api -X PUT repos/DavidGodefroid/lea/collaborators/lea-allowlist-probe 2>&1 | grep -q "lea refuses this GitHub call"'
  must   "gh: a GraphQL mutation off lea's allowlist is refused" sh -c 'gh api graphql -f query="mutation { transferRepository(input: {}) { clientMutationId } }" 2>&1 | grep -q "lea refuses this GitHub call"'
  if [ "${FORGE_EXPECT:-}" = auth ]; then
    # 60 an hour is GitHub's limit without a token.
    must "gh api works with the token added by the proxy" sh -c 'l=$(gh api rate_limit --jq .rate.limit) && [ "$l" -gt 60 ]'
    must "git reaches github.com through the proxy's forwarder" git ls-remote https://github.com/DavidGodefroid/lea HEAD
    # GitHub redirects job logs to Azure blob storage: gh follows the redirect through the relay
    # too, and the forwarder sends it there (not to github.com), without the token.
    must "gh run view --log follows the redirect to its own host" sh -c '
      R=DavidGodefroid/lea
      r=$(gh run list -R "$R" -s success -L 1 --json databaseId --jq ".[0].databaseId") && [ -n "$r" ] &&
      j=$(gh run view -R "$R" "$r" --json jobs --jq ".jobs[0].databaseId") && [ -n "$j" ] &&
      [ "$(gh run view -R "$R" --job "$j" --log | wc -c)" -gt 0 ]'
  else
    must "gh api reaches GitHub with the token added by the proxy" sh -c 'gh api rate_limit 2>&1 | grep -q "Bad credentials"'
  fi
fi
must   "public HTTPS works through the proxy"   curl -sf --max-time 20 -o /dev/null https://registry.npmjs.org/
# The Claude API answers through the proxy (any HTTP status: no key is sent, 4xx is expected).
must   "the Claude API answers through the proxy" sh -c 'c=$(curl -s --max-time 20 -o /dev/null -w "%{http_code}" https://api.anthropic.com/v1/messages); [ "${c:-000}" != 000 ]'
# DNS (#98): the agent resolves no external name, whatever the resolver; the proxy does. lea-proxy
# (a container on lea's network) still resolves: Docker's embedded resolver answers it, and
# forwards nothing else (lea starts the container with --dns 192.0.2.1, which it has no route to).
# dns_answers SERVER udp|tcp NAME QTYPE: any DNS answer from SERVER:53 within 4 s.
# shellcheck disable=SC2317,SC2329 # run by must
dns_answers() {
  python3 -I -c 'import random, socket, struct, sys
s, proto, name, qt = sys.argv[1:]
q = struct.pack(">HHHHHH", random.randrange(65536), 0x0100, 1, 0, 0, 0)
q += b"".join(bytes([len(p)]) + p.encode() for p in name.split(".")) + b"\0" + struct.pack(">HH", int(qt), 1)
try:
    if proto == "udp":
        c = socket.socket(socket.AF_INET, socket.SOCK_DGRAM); c.settimeout(4); c.sendto(q, (s, 53)); c.recvfrom(4096)
    else:
        c = socket.create_connection((s, 53), timeout=4); c.settimeout(4); c.sendall(struct.pack(">H", len(q)) + q)
        if len(c.recv(2)) < 2: sys.exit(1)
except OSError: sys.exit(1)' "$@"
}
if [ "${SECOND_KERNEL:-}" = 1 ]; then
  # A sandboxed runtime's network stack may not carry Docker's embedded resolver: not containment.
  if getent hosts lea-proxy >/dev/null 2>&1; then echo "info lea-proxy resolves"; else echo "info lea-proxy does not resolve"; fi
else
  must   "lea-proxy resolves (Docker's embedded resolver)" getent hosts lea-proxy
fi
mustnt "an external name does not resolve (example.com)" getent hosts example.com
mustnt "an allowed name does not resolve (registry.npmjs.org)" getent hosts registry.npmjs.org
for s in 8.8.8.8 1.1.1.1 ${DECOY_DNS_IP:-} ${GW_IP:-} ${HOST_IP:-}; do
  for p in udp tcp; do
    mustnt "no direct DNS to $s:53 over $p" dns_answers "$s" "$p" example.com 1
  done
done
if [ -n "${DNS_MARK:-}" ]; then
  # Exfiltration attempts, a long label of a TXT lookup: through the resolver of resolv.conf (UDP
  # and TCP), glibc's, and straight to the decoy. isolation.sh asserts that none reached the decoy.
  long=aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa
  ns=$(awk '$1 == "nameserver" { print $2; exit }' /etc/resolv.conf)
  for p in udp tcp; do dns_answers "${ns:-127.0.0.11}" "$p" "$long.$p.$DNS_MARK" 16 || true; done
  getent hosts "$long.glibc.$DNS_MARK" >/dev/null 2>&1 || true
  [ -z "${DECOY_DNS_IP:-}" ] || dns_answers "$DECOY_DNS_IP" udp "$long.direct.$DNS_MARK" 16 || true
  ok "DNS exfiltration attempts sent (isolation.sh checks the decoy's log)"
fi
# The same from a nested container (pasta forwards its DNS to lea's resolver).
nested_dns() {
  docker run --rm -e S="$1" -e L="${2:-}" busybox:1.37 sh -c "$3" >/dev/null 2>&1
}
mustnt "nested: an external name does not resolve (example.com)" nested_dns '' '' 'timeout 20 nslookup example.com'
for s in 8.8.8.8 1.1.1.1 ${DECOY_DNS_IP:-}; do
  mustnt "nested: no direct DNS to $s:53 over udp" nested_dns "$s" '' 'timeout 20 nslookup example.com "$S"'
  mustnt "nested: no direct DNS to $s:53 over tcp" nested_dns "$s" '' 'timeout 20 nc -w 5 "$S" 53 </dev/null'
done
if [ -n "${DNS_MARK:-}" ]; then
  nested_dns '' "$long.nested.$DNS_MARK" 'timeout 20 nslookup -type=txt "$L"' || true
  [ -z "${DECOY_DNS_IP:-}" ] || nested_dns "$DECOY_DNS_IP" "$long.nested-direct.$DNS_MARK" 'timeout 20 nslookup -type=txt "$L" "$S"' || true
  ok "nested: DNS exfiltration attempts sent (isolation.sh checks the decoy's log)"
fi
# Plain HTTP between nested containers stays direct (containers.conf: http_proxy = false): a
# server, and a client in its network namespace fetching it at the namespace's own address (a
# private one, not in NO_PROXY, which the proxy would refuse). Not over a network of their own:
# the nested Podman cannot create one (README, Known limitations): netavark writes
# net/ipv4/conf/<bridge>/route_localnet in the rootless network namespace, whose value comes from
# the host's net.ipv4.conf.default (1 is needed, 0 is the usual default), and Docker mounts
# /proc/sys read-only. Port 8080: under gVisor the nested containers share lea's network namespace
# (image/entrypoint), where an unprivileged user binds no port below 1024. Its engine's output goes
# to a log, shown when it fails.
# shellcheck disable=SC2317,SC2329 # run by must
nested_http() (
  exec 2>>"$1"
  docker run -d --rm --name lea-it-web busybox:1.37 sh -c 'mkdir -p /w && echo hi >/w/index.html && httpd -f -p 8080 -h /w' >&2
  rc=1
  for _ in 1 2 3 4 5; do
    if docker run --rm --network container:lea-it-web busybox:1.37 sh -c '
      a=$(ip -4 -o addr show scope global | awk "{ print \$4 }" | cut -d/ -f1 | head -n 1)
      echo "address: ${a:-none}; HTTP proxy: ${http_proxy:-${HTTP_PROXY:-none}}" >&2
      [ -z "${http_proxy:-}${HTTP_PROXY:-}" ] && [ -n "$a" ] && wget -T 10 -qO- "http://$a:8080/"' | grep -qx hi; then rc=0; break; fi
    sleep 1
  done
  docker rm -f -t 0 lea-it-web >/dev/null 2>&1
  exit "$rc"
)
itlog=$(mktemp)
before=$fail; fail=0
must   "nested HTTP between containers is direct" nested_http "$itlog"
if [ "$fail" = 1 ]; then sed 's/^/     | /' "$itlog"; else fail=$before; fi
rm -f "$itlog"
exit "$fail"
