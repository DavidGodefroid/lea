package grants

import "strings"

// wild marks, inside a word segments returns, an unquoted character the shell would expand (a
// glob, a brace, a leading ~): such a word names refs the shell chooses, not the agent.
const wild = "\x00"

// segments splits a shell command into its simple commands (cut at ; & | && || newlines and
// parentheses), each as the words its program receives. Redirections (>f, 2>&1, &>f, <f) are
// dropped with their target. ok is false for what it cannot read: an escape, an expansion, a
// heredoc, a process substitution, an unterminated quote.
func segments(cmd string) (segs [][]string, ok bool) {
	var words []string
	var cur strings.Builder
	in := false
	end := func() {
		if in {
			words = append(words, cur.String())
			cur.Reset()
			in = false
		}
	}
	cut := func() {
		end()
		if len(words) > 0 {
			segs = append(segs, words)
			words = nil
		}
	}
	for i := 0; i < len(cmd); i++ {
		ch := cmd[i]
		switch {
		case ch == ' ' || ch == '\t':
			end()
		case ch == '\'' || ch == '"':
			j := strings.IndexByte(cmd[i+1:], ch)
			if j < 0 {
				return nil, false
			}
			s := cmd[i+1 : i+1+j]
			if strings.ContainsAny(s, "\n\r") || (ch == '"' && strings.ContainsAny(s, "$`\\")) {
				return nil, false
			}
			cur.WriteString(s)
			in = true
			i += j + 1
		case ch == ';' || ch == '\n' || ch == '|' || ch == '(' || ch == ')':
			cut()
		case ch == '&' && (i+1 >= len(cmd) || cmd[i+1] != '>'):
			cut()
		case ch == '&' || ch == '<' || ch == '>':
			// The fd number before a redirection is not an argument.
			if in && number(cur.String()) && ch != '&' {
				cur.Reset()
				in = false
			}
			end()
			j := i + 1
			if ch == '&' {
				j++ // &>
			}
			if j < len(cmd) && (cmd[j] == '(' || (ch == '<' && cmd[j] == '<')) {
				return nil, false
			}
			for j < len(cmd) && (cmd[j] == '>' || cmd[j] == '&' || cmd[j] == '|') {
				j++
			}
			for j < len(cmd) && (cmd[j] == ' ' || cmd[j] == '\t') {
				j++
			}
			k := j
			for k < len(cmd) && strings.IndexByte(" \t\n;&|()<>", cmd[k]) < 0 {
				if strings.IndexByte("'\"$`\\", cmd[k]) >= 0 {
					return nil, false
				}
				k++
			}
			if k == j {
				return nil, false
			}
			i = k - 1
		case ch == '#' && !in:
			j := strings.IndexByte(cmd[i:], '\n')
			if j < 0 {
				i = len(cmd)
			} else {
				i += j - 1
			}
		case ch < 0x20 || ch == 0x7f || ch == '$' || ch == '`' || ch == '\\':
			return nil, false
		case strings.IndexByte("*?[]{}", ch) >= 0 || (ch == '~' && !in):
			cur.WriteString(wild)
			cur.WriteByte(ch)
			in = true
		default:
			cur.WriteByte(ch)
			in = true
		}
	}
	cut()
	return segs, true
}

// Reasons of hardPush.
const (
	pushUnread    = "a git push lea cannot read (heredoc, process substitution, odd quoting) is never allowed: run it plainly"
	pushIndirect  = "a git push run through another program, a shell or with variables before it is never allowed: run git push plainly"
	pushGlobals   = "git options before the subcommand other than -C (-c, --config-env, --git-dir…) are never allowed in a command that mentions a push"
	pushWild      = "a git push with an unquoted *, ?, [, {, or ~ is never allowed: the shell would choose the refs"
	pushForce     = "force-push (or a push that rewrites or prunes remote branches) is never allowed"
	pushEvery     = "pushing every branch or tag (--all, --branches, --tags, --mirror, --prune, a wildcard or a bare \":\") is never allowed: name the branch"
	pushMain      = "pushing to the default branch is never allowed"
	pushImplicit  = "a push that does not name its branch (bare git push, HEAD) is allowed only at the root of the agent's own worktree, on its own branch: name it, git push origin <branch>"
	pushDelete    = "deleting a branch that is not the agent's own is never allowed"
	pushDeleteAnd = "deleting a remote branch in a compound command is never allowed"
)

// pushFlags are the git push options that change neither what is pushed nor where.
var pushFlags = map[string]bool{"--set-upstream": true, "--verbose": true, "--quiet": true, "--dry-run": true, "--porcelain": true,
	"--progress": true, "--no-progress": true, "--atomic": true, "--no-atomic": true, "--verify": true, "--no-verify": true,
	"--ipv4": true, "--ipv6": true, "--thin": true, "--no-thin": true, "--follow-tags": true, "--no-follow-tags": true}

// gitGlobals are git's own options a push may carry (-C takes a directory).
var gitGlobals = map[string]bool{"--no-pager": true, "-P": true, "--no-replace-objects": true, "--no-optional-locks": true}

// hardPush returns why a command holding a git push is a hard deny, or "". It parses every
// simple command and every refspec: what it cannot read is denied, never guessed. It runs on
// every command that names a push. An alias the repository's config
// already maps to push is out of its reach (writing config is a hard deny of its own): branch
// protection on the remote remains the control that holds.
func hardPush(raw string, c Caller) string {
	segs, ok := segments(raw)
	if !ok {
		return pushUnread
	}
	moved := false // a cd makes "here" another repository
	for _, w := range segs {
		moved = moved || w[0] == "cd" || w[0] == "pushd" || w[0] == "popd"
	}
	for _, w := range segs {
		if why := segmentPush(w, c, len(segs) > 1, moved); why != "" {
			return why
		}
	}
	return ""
}

func isGit(w string) bool { return w == "git" || strings.HasSuffix(w, "/git") }

// segmentPush checks one simple command.
func segmentPush(w []string, c Caller, compound, moved bool) string {
	if !isGit(w[0]) {
		if strings.Contains(w[0], wild) && contains(w, "push") {
			return pushWild // the shell picks the program: maybe git
		}
		for k, x := range w {
			// git run by another program (xargs, env, find…) gets its words from it, maybe
			// "push"; a word holding a whole git push is a script some interpreter may run.
			if k > 0 && (isGit(x) || pushRe.MatchString(x)) {
				return pushIndirect
			}
		}
		return ""
	}
	here := !moved
	j := 1
	for j < len(w) {
		switch {
		case w[j] == "-C" && j+1 < len(w):
			here = false
			j += 2
			continue
		case gitGlobals[w[j]]:
			j++
			continue
		case strings.HasPrefix(w[j], "-"):
			// -c, --config-env, --git-dir…: an alias or a config can make any subcommand a push.
			return pushGlobals
		}
		break
	}
	if j >= len(w) {
		return ""
	}
	if w[j] != "push" {
		// Another subcommand that pushes or runs commands (subtree push, send-pack, submodule
		// foreach git push…): any word naming a push, but a commit's message, is denied.
		for k := j + 1; k < len(w); k++ {
			if w[j] == "commit" && (w[k] == "-m" || w[k] == "--message") {
				k++
				continue
			}
			if pushWordRe.MatchString(w[k]) {
				return pushIndirect
			}
		}
		return ""
	}
	return parsePush(w[j+1:], c, here, compound)
}

// branchOf is the branch a push destination names on the remote: git reads "heads/x" as
// refs/heads/x.
func branchOf(dst string) string {
	if b, ok := strings.CutPrefix(dst, "refs/heads/"); ok {
		return b
	}
	return strings.TrimPrefix(dst, "heads/")
}

// parsePush checks git push's arguments. here says git runs at the root of the caller's own
// worktree, where HEAD is the caller's branch: only there may a push leave its branch implicit.
func parsePush(args []string, c Caller, here, compound bool) string {
	own := c.Branch != "" && BranchName(c.Branch) && !Protected(c.Branch)
	here = here && own && c.OwnWorktree
	var pos []string
	del, opts := false, true
	for i := 0; i < len(args); i++ {
		a := args[i]
		if strings.Contains(a, wild) {
			return pushWild
		}
		if !opts || a == "-" || !strings.HasPrefix(a, "-") {
			pos = append(pos, a)
			continue
		}
		switch {
		case a == "--":
			opts = false
		case a == "--delete":
			del = true
		case a == "-o" || a == "--push-option":
			if i+1 >= len(args) {
				return "a git push option lea does not know (" + a + ") is never allowed"
			}
			i++
		case strings.HasPrefix(a, "--push-option="), pushFlags[a]:
		case strings.HasPrefix(a, "--force"):
			return pushForce
		case a == "--all" || a == "--branches" || a == "--mirror" || a == "--prune" || a == "--tags":
			return pushEvery
		case !strings.HasPrefix(a, "--"):
			for k := 1; k < len(a); k++ {
				switch a[k] {
				case 'f':
					return pushForce
				case 'd':
					del = true
				case 'u', 'v', 'q', 'n', '4', '6':
				case 'o':
					if k == len(a)-1 {
						if i+1 >= len(args) {
							return "a git push option lea does not know (" + a + ") is never allowed"
						}
						i++
					}
					k = len(a)
				default:
					return "a git push option lea does not know (" + a + ") is never allowed"
				}
			}
		default:
			return "a git push option lea does not know (" + a + ") is never allowed"
		}
	}
	if len(pos) < 2 {
		if del {
			return pushDelete
		}
		if !here {
			return pushImplicit
		}
		return ""
	}
	deletes := false
	for _, r := range pos[1:] {
		if del {
			if !own || r != c.Branch {
				return pushDelete
			}
			deletes = true
			continue
		}
		if strings.HasPrefix(r, "+") {
			return pushForce
		}
		if strings.Contains(r, "*") {
			return pushEvery
		}
		src, dst, colon := strings.Cut(r, ":")
		switch {
		case colon && strings.Contains(dst, ":"):
			return "a refspec lea cannot read (" + r + ") is never allowed"
		case colon && src == "" && dst == "":
			return pushEvery
		case colon && src == "":
			if !own || dst != c.Branch {
				return pushDelete
			}
			deletes = true
			continue
		case colon && dst == "":
			return "a refspec lea cannot read (" + r + ") is never allowed"
		case !colon && (src == "HEAD" || src == "@"):
			if !here {
				return pushImplicit
			}
			continue
		case !colon:
			dst = src
		}
		if strings.HasPrefix(dst, "refs/") && !strings.HasPrefix(dst, "refs/heads/") {
			continue // a tag or another namespace: no branch
		}
		if Protected(branchOf(dst)) {
			return pushMain
		}
	}
	if deletes && compound {
		return pushDeleteAnd
	}
	return ""
}
