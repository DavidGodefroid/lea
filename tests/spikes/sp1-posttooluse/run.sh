#!/bin/sh
# Spike SP1 (#154). Usage: run.sh headless | run.sh interactive  (interactive needs python3 pty)
# Writes mcp.json/settings.json next to this script, runs Claude Code, prints hooks.log.
set -eu
D=$(cd "$(dirname "$0")" && pwd)
cd "$D"; rm -f hooks.log
printf '{"mcpServers":{"stub":{"command":"python3","args":["%s/stub.py"]}}}\n' "$D" > mcp.json
cat > settings.json <<JSON
{"hooks":{
 "PostToolUse":[{"matcher":"mcp__stub__.*","hooks":[
   {"type":"command","command":"$D/hook.sh PostToolUse"},
   {"type":"command","command":"$D/wake.sh","asyncRewake":true,"timeout":600}]}],
 "Stop":[{"hooks":[{"type":"command","command":"$D/hook.sh Stop"}]}],
 "Notification":[{"hooks":[{"type":"command","command":"$D/hook.sh Notification"}]}],
 "UserPromptSubmit":[{"hooks":[{"type":"command","command":"$D/hook.sh UserPromptSubmit"}]}],
 "SessionEnd":[{"hooks":[{"type":"command","command":"$D/hook.sh SessionEnd"}]}]}}
JSON
prompt="Call the mcp__stub__wait tool once, then reply with the single word DONE."
# A nested run must not inherit the calling session's markers (transcript saving is off otherwise).
unset CLAUDE_CODE_CHILD_SESSION CLAUDE_CODE_SESSION_ID CLAUDECODE CLAUDE_CODE_ENTRYPOINT 2>/dev/null || true
case "${1:-headless}" in
headless)
  claude -p "$prompt" --mcp-config mcp.json --strict-mcp-config --settings settings.json \
    --allowedTools mcp__stub__wait --output-format json </dev/null ;;
interactive)
  python3 "$D/drive.py" "$D" 130 "$prompt" ;;
esac
echo; cut -c1-60 hooks.log
