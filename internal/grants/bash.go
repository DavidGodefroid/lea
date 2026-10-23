package grants

import (
	"regexp"
	"strings"
)

// Words splits a shell command into its words, or ok false when the command does anything but run
// one plain program with literal arguments: no chaining (; & | newline), no redirection, no
// expansion ($ ` ~ globs), no subshell, no escape. Single quotes keep anything but a newline;
// double quotes keep anything but $ ` \ ! and a newline. The result is what the program receives
// as argv, so matching on it never confuses a quoted word with an operator.
func Words(cmd string) ([]string, bool) {
	var words []string
	var cur strings.Builder
	in := false // inside a word
	for i := 0; i < len(cmd); i++ {
		ch := cmd[i]
		switch {
		case ch == ' ' || ch == '\t':
			if in {
				words = append(words, cur.String())
				cur.Reset()
				in = false
			}
		case ch == '\'':
			j := strings.IndexByte(cmd[i+1:], '\'')
			if j < 0 {
				return nil, false
			}
			s := cmd[i+1 : i+1+j]
			if strings.ContainsAny(s, "\n\r") {
				return nil, false
			}
			cur.WriteString(s)
			in = true
			i += j + 1
		case ch == '"':
			j := strings.IndexByte(cmd[i+1:], '"')
			if j < 0 {
				return nil, false
			}
			s := cmd[i+1 : i+1+j]
			if strings.ContainsAny(s, "$`\\!\n\r") {
				return nil, false
			}
			cur.WriteString(s)
			in = true
			i += j + 1
		case ch < 0x20 || ch == 0x7f || strings.IndexByte(";&|<>$`\\(){}*?[]~#!=", ch) >= 0:
			// '=' rejects VAR=value prefixes and --flag=value forms alike: both have a plain
			// two-word spelling the matchers accept.
			return nil, false
		default:
			cur.WriteByte(ch)
			in = true
		}
	}
	if in {
		words = append(words, cur.String())
	}
	if len(words) == 0 {
		return nil, false
	}
	return words, true
}

var (
	adminRe  = regexp.MustCompile(`\bgh\b[\s\S]*\bmerge\b[\s\S]*--admin\b`)
	pushRe   = regexp.MustCompile(`\bgit\b[\s\S]*\bpush\b`)
	forceRe  = regexp.MustCompile(`\s(--force(-with-lease|-if-includes)?|--mirror|--prune|-[A-Za-z]*f[A-Za-z]*)(\s|=|$)|\s\+\S`)
	toMainRe = regexp.MustCompile(`[\s:](refs/heads/)?(main|master)(\s|$|[;&|)])`)
	deleteRe = regexp.MustCompile(`\s(--delete|-[A-Za-z]*d[A-Za-z]*)(\s|=|$)|\s:\S`)
	// gitSub matches a git subcommand after git's own options (git -C x -c y=z config ...).
	gitConfigRe  = regexp.MustCompile(`\bgit(\s+-\S+(\s+[^-\s]\S*)?)*\s+config\b`)
	gitRemoteRe  = regexp.MustCompile(`\bgit(\s+-\S+(\s+[^-\s]\S*)?)*\s+remote\s+(set-url|add|rename|remove|rm|set-head|set-branches)\b`)
	ghDefaultRe  = regexp.MustCompile(`\bgh\s+repo\s+set-default\b`)
	ghEnvRe      = regexp.MustCompile(`\bGH_(REPO|HOST|ENTERPRISE_TOKEN|CONFIG_DIR)\s*=`)
	obfuscatedRe = regexp.MustCompile("[\\\\$`]")
	// sensitiveRe names what a hard deny protects, loosely: a command that mentions it and hides
	// words behind escapes or expansions is denied, whatever it does.
	sensitiveRe = regexp.MustCompile(`\.claude|\.git\b|gitconfig|\.(bash|zsh|profile)|lea/state|share/lea|\.config/(git|lea)|` +
		`\bgit\b[\s\S]*\b(push|config|remote)\b|\bgh\b[\s\S]*\b(repo|merge|--admin)\b|GH_`)
	readOnlyOn = map[string]bool{"cat": true, "head": true, "tail": true, "less": true, "jq": true, "grep": true, "ls": true, "wc": true, "diff": true, "stat": true}
	unquote    = strings.NewReplacer(`"`, "", `'`, "", `\`, "")
)

// hardBash returns why a shell command is a hard deny, or "". It looks at the raw text with its
// quotes removed, so it holds for chained and quoted commands the parser rejects.
func hardBash(cmd string, c Caller) string {
	raw := cmd
	cmd = unquote.Replace(cmd)
	if _, ok := Words(raw); !ok && obfuscatedRe.MatchString(raw) && sensitiveRe.MatchString(cmd) {
		return "a command with escapes or expansions that touches git, gh or protected files is never allowed: run it plainly"
	}
	if adminRe.MatchString(cmd) {
		return "gh pr merge --admin bypasses branch protection"
	}
	if protectedPathRe.MatchString(cmd) {
		if w, ok := Words(raw); !ok || !readOnlyOn[w[0]] {
			return "editing Claude's, git's, the shell's or lea's own settings from inside an agent is never allowed"
		}
	}
	if gitConfigRe.MatchString(cmd) && !gitConfigRead(raw) {
		return "changing git's configuration from inside an agent is never allowed"
	}
	if gitRemoteRe.MatchString(cmd) || (ghDefaultRe.MatchString(cmd) && !strings.Contains(cmd, "--view")) {
		return "changing where git or gh push and merge is never allowed"
	}
	if ghEnvRe.MatchString(cmd) {
		return "pointing gh at another repository or host is never allowed"
	}
	if !pushRe.MatchString(cmd) {
		return ""
	}
	if forceRe.MatchString(cmd) {
		return "force-push (or a push that rewrites or prunes remote branches) is never allowed"
	}
	if toMainRe.MatchString(cmd) {
		return "pushing to the default branch is never allowed"
	}
	if deleteRe.MatchString(cmd) {
		w, ok := Words(raw)
		if !ok {
			return "deleting a remote branch in a compound command is never allowed"
		}
		for _, b := range pushDeletes(w) {
			if c.Branch == "" || b != c.Branch || Protected(b) {
				return "deleting a branch that is not the agent's own is never allowed"
			}
		}
	}
	return ""
}

// gitConfigRead says whether cmd is one plain "git config" that only reads: --get, --get-all or
// --get-regexp with exactly one key, or --list/-l with none, plus scope and format modifiers.
func gitConfigRead(cmd string) bool {
	w, ok := Words(cmd)
	if !ok || len(w) < 3 || w[0] != "git" || w[1] != "config" {
		return false
	}
	action, pos := "", 0
	for _, a := range w[2:] {
		switch a {
		case "--get", "--get-all", "--get-regexp", "--list", "-l":
			if action != "" {
				return false
			}
			action = a
		case "--show-origin", "--show-scope", "--name-only", "--null", "-z", "--local", "--global", "--system", "--worktree":
		default:
			if strings.HasPrefix(a, "-") {
				return false
			}
			pos++
		}
	}
	switch action {
	case "--get", "--get-all", "--get-regexp":
		return pos == 1
	case "--list", "-l":
		return pos == 0
	}
	return false
}

// pushDeletes returns the branches a git push deletes ("git push origin --delete b", ":b").
func pushDeletes(w []string) []string {
	if len(w) < 2 || w[0] != "git" || w[1] != "push" {
		return []string{""} // a push we cannot read deletes something unknown: never own
	}
	var pos, out []string
	del := false
	for _, x := range w[2:] {
		switch {
		case x == "--delete" || x == "-d":
			del = true
		case strings.HasPrefix(x, "-"):
			if strings.Contains(strings.TrimLeft(x, "-"), "d") && !strings.HasPrefix(x, "--") {
				del = true
			}
		default:
			pos = append(pos, x)
		}
	}
	for i, p := range pos {
		if strings.HasPrefix(p, ":") {
			out = append(out, p[1:])
		} else if del && i > 0 {
			out = append(out, p)
		}
	}
	if del && len(pos) < 2 {
		out = append(out, "")
	}
	return out
}

// bashRequest recognises a command a grant can cover. Every word must be one the matcher knows:
// an unknown flag means no grant applies.
func bashRequest(w []string, c Caller) (Request, bool) {
	// gh and git act on the repository of the directory they run in: only the caller's own
	// worktree binds them to the caller's repo and branch.
	if !c.OwnWorktree {
		return Request{}, false
	}
	var r Request
	ok := false
	switch {
	case len(w) >= 3 && w[0] == "gh" && w[1] == "pr" && w[2] == "merge":
		r, ok = ghMerge(w[3:], c)
	case len(w) >= 3 && w[0] == "gh" && w[1] == "issue":
		r, ok = ghIssue(w[2:])
	case len(w) >= 2 && w[0] == "git" && w[1] == "commit":
		r, ok = gitCommit(w[2:])
	case len(w) >= 2 && w[0] == "git" && w[1] == "push":
		r, ok = gitPush(w[2:], c)
	}
	if !ok {
		return Request{}, false
	}
	r.Prog, r.Args = w[0], w[1:]
	return r, true
}

func number(s string) bool {
	if s == "" || len(s) > 9 {
		return false
	}
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return false
		}
	}
	return true
}

// ghMerge: gh pr merge <number>|<own branch> [--squash|--merge|--rebase] [--delete-branch].
// No --repo from the agent: lea pins it to the grant's repository (Command). The PR is named:
// with -R, gh has no worktree branch to fall back on. --delete-branch deletes the PR's head
// branch: it is accepted only when the PR is named by the caller's own branch (not a number),
// never a protected one.
func ghMerge(args []string, c Caller) (Request, bool) {
	target := ""
	method, del, own := false, false, false
	for i, a := range args {
		switch a {
		case "--squash", "-s", "--merge", "-m", "--rebase", "-r":
			if method {
				return Request{}, false
			}
			method = true
		case "--delete-branch", "-d":
			del = true
		default:
			switch {
			case i != 0:
				return Request{}, false
			case number(a):
				target = "pr " + a
			case c.Branch != "" && a == c.Branch && !Protected(a):
				target, own = "pr of "+a, true
			default:
				return Request{}, false
			}
		}
	}
	if target == "" || (del && !own) {
		return Request{}, false
	}
	return Request{Action: Merge, Target: target}, true
}

// flagArgs checks args: one positional issue/PR number first when want, then flags each from
// valued (takes one argument) or bare.
func flagArgs(args []string, want bool, valued, bare map[string]bool) (string, bool) {
	n := ""
	if want {
		if len(args) == 0 || !number(args[0]) {
			return "", false
		}
		n, args = args[0], args[1:]
	}
	for i := 0; i < len(args); i++ {
		switch {
		case valued[args[i]] && i+1 < len(args):
			i++
		case bare[args[i]]:
		default:
			return "", false
		}
	}
	return n, true
}

func ghIssue(args []string) (Request, bool) {
	var valued map[string]bool
	switch args[0] {
	case "close":
		valued = map[string]bool{"--comment": true, "-c": true, "--reason": true, "-r": true}
	case "reopen":
		valued = map[string]bool{"--comment": true, "-c": true}
	case "comment":
		valued = map[string]bool{"--body": true, "-b": true}
	default:
		return Request{}, false
	}
	n, ok := flagArgs(args[1:], true, valued, nil)
	if !ok {
		return Request{}, false
	}
	return Request{Action: CloseIssue, Target: args[0] + " issue " + n}, true
}

// gitCommit: git commit with a message and the usual flags; no pathspec, no -c/--author/-F, no
// --amend (it rewrites a commit that may be pushed already, or someone else's).
func gitCommit(args []string) (Request, bool) {
	valued := map[string]bool{"-m": true, "--message": true}
	bare := map[string]bool{"-a": true, "--all": true, "-s": true, "--signoff": true, "-q": true, "--quiet": true}
	hasMsg := false
	for _, a := range args {
		hasMsg = hasMsg || valued[a]
	}
	if _, ok := flagArgs(args, false, valued, bare); !ok || !hasMsg {
		return Request{}, false
	}
	return Request{Action: CommitIdentity, Target: "commit"}, true
}

// gitPush: "git push [-u|--set-upstream] origin <own branch>" (commit-identity) or
// "git push origin --delete <own branch>" (delete-own-branch). The branch must be named: a bare
// "git push" pushes whatever the current directory has checked out, which lea does not know.
func gitPush(args []string, c Caller) (Request, bool) {
	var pos []string
	del := false
	for _, a := range args {
		switch a {
		case "-u", "--set-upstream":
		case "--delete", "-d":
			del = true
		default:
			if strings.HasPrefix(a, "-") || strings.ContainsAny(a, ":+") {
				return Request{}, false
			}
			pos = append(pos, a)
		}
	}
	if len(pos) != 2 || pos[0] != "origin" || c.Branch == "" || pos[1] != c.Branch || Protected(pos[1]) {
		return Request{}, false
	}
	if del {
		return Request{Action: DeleteOwnBranch, Target: pos[1]}, true
	}
	return Request{Action: CommitIdentity, Target: "push " + pos[1]}, true
}
