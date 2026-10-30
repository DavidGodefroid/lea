load helpers
# image/lea-session with a fake claude: the default runs Claude Code, --shell a shell or command.
setup() {
  setup_fakes
  F="$BATS_TEST_TMPDIR/fbin"; mkdir -p "$F"
  cat >"$F/claude" <<'EOT'
#!/bin/sh
{ printf 'claude'; for a in "$@"; do printf ' [%s]' "$a"; done; printf '\n'; } >>"$FAKE_LOG"
EOT
  chmod +x "$F/claude"
  sed 's/claude/codex/' "$F/claude" >"$F/codex"; chmod +x "$F/codex"
  export PATH="$F:$PATH"
  SESSION="$LEA_ROOT/image/lea-session"
  cd "$BATS_TEST_TMPDIR"
}

@test "the session runs Claude directly" {
  run "$SESSION" --resume
  [ "$status" -eq 0 ]
  run calls
  [ "$output" = "claude [--resume]" ]
}

@test "--shell runs the command, not Claude" {
  run "$SESSION" --shell true
  [ "$status" -eq 0 ]
  run calls
  [[ "$output" != *claude* ]]
}

@test "--claude runs Claude directly" {
  run "$SESSION" --claude --resume
  [ "$status" -eq 0 ]
  run calls
  [ "$output" = "claude [--resume]" ]
}

# lea's hooks come from a --settings file, not from managed settings: an organisation's
# server-managed settings replace file-based managed settings, which then load no hook at all.
@test "--claude passes lea's hook settings to Claude" {
  export LEA_CLAUDE_SETTINGS="$BATS_TEST_TMPDIR/claude-settings.json"; echo '{}' >"$LEA_CLAUDE_SETTINGS"
  run "$SESSION" --claude --resume
  [ "$status" -eq 0 ]
  run calls
  [ "$output" = "claude [--settings] [$LEA_CLAUDE_SETTINGS] [--resume]" ]
}

@test "the default session passes lea's hook settings to Claude" {
  export LEA_CLAUDE_SETTINGS="$BATS_TEST_TMPDIR/claude-settings.json"; echo '{}' >"$LEA_CLAUDE_SETTINGS"
  run "$SESSION"
  [ "$status" -eq 0 ]
  run calls
  [ "$output" = "claude [--settings] [$LEA_CLAUDE_SETTINGS]" ]
}

# lea's plugin (its skills, /lea:control-tower) loads for the session only, from the image.
@test "the session loads lea's Claude plugin when the image has it" {
  export LEA_CLAUDE_PLUGIN="$BATS_TEST_TMPDIR/claude-plugin"; mkdir -p "$LEA_CLAUDE_PLUGIN"
  export LEA_CLAUDE_SETTINGS="$BATS_TEST_TMPDIR/claude-settings.json"; echo '{}' >"$LEA_CLAUDE_SETTINGS"
  run "$SESSION" --claude --resume
  [ "$status" -eq 0 ]
  run calls
  [ "$output" = "claude [--settings] [$LEA_CLAUDE_SETTINGS] [--plugin-dir] [$LEA_CLAUDE_PLUGIN] [--resume]" ]
}

@test "the session runs Claude without a plugin when the image has none" {
  export LEA_CLAUDE_PLUGIN="$BATS_TEST_TMPDIR/no-plugin"
  run "$SESSION" --claude --resume
  [ "$status" -eq 0 ]
  run calls
  [ "$output" = "claude [--resume]" ]
}

# Codex gets lea's flags from the daemon (internal/providers): the session adds nothing, and
# never Claude's settings or plugin.
@test "--codex runs Codex with its arguments only" {
  export LEA_CLAUDE_PLUGIN="$BATS_TEST_TMPDIR/claude-plugin"; mkdir -p "$LEA_CLAUDE_PLUGIN"
  export LEA_CLAUDE_SETTINGS="$BATS_TEST_TMPDIR/claude-settings.json"; echo '{}' >"$LEA_CLAUDE_SETTINGS"
  run "$SESSION" --codex --model gpt-5 "do it"
  [ "$status" -eq 0 ]
  run calls
  [ "$output" = "codex [--model] [gpt-5] [do it]" ]
}

# A resumed session's transcript is repaired first (#219), by lea inside the container.
@test "the session repairs the transcript of the session it resumes" {
  cat >"$F/lea" <<'EOT'
#!/bin/sh
{ printf 'lea'; for a in "$@"; do printf ' [%s]' "$a"; done; printf '\n'; } >>"$FAKE_LOG"
EOT
  chmod +x "$F/lea"
  export LEA_CLAUDE_PLUGIN="$BATS_TEST_TMPDIR/no-plugin" LEA_CLAUDE_SETTINGS="$BATS_TEST_TMPDIR/none.json"
  run "$SESSION" --claude --resume 0f8e2c1a-1b2c-4d5e-8f90-123456789abc --model opus "go on"
  [ "$status" -eq 0 ]
  run calls
  [ "$output" = "lea [repair-transcript] [0f8e2c1a-1b2c-4d5e-8f90-123456789abc]
claude [--resume] [0f8e2c1a-1b2c-4d5e-8f90-123456789abc] [--model] [opus] [go on]" ]
}

@test "a failed repair does not stop the resume, and a new session repairs nothing" {
  printf '#!/bin/sh\necho "lea $*" >>"$FAKE_LOG"; exit 1\n' >"$F/lea"; chmod +x "$F/lea"
  export LEA_CLAUDE_PLUGIN="$BATS_TEST_TMPDIR/no-plugin" LEA_CLAUDE_SETTINGS="$BATS_TEST_TMPDIR/none.json"
  run "$SESSION" --claude --resume=abc
  [ "$status" -eq 0 ]
  run calls
  [ "$output" = "lea repair-transcript abc
claude [--resume=abc]" ]
  : >"$FAKE_LOG"
  run "$SESSION" --claude "first prompt"
  [ "$status" -eq 0 ]
  run calls
  [ "$output" = "claude [first prompt]" ]
}
