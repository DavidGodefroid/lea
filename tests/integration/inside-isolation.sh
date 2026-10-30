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
if [ -e "$HOME/.claude/settings.json" ]; then
  mustnt "settings.json is read-only"           touch "$HOME/.claude/settings.json"
fi
if [ -d "$HOME/.claude/plugins" ]; then
  mustnt "plugins are read-only"                touch "$HOME/.claude/plugins/.lea-write-test"
  if [ -e "$HOME/.claude/plugins/installed_plugins.json" ]; then
    mustnt "installed_plugins.json is read-only" touch "$HOME/.claude/plugins/installed_plugins.json"
  fi
  # Claude Code mkdirs each plugin's CLAUDE_PLUGIN_DATA here before running its hooks (#90).
  must   "plugins/data is writable"             sh -c 'mkdir -p "$HOME/.claude/plugins/data/lea-inline" && touch "$HOME/.claude/plugins/data/lea-inline/.lea-write-test"'
fi
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
