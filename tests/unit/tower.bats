load helpers
setup() {
  setup_fakes
  P="$LEA_ROOT/image/claude-plugin"
}

# The tower guard is lea's own (#383): the agent's UserPromptSubmit hook records the mark in lea's
# store and its PreToolUse hook (grant_check) denies, so a resumed tower keeps it and no file in
# the container turns it off. Its behaviour is tested in internal/daemon/tower_test.go and
# internal/hook/claude_test.go.
@test "the plugin carries no tower guard of its own: lea's hooks hold the tower" {
  [ ! -e "$P/hooks/tower-guard" ]
  [ ! -e "$P/hooks/hooks.json" ]
  s="$LEA_ROOT/image/claude-settings.json"
  jq -r '.hooks.UserPromptSubmit[0].hooks[0].command' "$s" | grep -q 'lea hook claude UserPromptSubmit'
  jq -r '.hooks.PreToolUse[0].hooks[0].command' "$s" | grep -q 'lea hook claude PreToolUse'
  [ "$(jq -r '.hooks.PreToolUse[0].hooks[0].onFailure' "$s")" = block ]
}

@test "the skill states the hard rule, one task per agent and its anti-pattern" {
  k="$P/skills/control-tower/SKILL.md"
  grep -q '^## The hard rule: you never act, you delegate$' "$k"
  grep -q '^| Allowed | Forbidden |$' "$k"
  grep -q '^Anti-pattern, never do this:$' "$k"
  grep -q '^## One agent, one task$' "$k"
  grep -q '/lea:control-tower off' "$k"
  grep -q 'after a resume' "$k"
}

@test "the removed name tower-control is gone" {
  [ ! -e "$P/skills/tower-control" ]
}
