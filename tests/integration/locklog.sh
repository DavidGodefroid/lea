#!/bin/sh
# lea#324, end to end: a lea agent reaches a locklog lock that lockhub's `lox run` runs for the
# projects directory, through the locklog client of lea's image (config/locks.lock), wired as lea
# wires it (LEA_LOCKLOG=1: lox run's socket volume of the lock on /run/locklog, read-only, and the
# locklog-clients group, 10050). In a real lea (lea:dev, `lea shell`), as the session account,
# with no model:
#   - locklog_count: free, no ticket, and masked (no e-mail address of the log reaches the agent);
#   - locklog_search then locklog_unmask of one record's e-mail pseudonym: pending on the hub, a
#     masked ticket; approved at lox run's terminal facade (lox approve, as a human at a terminal);
#     the unmask run again with that ticket gives the agent the one clear value, and the hub's
#     data never holds it.
# Flow: agent -> locklog client -> socket -> console -> masked ticket -> hub -> lox approve ->
# signed decision -> console -> agent. The agent never talks to the hub.
#   - the set's releases: locklog's client archive is the pinned one (its checksums.txt signed by
#     locklog's release workflow at that tag, cosign), the console runs the set's image by digest,
#     cosign-verified, as do the hub and lox (lockhub LOCKHUB_VERSION, the set's lox by default);
#   - lea doctor finds the lock lox runs compatible with lea's locklog client;
#   - negative control: without the clients group, the kernel refuses the agent the socket.
# Linux hosts with docker (compose), sudo (the log directory under /var/log, as a host's logs),
# cosign, curl, jq, python3, script (util-linux), the images lea:dev and lea-proxy:dev (make
# build), build/lea, and network access to GitHub and ghcr.io. Its own XDG_RUNTIME_DIR,
# XDG_STATE_HOME and XDG_DATA_HOME; creates only lea-locklog-<pid>* resources, /var/log/<the same>
# and the compose project lox run makes for its temporary projects directory, and removes them.
set -eu
root=$(cd "$(dirname "$0")/../.." && pwd -P)
set_k() { sed -n "s/^$1=//p" "$root/config/locks.lock"; }
set_v() { sed -n "s/^$1=v\{0,1\}\([0-9][0-9.]*\)\$/\1/p" "$root/config/locks.lock"; }
LOCKHUB_VERSION=${LOCKHUB_VERSION:-$(set_v LOCKS_LOX_VERSION)}
case $(uname -m) in x86_64) arch=amd64 ;; aarch64) arch=arm64 ;; *) echo "locklog-e2e: no release for $(uname -m)" >&2; exit 1 ;; esac
id=lea-locklog-$$
uid=$(id -u)
fail=0
ok() { echo "ok    $1"; }
ko() { echo "FAIL  $1"; fail=1; }
die() { echo "locklog-e2e: $*" >&2; exit 1; }
say() { printf '\n=== %s\n' "$*"; }

for i in lea:dev lea-proxy:dev; do docker image inspect "$i" >/dev/null 2>&1 || die "no image $i: make build"; done
[ -x "$root/build/lea" ] || die "no build/lea: go build -o build/lea ./cmd/lea"
for c in cosign curl jq python3 script sudo; do command -v "$c" >/dev/null 2>&1 || die "$c is missing"; done
docker compose version >/dev/null 2>&1 || die "docker compose is missing"

work=$(mktemp -d)
work=$(cd "$work" && pwd -P)
projects=$work/projects
logs=/var/log/$id
mkdir -p "$projects/.e2e" "$work/run" "$work/state" "$work/bin"
chmod 700 "$work/run"
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
  docker rm -fv "$id-$lid" "$id-proxy" "$id-plain" >/dev/null 2>&1 || true
  docker network rm "$id-internal-$lid" "$id-egress" >/dev/null 2>&1 || true
  docker volume rm "$id-podman-$lid" >/dev/null 2>&1 || true
  for c in "$XDG_STATE_HOME"/lockfamily/*/compose.yaml; do
    [ -f "$c" ] || continue
    d=$(dirname "$c")
    docker compose -p "$(basename "$d")" --project-directory "$d" -f "$c" down -v --remove-orphans >/dev/null 2>&1 || true
  done
  if [ -n "$slug" ]; then rm -r "${HOME:?}/.claude/projects/$slug" 2>/dev/null || true; fi
  sudo rm -rf "$logs"
  docker run --rm --network none -v "$work:/w" docker.io/library/alpine:3.22 rm -rf /w/projects /w/data /w/state /w/run >/dev/null 2>&1 || true
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
# agent.py OUT: lea's agent, run by lea shell in lea's container as the session account, from the
# projects directory: each call is the MCP server of lea's locklog client (`locklog mcp`, what a
# Codex agent's MCP list and a Claude agent's .mcp.json run) over stdio, as an agent's harness
# makes it: initialize, notifications/initialized, tools/call. Each answer (its structured
# content) goes to OUT/<step>.json: status, count, search, unmask1 (pending: its ticket also to
# OUT/ticket), then unmask2, the unmask with that ticket, retried until the decision is there.
# agent.py OUT status-only: locklog_status alone, to OUT/plain-status.json (the negative control).
cat >"$projects/.e2e/agent.py" <<'PY'
import json, os, re, subprocess, sys, time
out = sys.argv[1]
os.chdir(os.path.dirname(os.path.dirname(os.path.abspath(__file__))))
print("id:", subprocess.run(["id"], capture_output=True, text=True).stdout.strip(), flush=True)

def call(tool, args, timeout=120):
    p = subprocess.Popen(["locklog", "mcp"], stdin=subprocess.PIPE, stdout=subprocess.PIPE, text=True)
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
        return {"error": {"message": "locklog mcp ended before answering"}}
    send({"jsonrpc": "2.0", "id": 1, "method": "initialize", "params": {"protocolVersion": "2025-06-18",
          "capabilities": {}, "clientInfo": {"name": "lea-locklog-e2e", "version": "0"}}})
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

if sys.argv[2:] == ["status-only"]:
    save("plain-status", *call("locklog_status", {}))
    sys.exit(0)
rng = {"from": "2026-11-06T09:00:00Z", "to": "2026-11-06T11:00:00Z"}
save("status", *call("locklog_status", {}))
save("count", *call("locklog_count", dict(rng)))
err, s = call("locklog_search", dict(rng, limit=1))
save("search", err, s)
if err:
    sys.exit(1)
rec = s["data"]["records"][0]
tok = re.search(r"⟦email:[A-Z0-9]*⟧", json.dumps(rec, ensure_ascii=False)).group(0)
ask = {"ref": rec["ref"], "pseudonyms": [tok], "reason": "lea e2e: the user asked who failed to pay"}
err, u = call("locklog_unmask", ask)
save("unmask1", err, u)
ticket = (u.get("data") or {}).get("ticket") if not err else None
if not ticket:
    sys.exit(1)
with open(os.path.join(out, "ticket.tmp"), "w") as f:
    f.write(ticket + "\n" + tok + "\n")
os.rename(os.path.join(out, "ticket.tmp"), os.path.join(out, "ticket"))
end = time.time() + 300
while time.time() < end:
    err, u2 = call("locklog_unmask", dict(ask, ticket=ticket))
    d = u2.get("data") if isinstance(u2, dict) else None
    if not err and not (isinstance(d, dict) and d.get("status") == "pending") and "no decision yet" not in json.dumps(u2):
        save("unmask2", err, u2)
        sys.exit(0)
    time.sleep(2)
save("unmask2", True, u2)
sys.exit(1)
PY

say "the releases config/locks.lock pins, as one set"
A=$(printf '%s' "$arch" | tr '[:lower:]' '[:upper:]')
ll_v=$(set_v LOCKS_LOCKLOG_VERSION) ll_sum=$(set_k "LOCKS_LOCKLOG_SHA256_$A") ll_pin=$(set_k LOCKS_LOCKLOG_IMAGE)
for x in "$ll_v" "$ll_sum" "$ll_pin" "$LOCKHUB_VERSION"; do
  [ -n "$x" ] || die "config/locks.lock no longer pins locklog or lox as this test reads them: update it"
done
echo "$(set_k LOCKS_SET): locklog $ll_v, lox $LOCKHUB_VERSION ($arch)"
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
a=locklog_${ll_v}_linux_$arch.tar.gz
if verified locklog "$ll_v" "$a" && (cd "$work/rel-locklog" && grep "  $a\$" checksums.txt | sha256sum -c - >/dev/null) &&
  echo "$ll_sum  $work/rel-locklog/$a" | sha256sum -c - >/dev/null; then
  ok "locklog v$ll_v: the archive lea's image installs is the pinned one, its checksum signed by the release workflow at v$ll_v"
else die "locklog v$ll_v: the archive does not match the pin, or its checksums are not signed by its release"; fi
# digest REPO VERSION: ghcr.io/davidgodefroid/REPO:VERSION by digest, once cosign verifies it.
digest() {
  tok=$(curl -fsS "https://ghcr.io/token?scope=repository:davidgodefroid/$1:pull" | jq -r .token)
  dg=$(curl -fsSI -H "Authorization: Bearer $tok" \
    -H 'Accept: application/vnd.oci.image.index.v1+json, application/vnd.docker.distribution.manifest.list.v2+json' \
    "https://ghcr.io/v2/davidgodefroid/$1/manifests/$2" | tr -d '\r' | sed -n 's/^[Dd]ocker-[Cc]ontent-[Dd]igest: *//p')
  printf '%s\n' "$dg" | grep -Eqx 'sha256:[0-9a-f]{64}' || die "no digest for ghcr.io/davidgodefroid/$1:$2"
  cosign verify --certificate-oidc-issuer https://token.actions.githubusercontent.com \
    --certificate-identity "https://github.com/DavidGodefroid/$1/.github/workflows/release.yml@refs/tags/v$2" \
    "ghcr.io/davidgodefroid/$1@$dg" >/dev/null 2>&1 || die "ghcr.io/davidgodefroid/$1@$dg is not signed by $1's release v$2"
  printf 'ghcr.io/davidgodefroid/%s@%s' "$1" "$dg"
}
ll_img=$(digest locklog "$ll_v")
if [ "$ll_img" = "$ll_pin" ]; then ok "locklog image $ll_img, signed by its release workflow at v$ll_v, the set's"
else ko "locklog image $ll_img (signed at v$ll_v) is not the one config/locks.lock pins ($ll_pin)"; fi
hub=$(digest lockhub "$LOCKHUB_VERSION")
ok "hub image $hub, signed by lockhub's release workflow at v$LOCKHUB_VERSION"
a=lox_${LOCKHUB_VERSION}_linux_$arch.tar.gz
if verified lockhub "$LOCKHUB_VERSION" "$a" && (cd "$work/rel-lockhub" && grep "  $a\$" checksums.txt | sha256sum -c - >/dev/null); then
  ok "lox v$LOCKHUB_VERSION: the archive's checksum signed by lockhub's release workflow"
else die "$a does not match lockhub's signed checksums"; fi
tar -xzf "$work/rel-lockhub/$a" -C "$work/bin" lox
lox=$work/bin/lox
"$lox" version

say "host logs: a logfmt file with e-mail addresses (personal data the console masks)"
sudo mkdir -p "$logs/app"
{
  echo 'time=2026-11-06T10:00:00Z level=error msg="payment failed" user=alice.lea@example.com status=502'
  echo 'time=2026-11-06T10:00:05Z level=info msg="payment ok" user=bob.lea@example.com status=200'
  echo 'time=2026-11-06T10:00:09Z level=error msg="payment failed" user=carol.lea@example.com status=502'
} | sudo tee "$logs/app/app.log" >/dev/null
sudo chmod 0755 "$logs" "$logs/app"
sudo chmod 0644 "$logs/app/app.log"
clear_values='alice.lea@example.com bob.lea@example.com carol.lea@example.com'
# masked FILE: no clear e-mail address of the log in FILE.
masked() {
  for v in $clear_values; do if grep -qF "$v" "$1"; then return 1; fi; done
  return 0
}
# clear FILE: the clear values of the log FILE holds, one per line.
clear() { for v in $clear_values; do if grep -qF "$v" "$1"; then echo "$v"; fi; done; }

say "lox add locklog logs: the set's image, its profile (.locklog/config.toml), the set's hub"
mkdir -m 0755 "$projects/.locklog"
cat >"$projects/.locklog/config.toml" <<EOF
version = 1

[profiles.files]
production = false
unmask = "approved"

[profiles.files.sources.app]
kind = "file"
paths = ["$logs/app/*.log"]
format = "logfmt"
EOF
chmod 0644 "$projects/.locklog/config.toml"
cd "$projects"
"$lox" add locklog logs --image "$ll_img" --profile files --hub-image "$hub"
# allowed_uids: the uid lea's agents run as (lox add fills it when lea is on the PATH; lox run
# reviews it): lea's session account has no account in the console's image.
python3 -I - "$projects/.lockfamily/lockfamily.yaml" "$uid" <<'PY'
import sys
p, uid = sys.argv[1], sys.argv[2]
out = []
for line in open(p):
    out.append(line)
    if line.rstrip("\n") == "  logs:":
        out.append("    allowed_uids: [%s]\n" % uid)
open(p, "w").write("".join(out))
PY
cat .lockfamily/lockfamily.yaml
if grep -q "allowed_uids: \[$uid\]" .lockfamily/lockfamily.yaml; then ok "lockfamily.yaml: lock logs (locklog), allowed_uids [$uid]"
else die "lockfamily.yaml lacks the lock's allowed_uids"; fi

say "lox run: its review (the lock, its mount plan, its rules), then the hub and the console"
python3 -I "$work/expect.py" 600 'Apply? [y/N]' 'y\n' "up: project " "<pause>qq" -- "$lox" run >"$work/run.raw" 2>&1 ||
  { tr -d '\r' <"$work/run.raw"; die "lox run failed"; }
tr -d '\r' <"$work/run.raw" >"$work/run.out"
sed -n '1,/^up: project /p' "$work/run.out"
if grep -qF "lock logs plan file $logs/app $logs/app/*.log" "$work/run.out" && grep -qF "lock logs bind $logs/app (read-only" "$work/run.out"; then
  ok "lox run reviewed the lock's mount plan: $logs/app, read-only"
else ko "lox run's review lacks the lock's mount plan"; fi
p=$(jq -r .project .lockfamily/up.json)
if [ -z "$p" ] || [ "$p" = null ]; then die "no project in up.json"; fi
v=$(jq -r '.locks["logs"].socket_volume' .lockfamily/up.json)
if [ "$v" = "${p}_logs-run" ] && [ "$(docker volume inspect -f '{{index .Labels "dev.lockfamily.type"}}' "$v" 2>/dev/null)" = locklog ]; then
  ok "up.json: lock logs (locklog), its socket volume $v"
else ko "up.json: lock logs: socket volume '$v'"; fi
if python3 -I "$work/attach.py" 120 'Listening on ' -- "$lox" attach logs >"$work/attach.log" 2>&1; then
  ok "lox attach logs: its policy approved at its terminal, the console listens"
else
  tr -d '\r' <"$work/attach.log" | tail -30 >&2
  die "lox attach logs: the console never listened"
fi

say "lea: a session wired for locklog (LEA_LOCKLOG=1, lea shell); its agent's calls"
cat >"$work/lea.conf" <<CONF
LEA_PREFIX=$id
LEA_IMAGE=lea:dev
LEA_PROXY_IMAGE=lea-proxy:dev
LEA_LOCKLOG=1
LEA_HUB=0
CONF
export LEA_CONFIG="$work/lea.conf"
lea_up=1
state=$XDG_DATA_HOME/lea/state
mkdir -p "$state"
printf '{}\n' >"$state/claude.json"
o=$projects/.e2e/out
mkdir -p "$o"
LEA_PROJECTS_DIR="$projects" "$root/bin/lea" shell -- python3 -I "$projects/.e2e/agent.py" "$o" </dev/null >"$work/agent.out" 2>&1 &
agent_pid=$!
i=0
until [ -f "$o/ticket" ] || ! kill -0 "$agent_pid" 2>/dev/null; do
  i=$((i + 1))
  [ "$i" -lt 300 ] || break
  sleep 1
done
show() { echo "      $1: $(cut -c1-400 "$o/$1.json" 2>/dev/null)"; }
for s in status count search unmask1; do show "$s"; done
if jq -e '[.data[] | select(.profile == "files" and .running == true)] | length == 1' "$o/status.json" >/dev/null 2>&1; then
  ok "lea's agent: locklog_status reaches the running locklog console (profile files)"
else ko "lea's agent: locklog_status: $(cat "$o/status.json" 2>/dev/null)"; fi
if [ "$(jq '[.data.groups[].n | tonumber] | add' "$o/count.json" 2>/dev/null)" = 3 ] && masked "$o/count.json"; then
  ok "lea's agent: locklog_count counts the 3 records, masked, with no ticket"
else ko "lea's agent: locklog_count: $(cat "$o/count.json" 2>/dev/null)"; fi
if jq -e '.data.records | length == 1' "$o/search.json" >/dev/null 2>&1 && masked "$o/search.json"; then
  ok "lea's agent: locklog_search gives a record, its e-mail address masked (a pseudonym)"
else ko "lea's agent: locklog_search: $(cat "$o/search.json" 2>/dev/null)"; fi
ticket=$(sed -n 1p "$o/ticket" 2>/dev/null)
if [ -n "$ticket" ] && jq -e --arg t "$ticket" '.data.status == "pending" and .data.lock == "locklog" and .data.instance == "logs" and .data.ticket == $t' \
  "$o/unmask1.json" >/dev/null 2>&1 && masked "$o/unmask1.json"; then
  ok "lea's agent: locklog_unmask is pending on the hub, ticket $ticket, still masked"
else
  cat "$work/agent.out"
  die "lea's agent: locklog_unmask did not answer pending with a ticket: $(cat "$o/unmask1.json" 2>/dev/null)"
fi

say "the unmask (read-pii), approved at lox run's terminal facade (lox approve)"
okd=
for i in $(seq 1 30); do
  tty 'y\n' "$lox" approve --once >"$work/approve.out" 2>&1 || true
  if tr -d '\r' <"$work/approve.out" | grep -Eq '(^|: )approved$'; then okd=1; break; fi
  sleep 2
done
tr -d '\r' <"$work/approve.out"
if [ -n "$okd" ] && tr -d '\r' <"$work/approve.out" | grep -q "$ticket" && tr -d '\r' <"$work/approve.out" | grep -q locklog; then
  ok "lox approve: the locklog ticket $ticket approved, as a human at a terminal"
else
  docker logs "${p}-lockhub-1" 2>&1 | tail -20 >&2
  die "the agent's unmask ticket was not approved at lox approve"
fi
if masked "$work/approve.out"; then ok "the facade's screen shows the masked ticket, no clear value"; else ko "the facade's screen holds a clear value"; fi

say "the unmask run again with the ticket: the clear value, to lea's agent only"
wait "$agent_pid" || true
agent_pid=
cat "$work/agent.out"
show unmask2
got=$(clear "$o/unmask2.json" 2>/dev/null || true)
if [ -n "$got" ] && [ "$(printf '%s\n' "$got" | wc -l)" = 1 ]; then
  ok "lea's agent: the approved locklog_unmask gives the one clear value of the record's pseudonym ($got)"
else ko "lea's agent: the approved locklog_unmask: $(cat "$o/unmask2.json" 2>/dev/null)"; fi
# The hub keeps the masked ticket only: never the clear value (its database must be there, or the
# check would read nothing).
if docker run --rm --network none -v "$XDG_STATE_HOME/lockfamily/$p/hub:/h:ro" docker.io/library/alpine:3.22 \
  sh -c 'test -s /h/hub.db || exit 3; for f in /h/*; do [ -f "$f" ] && cat "$f"; done; exit 0' >"$work/hubdata.bin" && [ -s "$work/hubdata.bin" ]; then
  if [ -n "$got" ] && grep -aqF "$got" "$work/hubdata.bin"; then ko "the hub's data holds the clear value"
  else ok "the hub's data holds no clear value of the log"; fi
else ko "the hub's database is missing or empty: the clear-value check would read nothing"; fi

say "lea's container: the wiring, and lea doctor"
c=$id-$lid
w=$(docker inspect -f '{{json .HostConfig.GroupAdd}} {{range .Mounts}}{{if eq .Type "volume"}}{{.Name}}:{{.Destination}}:{{.RW}} {{end}}{{end}}' "$c" 2>/dev/null) || w=
echo "      $c: $w"
case " $w " in
  *'"10050"'*" ${p}_logs-run:/run/locklog:false "*) ok "lea's container: --group-add 10050, lox run's socket volume on /run/locklog, read-only" ;;
  *) ko "lea's container lacks the locklog wiring: $w" ;;
esac
case $(sed -n 's/^id: //p' "$work/agent.out") in
  *"10050(locklog-clients)"*) ok "the session account (uid $uid) is in locklog-clients" ;;
  *) ko "the session account's groups: $(sed -n 's/^id: //p' "$work/agent.out")" ;;
esac
LEA_PROJECTS_DIR="$projects" "$root/bin/lea" doctor </dev/null >"$work/doctor.out" 2>&1 || true
grep -i 'lock\|lox' "$work/doctor.out" | sed 's/^/      doctor: /' || true
if grep -qF "ok    lock logs (locklog): runs locklog v$ll_v, the release of lea's locklog client v$ll_v (protocol " "$work/doctor.out"; then
  ok "lea doctor: lock logs (locklog) runs v$ll_v, the release of lea's client"
else ko "lea doctor: lock logs (locklog): $(grep -F 'lock logs (locklog)' "$work/doctor.out" || echo 'no line')"; fi

say "negative control: lea's image as the session account, without locklog-clients"
docker run --rm --name "$id-plain" --network none --user "$uid:$(id -g)" -e HOME=/tmp -w "$projects" \
  --mount "type=bind,src=$projects,dst=$projects" --mount "type=volume,src=${p}_logs-run,dst=/run/locklog,readonly" \
  --entrypoint python3 lea:dev -I "$projects/.e2e/agent.py" "$o" status-only >"$work/plain.out" 2>&1 || true
show plain-status
if [ -s "$o/plain-status.json" ] && ! jq -e '[.data[]? | select(.running == true)] | length > 0' "$o/plain-status.json" >/dev/null 2>&1; then
  ok "without locklog-clients, lea's image reaches no console: $(cut -c1-160 "$o/plain-status.json")"
else ko "without its group: $(cat "$work/plain.out" "$o/plain-status.json" 2>/dev/null)"; fi
exit "$fail"
