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

@test "the skill relays a child on the live relay_tokens and spawns with queue: true (#422)" {
  k="$P/skills/control-tower/SKILL.md"
  grep -qF 'read `limits_get`: `relay_tokens`' "$k"
  grep -qF '`ctx_tokens`' "$k"
  grep -qF '`relay: true`' "$k"
  grep -qF 'always pass `queue: true`' "$k"
  grep -qF 'also relay a child' "$k"
  ! grep -qE 'Above 50%|50 ?%' "$k"
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
