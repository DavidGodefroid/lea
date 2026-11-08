#!/bin/sh
# Without the deck (LEA_DECK=0, #135), on a real engine: `lea` runs the user's own tool inside the
# same lea container as the deck would start (its isolation checked from inside), `lea attach
# --print` gives a docker exec command that works as is from another terminal, and `lea config
# deck on|off` switches between the deck and the tool with the same container, never rebuilt.
# Linux hosts; needs make build, build/lea, tmux 3.2 or later and script(1). Creates and removes
# only lea-nodeck-* resources; lea's tmux server and daemon are this test's own (TMUX_TMPDIR,
# XDG_DATA_HOME).
set -eu
root=$(cd "$(dirname "$0")/../.." && pwd -P)
image=${LEA_TEST_IMAGE:-lea:dev}
if docker info --format '{{json .SecurityOptions}}' 2>/dev/null | grep -q 'name=apparmor'; then
  if ! docker run --rm --pull never --security-opt apparmor=lea --entrypoint true "$image" >/dev/null 2>&1; then
    echo "nodeck: the AppArmor profile 'lea' is not usable (or $image is not built): run 'lea init' (AppArmor) and 'make build' first" >&2
    exit 1
  fi
fi
command -v script >/dev/null 2>&1 || { echo "nodeck: script(1) is needed (util-linux)" >&2; exit 1; }

work=$(mktemp -d) || exit 1
work=$(cd "$work" && pwd -P) || exit 1
projects="$work/projects"; mkdir -p "$projects"
export XDG_DATA_HOME="$work/data" TMUX_TMPDIR="$work/tmux"
mkdir -p "$TMUX_TMPDIR"
cat >"$work/lea.conf" <<CONF
LEA_PREFIX=lea-nodeck
LEA_IMAGE=$image
LEA_PROXY_IMAGE=lea-proxy:dev
LEA_HUB=0
CONF
export LEA_CONFIG="$work/lea.conf" LEA_PROJECTS_DIR="$projects"
state="$XDG_DATA_HOME/lea/state"; mkdir -p "$state"; printf '{}\n' >"$state/claude.json"
id=$(printf '%s' "$projects" | sha256sum | cut -c1-10)
name="lea-nodeck-$id"
slug=$(printf '%s' "$projects" | sed 's/[^A-Za-z0-9]/-/g')
lea="$root/bin/lea"
# A file in the host's ~/.ssh (made when there is none, as on CI): the tool must not see it.
made_ssh='' canary="$HOME/.ssh/lea-nodeck-canary-$$"
if [ ! -d "$HOME/.ssh" ]; then mkdir -m 700 "$HOME/.ssh"; made_ssh=1; fi
: >"$canary"
holder=''

# shellcheck disable=SC2317 # invoked via trap
cleanup() {
  : >"$projects/.release"
  if [ -n "$holder" ]; then
    i=0
    while kill -0 "$holder" 2>/dev/null && [ "$i" -lt 30 ]; do i=$((i + 1)); sleep 1; done
  fi
  "$lea" stop --all >/dev/null 2>&1 || true
  docker rm -f "$name" lea-nodeck-proxy >/dev/null 2>&1 || true
  docker network rm "lea-nodeck-internal-$id" lea-nodeck-egress >/dev/null 2>&1 || true
  docker volume rm "lea-nodeck-podman-$id" >/dev/null 2>&1 || true
  if [ -n "$slug" ]; then rm -r "${HOME:?}/.claude/projects/$slug" 2>/dev/null || true; fi
  rm -f "$canary"
  if [ -n "$made_ssh" ]; then rmdir "$HOME/.ssh" 2>/dev/null || true; fi
  rm -rf "${work:?}"
}
trap cleanup EXIT

rc=0
ok() { echo "ok   $1"; }
ko() { echo "FAIL $1"; rc=1; }
cid() { docker container inspect -f '{{.Id}}' "$name" 2>/dev/null || true; }

# 0. A configuration without LEA_DECK (an install made before the deck was optional, #335), on a
# pty: lea asks the deck question once; n, then Enter (the default command) runs Claude Code in
# lea-<id> without the deck, and the answer is recorded so the next lea asks nothing.
printf 'n\n\n' | script -qec "timeout 600 $lea --version" /dev/null >"$work/ask.log" 2>&1 || true
if grep -q 'Use the lea deck' "$work/ask.log" && grep -q '(Claude Code)' "$work/ask.log" && ! tmux -L lea has-session -t "=$name" 2>/dev/null; then
  ok "no LEA_DECK: lea asks the deck question, n runs Claude Code without the deck"
else ko "the deck question on a pty:"; tail -20 "$work/ask.log"; fi
if grep -qx "LEA_DECK='0'" "$LEA_CONFIG" && grep -qx "LEA_TOOL='claude'" "$LEA_CONFIG"; then ok "the answer is recorded (LEA_DECK=0, LEA_TOOL=claude)"
else ko "the answer is not recorded: $(cat "$LEA_CONFIG")"; fi
printf '' | script -qec "timeout 600 $lea --version" /dev/null >"$work/ask2.log" 2>&1 || true
if ! grep -q 'Use the lea deck' "$work/ask2.log" && grep -q '(Claude Code)' "$work/ask2.log"; then ok "the next lea asks nothing"
else ko "the deck question came again:"; tail -20 "$work/ask2.log"; fi

# 1. A fresh install's choice (lea init --no-deck --tool claude records the same two keys).
"$lea" config deck off
"$lea" config tool claude
if grep -qx "LEA_DECK='0'" "$LEA_CONFIG" && grep -qx "LEA_TOOL='claude'" "$LEA_CONFIG"; then ok "lea config deck off, lea config tool claude"
else ko "lea config deck off, lea config tool claude: $(cat "$LEA_CONFIG")"; fi

# 2. lea drops into Claude Code inside lea-<id> (here asked for its version: no login needed).
out=$(timeout 600 "$lea" --version </dev/null 2>&1) || true
case $out in *"(Claude Code)"*) ok "lea runs Claude Code in the lea container ($(printf '%s' "$out" | tail -1))" ;;
  *) ko "lea runs Claude Code in the lea container:"; printf '%s\n' "$out" | tail -20 ;; esac
if docker container inspect "$name" >/dev/null 2>&1; then ok "the session ran in $name"; else ko "no container $name"; fi
if docker container inspect "$name" -f '{{.Config.Image}}' 2>/dev/null | grep -qxF "$image"; then ok "$name runs $image"; else ko "$name does not run $image"; fi

# 3. The isolation, checked from inside by the user's own tool (a script, run as LEA_TOOL).
cat >"$projects/inside-nodeck.sh" <<'EOF'
#!/bin/sh
set -u
fail=0
ok() { echo "ok   $1"; }
ko() { echo "FAIL $1"; fail=1; }
[ -z "${LEA_AGENT:-}" ] && ok "the tool is the user's, not an agent" || ko "LEA_AGENT=$LEA_AGENT in the user's tool"
[ "$(cat /proc/sys/kernel/hostname)" = lea ] && ok "inside the lea container (hostname lea)" || ko "hostname $(cat /proc/sys/kernel/hostname)"
c=$(curl -s -o /dev/null -w '%{http_code}' --max-time 20 https://example.org/ 2>/dev/null); c=${c:-000}
[ "$c" != 200 ] && ok "egress to a domain off the allowlist refused ($c)" || ko "egress to example.org answered 200"
curl -sf --max-time 20 -o /dev/null https://registry.npmjs.org/ && ok "an allowed domain answers through the proxy (positive control)" ||
  ko "registry.npmjs.org does not answer through the proxy"
canary=$(cat "$(dirname "$0")/.canary")
[ ! -e "$canary" ] && ok "the host's ~/.ssh is not visible ($canary absent)" || ko "the host's $canary is visible"
! grep -q '/\.ssh ' /proc/self/mountinfo && ok "no .ssh directory is mounted" || ko "a .ssh directory is mounted"
locksql --version >/dev/null 2>&1 && ok "the locksql client is in the container" || ko "no locksql client"
[ -S "${LEA_BUS_SOCK:-/run/lea/bus/bus.sock}" ] && ok "lea's bus (its MCP server's socket) is mounted" || ko "no bus socket"
exit "$fail"
EOF
printf '%s\n' "$canary" >"$projects/.canary"
"$lea" config tool sh "$projects/inside-nodeck.sh" >/dev/null 2>&1
if timeout 300 "$lea" </dev/null; then ok "the checks inside the tool's session"; else ko "the checks inside the tool's session"; fi

# 4. A session held open, then lea attach --print from a second terminal, run as is.
cat >"$projects/hold.sh" <<'EOF'
#!/bin/sh
while [ ! -e "$(dirname "$0")/.release" ]; do sleep 1; done
EOF
"$lea" config tool sh "$projects/hold.sh" >/dev/null 2>&1
timeout 900 "$lea" </dev/null >"$work/hold.log" 2>&1 &
holder=$!
i=0
until [ "$(docker container inspect -f '{{.State.Running}}' "$name" 2>/dev/null)" = true ] && docker exec "$name" pgrep -f hold.sh >/dev/null 2>&1; do
  i=$((i + 1)); if [ "$i" -ge 180 ]; then ko "the held session did not start"; cat "$work/hold.log"; exit 1; fi; sleep 1
done
c1=$(cid)
cat >"$projects/probe.sh" <<'EOF'
#!/bin/sh
printf '%s %s %s\n' "$(cat /proc/sys/kernel/hostname)" "$(id -u)" "$(pwd)" >"$(dirname "$0")/.probe"
EOF
"$lea" config tool sh "$projects/probe.sh" >/dev/null 2>&1
cmd=$("$lea" attach --print)
echo "     lea attach --print: $cmd"
rm -f "$projects/.probe"
script -qec "$cmd" /dev/null </dev/null >"$work/attach.log" 2>&1 || true
if [ "$(cat "$projects/.probe" 2>/dev/null)" = "lea $(id -u) $projects" ]; then ok "lea attach --print works as is from another terminal"
else ko "lea attach --print's command: got '$(cat "$projects/.probe" 2>/dev/null)'"; cat "$work/attach.log"; fi

# 5. lea config deck on, then lea: the deck opens on the same container; off: the tool again.
"$lea" config deck on
img1=$(docker image inspect -f '{{.Id}}' "$image")
script -qec "timeout 30 $lea" /dev/null </dev/null >"$work/deck.log" 2>&1 || true
if tmux -L lea has-session -t "=$name" 2>/dev/null; then ok "lea config deck on, then lea: the deck opens"
else ko "the deck did not open"; tail -20 "$work/deck.log"; fi
if [ "$(cid)" = "$c1" ]; then ok "the deck runs in the same container"; else ko "the container changed with the deck ($c1, now $(cid))"; fi
"$lea" config deck off
rm -f "$projects/.probe"
timeout 300 "$lea" </dev/null >"$work/back.log" 2>&1 || true
if [ -s "$projects/.probe" ]; then ok "lea config deck off, then lea: the tool again"; else ko "the tool did not run after deck off"; cat "$work/back.log"; fi
if [ "$(cid)" = "$c1" ]; then ok "the tool runs in the same container"; else ko "the container changed with deck off ($c1, now $(cid))"; fi
if [ "$(docker image inspect -f '{{.Id}}' "$image")" = "$img1" ]; then ok "no image was rebuilt"; else ko "the image changed"; fi
if [ "$rc" -eq 0 ]; then echo "nodeck: OK"; fi
exit "$rc"
