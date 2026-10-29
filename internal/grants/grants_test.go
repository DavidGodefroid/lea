package grants

import (
	"encoding/json"
	"strings"
	"testing"
)

const now = 1_000_000

var dev = Caller{Key: "lea-a/dev-2", Container: "lea-a", Repo: "lea", Branch: "feat/73-x",
	Lineage: []string{"lea-a/dev-2", "lea-a/claude-1"}, OwnWorktree: true, Worktree: "/p/.lea/worktrees/lea/dev-2", Cwd: "/p/.lea/worktrees/lea/dev-2",
	Env: map[string]string{"HOME": "/home/u", "GH_CONFIG_DIR": "/lea/state/gh", "GLAB_CONFIG_DIR": "/lea/state/glab-cli"}, Proxy: LeaProxy}

func all(action string) Grant {
	g := Grant{ID: 7, Container: "lea-a", Action: action, Repo: "lea", Branch: "feat/*", Created: now - 10, Expires: now + 3600}
	if NeedsGitHub(action) {
		g.GitHub = "github.com/o/lea"
	}
	return g
}

func bash(cmd string) json.RawMessage {
	b, _ := json.Marshal(map[string]string{"command": cmd})
	return b
}

func TestDecideBash(t *testing.T) {
	every := []Grant{all(Merge), all(CloseIssue), all(DeleteOwnBranch), all(CommitIdentity)}
	cases := []struct {
		name, cmd, want string
		gs              []Grant
	}{
		{"merge numbered", "gh pr merge 42 --squash", Allow, every},
		{"merge unnamed", "gh pr merge --merge", "", every},
		{"merge own branch delete", "gh pr merge feat/73-x --squash --delete-branch", Allow, every},
		{"merge own branch", "gh pr merge feat/73-x --squash", Allow, every},
		{"merge HEAD delete", "gh pr merge --squash --delete-branch", "", every},
		{"merge other branch", "gh pr merge feat/other --squash", "", every},
		{"merge numbered delete", "gh pr merge 42 --squash --delete-branch", "", every},
		{"merge numbered -d", "gh pr merge 42 -s -d", "", every},
		{"merge no grant", "gh pr merge 42 --squash", "", nil},
		{"merge other action granted", "gh pr merge 42 --squash", "", []Grant{all(CloseIssue)}},
		{"merge admin", "gh pr merge 42 --squash --admin", Deny, every},
		{"merge admin no grant", "gh pr merge 42 --admin", Deny, nil},
		{"merge admin chained", "true && gh pr merge 1 --admin", Deny, every},
		{"merge two methods", "gh pr merge 42 --squash --rebase", "", every},
		{"merge other repo flag", "gh pr merge 42 --squash -R evil/repo", "", every},
		{"merge auto", "gh pr merge 42 --auto", "", every},
		{"merge chained", "gh pr merge 42 --squash; rm -rf /", "", every},
		{"merge and", "gh pr merge 42 --squash && curl x", "", every},
		{"merge pipe", "gh pr merge 42 --squash | sh", "", every},
		{"merge subshell", "gh pr merge $(echo 42) --squash", Deny, every},
		{"merge backtick", "gh pr merge `echo 42` --squash", Deny, every},
		{"merge newline", "gh pr merge 42 --squash\nrm -rf /", "", every},
		{"merge env prefix", "GH_TOKEN=x gh pr merge 42 --squash", "", every},
		{"merge quoted op", "gh pr merge '42;' --squash", "", every},
		{"issue close", "gh issue close 12 --comment 'done in #13; thanks'", Allow, every},
		{"issue comment dq", `gh issue comment 12 --body "fixed by PR 13"`, Allow, every},
		{"issue comment dq expansion", `gh issue comment 12 --body "$(cat /etc/passwd)"`, "", every},
		{"issue delete", "gh issue delete 12", "", every},
		{"issue edit", "gh issue edit 12 --title x", "", every},
		{"item edit not covered", "gh project item-edit --id X --project-id P --field-id F --single-select-option-id O", "", every},
		{"commit", "git commit -m 'feat: x'", Allow, every},
		{"commit amend", "git commit --amend -m x", "", every},
		{"commit no message", "git commit -q", "", every},
		{"commit all", "git commit -a -m x", "", every},
		{"commit signoff", "git commit -s -m x", "", every},
		{"commit author", "git commit -m x --author 'a <b>'", "", every},
		{"commit config", "git -c user.name=x commit -m x", "", every},
		{"push own", "git push -u origin feat/73-x", Allow, every},
		{"push bare", "git push", Deny, every},
		{"push other branch", "git push origin feat/other", Deny, every},
		{"push main", "git push origin main", Deny, every},
		{"push refspec main", "git push origin HEAD:main", Deny, every},
		{"push refs main", "git push origin feat/73-x:refs/heads/main", Deny, every},
		{"push force", "git push --force origin feat/73-x", Deny, every},
		{"push -f", "git push -f origin feat/73-x", Deny, every},
		{"push -uf", "git push -uf origin feat/73-x", Deny, every},
		{"push lease", "git push --force-with-lease origin feat/73-x", Deny, every},
		{"push plus", "git push origin +feat/73-x", Deny, every},
		{"push mirror", "git push --mirror", Deny, nil},
		{"push force chained", "echo ok && git push --force", Deny, nil},
		{"delete own", "git push origin --delete feat/73-x", Allow, every},
		{"delete own no grant", "git push origin --delete feat/73-x", "", nil},
		{"delete other", "git push origin --delete feat/other", Deny, every},
		{"delete other colon", "git push origin :feat/other", Deny, every},
		{"delete own colon", "git push origin :feat/73-x", "", every},
		{"delete -d other", "git push -d origin feat/other", Deny, every},
		{"delete chained", "true; git push origin --delete feat/73-x", Deny, every},
		{"delete main", "git push origin --delete main", Deny, every},
		{"edit settings by shell", "sed -i s/a/b/ ~/.claude/settings.json", Deny, nil},
		{"write settings redirect", "echo {} > /home/u/.claude/settings.local.json", Deny, nil},
		{"read settings", "cat /home/u/.claude/settings.json", "", nil},
		{"hooks dir", "cp x /home/u/.claude/hooks/pre.sh", Deny, nil},
		{"lea settings", "cp x /usr/local/share/lea/claude-settings.json", Deny, nil},
		{"lea state git identity", "git config --file /lea/state/git/config user.name x", Deny, nil},
		{"repo's own claude-settings source", "go test ./image/claude-settings.json", "", nil},
		{"unrelated", "go test ./...", "", every},
		// B1: nothing may repoint git or gh.
		{"gh set-default", "gh repo set-default evil/repo", Deny, every},
		{"gh set-default view", "gh repo set-default --view", "", every},
		{"remote set-url", "git remote set-url origin https://github.com/evil/repo", Deny, every},
		{"remote add chained", "git remote add x u && git push x feat/73-x", Deny, every},
		{"config write", "git config remote.origin.url https://evil", Deny, every},
		{"config write global", "git -C /x config --global url.https://evil/.insteadOf https://github.com/", Deny, every},
		{"config read", "git config --get remote.origin.url", "", every},
		{"config list", "git config --list --show-origin", "", every},
		{"config write disguised as read", "git config -z core.hooksPath /tmp/h", Deny, every},
		{"config get plus value", "git config --get core.hooksPath /tmp/h", Deny, every},
		{"config escaped", `git con\fig core.hooksPath /tmp/h`, Deny, every},
		// Escapes and expansions never hide a protected target.
		{"push expansion main", "git push origin $(echo main)", Deny, every},
		{"push backtick", "git push origin `echo main`", Deny, every},
		{"push ansi-c", "git push origin $'\\x6dain'", Deny, every},
		{"path expansion", "cp x .claude/sett$(echo ings).json", Deny, nil},
		{"remote expansion", "git $(echo remote) set-url origin x", Deny, nil},
		{"harmless expansion", "git diff $(git merge-base HEAD origin/main)", "", every},
		{"built verb push", "$(printf g)it push origin x", Deny, nil},
		{"variable program push", "$G push origin x", Deny, nil},
		{"built remote", "g${x}it remote set-url origin y", Deny, nil},
		{"files loop", "for f in $(git ls-files); do wc -l $f; done", "", nil},
		{"GH_REPO prefix", "GH_REPO=evil/repo gh pr merge 1 --squash", Deny, every},
		{"GH_HOST export", "export GH_HOST=evil.example", Deny, every},
		{"agent -R", "gh pr merge 1 --squash -R evil/repo", "", every},
		// B2: nothing may plant a program git runs.
		{"hook planted", "cp x .git/hooks/pre-commit", Deny, nil},
		{"hook abs", "chmod +x /p/lea/.git/hooks/pre-push", Deny, nil},
		{"git config file", "sed -i s/x/y/ .git/config", Deny, nil},
		{"worktree config", "echo x >> /p/lea/.git/worktrees/dev-2/config.worktree", Deny, nil},
		{"gitconfig", "echo '[core] hooksPath=/tmp' >> ~/.gitconfig", Deny, nil},
		{"bashrc", "echo 'alias git=evil' >> ~/.bashrc", Deny, nil},
		{"shell snapshot", "cp x ~/.claude/shell-snapshots/s.sh", Deny, nil},
		// (a) relative and quoted paths.
		{"relative settings", "cp x .claude/settings.json", Deny, nil},
		{"quoted settings", `cp x ".claude/settings.local.json"`, Deny, nil},
		{"single-quoted settings", "cp x '.claude/settings.json'", Deny, nil},
		{"dot segment settings", "cp x .claude/./settings.json", Deny, nil},
		{"double slash hooks", "cp x .git//hooks/pre-commit", Deny, nil},
		// (a') relative to the directory a cd, pushd or -C moves to.
		{"cd then add", "cd .claude && git add settings.json", Deny, nil},
		{"cd semicolon", "cd .claude; cp x settings.local.json", Deny, nil},
		{"cd or", "cd .claude || true; cp x settings.json", Deny, nil},
		{"cd twice", "cd .git && cd hooks && cp x pre-commit", Deny, nil},
		{"cd dir slash", "cd .git/ && sed -i s/a/b/ config", Deny, nil},
		{"cd up", "cd internal && cp x ../.claude/settings.json", Deny, nil},
		{"cd up from inside", "cd .claude/x && cp y ../settings.json", Deny, nil},
		{"cd abs", "cd /home/u/.claude && cp x settings.json", Deny, nil},
		{"cd home", "cd ~/.claude && cp x settings.json", Deny, nil},
		{"cd bare home", "cd && cd .claude && cp x settings.json", Deny, nil},
		{"cd -P", "cd -P .claude && cp x settings.json", Deny, nil},
		{"cd redirect", "cd .claude && echo {} > settings.json", Deny, nil},
		{"cd subshell", "(cd .claude && cp x settings.json)", Deny, nil},
		{"cd pipeline", "true | cd .git; echo x >> config", Deny, nil},
		{"cd newline", "cd .git\nchmod +x hooks/pre-push", Deny, nil},
		{"pushd", "pushd .claude && cp x hooks/pre.sh", Deny, nil},
		{"git -C add", "git -C .claude add settings.json", Deny, nil},
		{"git -C checkout", "git -C .git checkout -- config", Deny, nil},
		{"git -C twice", "git -C .claude -C hooks status x", Deny, nil},
		{"git work-tree", "git --work-tree=.claude checkout HEAD -- settings.json", Deny, nil},
		{"git work-tree spaced", "git --work-tree .claude checkout HEAD -- settings.json", Deny, nil},
		{"git git-dir", "git --git-dir .git add hooks/pre-commit", Deny, nil},
		{"env chdir", "env --chdir=.claude cp x settings.json", Deny, nil},
		{"tar -C", "tar -C .claude -xf a.tar settings.json", Deny, nil},
		{"shell script", "bash -c 'cd .claude && cp x settings.json'", Deny, nil},
		{"inner substitution", "echo $(cd .claude && cp x settings.json)", Deny, nil},
		// What cannot be resolved fails closed.
		{"cd variable", `cd "$D" && cp x settings.json`, Deny, nil},
		{"cd substitution", "cd $(dirname x) && cp x settings.json", Deny, nil},
		{"cd dash", "cd - && cp x settings.json", Deny, nil},
		{"cd glob", "cd .cl* && cp x settings.json", Deny, nil},
		{"cd tilde user", "cd ~u/.claude && cp x settings.json", Deny, nil},
		{"popd", "popd && echo x >> config", Deny, nil},
		{"git -C variable", "git -C $D add hooks/pre-commit", Deny, nil},
		{"CDPATH", "CDPATH=/home/u/.claude cd hooks && cp x pre.sh", Deny, nil},
		{"cd variable unrelated", `cd "$D" && go test ./...`, "", nil},
		{"cd toplevel", `cd "$(git rev-parse --show-toplevel)" && go test ./internal/grants/`, "", nil},
		{"cd dash config dir", "cd - && go test ./config/... ./internal/config/...", "", nil},
		// Plain cds that reach nothing protected are left alone.
		{"cd test", "cd internal/grants && go test ./...", "", nil},
		{"cd claude read docs", "cd .claude && ls", "", nil},
		{"git -C status", "git -C /p/lea status", "", nil},
		{"cd to repo then add", "cd /p/lea && git add internal/grants/bash.go", "", nil},
		{"cd heredoc message", "cd /p/lea && git commit -m \"$(cat <<'EOF'\nfix hooks; config settings.json\nEOF\n)\"", "", nil},
		{"cd claude heredoc", "cd .claude && git commit -m \"$(cat <<'EOF'\nx\nEOF\n)\" && cp x settings.json", Deny, nil},
		// (b) quoted push targets.
		{"quoted plus", `git push origin "+feat/73-x"`, Deny, every},
		{"quoted main", "git push origin 'main'", Deny, every},
		{"quoted refspec main", `git push origin HEAD:"main"`, Deny, every},
		{"escaped main compound", `true; git push origin ma\in`, Deny, nil},
		// Pushes to the default branch however the refspec spells it (a parse, not a regex).
		{"push all", "git push --all origin", Deny, nil},
		{"push branches", "git push --branches origin", Deny, nil},
		{"push tags", "git push --tags origin", Deny, nil},
		{"push heads main", "git push origin heads/main", Deny, nil},
		{"push refs heads main", "git push origin refs/heads/main", Deny, nil},
		{"push src refs main", "git push origin feat/73-x:heads/main", Deny, nil},
		{"push main redirect", "git push origin main>/dev/null", Deny, nil},
		{"push main 2>&1", "git push origin main 2>&1", Deny, nil},
		{"push second refspec main", "git push origin feat/73-x main", Deny, nil},
		{"push option then main", "git push -o ci.skip origin main", Deny, nil},
		{"push matching colon", "git push origin :", Deny, nil},
		{"push wildcard refspec", "git push origin 'refs/heads/*:refs/heads/*'", Deny, nil},
		{"push glob", "git push origin ma?n", Deny, nil},
		{"push brace", "git push origin {feat/73-x,main}", Deny, nil},
		{"push delete colon main", "git push origin :main", Deny, nil},
		{"push unknown option", "git push --repo=x origin feat/73-x", Deny, nil},
		{"push config pin", "git -c remote.origin.push=refs/heads/main push", Deny, nil},
		{"push git-dir", "git --git-dir=/p/lea/.git push origin HEAD", Deny, nil},
		{"push other dir HEAD", "git -C /p/lea push origin HEAD", Deny, nil},
		{"push cd then bare", "cd /p/lea && git push", Deny, nil},
		{"push env prefix", "GIT_DIR=/p/lea/.git git push", Deny, nil},
		{"push via shell", "sh -c 'git push origin main'", Deny, nil},
		{"push via xargs", "echo main | xargs git push origin", Deny, nil},
		{"push heredoc", "git push origin <<EOF\nmain\nEOF", Deny, nil},
		{"push abs git main", "/usr/bin/git push origin main", Deny, nil},
		{"push alias via -c", "git -c alias.p=push p origin main", Deny, nil},
		{"push alias via config-env", "git --config-env=alias.p=P p origin main", Deny, nil},
		{"push via xargs stdin", "echo push origin main | xargs git", Deny, nil},
		{"push via env", "env git push origin main", Deny, nil},
		{"alias from a variable", "git -c alias.p=$X p origin main", Deny, nil},
		{"alias by GIT_CONFIG env", "GIT_CONFIG_COUNT=1 GIT_CONFIG_KEY_0=alias.p GIT_CONFIG_VALUE_0=$V git p origin main", Deny, nil},
		{"pager pin", "git -c core.pager=cat log --oneline", Deny, nil},
		{"other config pin", "git -c core.fsmonitor=/tmp/x status", Deny, nil},
		{"push via python", `python3 -c "import os; os.system('git push origin main')"`, Deny, nil},
		{"subtree push", "git subtree push --prefix x origin main", Deny, nil},
		{"submodule foreach push", "git submodule foreach git push origin main", Deny, nil},
		{"globbed program", "/usr/bin/gi? push origin main", Deny, nil},
		{"globbed short program", "g?t push origin main", Deny, nil},
		{"docker push", "docker push img:1", "", nil},
		{"alias upper case", "git -c ALIAS.p=$X p origin main", Deny, nil},
		{"alias by include", "git -c include.path=/tmp/x p origin main", Deny, nil},
		{"alias by includeIf", "git -c includeIf.onbranch:x.path=/tmp/x p origin main", Deny, nil},
		{"config file by env", "GIT_CONFIG_GLOBAL=/tmp/cfg git p origin main", Deny, nil},
		{"config file by export", "export GIT_CONFIG_GLOBAL=/tmp/cfg", Deny, nil},
		{"exec path", "git --exec-path=/tmp/x p origin main", Deny, nil},
		{"identity pin is fine", "git -c user.name=x -c user.email=y log -1", "", nil},
		{"mention config env is fine", "grep -n GIT_CONFIG_GLOBAL internal/grants/grants.go", "", nil},
		{"push own HEAD", "git push -u origin HEAD", Deny, nil},
		{"push own refspec", "git push origin HEAD:feat/73-x", "", nil},
		{"send-pack main", "git send-pack https://github.com/o/lea.git feat/73-x:main", Deny, nil},
		{"http-push main", "git http-push https://x/lea.git feat/73-x:main", Deny, nil},
		{"remote helper", "git remote-ext 'sh -c x' https://x", Deny, nil},
		{"push @", "git push origin @", Deny, nil},
		{"push origin only", "git push origin", Deny, nil},
		{"push to other agent", "git push origin feat/73-x:feat/other", Deny, nil},
		{"push tag ref", "git push origin refs/tags/v1", Deny, nil},
		{"push follow-tags", "git push --follow-tags origin feat/73-x", Deny, every},
		{"push follow-tags last", "git push origin feat/73-x --follow-tags", Deny, every},
		{"push follow-tags chained", "git commit -m x && git push --follow-tags origin feat/73-x", Deny, nil},
		{"push no-follow-tags", "git push --no-follow-tags origin feat/73-x", "", nil},
		{"push mirror named", "git push --mirror origin", Deny, nil},
		{"push tag keyword", "git push origin tag v1", Deny, nil},
		{"push branch to tag", "git push origin feat/73-x:refs/tags/v1", Deny, every},
		{"push tags short", "git push origin feat/73-x:tags/v1", Deny, every},
		{"push follow-tags config", "git -c push.followTags=true push origin feat/73-x", Deny, nil},
		{"push own full ref", "git push origin HEAD:refs/heads/feat/73-x", "", nil},
		{"gh pr title mentions push", `gh pr create --title "fix git push" --body "no more git push origin main"`, "", nil},
		{"log grep push", "git log --grep push", "", nil},
		{"echo git", "echo git", "", nil},
		{"no-pager diff", "git --no-pager diff --stat", "", nil},
		{"xargs git add", "git ls-files -m | xargs git add", Deny, nil},
		{"gh body with push", `gh pr comment 1 --body "run git push origin main yourself"`, "", nil},
		{"rebase exec push", "git rebase -x 'git push origin main' HEAD~2", Deny, nil},
		{"sudo env assignment", "sudo GIT_DIR=/tmp/r git p origin main", Deny, nil},
		{"unlisted interpreter", `pwsh -c "git push origin main"`, Deny, nil},
		{"deno eval", `deno eval "Deno.run({cmd:['git','push','origin','main']})"`, Deny, nil},
		{"gh alias shell", `gh alias set p '!git push origin main'`, Deny, nil},
		{"upload-pack program", `git ls-remote --upload-pack='sh -c "git push origin main"' .`, Deny, nil},
		{"upload-pack file", "git fetch --upload-pack=/tmp/x.sh origin", Deny, nil},
		{"receive-pack", "git push --receive-pack=/tmp/x origin feat/73-x", Deny, nil},
		{"grep -O pager", "git grep -O/tmp/x.sh foo", Deny, nil},
		{"commit message fix git push", `git commit -m "fix git push parsing"`, "", nil},
		{"xargs -I subcommand", "echo push | xargs -I X git X origin feat/73-x:main", Deny, nil},
		{"subst subcommand", "git $(printf pu)sh origin feat/73-x:main", Deny, nil},
		{"backtick subcommand", "git `printf pu`sh origin feat/73-x:main", Deny, nil},
		{"var subcommand", "git ${p}sh origin feat/73-x:main", Deny, nil},
		{"dq var subcommand", `git "$P" origin feat/73-x:main`, Deny, nil},
		{"subst refspec", "git push origin feat/73-x:$(printf main)", Deny, nil},
		{"computed program", "$G $P origin feat/73-x:main", Deny, nil},
		{"env computed program", "env $G $P origin feat/73-x:main", Deny, nil},
		{"sh positional push", `sh -c 'git "$0" origin feat/73-x:main' push`, Deny, nil},
		{"bash positional push", `bash -c 'git "$@"' x push origin feat/73-x:main`, Deny, nil},
		{"push inside subst", "echo $(git push origin feat/73-x:main)", Deny, nil},
		{"escaped push", `git pu\sh origin feat/73-x:main`, Deny, nil},
		{"diff merge-base subst", "git diff $(git merge-base HEAD origin/main)", "", nil},
		{"sudo xargs git", "echo push | sudo xargs -I X git X origin feat/73-x:main", Deny, nil},
		{"xargs sh -c git", "echo push | xargs -I X sh -c 'git X origin feat/73-x:main'", Deny, nil},
		{"quote-split script", `sh -c 'g""it pu""sh origin feat/73-x:main'`, Deny, nil},
		{"quote-split script send-pack", `sh -c 'g""it se""nd-pack u feat/73-x:main'`, Deny, nil},
		{"quote-split under xargs", `echo x | xargs sh -c 'g""it status'`, Deny, nil},
		{"dq backtick escaped quote", "echo \"`g\\\"\\\"it se\\\"\\\"nd-pack u feat/73-x:main`\"", Deny, nil},
		// send-pack: no word the escape/expansion rule of hardBash knows, only the parser sees it.
		{"dq dollar quote swallow", `echo "$"; git send-pack u feat/73-x:main; echo "x"`, Deny, nil},
		{"dq dollar squote", `echo "a$'"; git send-pack u feat/73-x:main; echo "'"`, Deny, nil},
		{"locale string subst", `echo $"$(git send-pack u feat/73-x:main)"`, Deny, nil},
		{"nested backtick", "echo `echo \\`git send-pack u feat/73-x:main\\``", Deny, nil},
		{"find exec sh git", "find . -exec sh -c 'git add \"$1\"' _ {} ;", Deny, nil},
		{"parallel script git", "parallel 'git {} origin feat/73-x:main' ::: push", Deny, nil},
		{"env git no subcommand", "env git", Deny, nil},
		{"submodule foreach", "git submodule foreach 'g=git; $g status'", Deny, nil},
		{"bisect run", "git bisect run ./t.sh", Deny, nil},
		{"filter-branch", "git filter-branch --tree-filter 'rm x' HEAD", Deny, nil},
		{"subtree push", "git subtree push --prefix x origin feat/73-x", Deny, nil},
		{"commit heredoc message", "git commit -m \"$(cat <<'EOF'\nfeat: x\nEOF\n)\"", "", nil},
		{"commit subst message", `git commit -m "$(cat msg.txt)"`, "", nil},
		// #96: a quoted heredoc expands nothing: its text is a message, not a command.
		{"commit heredoc push", "git commit -m \"$(cat <<'EOF'\nfix: git push parsing\n\nrun `git push origin main` with $X\nEOF\n)\"", "", nil},
		{"pr create heredoc push", "gh pr create --title \"fix push\" --body \"$(cat <<'EOF'\n## Summary\n- `git push origin main` no longer denied; see .claude/settings\nEOF\n)\"", "", nil},
		{"add then commit heredoc push", "git add x && git commit -m \"$(cat <<'EOF'\nfix push\nEOF\n)\"", "", nil},
		{"commit heredoc dq delim", "git commit -m \"$(cat <<\"EOF\"\nfix push\nEOF\n)\"", "", nil},
		{"commit heredoc then push main", "git commit -m \"$(cat <<'EOF'\nfix push\nEOF\n)\" && git push origin main", Deny, nil},
		{"unquoted heredoc push", "git commit -m \"$(cat <<EOF\nfix push $(git push origin main)\nEOF\n)\"", Deny, nil},
		{"heredoc to shell", "bash -c \"$(cat <<'EOF'\ngit push origin main\nEOF\n)\"", Deny, nil},
		{"heredoc to eval", "eval \"$(cat <<'EOF'\ngit push origin main\nEOF\n)\"", Deny, nil},
		{"heredoc not a message", "git \"$(cat <<'EOF'\npush\nEOF\n)\" origin main", Deny, nil},
		{"heredoc after cat function", "cat() { bash; } && git commit -m \"$(cat <<'EOF'\ngit push origin main\nEOF\n)\"", Deny, nil},
		{"heredoc after hash", "hash -p /tmp/sh cat && git commit -m \"$(cat <<'EOF'\ngit push origin main\nEOF\n)\"", Deny, nil},
		{"heredoc cat piped", "git commit -m \"$(cat <<'EOF' | sh\ngit push origin main\nEOF\n)\"", Deny, nil},
		{"heredoc trailing code", "git commit -m \"$(cat <<'EOF'\nfix push\nEOF\ngit push origin main\n)\"", Deny, nil},
		// bash ends a heredoc inside $(…) on "EOF)" too: the rest runs as code.
		{"heredoc delim paren", "git commit -m \"$(cat <<'EOF'\nx\nEOF)\" && git push origin main\nb\nEOF\n)\"", Deny, nil},
		{"heredoc delim space paren", "git commit -m \"$(cat <<'EOF'\nx\nEOF )\" ; git push origin main\nb\nEOF\n)\"", Deny, nil},
		{"heredoc delim tab prefix", "git commit -m \"$(cat <<-'EOF'\npush\n\tEOF;\nEOF\n)\"", Deny, nil},
		{"heredoc delim indented", "git commit -m \"$(cat <<'EOF'\n push\n  EOF\nEOF\n)\"", Deny, nil},
		{"heredoc gh api", "gh api -f body=\"$(cat <<'EOF'\npush\nEOF\n)\" repos/o/r", Deny, nil},
		{"own push with var-free dq", `git push origin "feat/73-x"`, "", nil},
		{"find exec git", "find . -name x -exec git {} origin feat/73-x:main ;", Deny, nil},
		{"find execdir git", "find . -execdir git add {} +", Deny, nil},
		{"parallel git", "parallel git ::: push", Deny, nil},
		{"worktree add push branch", "git worktree add ../x fix/grants-bash-merge-push", "", nil},
		{"rebase on push branch", "git rebase origin/fix/grants-bash-merge-push", "", nil},
		{"env git on push branch", "LANG=C git rebase origin/fix/grants-bash-merge-push", "", nil},
		{"git-dir then alias", "git --git-dir .git -c alias.p=push p origin main", Deny, nil},
		{"work-tree", "git --work-tree /tmp/x status", Deny, nil},
		{"home config", "HOME=/tmp/h git p origin main", Deny, nil},
		{"xdg config", "XDG_CONFIG_HOME=/tmp/x git p origin main", Deny, nil},
		{"read config var", "read GIT_CONFIG_GLOBAL < f; git p origin main", Deny, nil},
		{"git editor true", "GIT_EDITOR=true git rebase --continue", "", nil},
		{"git editor colon", "GIT_SEQUENCE_EDITOR=: git rebase --continue", "", nil},
		{"git pager cat", "GIT_PAGER=cat git log -1", "", nil},
		{"git prompt off", "GIT_TERMINAL_PROMPT=0 git fetch origin", "", nil},
		{"git editor program", "GIT_EDITOR=/tmp/x.sh git rebase --continue", Deny, nil},
		{"git pager program", "GIT_PAGER=less git log -1", Deny, nil},
		{"push own chained", "git commit -m 'push it' && git push origin feat/73-x", "", nil},
		{"push quiet own", "git push -q -o ci.skip origin feat/73-x", "", nil},
		{"commit mentions push main", "git commit -m 'git push origin main'", "", nil},
		// A word holding a whole git push is checked only for programs that run it.
		{"grep push main", "grep -rn 'git push origin main' docs", "", nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			d := Decide("Bash", bash(tc.cmd), dev, tc.gs, now, false)
			if d.Verdict != tc.want {
				t.Fatalf("%q: verdict %q (%s), want %q", tc.cmd, d.Verdict, d.Reason, tc.want)
			}
			if d.Verdict == Allow && (d.Grant != 7 || d.Command == "") {
				t.Fatalf("allow names grant %d, command %q", d.Grant, d.Command)
			}
		})
	}
}

func TestDecideScope(t *testing.T) {
	cmd := bash("gh pr merge 42 --squash")
	base := all(Merge)
	mod := func(f func(*Grant)) []Grant { g := base; f(&g); return []Grant{g} }
	cases := []struct {
		name string
		gs   []Grant
		c    Caller
		want string
	}{
		{"match", []Grant{base}, dev, Allow},
		{"expired", mod(func(g *Grant) { g.Expires = now }), dev, ""},
		{"revoked", mod(func(g *Grant) { g.Revoked = now - 1 }), dev, ""},
		{"other lea", mod(func(g *Grant) { g.Container = "lea-b" }), dev, ""},
		{"other repo", mod(func(g *Grant) { g.Repo = "other" }), dev, ""},
		{"no repo binding", mod(func(g *Grant) { g.Repo = "" }), dev, ""},
		{"branch glob miss", mod(func(g *Grant) { g.Branch = "fix/*" }), dev, ""},
		{"branch any", mod(func(g *Grant) { g.Branch = "" }), dev, Allow},
		{"subtree of lead", mod(func(g *Grant) { g.Agent = "lea-a/claude-1" }), dev, Allow},
		{"subtree self", mod(func(g *Grant) { g.Agent = "lea-a/dev-2" }), dev, Allow},
		{"subtree sibling", mod(func(g *Grant) { g.Agent = "lea-a/dev-3" }), dev, ""},
		{"caller without repo", []Grant{base}, Caller{Key: "lea-a/x", Container: "lea-a", Lineage: []string{"lea-a/x"}}, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if d := Decide("Bash", cmd, tc.c, tc.gs, now, false); d.Verdict != tc.want {
				t.Fatalf("verdict %q (%s), want %q", d.Verdict, d.Reason, tc.want)
			}
		})
	}
}

// TestPinnedCommand: an allowed call runs lea's command, built from the validated argv: absolute
// program, scrubbed environment, git in the caller's worktree with hooks and command-running
// settings off, gh on the grant's repository; the audit target names the repository.
// TestProtectedCwdAndGlobs: relative words are read from the directory the call runs in (the
// hook's cwd, which a cd in an earlier call may have moved), and a glob that may match a
// protected path is one.
func TestProtectedCwdAndGlobs(t *testing.T) {
	cases := []struct {
		name, cwd, tool, cmd string
		want                 string
	}{
		{"cwd claude", "/home/u/.claude", "Bash", "cp x settings.json", Deny},
		{"cwd claude add", "/home/u/.claude", "Bash", "git add settings.local.json", Deny},
		{"cwd git config", "/p/lea/.git", "Bash", "echo x >> config", Deny},
		{"cwd git hooks", "/p/lea/.git/hooks", "Bash", "chmod +x pre-push", Deny},
		{"cwd git then cd", "/p/lea/.git", "Bash", "cd hooks && cp x pre-commit", Deny},
		{"cwd home", "/home/u", "Bash", "echo x >> .bashrc", Deny},
		{"cwd up", "/home/u/.claude/x", "Bash", "cp y ../settings.json", Deny},
		{"cwd claude read", "/home/u/.claude", "Bash", "cat settings.json", ""},
		{"cwd worktree", "/p/.lea/worktrees/lea/dev-2", "Bash", "cp x config", ""},
		{"cwd missing", "", "Bash", "cp x settings.json", Deny},
		{"cwd relative", "u/.claude", "Bash", "cp x hooks/pre.sh", Deny},
		{"cwd missing harmless", "", "Bash", "go test ./...", ""},
		{"edit relative", "/home/u/.claude", "Edit", "settings.json", Deny},
		{"edit relative worktree", "/p/.lea/worktrees/lea/dev-2", "Edit", "internal/x.go", ""},
		// Globs.
		{"glob settings", "/w", "Bash", "cp x .claude/sett*.json", Deny},
		{"glob claude dir", "/w", "Bash", "rm .clau?e/*", Deny},
		{"glob hooks", "/w", "Bash", "cp x .git/hoo*/pre-commit", Deny},
		{"glob class", "/w", "Bash", "cp x .git/[ch]onfig", Deny},
		{"glob rc", "/w", "Bash", "cp x ~/.bash*", Deny},
		{"glob brace", "/w", "Bash", "cp x .cla{u,x}de/settings.json", Deny},
		{"glob any dir", "/w", "Bash", "cp x .*/settings.json", Deny},
		{"glob worktree config", "/w", "Bash", "cp x .git/worktrees/*/config", Deny},
		{"glob after cd", "/w", "Bash", "cd .claude && cp x sett*", Deny},
		{"glob after unknown cd", "/w", "Bash", `cd "$D" && cp x sett*.json`, Deny},
		{"glob in cwd", "/home/u/.claude", "Bash", "cp x s*.json", Deny},
		{"glob dotglob", "/w", "Bash", "shopt -s dotglob; cp x */settings.json", Deny},
		{"glob brace no dot", "/w", "Bash", "cp x {.claude,x}/settings.json", Deny},
		{"glob dotglob escaped", "/w", "Bash", `shopt -s dot\glob; cp x */settings.json`, Deny},
		{"glob dotglob quoted", "/w", "Bash", `shopt -s "dot"glob; cp x */settings.json`, Deny},
		{"glob shopt computed", "/w", "Bash", `shopt -s $(echo x); cp x */settings.json`, Deny},
		{"glob GLOBIGNORE", "/w", "Bash", "GLOBIGNORE=x; cp x */settings.json", Deny},
		{"glob nocaseglob", "/w", "Bash", "shopt -s nocaseglob; cp x .CLAUDE/SETTINGS.jso?", Deny},
		{"glob nocase class", "/w", "Bash", "shopt -s nocaseglob; cp x .[C]laude/settings.json", Deny},
		{"glob zsh setopt", "/w", "Bash", "setopt globdots; cp x */settings.json", Deny},
		{"glob zsh qualifier", "/w", "Bash", "cp x */settings.json(D)", Deny},
		{"extglob", "/w", "Bash", "cp x .claude/@(settings).json", Deny},
		{"extglob negated", "/w", "Bash", "cp x .git/!(x)", Deny},
		{"extglob nested", "/w", "Bash", "cp x .claude/@(@(settings)).json", Deny},
		{"extglob quoted group", "/w", "Bash", "cp x .claude/@('settings').json", Deny},
		{"glob shopt computed program", "/w", "Bash", "$(printf sh%sopt '') -s dotg$(echo l)ob; cp x */settings.json", Deny},
		{"glob shopt eval", "/w", "Bash", "eval x; cp x */settings.json", Deny},
		{"glob shopt sourced", "/w", "Bash", "source x; cp x */settings.json", Deny},
		{"glob shopt dot sourced", "/w", "Bash", ". ./x; cp x */settings.json", Deny},
		{"glob star no dot", "/w", "Bash", "cp x */settings.json", ""},
		{"glob build", "/w", "Bash", "rm -rf build/*", ""},
		{"glob out", "/w", "Bash", "rm -f *.out", ""},
		{"glob go test", "/w", "Bash", "go test ./internal/*/...", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := dev
			c.Cwd = tc.cwd
			in := bash(tc.cmd)
			if tc.tool != "Bash" {
				in, _ = json.Marshal(map[string]string{"file_path": tc.cmd})
			}
			if d := Decide(tc.tool, in, c, nil, now, false); d.Verdict != tc.want {
				t.Fatalf("%q in %q: verdict %q (%s), want %q", tc.cmd, tc.cwd, d.Verdict, d.Reason, tc.want)
			}
		})
	}
}

func TestPinnedCommand(t *testing.T) {
	gs := []Grant{all(Merge), all(CommitIdentity)}
	d := Decide("Bash", bash("gh pr merge feat/73-x --squash --delete-branch"), dev, gs, now, false)
	if d.Verdict != Allow || !strings.HasPrefix(d.Command, `'/usr/bin/env' '-i' 'PATH=/usr/bin:/bin'`) ||
		!strings.Contains(d.Command, `'HOME=/home/u' 'GH_CONFIG_DIR=/lea/state/gh' 'GLAB_CONFIG_DIR=/lea/state/glab-cli' '/bin/sh' '-c' `) || d.Request.Target != "github.com/o/lea pr of feat/73-x" {
		t.Fatalf("merge: %+v", d)
	}
	if s := lastWord(t, d.Command); !strings.Contains(s, `'/usr/bin/gh' 'pr' 'view' 'feat/73-x' '--json' 'headRefName,isCrossRepository'`) ||
		!strings.HasSuffix(s, "\n"+`'/usr/bin/gh' 'pr' 'merge' 'feat/73-x' '--squash' '--delete-branch' '-R' 'github.com/o/lea'`) {
		t.Fatalf("merge script %q", s)
	}
	// A PR named by number is merged only after gh says its head is the caller's own branch
	// (in the grant's repository, not a fork): the grant's branch glob matched that branch.
	d = Decide("Bash", bash("gh pr merge 42 --squash"), dev, gs, now, false)
	s0 := lastWord(t, d.Command)
	for _, w := range []string{`'/usr/bin/gh' 'pr' 'view' '42' '--json' 'headRefName,isCrossRepository'`, `'-R' 'github.com/o/lea') || exit 1`,
		`[ "$head" != 'false feat/73-x' ]`, `'/usr/bin/gh' 'pr' 'merge' '42' '--squash' '-R' 'github.com/o/lea'`} {
		if !strings.Contains(s0, w) {
			t.Fatalf("numbered merge script lacks %s:\n%s", w, s0)
		}
	}
	if strings.Index(s0, "'view'") > strings.Index(s0, "'merge'") {
		t.Fatalf("head checked after the merge:\n%s", s0)
	}
	d = Decide("Bash", bash(`git commit -m "it's done"`), dev, gs, now, false)
	s := lastWord(t, d.Command)
	for _, w := range []string{`'/usr/bin/git' '-C' '/p/.lea/worktrees/lea/dev-2'`, `'-c' 'core.hooksPath=/dev/null'`,
		`'-c' 'core.fsmonitor=false'`, `'-c' 'core.sshCommand=/usr/bin/ssh'`, `'-c' 'protocol.allow=never'`, ` write-tree)`,
		` commit-tree "$tree" -p "$parent" '-m' 'it'\''s done'`, `'-c' 'credential.helper=' '-c' 'credential.helper=store`} {
		if !strings.Contains(s, w) {
			t.Fatalf("commit script lacks %s:\n%s", w, s)
		}
	}
	if d.Request.Target != "lea commit" || !strings.Contains(d.Command, `'GIT_CONFIG_GLOBAL=/dev/null'`) {
		t.Fatalf("commit %+v", d)
	}
	// A merge grant without a GitHub repository allows no gh call.
	g := all(Merge)
	g.GitHub = ""
	if d := Decide("Bash", bash("gh pr merge 42 --squash"), dev, []Grant{g}, now, false); d.Verdict != "" {
		t.Fatalf("unpinned merge: %+v", d)
	}
	// An environment value lea cannot vouch for: no command, no allow.
	for _, bad := range []map[string]string{{"HOME": "home/u"}, {"HOME": "/home/u;evil"}, {"GH_CONFIG_DIR": "/x y"}} {
		odd := dev
		odd.Env = bad
		if d := Decide("Bash", bash("git commit -m x"), odd, gs, now, false); d.Verdict != "" {
			t.Fatalf("env %v: %+v", bad, d)
		}
	}
	// Under LockHTTP lea names the gate, with the agent's own proxy password: allowed, and nothing
	// but an agent name and a hex token may stand before the host.
	gated := dev
	gated.Proxy = "http://dev-2:0123456789abcdef0123456789abcdef@lea-proxy:3130"
	for _, call := range []string{"gh pr merge 42 --squash", "git push origin feat/73-x"} {
		d := Decide("Bash", bash(call), gated, gs, now, false)
		if d.Verdict != Allow || !strings.Contains(d.Command, gated.Proxy) {
			t.Fatalf("%s through the gate: %+v", call, d)
		}
	}
	for _, bad := range []string{"http://dev-2:0123456789abcdef0123456789abcdef@lea-proxy:3130/x", "http://dev-2:not-hex@lea-proxy:3130",
		"http://Dev:0123456789abcdef0123456789abcdef@lea-proxy:3130", "http://dev-2@lea-proxy:3130", "http://a:b:c@lea-proxy:3130",
		"http://dev-2:0123456789abcdef0123456789abcdef@@lea-proxy:3130", "https://lea-proxy:3130"} {
		b := dev
		b.Proxy = bad
		if d := Decide("Bash", bash("gh pr merge 42 --squash"), b, gs, now, false); d.Command != "" {
			t.Fatalf("proxy %q pinned: %+v", bad, d)
		}
	}
	// The container's network is internal: every pinned call goes out through lea's proxy, set
	// by lea (Caller.Proxy, from the daemon), never through one the agent's environment names.
	sneaky := dev
	sneaky.Env = map[string]string{"HOME": "/home/u", "HTTPS_PROXY": "http://evil:1", "https_proxy": "http://evil:1"}
	for _, call := range []string{"gh pr merge 42 --squash", "git push origin feat/73-x", "git commit -m x"} {
		d := Decide("Bash", bash(call), sneaky, gs, now, false)
		if d.Verdict != Allow || strings.Contains(d.Command, "evil") {
			t.Fatalf("%s: %+v", call, d)
		}
		if call[:2] == "gh" && !strings.Contains(d.Command, `'HTTPS_PROXY=http://lea-proxy:3128' 'https_proxy=http://lea-proxy:3128'`) {
			t.Fatalf("%s: gh not through lea's proxy: %s", call, d.Command)
		}
		if s := lastWord(t, d.Command); call[:3] == "git" && (!strings.Contains(s, `'-c' 'http.proxy=http://lea-proxy:3128'`) || strings.Contains(s, `'http.proxy='`)) {
			t.Fatalf("%s: git not through lea's proxy:\n%s", call, s)
		}
	}
	// No proxy lea can vouch for: no command, no allow.
	for _, p := range []string{"", "lea-proxy:3128", "http://lea-proxy", "http://a b:1", "http://x:1/'$(id)'", "https://lea-proxy:3128", "http://lea-proxy:3128/"} {
		odd := dev
		odd.Proxy = p
		if d := Decide("Bash", bash("gh pr merge 42 --squash"), odd, gs, now, false); d.Verdict != "" {
			t.Fatalf("proxy %q: %+v", p, d)
		}
	}
	// No worktree, no git command.
	away := dev
	away.Worktree = ""
	if d := Decide("Bash", bash("git commit -m x"), away, gs, now, false); d.Verdict != "" {
		t.Fatalf("no worktree: %+v", d)
	}
}

func TestOwnWorktreeAndBranch(t *testing.T) {
	every := []Grant{all(Merge), all(CloseIssue), all(DeleteOwnBranch), all(CommitIdentity)}
	away := dev
	away.OwnWorktree = false
	for _, c := range []string{"gh pr merge 42 --squash", "gh issue close 3", "git commit -m x", "git push origin feat/73-x", "git push origin --delete feat/73-x"} {
		if d := Decide("Bash", bash(c), away, every, now, false); d.Verdict != "" {
			t.Fatalf("outside its worktree %q: %+v", c, d)
		}
	}
	shared := dev
	shared.Branch = ""
	if d := Decide("Bash", bash("git push origin --delete feat/73-x"), shared, every, now, false); d.Verdict != Deny {
		t.Fatalf("shared branch delete: %+v", d)
	}
	if d := Decide("Bash", bash("gh pr merge feat/73-x --squash -d"), shared, every, now, false); d.Verdict != "" {
		t.Fatalf("shared branch merge+delete: %+v", d)
	}
	if d := Decide("Bash", bash("git push origin feat/73-x"), shared, every, now, false); d.Verdict != Deny {
		t.Fatalf("shared branch push: %+v", d)
	}
	for _, tc := range []struct {
		cwd string
		ok  bool
	}{{"/p/.lea/worktrees/lea/dev-2", true}, {"/p/.lea/worktrees/lea/dev-2/", true}, {"/p/.lea/worktrees/lea/dev-2/internal", false},
		{"/p/.lea/worktrees/lea/dev-2/x/.lea/worktrees/lea/dev-2", false}, {"/p/.lea/worktrees/lea/./dev-2", false},
		{"/p/.lea/worktrees/lea/dev-22", false}, {"/p/.lea/worktrees/other/dev-2", false}, {"/p/lea", false},
		{"/p/.lea/worktrees/lea/dev-2/../dev-3", false}, {"p/.lea/worktrees/lea/dev-2", false}, {"", false}} {
		if InWorktree(tc.cwd, "lea", "dev-2") != tc.ok {
			t.Fatalf("InWorktree(%q) != %v", tc.cwd, tc.ok)
		}
	}
}

func TestDecideTools(t *testing.T) {
	stop := json.RawMessage(`{"name":"dev-3"}`)
	gs := []Grant{all(StopFinished)}
	gs[0].Branch = ""
	if d := Decide(StopTool, stop, dev, gs, now, true); d.Verdict != Allow || d.Request.Target != "dev-3" {
		t.Fatalf("finished child: %+v", d)
	}
	if d := Decide(StopTool, stop, dev, gs, now, false); d.Verdict != "" {
		t.Fatalf("unfinished child allowed: %+v", d)
	}
	if d := Decide(StopTool, stop, dev, nil, now, true); d.Verdict != "" {
		t.Fatalf("no grant allowed: %+v", d)
	}
	for _, p := range []string{"/home/u/.claude/settings.json", "/home/u/.claude/settings.local.json", "/repo/.claude/settings.json", "/usr/local/share/lea/claude-settings.json", "/lea/state/git/config", "/p/lea/.git/config", "/p/lea/.git/hooks/pre-commit", "/home/u/.gitconfig", "/home/u/.bashrc", ".claude/settings.json"} {
		in, _ := json.Marshal(map[string]string{"file_path": p})
		if d := Decide("Edit", in, dev, gs, now, true); d.Verdict != Deny {
			t.Fatalf("edit %s: %+v", p, d)
		}
	}
	for _, p := range []string{"/repo/main.go", "/repo/image/claude-settings.json", "/repo/lea/state.go"} {
		in, _ := json.Marshal(map[string]string{"file_path": p})
		if d := Decide("Edit", in, dev, gs, now, true); d.Verdict != "" {
			t.Fatalf("edit %s: %+v", p, d)
		}
	}
	in, _ := json.Marshal(map[string]string{"file_path": "/repo/main.go"})
	if d := Decide("Write", in, dev, gs, now, true); d.Verdict != "" {
		t.Fatalf("plain write: %+v", d)
	}
	for _, bad := range []string{``, `nope`, `{"command":7}`} {
		if d := Decide("Bash", json.RawMessage(bad), dev, []Grant{all(Merge)}, now, true); d.Verdict == Allow {
			t.Fatalf("bad input %q allowed", bad)
		}
	}
}

func TestWords(t *testing.T) {
	w, ok := Words(`gh issue close 3 --comment 'a; b' -r "not planned"`)
	if !ok || len(w) != 8 || w[5] != "a; b" || w[7] != "not planned" {
		t.Fatalf("%q %v", w, ok)
	}
	for _, bad := range []string{"", "  ", "a 'b", `a "b`, "a\\ b", "a=b c", "a > f", "a ~/x", "a *", "a #c", `a "$x"`, "a !b"} {
		if _, ok := Words(bad); ok {
			t.Fatalf("%q parsed", bad)
		}
	}
}

func TestGlob(t *testing.T) {
	for _, tc := range []struct {
		p, s string
		ok   bool
	}{{"*", "a/b", true}, {"feat/*", "feat/73-x", true}, {"feat/*", "fix/1", false}, {"*-x", "feat/73-x", true},
		{"feat/*/x", "feat/a/b/x", true}, {"main", "main", true}, {"main", "mainx", false}, {"a*b*c", "axc", false}} {
		if Glob(tc.p, tc.s) != tc.ok {
			t.Fatalf("Glob(%q, %q) != %v", tc.p, tc.s, tc.ok)
		}
	}
}

// TestPRCreate: pr-create opens a PR from the caller's own branch, gh pinned to the grant's
// repository; a body file must be a plain file of the caller's worktree.
func TestPRCreate(t *testing.T) {
	gs := []Grant{all(PRCreate)}
	cases := []struct{ name, cmd, want string }{
		{"title body", `gh pr create --head feat/73-x --base main --title "fix: x" --body "does y"`, Allow},
		{"short flags", `gh pr create -H feat/73-x -B main -t "fix: x" -b "does y" -d`, Allow},
		{"body file", `gh pr create --head feat/73-x --title "fix: x" --body-file pr.md`, Allow},
		{"body file in dir", `gh pr create --head feat/73-x --title x --body-file docs/pr.md --draft`, Allow},
		{"no head", `gh pr create --title x --body y`, ""},
		{"other head", `gh pr create --head feat/other --title x --body y`, ""},
		{"head main", `gh pr create --head main --title x --body y`, ""},
		{"no title", `gh pr create --head feat/73-x --body y`, ""},
		{"no body", `gh pr create --head feat/73-x --title x`, ""},
		{"body and file", `gh pr create --head feat/73-x --title x --body y --body-file f`, ""},
		{"absolute body file", `gh pr create --head feat/73-x --title x --body-file /etc/passwd`, ""},
		{"parent body file", `gh pr create --head feat/73-x --title x --body-file ../dev-3/notes.md`, ""},
		{"stdin body file", `gh pr create --head feat/73-x --title x --body-file -`, ""},
		{"git dir body file", `gh pr create --head feat/73-x --title x --body-file .git/HEAD`, ""},
		{"repo flag", `gh pr create --head feat/73-x --title x --body y -R evil/r`, ""},
		{"web", `gh pr create --head feat/73-x --title x --body y --web`, ""},
		{"fill", `gh pr create --head feat/73-x --fill`, ""},
		{"bad base", `gh pr create --head feat/73-x --base 'a b' --title x --body y`, ""},
		{"duplicate title", `gh pr create --head feat/73-x --title x --title z --body y`, ""},
		{"own-branch heredoc body", "gh pr create --head feat/73-x --title x --body \"$(cat <<'EOF'\ny\nEOF\n)\"", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			d := Decide("Bash", bash(tc.cmd), dev, gs, now, false)
			if d.Verdict != tc.want {
				t.Fatalf("%q: verdict %q (%s), want %q", tc.cmd, d.Verdict, d.Reason, tc.want)
			}
		})
	}
	if d := Decide("Bash", bash(`gh pr create --head feat/73-x --title x --body y`), dev, []Grant{all(Merge)}, now, false); d.Verdict != "" {
		t.Fatalf("merge grant covers pr create: %+v", d)
	}
	d := Decide("Bash", bash(`gh pr create --head feat/73-x --title "fix: x" --body y`), dev, gs, now, false)
	if s := lastWord(t, d.Command); s != ghIsolated()+`'/usr/bin/gh' 'pr' 'create' '--head' 'feat/73-x' '--title' 'fix: x' '--body' 'y' '-R' 'github.com/o/lea'` ||
		d.Request.Target != "github.com/o/lea pr create feat/73-x" {
		t.Fatalf("pr create: %q %+v", s, d)
	}
	// A body file is opened once in the caller's worktree; gh reads that open file on its stdin,
	// after a check that it resolves (symlinks too) to a plain file of the worktree outside .git.
	d = Decide("Bash", bash(`gh pr create --head feat/73-x --title x --body-file docs/pr.md`), dev, gs, now, false)
	s := lastWord(t, d.Command)
	for _, w := range []string{"[ -f '/p/.lea/worktrees/lea/dev-2/docs/pr.md' ] || {", "exec 3< '/p/.lea/worktrees/lea/dev-2/docs/pr.md' || exit 1",
		`r=$('/usr/bin/realpath' '-e' '--' '/proc/self/fd/3') || exit 1`, `'--body-file' '-' '-R' 'github.com/o/lea' <&3`,
		`case "$r" in '/p/.lea/worktrees/lea/dev-2'/.git|'/p/.lea/worktrees/lea/dev-2'/.git/*)`, `[ -f /proc/self/fd/3 ] ||`} {
		if !strings.Contains(s, w) {
			t.Fatalf("body file script lacks %s:\n%s", w, s)
		}
	}
}

// TestStanding: delivery's standing grants never expire and show as such.
func TestStanding(t *testing.T) {
	g := all(PRCreate)
	g.Expires = Standing
	if !g.Active(now) || !g.IsStanding() || all(PRCreate).IsStanding() {
		t.Fatal("standing")
	}
	for _, a := range DeliveryActions {
		if !ValidAction(a) || !NeedsGitHub(a) {
			t.Fatalf("delivery action %q", a)
		}
	}
}

// TestGitLab: a grant pinned to a GitLab project (gitlab:HOST/PATH) covers glab mr create and
// glab mr merge on the caller's own branch, and pushes to that project; gh is not covered by it,
// nor glab by a GitHub grant.
func TestGitLab(t *testing.T) {
	gl := func(action string) Grant {
		g := all(action)
		g.GitHub = "gitlab:gitlab.example.com/grp/sub/app"
		return g
	}
	gs := []Grant{gl(PRCreate), gl(Merge), gl(CommitIdentity), gl(DeleteOwnBranch)}
	cases := []struct{ name, cmd, want string }{
		{"mr create", `glab mr create -s feat/73-x -b main -t "fix: x" -d "does y" -y`, Allow},
		{"mr create long", `glab mr create --source-branch feat/73-x --target-branch main --title x --description y --draft --yes`, Allow},
		{"mr create no description", `glab mr create -s feat/73-x -t x -y`, Allow},
		{"mr create remove source", `glab mr create -s feat/73-x -t x --remove-source-branch -y`, Allow},
		{"mr create no source", `glab mr create -t x -d y -y`, ""},
		{"mr create other source", `glab mr create -s feat/other -t x -y`, ""},
		{"mr create no title", `glab mr create -s feat/73-x -d y -y`, ""},
		{"mr create fill", `glab mr create -s feat/73-x --fill -y`, ""},
		{"mr create push", `glab mr create -s feat/73-x -t x --push -y`, ""},
		{"mr create repo", `glab mr create -s feat/73-x -t x -y -R evil/x`, ""},
		{"mr create head repo", `glab mr create -s feat/73-x -t x -y -H evil/x`, ""},
		{"mr create editor", `glab mr create -s feat/73-x -t x -d - -y`, ""},
		{"mr create body mentions push", `glab mr create -s feat/73-x -t x -d "run git push origin main yourself" -y`, Allow},
		{"mr merge numbered", `glab mr merge 12 --squash -y`, Allow},
		{"mr merge own branch remove", `glab mr merge feat/73-x -s -d -y`, Allow},
		{"mr merge rebase", `glab mr merge 12 -r -y`, ""},
		{"mr merge rebase long", `glab mr merge 12 --rebase -y`, ""},
		{"mr merge numbered remove", `glab mr merge 12 -d -y`, ""},
		{"mr merge unnamed", `glab mr merge -s -y`, ""},
		{"mr merge other branch", `glab mr merge feat/other -s -y`, ""},
		{"mr merge sha", `glab mr merge 12 --sha abc -y`, ""},
		{"mr merge two methods", `glab mr merge 12 -s -r -y`, ""},
		{"push", "git push -u origin feat/73-x", Allow},
		{"delete", "git push origin --delete feat/73-x", Allow},
		{"gh on a gitlab grant", "gh pr merge 42 --squash", ""},
		{"gh create on a gitlab grant", "gh pr create --head feat/73-x --title x --body y", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			d := Decide("Bash", bash(tc.cmd), dev, gs, now, false)
			if d.Verdict != tc.want {
				t.Fatalf("%q: verdict %q (%s), want %q", tc.cmd, d.Verdict, d.Reason, tc.want)
			}
		})
	}
	if d := Decide("Bash", bash("glab mr merge 12 -s -y"), dev, []Grant{all(Merge)}, now, false); d.Verdict != "" {
		t.Fatalf("glab on a github grant: %+v", d)
	}
	// glab only reads the token; curl talks to the project's API (gitlab_test.go runs them).
	d := Decide("Bash", bash(`glab mr create -s feat/73-x -t x -y`), dev, gs, now, false)
	s := lastWord(t, d.Command)
	for _, w := range []string{"cd / || exit 1\n", `'/usr/bin/glab' 'config' 'get' 'token' '--host' 'gitlab.example.com'`,
		"api='https://gitlab.example.com/api/v4/projects/grp%2Fsub%2Fapp'\n", `'/usr/bin/curl' '-q' '--proto' '=https'`,
		`'--data-urlencode' 'source_branch=feat/73-x'`, `'--data-urlencode' 'title=x'`} {
		if !strings.Contains(s, w) {
			t.Fatalf("mr create script lacks %s:\n%s", w, s)
		}
	}
	if d.Request.Target != "gitlab:gitlab.example.com/grp/sub/app mr create feat/73-x" || strings.Contains(s, "-R") || strings.Contains(s, "'mr'") {
		t.Fatalf("mr create: %q %+v", s, d)
	}
	d = Decide("Bash", bash("glab mr merge 12 --squash -y"), dev, gs, now, false)
	s = lastWord(t, d.Command)
	for _, w := range []string{"iid='12'\n", `"$api/merge_requests/$iid"`, `'--arg' 'b' 'feat/73-x'`, "export HOME='/usr/local/share/lea/no-forge-config'\n", `'-X' 'PUT' --data-urlencode "sha=$sha" '--data' 'squash=true' $am "$api/merge_requests/$iid/merge"`} {
		if !strings.Contains(s, w) {
			t.Fatalf("numbered mr merge script lacks %s:\n%s", w, s)
		}
	}
	d = Decide("Bash", bash("git push -u origin feat/73-x"), dev, gs, now, false)
	if s := lastWord(t, d.Command); !strings.Contains(s, ` push 'https://gitlab.example.com/grp/sub/app.git' "$sha"':refs/heads/feat/73-x'`) {
		t.Fatalf("gitlab push script:\n%s", s)
	}
	for pin, ok := range map[string]bool{"github.com/o/x": true, "gitlab:gitlab.com/g/x": true, "gitlab:h.example.com/a/b/c/d": true,
		"gitlab:gitlab.com/x": false, "gitlab:gitlab.com/g/../x": false, "gitlab:/g/x": false, "gitlab:h_x/g/x": false,
		"gitlab:gitlab.com/g/x.git": false, "github.com/o/x/y": false, "gitlab.com/g/x": false, "gitlab:gitlab.com/-g/x": false} {
		if got := ValidPin(pin); got != ok {
			t.Errorf("ValidPin(%q) = %v", pin, got)
		}
	}
}
