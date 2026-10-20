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
  prompt "/lea:tower-control"
  for t in mcp__lea__agents_spawn mcp__lea__agents_list mcp__lea__msg_send mcp__lea__msg_reply mcp__lea__msg_inbox \
           mcp__lea__board_read mcp__lea__task_update AskUserQuestion ToolSearch; do
    [ "$(tool "$t")" = allow ]
  done
  for t in Edit Write Read Grep Glob Agent WebFetch NotebookEdit mcp__lea__lease_acquire mcp__github__create_pull_request; do
    [ "$(tool "$t")" = deny ]
  done
}

@test "in tower mode Bash runs only the inbox wait" {
  prompt "/lea:tower-control"
  [ "$(tool Bash '{"command":"lea inbox --agent --wait"}')" = allow ]
  [ "$(tool Bash '{"command":"lea inbox --agent --wait 3600"}')" = allow ]
  for c in "git push" "gh pr create" "git clone x" "lea inbox --agent --wait 60; git push" \
           "lea inbox --agent --wait 60 && rm -rf x" "$(printf 'lea inbox --agent --wait 60\ngit push')" \
           "lea inbox --agent --wait \$(git push)" "curl https://api.github.com"; do
    [ "$(tool Bash "$(jq -n --arg c "$c" '{command: $c}')")" = deny ]
  done
}

@test "the denial tells the tower to delegate" {
  prompt "/lea:tower-control"
  out=$(jq -n '{session_id: "s1", tool_name: "Bash", tool_input: {command: "git push"}}' | sh "$G" tool)
  printf '%s' "$out" | jq -r .hookSpecificOutput.permissionDecisionReason | grep -q 'Give this to an agent'
}

@test "loading the skill through the Skill tool turns tower mode on, off turns it off" {
  [ "$(tool Skill '{"skill":"lea:tower-control"}')" = allow ]
  [ "$(tool Edit)" = deny ]
  prompt "/lea:tower-control off"
  [ "$(tool Edit)" = allow ]
}

@test "tower mode belongs to its session only" {
  prompt "/lea:tower-control"
  out=$(jq -n '{session_id: "s2", tool_name: "Edit", tool_input: {}}' | sh "$G" tool)
  [ -z "$out" ]
}

@test "a prompt that only mentions the skill does not turn tower mode on" {
  prompt "explain what /lea:tower-control does"
  [ "$(tool Edit)" = allow ]
}

@test "the skill states the hard rule, one task per agent and its anti-pattern" {
  k="$P/skills/tower-control/SKILL.md"
  grep -q '^## The hard rule: you never act, you delegate$' "$k"
  grep -q '^| Allowed | Forbidden |$' "$k"
  grep -q '^Anti-pattern, never do this:$' "$k"
  grep -q '^## One agent, one task$' "$k"
  grep -q '/lea:tower-control off' "$k"
}
