load helpers
setup() {
  setup_fakes
  K="$LEA_ROOT/image/claude-plugin/skills/check/SKILL.md"
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

# The tower guard (lea's daemon, #383) lets a tower load /check and call every lea tool the
# skill names: internal/daemon/tower_test.go.
