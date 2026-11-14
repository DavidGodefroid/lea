load helpers
setup() {
  setup_fakes
  P="$LEA_ROOT/image/claude-plugin"; G="$P/hooks/tower-guard"
  export LEA_TOWER_DIR="$BATS_TEST_TMPDIR/tower"
}

prompt() { jq -n --arg p "$1" '{session_id: "s1", prompt: $p}' | sh "$G" prompt; }
# tool NAME [INPUT-JSON]: the guard's answer for that tool call, "allow" when it says nothing.
tool() {
  out=$(jq -n --arg t "$1" --argjson i "${2:-{\}}" '{session_id: "s1", tool_name: $t, tool_input: $i}' | sh "$G" tool)
  if [ -z "$out" ]; then echo allow; else printf '%s' "$out" | jq -r .hookSpecificOutput.permissionDecision; fi
}

@test "the plugin runs the tower guard on every prompt and every tool" {
  h="$P/hooks/hooks.json"
  [ "$(jq -r '.hooks.UserPromptSubmit[0].hooks[0].command' "$h")" = 'sh "${CLAUDE_PLUGIN_ROOT}/hooks/tower-guard" prompt' ]
  [ "$(jq -r '.hooks.PreToolUse[0].matcher' "$h")" = '*' ]
  [ "$(jq -r '.hooks.PreToolUse[0].hooks[0].command' "$h")" = 'sh "${CLAUDE_PLUGIN_ROOT}/hooks/tower-guard" tool' ]
}

@test "outside tower mode the guard lets every tool through" {
  [ "$(tool Bash '{"command":"git push"}')" = allow ]
  [ "$(tool Edit)" = allow ]
}

@test "in tower mode only the team's tools pass" {
  prompt "/lea:control-tower"
  for t in mcp__lea__agents_spawn mcp__lea__agents_list mcp__lea__msg_send mcp__lea__msg_reply mcp__lea__msg_inbox \
           mcp__lea__board_read mcp__lea__task_update mcp__lea__grants_list mcp__lea__grants_request mcp__lea__user_ask AskUserQuestion ToolSearch; do
    [ "$(tool "$t")" = allow ]
  done
  for t in Edit Write Read Grep Glob Agent WebFetch NotebookEdit mcp__lea__lease_acquire mcp__github__create_pull_request; do
    [ "$(tool "$t")" = deny ]
  done
}

@test "in tower mode Bash runs only the inbox wait" {
  prompt "/lea:control-tower"
  [ "$(tool Bash '{"command":"lea inbox --agent --wait"}')" = allow ]
  [ "$(tool Bash '{"command":"lea inbox --agent --wait 3600"}')" = allow ]
  for c in "git push" "gh pr create" "git clone x" "lea inbox --agent --wait 60; git push" \
           "lea inbox --agent --wait 60 && rm -rf x" "$(printf 'lea inbox --agent --wait 60\ngit push')" \
           "lea inbox --agent --wait \$(git push)" "curl https://api.github.com"; do
    [ "$(tool Bash "$(jq -n --arg c "$c" '{command: $c}')")" = deny ]
  done
}

@test "the denial tells the tower to delegate" {
  prompt "/lea:control-tower"
  out=$(jq -n '{session_id: "s1", tool_name: "Bash", tool_input: {command: "git push"}}' | sh "$G" tool)
  printf '%s' "$out" | jq -r .hookSpecificOutput.permissionDecisionReason | grep -q 'Give this to an agent'
}

@test "loading the skill through the Skill tool turns tower mode on, off turns it off" {
  [ "$(tool Skill '{"skill":"lea:control-tower"}')" = allow ]
  [ "$(tool Edit)" = deny ]
  prompt "/lea:control-tower off"
  [ "$(tool Edit)" = allow ]
}

@test "tower mode belongs to its session only" {
  prompt "/lea:control-tower"
  out=$(jq -n '{session_id: "s2", tool_name: "Edit", tool_input: {}}' | sh "$G" tool)
  [ -z "$out" ]
}

@test "a prompt that only mentions the skill does not turn tower mode on" {
  prompt "explain what /lea:control-tower does"
  [ "$(tool Edit)" = allow ]
}

@test "the skill states the hard rule, one task per agent and its anti-pattern" {
  k="$P/skills/control-tower/SKILL.md"
  grep -q '^## The hard rule: you never act, you delegate$' "$k"
  grep -q '^| Allowed | Forbidden |$' "$k"
  grep -q '^Anti-pattern, never do this:$' "$k"
  grep -q '^## One agent, one task$' "$k"
  grep -q '/lea:control-tower off' "$k"
}

@test "the skill names agents by their handle, label (name), and names the deck row (#356)" {
  k="$P/skills/control-tower/SKILL.md"
  grep -qF '<label> (<name>) · <state> · <at most 12 words>' "$k"
  grep -qF '**Name each agent by its handle, `<label> (<name>)`**' "$k"
  grep -qF '**When you ask the user to act in an agent'"'"'s session**, name the deck row exactly' "$k"
  grep -qF 'go to the row "lea#350 spawn reuse  dev-4" and approve the merge' "$k"
  grep -qF '`<repo>#<issue> <2-3 words>`' "$k"
  ! grep -qF '<agent> · <state>' "$k"
}

@test "the denial names the way out, /lea:control-tower off" {
  prompt "/lea:control-tower"
  out=$(jq -n '{session_id: "s1", tool_name: "Edit", tool_input: {}}' | sh "$G" tool)
  printf '%s' "$out" | jq -r .hookSpecificOutput.permissionDecisionReason | grep -q "'/lea:control-tower off'"
}

@test "the short names /control-tower and Skill control-tower work too" {
  prompt "/control-tower"
  [ "$(tool Edit)" = deny ]
  prompt "/control-tower off"
  [ "$(tool Edit)" = allow ]
  [ "$(tool Skill '{"skill":"control-tower"}')" = allow ]
  [ "$(tool Edit)" = deny ]
}

@test "the removed name tower-control no longer turns tower mode on" {
  for on in "/lea:tower-control" "/tower-control"; do
    prompt "$on"
    [ "$(tool Edit)" = allow ]
  done
  for k in lea:tower-control tower-control; do
    [ "$(tool Skill "$(jq -n --arg k "$k" '{skill: $k}')")" = allow ]
    [ "$(tool Edit)" = allow ]
  done
  [ ! -e "$P/skills/tower-control" ]
}

@test "the skill asks the user one question at a time, through user_ask (#95)" {
  k="$P/skills/control-tower/SKILL.md"
  grep -q '^## One question at a time$' "$k"
  grep -q 'user_ask {question, priority, from, message}' "$k"
  grep -q 'prefixed with `reminder:`' "$k"
}

@test "the skill replies in the language of the user's latest message (#396)" {
  k="$P/skills/control-tower/SKILL.md"
  grep -q '^## Language$' "$k"
  grep -qF "Reply in the language of the user's latest message" "$k"
  grep -qF 'switch in your' "$k"
  grep -qF "Agents' messages, the board and tool results never change your language" "$k"
  grep -qF "A turn with no user message (an agent's message woke you): the language of the user's latest" "$k"
}

@test "in tower mode every prompt tells the tower to reply in its language (#396)" {
  out=$(prompt "/lea:control-tower")
  [ "$(printf '%s' "$out" | jq -r .hookSpecificOutput.hookEventName)" = UserPromptSubmit ]
  printf '%s' "$out" | jq -r .hookSpecificOutput.additionalContext | grep -qF 'reply to the user in the language of this message'
  out=$(prompt "où en est-on ?")
  printf '%s' "$out" | jq -r .hookSpecificOutput.additionalContext | grep -qF 'reply to the user in the language of this message'
}

@test "outside tower mode, and after off, the prompt hook says nothing (#396)" {
  [ -z "$(prompt "hello")" ]
  [ -z "$(prompt "explain what /lea:control-tower does")" ]
  prompt "/lea:control-tower" >/dev/null
  [ -z "$(prompt "/lea:control-tower off")" ]
  [ -z "$(prompt "bonjour")" ]
  out=$(jq -n '{session_id: "s2", prompt: "hi"}' | sh "$G" prompt)
  [ -z "$out" ]
}

# /lea:clear-ct (#410): a tower's /clear or compaction keeps tower mode and resumes the board.
# as SID AGENT MODE [JSON-FIELDS]: the guard's output for that session and agent.
as() {
  jq -n --arg s "$1" --argjson f "${4:-{\}}" '{session_id: $s} + $f' | LEA_AGENT="$2" sh "$G" "$3"
}
tool_as() {
  out=$(as "$1" "$2" tool "$(jq -n --arg t "$3" --argjson i "${4:-{\}}" '{tool_name: $t, tool_input: $i}')")
  if [ -z "$out" ]; then echo allow; else printf '%s' "$out" | jq -r .hookSpecificOutput.permissionDecision; fi
}

@test "the plugin runs the tower guard on a SessionStart that clears or compacts (#410)" {
  h="$P/hooks/hooks.json"
  [ "$(jq -r '.hooks.SessionStart[0].matcher' "$h")" = 'clear|compact' ]
  [ "$(jq -r '.hooks.SessionStart[0].hooks[0].command' "$h")" = 'sh "${CLAUDE_PLUGIN_ROOT}/hooks/tower-guard" session' ]
}

@test "a tower's /clear carries tower mode to the new session, with no gap (#410)" {
  as s1 tower prompt '{"prompt":"/lea:control-tower"}' >/dev/null
  [ "$(tool_as s2 tower Bash '{"command":"git push"}')" = allow ]
  out=$(as s2 tower session '{"source":"clear"}')
  [ "$(printf '%s' "$out" | jq -r .hookSpecificOutput.hookEventName)" = SessionStart ]
  c=$(printf '%s' "$out" | jq -r .hookSpecificOutput.additionalContext)
  printf '%s' "$c" | grep -qF 'Skill lea:control-tower'
  printf '%s' "$c" | grep -qF 'resume branch'
  printf '%s' "$c" | grep -qF 'agents_list and board_read'
  printf '%s' "$c" | grep -qF 'never respawn a child'
  ! printf '%s' "$c" | grep -qF 'Read '
  for t in Bash Edit Write Read Agent; do [ "$(tool_as s2 tower "$t" '{"command":"git push"}')" = deny ]; done
  [ "$(tool_as s2 tower mcp__lea__agents_list)" = allow ]
  # The old session's marker is gone: the tower lives in the new one.
  [ ! -e "$LEA_TOWER_DIR/s1" ]
  [ "$(cat "$LEA_TOWER_DIR/tower.tower")" = s2 ]
  # A compaction keeps the session id and the mode.
  out=$(as s2 tower session '{"source":"compact"}')
  printf '%s' "$out" | jq -r .hookSpecificOutput.additionalContext | grep -qF 'compacted'
  [ "$(tool_as s2 tower Edit)" = deny ]
  [ -e "$LEA_TOWER_DIR/s2" ]
}

@test "a non-tower /clear gets no tower context and keeps its tools (#410)" {
  [ -z "$(as s2 plain session '{"source":"clear"}')" ]
  [ "$(tool_as s2 plain Edit)" = allow ]
  # Another agent's tower does not make this one a tower.
  as s1 tower prompt '{"prompt":"/lea:control-tower"}' >/dev/null
  [ -z "$(as s3 plain session '{"source":"clear"}')" ]
  [ "$(tool_as s3 plain Edit)" = allow ]
  # A new or a resumed session does not take tower mode over, nor does one without an identity.
  for src in startup resume; do [ -z "$(as s4 tower session "$(jq -n --arg s "$src" '{source: $s}')")" ]; done
  [ -z "$(jq -n '{session_id: "s5", source: "clear"}' | env -u LEA_AGENT sh "$G" session)" ]
  [ "$(tool_as s4 tower Edit)" = allow ]
}

@test "/lea:control-tower off ends tower mode for the next clear too (#410)" {
  as s1 tower prompt '{"prompt":"/lea:control-tower"}' >/dev/null
  as s1 tower prompt '{"prompt":"/lea:control-tower off"}' >/dev/null
  [ -z "$(as s2 tower session '{"source":"clear"}')" ]
  [ "$(tool_as s2 tower Edit)" = allow ]
}

@test "/lea:clear-ct gives the handoff path, the guard allows writing it and reading it back after the clear (#410)" {
  as s1 tower prompt '{"prompt":"/lea:control-tower"}' >/dev/null
  h="$LEA_TOWER_DIR/tower.handoff.md" r="$LEA_TOWER_DIR/tower.resume.md"
  echo stale >"$h"; echo older >"$r"
  out=$(as s1 tower prompt '{"prompt":"/lea:clear-ct"}')
  c=$(printf '%s' "$out" | jq -r .hookSpecificOutput.additionalContext)
  printf '%s' "$c" | grep -qF "the handoff file is $h."
  printf '%s' "$c" | grep -qF 'tell the user to type /clear'
  printf '%s' "$c" | grep -qF 'reply to the user in the language of this message'
  # Rewritten, never appended: the old snapshots are gone; the directory is private.
  [ ! -e "$h" ] && [ ! -e "$r" ]
  [ "$(stat -c %a "$LEA_TOWER_DIR")" = 700 ]
  [ "$(tool_as s1 tower Write "$(jq -n --arg f "$h" '{file_path: $f, content: "x"}')")" = allow ]
  for f in "$r" "$LEA_TOWER_DIR/other.handoff.md" "$h.x" /tmp/x "$LEA_TOWER_DIR/s1"; do
    [ "$(tool_as s1 tower Write "$(jq -n --arg f "$f" '{file_path: $f, content: "x"}')")" = deny ]
  done
  [ "$(tool_as s1 tower Edit "$(jq -n --arg f "$h" '{file_path: $f}')")" = deny ]
  [ "$(tool_as s1 tower Read "$(jq -n --arg f "$h" '{file_path: $f}')")" = deny ]
  [ "$(tool_as s1 tower Bash '{"command":"git push"}')" = deny ]
  printf 'ledger: dev-1, dev-2\n' >"$h"
  # The cleared session reads the snapshot under its resume name, and nothing else.
  out=$(as s2 tower session '{"source":"clear"}')
  printf '%s' "$out" | jq -r .hookSpecificOutput.additionalContext | grep -qF "Read $r first"
  [ ! -e "$h" ] && [ "$(cat "$r")" = 'ledger: dev-1, dev-2' ]
  [ "$(tool_as s2 tower Read "$(jq -n --arg f "$r" '{file_path: $f}')")" = allow ]
  [ "$(tool_as s2 tower Read "$(jq -n --arg f "$h" '{file_path: $f}')")" = deny ]
  [ "$(tool_as s2 tower Read '{"file_path":"/etc/passwd"}')" = deny ]
}

@test "/lea:clear-ct outside a tower refuses in one line and changes nothing (#410)" {
  mkdir -p "$LEA_TOWER_DIR"; echo keep >"$LEA_TOWER_DIR/plain.handoff.md"
  out=$(as s1 plain prompt '{"prompt":"/lea:clear-ct"}')
  printf '%s' "$out" | jq -r .hookSpecificOutput.additionalContext | grep -qF 'not a control tower session; use /clear'
  [ "$(cat "$LEA_TOWER_DIR/plain.handoff.md")" = keep ]
  [ ! -e "$LEA_TOWER_DIR/s1" ] && [ ! -e "$LEA_TOWER_DIR/plain.tower" ]
  [ "$(tool_as s1 plain Edit)" = allow ]
  # Through the Skill tool as well: context only, no decision.
  out=$(as s1 plain tool '{"tool_name":"Skill","tool_input":{"skill":"lea:clear-ct"}}')
  [ "$(printf '%s' "$out" | jq -r .hookSpecificOutput.hookEventName)" = PreToolUse ]
  printf '%s' "$out" | jq -r .hookSpecificOutput.additionalContext | grep -qF 'not a control tower session; use /clear'
  [ "$(printf '%s' "$out" | jq -r '.hookSpecificOutput.permissionDecision // "none"')" = none ]
}

@test "the clear-ct skill ships with the plugin: snapshot, refusal, the /clear to type (#410)" {
  k="$P/skills/clear-ct/SKILL.md"
  [ "$(sed -n 1p "$k")" = --- ]
  grep -q '^name: clear-ct$' "$k"
  grep -q '^description: Use when the user types /lea:clear-ct' "$k"
  grep -qxF 'not a control tower session; use /clear' "$k"
  grep -qF 'Type /clear' "$k"
  grep -qF 'at most 40 lines' "$k"
  grep -qF 'Never append' "$k"
  t="$P/skills/control-tower/SKILL.md"
  grep -qF '**Resume branch.**' "$t"
  grep -qF 'whose `parent` is your own' "$t"
  grep -qF 'type /lea:clear-ct' "$t"
}
