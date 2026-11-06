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

mustnt "no host Docker socket"                 test -S /var/run/docker.sock
mustnt "no host Docker socket (/run)"          test -S /run/docker.sock
must   "docker is the nested Podman"            sh -c 'docker version 2>/dev/null | grep -qi podman'
must   "a nested container runs"                docker run --rm busybox:1.37 true
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
must   "public HTTPS works through the proxy"   curl -sf --max-time 20 -o /dev/null https://registry.npmjs.org/
# Plain HTTP between nested containers stays direct (containers.conf: http_proxy = false): a
# server, and a client in its network namespace fetching it at the namespace's own address (a
# private one, not in NO_PROXY, which the proxy would refuse). Not over a network of their own:
# the nested Podman cannot create one (README, Known limitations): netavark writes
# net/ipv4/conf/<bridge>/route_localnet in the rootless network namespace, whose value comes from
# the host's net.ipv4.conf.default (1 is needed, 0 is the usual default), and Docker mounts
# /proc/sys read-only. Its engine's output goes to a log, shown when it fails.
# shellcheck disable=SC2317,SC2329 # run by must
nested_http() (
  exec 2>>"$1"
  docker run -d --rm --name lea-it-web busybox:1.37 sh -c 'mkdir -p /w && echo hi >/w/index.html && httpd -f -p 80 -h /w' >&2
  rc=1
  for _ in 1 2 3 4 5; do
    if docker run --rm --network container:lea-it-web busybox:1.37 sh -c '
      a=$(ip -4 -o addr show scope global | awk "{ print \$4 }" | cut -d/ -f1 | head -n 1)
      echo "address: ${a:-none}; HTTP proxy: ${http_proxy:-${HTTP_PROXY:-none}}" >&2
      [ -z "${http_proxy:-}${HTTP_PROXY:-}" ] && [ -n "$a" ] && wget -T 10 -qO- "http://$a/"' | grep -qx hi; then rc=0; break; fi
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
