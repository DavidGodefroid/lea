#!/bin/sh
# Spike SP2 (#155): claude -p --resume with a prompt, and SessionStart(source=resume) hooks
# injecting context. Each case prints its exit code and result, then hooks.log.
# `run.sh truncated` adds the truncated-transcript cases (b1, b2): they cut the transcripts of the
# sessions they create, in ~/.claude/projects, so run them by hand in a throwaway account, never
# from an agent.
set -u
D=$(cd "$(dirname "$0")" && pwd)
cd "$D"; rm -f hooks.log
# A nested run must not inherit the calling session's markers (transcript saving is off otherwise).
unset CLAUDE_CODE_CHILD_SESSION CLAUDE_CODE_SESSION_ID CLAUDECODE CLAUDE_CODE_ENTRYPOINT 2>/dev/null || true
settings() { # $1: one hook mode of hook.sh
  a=false; case $1 in async-*) a=true ;; esac
  printf '{"hooks":{"SessionStart":[{"hooks":[{"type":"command","command":"%s/hook.sh %s","asyncRewake":%s,"timeout":60}]}]}}\n' "$D" "$1" "$a"
}
# cc <label> <claude args...>: one headless run; prints label, exit code and result.
cc() {
  l=$1; shift
  out=$(claude -p --output-format json "$@" </dev/null 2>"$D/$l.err"); rc=$?
  r=$(printf '%s' "$out" | python3 -c 'import json,sys
try: o=json.load(sys.stdin); print(repr(o.get("result")), o.get("session_id"))
except Exception as e: print("unparsable:", e)')
  printf '%-14s exit=%s result=%s\n' "$l" "$rc" "$r"
  if [ -s "$D/$l.err" ]; then sed 's/^/    stderr: /' "$D/$l.err" | head -3; fi
}
new() { claude -p --output-format json "Reply with the single word READY." </dev/null | python3 -c 'import json,sys; print(json.load(sys.stdin)["session_id"])'; }
file() { find "$HOME/.claude/projects" -name "$1.jsonl" | head -1; }

s=$(new); echo "session $s"
cc a-resume --resume "$s" "Reply with the single word OK."

if [ "${1:-}" = truncated ]; then
  # b1: the last line of the file (whatever it is) cut in the middle.
  s=$(new); f=$(file "$s"); python3 - "$f" <<'PY'
import sys; p=sys.argv[1]; b=open(p,'rb').read().rstrip(b"\n"); i=b.rfind(b"\n")+1; last=b[i:]
open(p,'wb').write(b[:i]+last[:len(last)//2]); print("b1 cut:", last[:60])
PY
  cc b1-truncated --resume "$s" "Reply with the single word OK."
  # b2: the last assistant message cut in the middle, nothing after it (a crash mid-write).
  s=$(new); f=$(file "$s"); python3 - "$f" <<'PY'
import sys, json; p=sys.argv[1]; ls=open(p,'rb').read().splitlines()
k=max(i for i,l in enumerate(ls) if json.loads(l).get("type")=="assistant")
open(p,'wb').write(b"\n".join(ls[:k])+b"\n"+ls[k][:len(ls[k])//2]); print("b2 cut at line", k+1, "of", len(ls))
PY
  cc b2-truncated --resume "$s" "Reply with the single word OK."
fi

q="If you were told a secret word, reply with that word only, else reply NONE."
for m in sync async-ctx async-wake; do
  settings "$m" > "settings-$m.json"
  s=$(new)
  cc "c-$m" --resume "$s" --settings "settings-$m.json" "$q"
done
echo; cat hooks.log
