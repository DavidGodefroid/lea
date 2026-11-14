#!/bin/sh
# lea#325, end to end: a lea agent reaches a lockssh lock that lockhub's `lox run` runs for the
# projects directory, through the lockssh client of lea's image (config/locks.lock), wired as lea
# wires it (LEA_LOCKSSH=1: lox run's socket volume of the lock on /run/lockssh, read-only, the
# lockssh-clients group, 10040, and the profile directory .lockssh read-only). In a real lea
# (lea:dev, `lea shell`), as the session account, with no model, against an SSH server in a
# container of this machine (login by key only, a log with e-mail addresses):
#   - lockssh_run of an allow-listed command (cat the log): its first connection approved at lox
#     run's terminal facade (lox approve), its output masked (no e-mail address reaches the agent);
#   - lockssh_run of a command outside the allow list (touch a file): pending on the hub, nothing
#     run on the server before the human decides; approved at lox approve; run again with the
#     ticket, it runs on the server.
# Flow: agent -> lockssh client -> socket -> console -> masked ticket -> hub -> lox approve ->
# signed decision -> console -> SSH -> agent. The agent never holds the key, nor talks to the hub.
#   - the set's releases: lockssh's client archive is the pinned one (its checksums.txt signed by
#     lockssh's release workflow at that tag, cosign), the console runs the set's image by digest,
#     cosign-verified, as do the hub and lox (lockhub LOCKHUB_VERSION, the set's lox by default);
#   - the agent cannot write .lockssh (its allowed commands and the auto rules lox run applies);
#   - lea doctor finds the lock lox runs compatible with lea's lockssh client;
#   - negative control: without the clients group, the kernel refuses the agent the socket.
# Linux hosts with docker (compose), cosign, curl, jq, python3, script (util-linux), ssh-keygen,
# the images lea:dev and lea-proxy:dev (make build), build/lea, and network access to GitHub and
# ghcr.io. Its own XDG_RUNTIME_DIR, XDG_STATE_HOME and XDG_DATA_HOME; creates only
# lea-lockssh-<pid>* resources and the compose project lox run makes for its temporary projects
# directory, and removes them.
set -eu
root=$(cd "$(dirname "$0")/../.." && pwd -P)
set_k() { sed -n "s/^$1=//p" "$root/config/locks.lock"; }
set_v() { sed -n "s/^$1=v\{0,1\}\([0-9][0-9.]*\)\$/\1/p" "$root/config/locks.lock"; }
LOCKHUB_VERSION=${LOCKHUB_VERSION:-$(set_v LOCKS_LOX_VERSION)}
case $(uname -m) in x86_64) arch=amd64 ;; aarch64) arch=arm64 ;; *) echo "lockssh-e2e: no release for $(uname -m)" >&2; exit 1 ;; esac
id=lea-lockssh-$$
uid=$(id -u)
port=${SSH_PORT:-22122}
fail=0
ok() { echo "ok    $1"; }
ko() { echo "FAIL  $1"; fail=1; }
die() { echo "lockssh-e2e: $*" >&2; exit 1; }
say() { printf '\n=== %s\n' "$*"; }

for i in lea:dev lea-proxy:dev; do docker image inspect "$i" >/dev/null 2>&1 || die "no image $i: make build"; done
[ -x "$root/build/lea" ] || die "no build/lea: go build -o build/lea ./cmd/lea"
for c in cosign curl jq python3 script ssh-keygen; do command -v "$c" >/dev/null 2>&1 || die "$c is missing"; done
docker compose version >/dev/null 2>&1 || die "docker compose is missing"

work=$(mktemp -d)
work=$(cd "$work" && pwd -P)
projects=$work/projects
keys=$work/keys
sshd=$id-sshd
mkdir -p "$projects/.e2e" "$work/run" "$work/state" "$work/bin" "$keys"
chmod 700 "$work/run"
chmod 0755 "$work" "$projects"
export XDG_DATA_HOME="$work/data" XDG_RUNTIME_DIR="$work/run" XDG_STATE_HOME="$work/state"
export TMUX_TMPDIR="$work"
lea_up=
lid=$(printf '%s' "$projects" | sha256sum | cut -c1-10)
slug=$(printf '%s' "$projects" | sed 's/[^A-Za-z0-9]/-/g')
agent_pid=
# shellcheck disable=SC2317 # run by the trap
cleanup() {
  [ -d "$work" ] || return 0
  if [ -n "$agent_pid" ]; then kill "$agent_pid" 2>/dev/null || true; fi
  if [ -n "$lea_up" ]; then "$root/bin/lea" stop --all >/dev/null 2>&1 || true; fi
  docker rm -fv "$id-$lid" "$id-proxy" "$id-plain" "$sshd" >/dev/null 2>&1 || true
  docker network rm "$id-internal-$lid" "$id-egress" >/dev/null 2>&1 || true
  docker volume rm "$id-podman-$lid" >/dev/null 2>&1 || true
  for c in "$XDG_STATE_HOME"/lockfamily/*/compose.yaml; do
    [ -f "$c" ] || continue
    d=$(dirname "$c")
    docker compose -p "$(basename "$d")" --project-directory "$d" -f "$c" down -v --remove-orphans >/dev/null 2>&1 || true
  done
  if [ -n "$slug" ]; then rm -r "${HOME:?}/.claude/projects/$slug" 2>/dev/null || true; fi
  docker run --rm --network none -v "$work:/w" docker.io/library/alpine:3.22 rm -rf /w/projects /w/data /w/state /w/run /w/keys >/dev/null 2>&1 || true
  rm -rf "$work"
}
trap cleanup EXIT
trap 'exit 130' INT TERM
# tty INPUT CMD...: CMD at a pseudo-terminal (lox approve refuses a pipe), INPUT typed in.
tty() { in=$1; shift; printf '%b' "$in" | script -qefc "$*" /dev/null; }

# expect.py TIMEOUT [PATTERN ANSWER]... -- CMD...: CMD at a pseudo-terminal; once PATTERN shows
# (after the previous one), ANSWER is typed (<pause>: 3 s first; \n escapes). Exit 1 when a
# pattern does not show within TIMEOUT seconds; else CMD's status. Its output to stdout.
cat >"$work/expect.py" <<'PY'
import os, pty, select, sys, time
a = sys.argv[1:]
cut = a.index("--")
head, cmd = a[:cut], a[cut + 1:]
timeout = float(head[0])
rules = [(head[i], head[i + 1]) for i in range(1, len(head), 2)]
pid, fd = pty.fork()
if pid == 0:
    os.execvp(cmd[0], cmd)
out, pos, end, eof = "", 0, time.time() + timeout, False
while time.time() < end and not eof and rules:
    if select.select([fd], [], [], 0.5)[0]:
        try:
            b = os.read(fd, 4096)
        except OSError:
            b = b""
        if not b:
            eof = True
        else:
            s = b.decode(errors="replace")
            sys.stdout.write(s)
            sys.stdout.flush()
            out += s
    while rules:
        pat, ans = rules[0]
        j = out.find(pat, pos)
        if j < 0:
            break
        pos = j + len(pat)
        rules.pop(0)
        if ans.startswith("<pause>"):
            time.sleep(3)
            ans = ans[len("<pause>"):]
        if ans:
            os.write(fd, ans.encode().decode("unicode_escape").encode())
if rules:
    sys.stderr.write("\nexpect.py: not seen within %ds: %r\n" % (timeout, rules[0][0]))
    os.kill(pid, 9)
    sys.exit(1)
while time.time() < end:
    if select.select([fd], [], [], 0.5)[0]:
        try:
            b = os.read(fd, 4096)
        except OSError:
            b = b""
        if not b:
            break
        sys.stdout.write(b.decode(errors="replace"))
if time.time() >= end:
    os.kill(pid, 9)
_, st = os.waitpid(pid, 0)
sys.exit(os.waitstatus_to_exitcode(st) if os.WIFEXITED(st) else 1)
PY
# attach.py TIMEOUT UNTIL -- CMD...: CMD (lox attach) at a pseudo-terminal; attaching replays
# nothing, so y is typed once attached (the console's policy review on a first start) and at each
# later [y/N]; detaches (Ctrl-P Ctrl-Q) once UNTIL shows (exit 0), else exit 1 after TIMEOUT.
cat >"$work/attach.py" <<'PY'
import os, pty, select, sys, time
a = sys.argv[1:]
timeout, until, cmd = float(a[0]), a[1], a[a.index("--") + 1:]
pid, fd = pty.fork()
if pid == 0:
    os.execvp(cmd[0], cmd)
time.sleep(3)
os.write(fd, b"y\r")
seen, yn, ok, end = "", 0, False, time.time() + timeout
while time.time() < end and not ok:
    if not select.select([fd], [], [], 0.5)[0]:
        continue
    try:
        b = os.read(fd, 4096)
    except OSError:
        break
    if not b:
        break
    s = b.decode(errors="replace")
    sys.stdout.write(s)
    seen += s
    if seen.count("[y/N]") > yn:
        yn = seen.count("[y/N]")
        os.write(fd, b"y\r")
    ok = until in seen
time.sleep(1)
try:
    os.write(fd, b"\x10\x11")
except OSError:
    pass
time.sleep(2)
try:
    os.kill(pid, 9)
except OSError:
    pass
sys.exit(0 if ok else 1)
PY
# agent.py OUT TOUCHED: lea's agent, run by lea shell in lea's container as the session account,
# from the projects directory: each call is the MCP server of lea's lockssh client (`lockssh mcp`,
# what a Codex agent's MCP list and a Claude agent's .mcp.json run) over stdio, as an agent's
# harness makes it: initialize, notifications/initialized, tools/call. Each answer (its structured
# content) goes to OUT/<step>.json: status; run1, the allow-listed command, retried while its
# first connection waits for the human; write, whether the agent can write
# .lockssh; exec1, the command outside the allow list (pending: its ticket to OUT/ticket); exec2,
# the same with that ticket, retried until the decision is there.
# agent.py OUT status-only: lockssh_status alone, to OUT/plain-status.json (the negative control).
cat >"$projects/.e2e/agent.py" <<'PY'
import json, os, subprocess, sys, time
out = sys.argv[1]
os.chdir(os.path.dirname(os.path.dirname(os.path.abspath(__file__))))
print("id:", subprocess.run(["id"], capture_output=True, text=True).stdout.strip(), flush=True)

def call(tool, args, timeout=300):
    p = subprocess.Popen(["lockssh", "mcp"], stdin=subprocess.PIPE, stdout=subprocess.PIPE, text=True)
    def send(m):
        p.stdin.write(json.dumps(m) + "\n")
        p.stdin.flush()
    def answer(i):
        end = time.time() + timeout
        for line in p.stdout:
            if time.time() > end:
                break
            line = line.strip()
            if line and json.loads(line).get("id") == i:
                return json.loads(line)
        return {"error": {"message": "lockssh mcp ended before answering"}}
    send({"jsonrpc": "2.0", "id": 1, "method": "initialize", "params": {"protocolVersion": "2025-06-18",
          "capabilities": {}, "clientInfo": {"name": "lea-lockssh-e2e", "version": "0"}}})
    answer(1)
    send({"jsonrpc": "2.0", "method": "notifications/initialized"})
    send({"jsonrpc": "2.0", "id": 2, "method": "tools/call", "params": {"name": tool, "arguments": args}})
    r = answer(2)
    p.stdin.close()
    try:
        p.wait(30)
    except subprocess.TimeoutExpired:
        p.kill()
    if "error" in r:
        return True, r["error"]
    res = r["result"]
    return bool(res.get("isError")), res.get("structuredContent", {"text": "\n".join(c.get("text", "") for c in res.get("content", []))})

def save(step, err, data):
    with open(os.path.join(out, step + ".json"), "w") as f:
        json.dump({"error": err, "data": data} if err else data, f)
    print("call", step, "error" if err else "ok", flush=True)
    return data

def mark(name, text=""):
    with open(os.path.join(out, name + ".tmp"), "w") as f:
        f.write(text)
    os.rename(os.path.join(out, name + ".tmp"), os.path.join(out, name))

def pending(err, d):
    dd = d.get("data") if isinstance(d, dict) else None
    return isinstance(dd, dict) and dd.get("status") == "pending" or "no decision yet" in json.dumps(d)

# until_done STEP ARGS: the call, again while it waits for the human (up to 300 s).
def until_done(step, args):
    end = time.time() + 300
    while True:
        err, d = call("lockssh_run", args)
        if not pending(err, d) or time.time() > end:
            return save(step, err, d), err
        time.sleep(2)

if sys.argv[2:] == ["status-only"]:
    save("plain-status", *call("lockssh_status", {}))
    sys.exit(0)
touched = sys.argv[2]
save("status", *call("lockssh_status", {}))
until_done("run1", {"argv": ["cat", "/home/deploy/app.log"], "intent": "lea e2e: who failed to pay"})
try:
    open(".lockssh/config.toml", "a").close()
    w = "written"
except OSError as e:
    w = "refused: " + e.strerror
save("write", False, {"config": w})
ask = {"argv": ["touch", touched], "intent": "lea e2e: a write the human approves"}
err, e1 = call("lockssh_run", ask)
save("exec1", err, e1)
ticket = (e1.get("data") or {}).get("ticket") if not err and isinstance(e1, dict) else None
if not ticket:
    sys.exit(1)
mark("ticket", ticket + "\n")
_, err = until_done("exec2", dict(ask, ticket=ticket))
sys.exit(1 if err else 0)
PY

say "the releases config/locks.lock pins, as one set"
A=$(printf '%s' "$arch" | tr '[:lower:]' '[:upper:]')
ls_v=$(set_v LOCKS_LOCKSSH_VERSION) ls_sum=$(set_k "LOCKS_LOCKSSH_SHA256_$A") ls_pin=$(set_k LOCKS_LOCKSSH_IMAGE)
for x in "$ls_v" "$ls_sum" "$ls_pin" "$LOCKHUB_VERSION"; do
  [ -n "$x" ] || die "config/locks.lock no longer pins lockssh or lox as this test reads them: update it"
done
echo "$(set_k LOCKS_SET): lockssh $ls_v, lox $LOCKHUB_VERSION ($arch)"
# verified REPO TAG: cosign's check of a release's checksums.txt, in $work/rel-REPO.
verified() {
  d=$work/rel-$1 u=https://github.com/DavidGodefroid/$1/releases/download/v$2
  mkdir -p "$d"
  for f in "${3:-}" checksums.txt checksums.txt.sigstore.json; do
    [ -z "$f" ] || curl -fsSL -o "$d/$f" "$u/$f" || die "cannot download $u/$f"
  done
  cosign verify-blob --bundle "$d/checksums.txt.sigstore.json" --certificate-oidc-issuer https://token.actions.githubusercontent.com \
    --certificate-identity "https://github.com/DavidGodefroid/$1/.github/workflows/release.yml@refs/tags/v$2" "$d/checksums.txt" >/dev/null 2>&1
}
a=lockssh_${ls_v}_linux_$arch.tar.gz
if verified lockssh "$ls_v" "$a" && (cd "$work/rel-lockssh" && grep "  $a\$" checksums.txt | sha256sum -c - >/dev/null) &&
  echo "$ls_sum  $work/rel-lockssh/$a" | sha256sum -c - >/dev/null; then
  ok "lockssh v$ls_v: the archive lea's image installs is the pinned one, its checksum signed by the release workflow at v$ls_v"
else die "lockssh v$ls_v: the archive does not match the pin, or its checksums are not signed by its release"; fi
# digest REPO VERSION: ghcr.io/davidgodefroid/REPO:VERSION by digest, once cosign verifies it (a
# cosign v2 retries the sigstore bundle format, as tools/locks-bump).
digest() {
  tok=$(curl -fsS "https://ghcr.io/token?scope=repository:davidgodefroid/$1:pull" | jq -r .token)
  dg=$(curl -fsSI -H "Authorization: Bearer $tok" \
    -H 'Accept: application/vnd.oci.image.index.v1+json, application/vnd.docker.distribution.manifest.list.v2+json' \
    "https://ghcr.io/v2/davidgodefroid/$1/manifests/$2" | tr -d '\r' | sed -n 's/^[Dd]ocker-[Cc]ontent-[Dd]igest: *//p')
  printf '%s\n' "$dg" | grep -Eqx 'sha256:[0-9a-f]{64}' || die "no digest for ghcr.io/davidgodefroid/$1:$2"
  for f in '' --new-bundle-format; do
    # shellcheck disable=SC2086 # an optional flag
    if cosign verify $f --certificate-oidc-issuer https://token.actions.githubusercontent.com \
      --certificate-identity "https://github.com/DavidGodefroid/$1/.github/workflows/release.yml@refs/tags/v$2" \
      "ghcr.io/davidgodefroid/$1@$dg" >/dev/null 2>&1; then
      printf 'ghcr.io/davidgodefroid/%s@%s' "$1" "$dg"
      return 0
    fi
  done
  die "ghcr.io/davidgodefroid/$1@$dg is not signed by $1's release v$2"
}
ls_img=$(digest lockssh "$ls_v")
if [ "$ls_img" = "$ls_pin" ]; then ok "lockssh image $ls_img, signed by its release workflow at v$ls_v, the set's"
else ko "lockssh image $ls_img (signed at v$ls_v) is not the one config/locks.lock pins ($ls_pin)"; fi
hub=$(digest lockhub "$LOCKHUB_VERSION")
ok "hub image $hub, signed by lockhub's release workflow at v$LOCKHUB_VERSION"
a=lox_${LOCKHUB_VERSION}_linux_$arch.tar.gz
if verified lockhub "$LOCKHUB_VERSION" "$a" && (cd "$work/rel-lockhub" && grep "  $a\$" checksums.txt | sha256sum -c - >/dev/null); then
  ok "lox v$LOCKHUB_VERSION: the archive's checksum signed by lockhub's release workflow"
else die "$a does not match lockhub's signed checksums"; fi
tar -xzf "$work/rel-lockhub/$a" -C "$work/bin" lox
lox=$work/bin/lox
"$lox" version

say "an SSH server: alpine's openssh, login deploy by key only, a log with e-mail addresses"
ssh-keygen -q -t ed25519 -N '' -C lea-lockssh-e2e -f "$keys/id_ed25519"
ssh-keygen -q -t ed25519 -N '' -C lea-sshd-e2e -f "$keys/host_ed25519"
chmod 0644 "$keys/host_ed25519" # read by the container's root only, from a throwaway mount
docker run -d --name "$sshd" -p "$port:22" -v "$keys:/keys:ro" docker.io/library/alpine:3.22 sh -c '
  set -e
  apk add --no-cache openssh >/dev/null
  adduser -D -s /bin/sh deploy
  sed -i "s/^deploy:!/deploy:*/" /etc/shadow
  install -d -m 0700 -o deploy -g deploy /home/deploy/.ssh
  install -m 0600 -o deploy -g deploy /keys/id_ed25519.pub /home/deploy/.ssh/authorized_keys
  install -m 0600 /keys/host_ed25519 /etc/ssh/ssh_host_ed25519_key
  printf "%s\n" "time=2026-11-14T10:00:00Z msg=\"payment failed\" user=alice.lea@example.com" \
    "time=2026-11-14T10:00:05Z msg=\"payment ok\" user=bob.lea@example.com" >/home/deploy/app.log
  chown deploy:deploy /home/deploy/app.log
  exec /usr/sbin/sshd -D -e -o PasswordAuthentication=no -o KbdInteractiveAuthentication=no -o PermitRootLogin=no \
    -o HostKey=/etc/ssh/ssh_host_ed25519_key -o AllowTcpForwarding=no -o AllowAgentForwarding=no -o X11Forwarding=no
' >/dev/null
up=
for i in $(seq 1 60); do
  if docker logs "$sshd" 2>&1 | grep -q 'Server listening'; then up=1; break; fi
  sleep 1
done
[ -n "$up" ] || { docker logs "$sshd"; die "sshd did not start"; }
# The host key the profile pins: the console asks no trust question.
printf '[host.docker.internal]:%s %s\n' "$port" "$(cut -d' ' -f1,2 "$keys/host_ed25519.pub")" >"$keys/known_hosts.e2e"
clear_values='alice.lea@example.com bob.lea@example.com'
# masked FILE: no clear e-mail address of the log in FILE.
masked() {
  for v in $clear_values; do if grep -qF "$v" "$1"; then return 1; fi; done
  return 0
}
touched=/home/deploy/$id

say "lox add lockssh web: the set's image, its profile (.lockssh/config.toml), the set's hub"
cd "$projects"
"$lox" add lockssh web --image "$ls_img" --profile e2e --hub-image "$hub"
mkdir -p .lockssh && chmod 0755 .lockssh
cat >.lockssh/config.toml <<EOF
[profiles.e2e]
host        = "host.docker.internal"
port        = $port
user        = "deploy"
auth        = "key"
key         = "~/.ssh/id_ed25519"
credentials = "none"
known_hosts = "~/.ssh/known_hosts.e2e"
production  = false
exec        = true
allow       = ["cat /home/deploy/app.log", "uptime"]
EOF
chmod 0644 .lockssh/config.toml
# allowed_uids: the uid lea's agents run as (lox add fills it when lea is on the PATH; lox run
# reviews it): lea's session account has no account in the console's image.
python3 -I - "$projects/.lockfamily/lockfamily.yaml" "$uid" <<'PY'
import sys
p, uid = sys.argv[1], sys.argv[2]
out = []
for line in open(p):
    out.append(line)
    if line.rstrip("\n") == "  web:":
        out.append("    allowed_uids: [%s]\n" % uid)
open(p, "w").write("".join(out))
PY
cat .lockfamily/lockfamily.yaml
if grep -q "allowed_uids: \[$uid\]" .lockfamily/lockfamily.yaml; then ok "lockfamily.yaml: lock web (lockssh), allowed_uids [$uid]"
else die "lockfamily.yaml lacks the lock's allowed_uids"; fi

say "lox run, a first look: the key and known_hosts copied into the lock's home as its review says"
python3 -I "$work/expect.py" 600 'Apply? [y/N]' 'n\n' -- "$lox" run >"$work/look.raw" 2>&1 || true
tr -d '\r' <"$work/look.raw" >"$work/look.out"
grep -q 'Apply? \[y/N\]' "$work/look.out" || { cat "$work/look.out"; die "lox run did not reach its review"; }
for f in id_ed25519 known_hosts.e2e; do
  cmd=$(sed -n "s/.* is not in the lock's home volume; copy it there: \(docker run .* < $f\)\$/\1/p" "$work/look.out" | head -1)
  [ -n "$cmd" ] || { cat "$work/look.out"; die "the review gives no command to copy $f into the lock's home"; }
  (cd "$keys" && sh -c "$cmd") || die "the review's copy command for $f failed"
done

say "lox run: its review (the lock, its egress, its auto rules), then the hub and the console"
python3 -I "$work/expect.py" 600 'Apply? [y/N]' 'y\n' "up: project " "<pause>qq" -- "$lox" run >"$work/run.raw" 2>&1 ||
  { tr -d '\r' <"$work/run.raw"; die "lox run failed"; }
tr -d '\r' <"$work/run.raw" >"$work/run.out"
sed -n '1,/^up: project /p' "$work/run.out"
if grep -qF "ssh deploy@host.docker.internal:$port" "$work/run.out"; then ok "lox run reviewed the lock's egress: ssh deploy@host.docker.internal:$port"
else ko "lox run's review lacks the lock's SSH egress"; fi
p=$(jq -r .project .lockfamily/up.json)
if [ -z "$p" ] || [ "$p" = null ]; then die "no project in up.json"; fi
v=$(jq -r '.locks["web"].socket_volume' .lockfamily/up.json)
if [ "$v" = "${p}_web-run" ] && [ "$(docker volume inspect -f '{{index .Labels "dev.lockfamily.type"}}' "$v" 2>/dev/null)" = lockssh ]; then
  ok "up.json: lock web (lockssh), its socket volume $v"
else ko "up.json: lock web: socket volume '$v'"; fi
if python3 -I "$work/attach.py" 120 'Listening on ' -- "$lox" attach web >"$work/attach.log" 2>&1; then
  ok "lox attach web: its policy approved at its terminal, the console listens"
else
  tr -d '\r' <"$work/attach.log" | tail -30 >&2
  die "lox attach web: the console never listened"
fi

# approve_one WHAT: one pending ticket approved at lox run's terminal facade (as a human at a
# terminal); its screen in $work/approve-WHAT.out.
approve_one() {
  for i in $(seq 1 60); do
    tty 'y\n' "$lox" approve --once >"$work/approve-$1.out" 2>&1 || true
    if tr -d '\r' <"$work/approve-$1.out" | grep -Eq '(^|: )approved$'; then return 0; fi
    sleep 2
  done
  return 1
}

say "lea: a session wired for lockssh (LEA_LOCKSSH=1, lea shell); its agent's calls"
cat >"$work/lea.conf" <<CONF
LEA_PREFIX=$id
LEA_IMAGE=lea:dev
LEA_PROXY_IMAGE=lea-proxy:dev
LEA_LOCKSSH=1
LEA_HUB=0
CONF
export LEA_CONFIG="$work/lea.conf"
lea_up=1
state=$XDG_DATA_HOME/lea/state
mkdir -p "$state"
printf '{}\n' >"$state/claude.json"
o=$projects/.e2e/out
mkdir -p "$o"
LEA_PROJECTS_DIR="$projects" "$root/bin/lea" shell -- python3 -I "$projects/.e2e/agent.py" "$o" "$touched" </dev/null >"$work/agent.out" 2>&1 &
agent_pid=$!
show() { echo "      $1: $(cut -c1-400 "$o/$1.json" 2>/dev/null)"; }

say "the allow-listed command: its first connection approved at lox approve, its output masked"
i=0
until [ -f "$o/status.json" ] || ! kill -0 "$agent_pid" 2>/dev/null; do
  i=$((i + 1))
  [ "$i" -lt 300 ] || break
  sleep 1
done
show status
if jq -e '[.data[] | select(.profile == "e2e" and .running == true)] | length == 1' "$o/status.json" >/dev/null 2>&1; then
  ok "lea's agent: lockssh_status reaches the running lockssh console (profile e2e)"
else
  cat "$work/agent.out"
  die "lea's agent: lockssh_status: $(cat "$o/status.json" 2>/dev/null)"
fi
# The first use of an SSH session is a connect ticket (read-pii, never auto on the console's first
# run), whether the agent's call waits for it or answers pending: the human approves it.
if approve_one connect && masked "$work/approve-connect.out" && tr -d '\r' <"$work/approve-connect.out" | grep -q lockssh; then
  ok "lox approve: the agent's first connection to the server, approved as a human at a terminal, masked"
else
  tr -d '\r' <"$work/approve-connect.out" 2>/dev/null
  ko "the agent's first connection was not approved at lox approve"
fi
i=0
until [ -f "$o/run1.json" ] || ! kill -0 "$agent_pid" 2>/dev/null; do
  i=$((i + 1))
  [ "$i" -lt 300 ] || break
  sleep 1
done
show run1
if jq -e '.data.exit_status == 0' "$o/run1.json" >/dev/null 2>&1 && masked "$o/run1.json" && grep -q 'email:' "$o/run1.json"; then
  ok "lea's agent: lockssh_run of an allowed command runs on the server, its e-mail addresses masked (pseudonyms)"
else ko "lea's agent: lockssh_run (allowed): $(cat "$o/run1.json" 2>/dev/null)"; fi

say "the command outside the allow list: pending on the hub, nothing run before the human decides"
i=0
until [ -f "$o/ticket" ] || ! kill -0 "$agent_pid" 2>/dev/null; do
  i=$((i + 1))
  [ "$i" -lt 300 ] || break
  sleep 1
done
show write
if jq -e '.config | startswith("refused")' "$o/write.json" >/dev/null 2>&1; then
  ok "lea's agent cannot write .lockssh/config.toml (its allowed commands and auto rules): $(jq -r .config "$o/write.json")"
else ko "lea's agent wrote .lockssh: $(cat "$o/write.json" 2>/dev/null)"; fi
show exec1
ticket=$(sed -n 1p "$o/ticket" 2>/dev/null)
if [ -n "$ticket" ] && jq -e --arg t "$ticket" '.data.status == "pending" and .data.lock == "lockssh" and .data.instance == "web" and .data.ticket == $t' \
  "$o/exec1.json" >/dev/null 2>&1; then
  ok "lea's agent: lockssh_run of a command outside the allow list is pending on the hub, ticket $ticket"
else
  cat "$work/agent.out"
  die "lea's agent: the command outside the allow list did not answer pending with a ticket: $(cat "$o/exec1.json" 2>/dev/null)"
fi
if docker exec "$sshd" test -e "$touched"; then ko "the command ran on the server before the human decided"
else ok "nothing ran on the server before the human decided"; fi
if approve_one exec && tr -d '\r' <"$work/approve-exec.out" | grep -q "$ticket"; then
  ok "lox approve: the lockssh ticket $ticket approved, as a human at a terminal"
else
  tr -d '\r' <"$work/approve-exec.out" 2>/dev/null
  docker logs "${p}-lockhub-1" 2>&1 | tail -20 >&2
  die "the agent's exec ticket was not approved at lox approve"
fi

say "the command run again with the ticket: it runs on the server"
wait "$agent_pid" || true
agent_pid=
cat "$work/agent.out"
show exec2
if jq -e '.data.exit_status == 0' "$o/exec2.json" >/dev/null 2>&1 && docker exec "$sshd" test -e "$touched"; then
  ok "lea's agent: the approved lockssh_run runs the command on the server"
else ko "lea's agent: the approved lockssh_run: $(cat "$o/exec2.json" 2>/dev/null)"; fi

say "lea's container: the wiring, and lea doctor"
c=$id-$lid
w=$(docker inspect -f '{{json .HostConfig.GroupAdd}} {{range .Mounts}}{{if eq .Type "volume"}}{{.Name}}:{{.Destination}}:{{.RW}} {{end}}{{end}}' "$c" 2>/dev/null) || w=
echo "      $c: $w"
case " $w " in
  *'"10040"'*" ${p}_web-run:/run/lockssh:false "*) ok "lea's container: --group-add 10040, lox run's socket volume on /run/lockssh, read-only" ;;
  *) ko "lea's container lacks the lockssh wiring: $w" ;;
esac
b=$(docker inspect -f '{{range .Mounts}}{{if eq .Type "bind"}}{{.Destination}}:{{.RW}} {{end}}{{end}}' "$c" 2>/dev/null) || b=
case " $b " in
  *" $projects/.lockssh:false "*) ok "lea's container: $projects/.lockssh, read-only" ;;
  *) ko "lea's container does not mount .lockssh read-only: $b" ;;
esac
case $(sed -n 's/^id: //p' "$work/agent.out") in
  *"10040(lockssh-clients)"*) ok "the session account (uid $uid) is in lockssh-clients" ;;
  *) ko "the session account's groups: $(sed -n 's/^id: //p' "$work/agent.out")" ;;
esac
LEA_PROJECTS_DIR="$projects" "$root/bin/lea" doctor </dev/null >"$work/doctor.out" 2>&1 || true
grep -i 'lock\|lox' "$work/doctor.out" | sed 's/^/      doctor: /' || true
if grep -qF "ok    lock web (lockssh): runs lockssh v$ls_v, the release of lea's lockssh client v$ls_v (protocol " "$work/doctor.out"; then
  ok "lea doctor: lock web (lockssh) runs v$ls_v, the release of lea's client"
else ko "lea doctor: lock web (lockssh): $(grep -F 'lock web (lockssh)' "$work/doctor.out" || echo 'no line')"; fi

say "negative control: lea's image as the session account, without lockssh-clients"
docker run --rm --name "$id-plain" --network none --user "$uid:$(id -g)" -e HOME=/tmp -w "$projects" \
  --mount "type=bind,src=$projects,dst=$projects" --mount "type=volume,src=${p}_web-run,dst=/run/lockssh,readonly" \
  --entrypoint python3 lea:dev -I "$projects/.e2e/agent.py" "$o" status-only >"$work/plain.out" 2>&1 || true
show plain-status
if [ -s "$o/plain-status.json" ] && ! jq -e '[.data[]? | select(.running == true)] | length > 0' "$o/plain-status.json" >/dev/null 2>&1; then
  ok "without lockssh-clients, lea's image reaches no console: $(cut -c1-160 "$o/plain-status.json")"
else ko "without its group: $(cat "$work/plain.out" "$o/plain-status.json" 2>/dev/null)"; fi
exit "$fail"
