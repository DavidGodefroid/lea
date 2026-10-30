load helpers
setup() {
  setup_fakes
  P="$LEA_ROOT/image/claude-plugin"; G="$P/hooks/tower-guard"; K="$P/skills/check/SKILL.md"
  export LEA_TOWER_DIR="$BATS_TEST_TMPDIR/tower"
}

prompt() { jq -n --arg p "$1" '{session_id: "s1", prompt: $p}' | sh "$G" prompt; }
tool() {
  out=$(jq -n --arg t "$1" --argjson i "${2:-{\}}" '{session_id: "s1", tool_name: $t, tool_input: $i}' | sh "$G" tool)
  if [ -z "$out" ]; then echo allow; else printf '%s' "$out" | jq -r .hookSpecificOutput.permissionDecision; fi
}

@test "the check skill is a plugin skill triggered by a status request or /check" {
  [ "$(sed -n 1p "$K")" = --- ]
  grep -q '^name: check$' "$K"
  grep -q '^description: Use when the user asks for a progress status' "$K"
  for w in '"point"' '"topo"' '/check'; do sed -n '/^description:/p' "$K" | grep -qF "$w"; done
}

@test "the check skill fixes the output: sections in order, 10 lines, 12 words, one line when unchanged" {
  done=$(grep -n '^✅ Done' "$K" | head -1 | cut -d: -f1)
  prog=$(grep -n '^🔄 In progress' "$K" | head -1 | cut -d: -f1)
  left=$(grep -n '^⏳ Left' "$K" | head -1 | cut -d: -f1)
  blk=$(grep -n '^⚠️ Blocked / to decide' "$K" | head -1 | cut -d: -f1)
  [ -n "$done" ]
  [ "$done" -lt "$prog" ]
  [ "$prog" -lt "$left" ]
  [ "$left" -lt "$blk" ]
  grep -q '10 lines' "$K"
  grep -q '12 words' "$K"
  grep -qi 'single line' "$K"
  grep -qi "user's language" "$K"
}

@test "the check skill builds a lead's status from the team tools, an agent's from its own task" {
  for t in board_read agents_list msg_inbox task_update; do grep -q "$t" "$K"; done
  grep -q 'since the last check' "$K"
}

@test "the control tower may run /check: loading it passes and keeps tower mode as it is" {
  prompt "/lea:control-tower"
  [ "$(tool Skill '{"skill":"lea:check"}')" = allow ]
  [ "$(tool Skill '{"skill":"check"}')" = allow ]
  [ "$(tool Edit)" = deny ]
  prompt "/lea:control-tower off"
  [ "$(tool Skill '{"skill":"lea:check"}')" = allow ]
  [ "$(tool Edit)" = allow ]
}

@test "/check does not turn tower mode on" {
  for p in "/check" "/lea:check"; do prompt "$p"; [ "$(tool Edit)" = allow ]; done
}

# Every lea tool the skill tells a tower to call must pass the tower guard.
@test "in tower mode every lea tool the check skill names passes the guard" {
  prompt "/lea:control-tower"
  ts=$(grep -o '`[a-z_]*`' "$K" | tr -d '`' | grep -E '^(agents|msg|board|task|lease)_' | sort -u)
  [ -n "$ts" ]
  for t in $ts; do [ "$(tool "mcp__lea__$t")" = allow ]; done
}
