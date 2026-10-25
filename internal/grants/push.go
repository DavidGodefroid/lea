package grants

import (
	"path"
	"strings"
)

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

// Reasons of hardGit.
const (
	pushUnread    = "a git push lea cannot read (heredoc, process substitution, odd quoting) is never allowed: run it plainly"
	pushIndirect  = "a git push run through another program, a shell or with variables before it is never allowed: run git push plainly"
	pushTransport = "git transports other than push (send-pack, http-push, remote-<helper>, svn, p4) are never allowed"
	pushWild      = "a git push with an unquoted *, ?, [, {, or ~ is never allowed: the shell would choose the refs"
	pushForce     = "force-push (or a push that rewrites or prunes remote branches) is never allowed"
	pushEvery     = "pushing every branch or tag (--all, --branches, --tags, --mirror, --prune, a wildcard or a bare \":\") is never allowed: name the branch"
	pushMain      = "pushing to the default branch is never allowed"
	pushImplicit  = "a push must name the agent's branch (git push origin <branch>): a bare git push, HEAD or @ follows whatever is checked out"
	pushOther     = "pushing to anything but the agent's own branch is never allowed"
	pushDelete    = "deleting a branch that is not the agent's own is never allowed"
	pushDeleteAnd = "deleting a remote branch in a compound command is never allowed"
	gitGlobal     = "git options before the subcommand other than -C, -c user.name|user.email|color.ui|core.quotepath and display options are never allowed: " +
		"a configuration, an alias or another repository could make any subcommand a push"
	gitEnv = "setting GIT_*, HOME or XDG_CONFIG_HOME is never allowed: git would read another configuration, repository or program"
)

// pushFlags are the git push options that change neither what is pushed nor where.
var pushFlags = map[string]bool{"--set-upstream": true, "--verbose": true, "--quiet": true, "--dry-run": true, "--porcelain": true,
	"--progress": true, "--no-progress": true, "--atomic": true, "--no-atomic": true, "--verify": true, "--no-verify": true,
	"--ipv4": true, "--ipv6": true, "--thin": true, "--no-thin": true, "--follow-tags": true, "--no-follow-tags": true}

// safeConfig are the keys "git -c" may set from inside an agent: none runs a program, includes
// a file or defines an alias.
var safeConfig = map[string]bool{"user.name": true, "user.email": true, "color.ui": true, "core.quotepath": true}

// gitFlags are git's own options that take no value and change neither its configuration nor
// its repository.
var gitFlags = map[string]bool{"--no-pager": true, "-P": true, "-p": true, "--paginate": true, "--no-replace-objects": true,
	"--no-optional-locks": true, "--literal-pathspecs": true, "--glob-pathspecs": true, "--noglob-pathspecs": true,
	"--icase-pathspecs": true, "--no-lazy-fetch": true, "--version": true, "--help": true, "-v": true, "-h": true}

// runners run their arguments as a command or a script: a word of theirs that holds a git push
// is a push lea cannot read.
var runners = map[string]bool{"sh": true, "bash": true, "dash": true, "zsh": true, "ksh": true, "fish": true, "eval": true,
	"exec": true, "command": true, "builtin": true, "su": true, "sudo": true, "doas": true, "ssh": true, "script": true,
	"perl": true, "ruby": true, "node": true, "nodejs": true, "php": true, "lua": true, "tclsh": true, "awk": true, "gawk": true,
	"busybox": true, "watch": true, "timeout": true, "nice": true, "nohup": true, "env": true, "time": true, "stdbuf": true,
	"setsid": true, "flock": true, "xargs": true, "parallel": true, "find": true, "make": true, "if": true, "then": true,
	"else": true, "do": true, "while": true, "until": true, "!": true}

// runsCommands are git subcommands that run commands given as arguments.
var runsCommands = map[string]bool{"submodule": true, "rebase": true, "bisect": true, "difftool": true, "mergetool": true,
	"filter-branch": true, "subtree": true, "worktree": true, "notes": true}

// declarers set or read the variables named in their arguments.
var declarers = map[string]bool{"export": true, "declare": true, "typeset": true, "local": true, "readonly": true, "read": true,
	"printf": true, "mapfile": true, "readarray": true, "getopts": true, "env": true, "let": true}

// gitVar says whether w names (or assigns) a variable that changes what git reads or runs.
func gitVar(w string) bool {
	name, _, _ := strings.Cut(w, "=")
	return strings.HasPrefix(name, "GIT_") || name == "HOME" || name == "XDG_CONFIG_HOME"
}

func isAssignment(w string) bool {
	name, _, ok := strings.Cut(w, "=")
	if !ok || name == "" || (name[0] >= '0' && name[0] <= '9') {
		return false
	}
	for i := 0; i < len(name); i++ {
		if c := name[i]; c != '_' && (c < 'A' || c > 'Z') && (c < 'a' || c > 'z') && (c < '0' || c > '9') {
			return false
		}
	}
	return true
}

func isGit(w string) bool { return w == "git" || strings.HasSuffix(w, "/git") }

func interpreter(w string) bool {
	b := path.Base(w)
	return runners[b] || strings.HasPrefix(b, "python") || strings.HasPrefix(b, "perl") || strings.HasPrefix(b, "ruby")
}

// hardGit returns why a command is a hard deny for what it makes git read or push, or "". It
// parses every simple command, git's own options and every refspec: what it cannot read is
// denied, never guessed. Out of its reach: an alias the repository's config already holds and a
// script file that pushes (writing config is a hard deny of its own); branch protection on the
// remote remains the control that holds.
func hardGit(raw, cmd string, c Caller) string {
	segs, ok := segments(raw)
	if !ok {
		switch {
		case gitEnvRe.MatchString(cmd):
			return gitEnv
		case gitOptRe.MatchString(cmd):
			return gitGlobal
		case pushWordRe.MatchString(cmd):
			return pushUnread
		}
		return ""
	}
	for _, w := range segs {
		if why := segmentGit(w, c, len(segs) > 1); why != "" {
			return why
		}
	}
	return ""
}

// segmentGit checks one simple command.
func segmentGit(w []string, c Caller, compound bool) string {
	if declarers[path.Base(w[0])] {
		for _, x := range w[1:] {
			if gitVar(x) {
				return gitEnv
			}
		}
	}
	p := 0 // the program, after VAR=value prefixes
	for p < len(w) && isAssignment(w[p]) {
		if gitVar(w[p]) {
			return gitEnv
		}
		p++
	}
	if p == len(w) {
		return ""
	}
	if !isGit(w[p]) {
		named := false
		for _, x := range w[p:] {
			named = named || pushWordRe.MatchString(x)
		}
		if strings.Contains(w[p], wild) && named {
			return pushWild // the shell picks the program: maybe git
		}
		for k := p + 1; k < len(w); k++ {
			// git run by another program (xargs, env, find…) gets its words from it, maybe
			// "push"; a word holding a whole git push is a script an interpreter may run.
			if isGit(w[k]) {
				if why := gitArgs(w[k:], c, compound); why != "" {
					return why
				}
				// xargs and parallel append words from stdin: maybe the subcommand.
				if named || (k == len(w)-1 && (path.Base(w[p]) == "xargs" || path.Base(w[p]) == "parallel")) {
					return pushIndirect
				}
			}
			if interpreter(w[p]) && pushWordRe.MatchString(w[k]) && strings.Contains(w[k], "git") {
				return pushIndirect
			}
		}
		return ""
	}
	if why := gitArgs(w[p:], c, compound); why != "" {
		return why
	}
	if p > 0 && pushWordRe.MatchString(strings.Join(w[p:], " ")) {
		return pushIndirect
	}
	return ""
}

// gitArgs checks one git call, w[0] being git: its own options, then its subcommand.
func gitArgs(w []string, c Caller, compound bool) string {
	j := 1
	for ; j < len(w) && strings.HasPrefix(w[j], "-"); j++ {
		switch {
		case w[j] == "-C" && j+1 < len(w):
			j++
		case w[j] == "-c" && j+1 < len(w):
			key, _, _ := strings.Cut(w[j+1], "=")
			if !safeConfig[strings.ToLower(key)] {
				return gitGlobal
			}
			j++
		case gitFlags[w[j]]:
		default:
			return gitGlobal
		}
	}
	if j >= len(w) {
		return ""
	}
	sub := w[j]
	switch {
	case sub == "push":
		return parsePush(w[j+1:], c, compound)
	case sub == "send-pack" || sub == "http-push" || strings.HasPrefix(sub, "remote-") || sub == "svn" || sub == "p4":
		return pushTransport
	case runsCommands[sub]:
		for _, x := range w[j+1:] {
			if pushWordRe.MatchString(x) {
				return pushIndirect
			}
		}
	}
	return ""
}

// branchOf is the branch a push destination names on the remote: git reads "heads/x" as
// refs/heads/x.
func branchOf(dst string) string {
	if b, ok := strings.CutPrefix(dst, "refs/heads/"); ok {
		return b
	}
	return strings.TrimPrefix(dst, "heads/")
}

// parsePush checks git push's arguments: every destination must be the caller's own branch,
// named (what HEAD is at push time, lea cannot know).
func parsePush(args []string, c Caller, compound bool) string {
	own := c.Branch != "" && BranchName(c.Branch) && !Protected(c.Branch)
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
		return pushImplicit
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
			return pushImplicit
		case !colon:
			dst = src
		}
		if strings.HasPrefix(dst, "refs/") && !strings.HasPrefix(dst, "refs/heads/") {
			return pushOther
		}
		b := branchOf(dst)
		if Protected(b) {
			return pushMain
		}
		if !own || b != c.Branch {
			return pushOther
		}
	}
	if deletes && compound {
		return pushDeleteAnd
	}
	return ""
}
