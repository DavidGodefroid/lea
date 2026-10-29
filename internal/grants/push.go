package grants

import (
	"path"
	"regexp"
	"strings"
)

// wild marks, inside a word segments returns, an unquoted character the shell would expand (a
// glob, a brace, a leading ~): such a word names refs the shell chooses, not the agent.
const wild = "\x00"

// expanded marks, inside a word segments returns, a shell expansion ($x, ${x}, $(…), `…`, $'…'):
// that part of the word is the shell's to compute, not the agent's to name.
const expanded = "\x01"

// segments splits a shell command into its simple commands (cut at ; & | && || newlines and
// parentheses), each as the words its program receives. Quotes and escapes are resolved; an
// expansion is kept as the expanded mark, and the command a substitution ($(…), `…`) runs is
// returned in inners, to be checked on its own. Redirections (>f, 2>&1, &>f, <f) are dropped
// with their target. ok is false for what it cannot read: a heredoc, a process substitution, an
// unterminated quote or substitution.
func segments(cmd string) (segs [][]string, inners []string, ok bool) {
	segs, _, inners, ok = split(cmd)
	return segs, inners, ok
}

// redir is a redirection's target, in the simple command segs[seg] of split (or one after it,
// when the redirection stands alone).
type redir struct {
	seg    int
	target string
}

// split is segments, plus the redirections' targets.
func split(cmd string) (segs [][]string, redirs []redir, inners []string, ok bool) {
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
	// expand reads the expansion at cmd[i] into cur; it returns where it ends.
	expand := func(i int) (int, bool) {
		e, inner, ok := expansion(cmd, i)
		if !ok {
			return 0, false
		}
		if e < 0 { // a lone $: itself
			cur.WriteByte('$')
			return i, true
		}
		cur.WriteString(expanded)
		if inner != "" {
			inners = append(inners, inner)
		}
		return e, true
	}
	for i := 0; i < len(cmd); i++ {
		ch := cmd[i]
		switch {
		case ch == ' ' || ch == '\t':
			end()
		case ch == '\'':
			j := strings.IndexByte(cmd[i+1:], '\'')
			if j < 0 {
				return nil, nil, nil, false
			}
			cur.WriteString(cmd[i+1 : i+1+j])
			in = true
			i += j + 1
		case ch == '"':
			for i++; i < len(cmd) && cmd[i] != '"'; i++ {
				switch c := cmd[i]; {
				case c == '\\' && i+1 < len(cmd) && strings.IndexByte("$`\"\\\n", cmd[i+1]) >= 0:
					i++
					if cmd[i] != '\n' {
						cur.WriteByte(cmd[i])
					}
				case c == '$' && i+1 < len(cmd) && (cmd[i+1] == '"' || cmd[i+1] == '\''):
					cur.WriteByte('$') // inside double quotes, $" and $' are a plain $
				case c == '$' || c == '`':
					e, ok := expand(i)
					if !ok {
						return nil, nil, nil, false
					}
					i = e
				default:
					cur.WriteByte(c)
				}
			}
			if i >= len(cmd) {
				return nil, nil, nil, false
			}
			in = true
		case ch == '\\':
			if i+1 >= len(cmd) {
				return nil, nil, nil, false
			}
			i++
			if cmd[i] != '\n' { // \newline: a line continuation
				cur.WriteByte(cmd[i])
				in = true
			}
		case ch == '$' && i+1 < len(cmd) && cmd[i+1] == '"':
			in = true // $"…" is "…" (a translated string): read by the double-quote case
		case ch == '$' || ch == '`':
			e, ok := expand(i)
			if !ok {
				return nil, nil, nil, false
			}
			i = e
			in = true
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
				return nil, nil, nil, false
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
					return nil, nil, nil, false
				}
				k++
			}
			if k == j {
				return nil, nil, nil, false
			}
			redirs = append(redirs, redir{len(segs), cmd[j:k]})
			i = k - 1
		case ch == '#' && !in:
			j := strings.IndexByte(cmd[i:], '\n')
			if j < 0 {
				i = len(cmd)
			} else {
				i += j - 1
			}
		case ch < 0x20 || ch == 0x7f:
			return nil, nil, nil, false
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
	return segs, redirs, inners, true
}

// expansion reads the shell expansion starting at cmd[i] ($ or `): end is its last byte (-1 for
// a lone $, which is itself) and inner the command it runs, if any.
func expansion(cmd string, i int) (end int, inner string, ok bool) {
	if cmd[i] == '`' {
		// The command a backtick runs is its body with \`, \$ and \\ unescaped: a nested
		// substitution is written \`…\`.
		var body strings.Builder
		for j := i + 1; j < len(cmd); j++ {
			switch c := cmd[j]; {
			case c == '\\' && j+1 < len(cmd) && cmd[j+1] == '"':
				return 0, "", false // \" is " inside double quotes only: not read, denied by the caller
			case c == '\\' && j+1 < len(cmd) && strings.IndexByte("`$\\", cmd[j+1]) >= 0:
				j++
				body.WriteByte(cmd[j])
			case c == '`':
				return j, body.String(), true
			default:
				body.WriteByte(c)
			}
		}
		return 0, "", false
	}
	if i+1 >= len(cmd) {
		return -1, "", true
	}
	switch n := cmd[i+1]; {
	case n == '(' || n == '{':
		close := byte(')')
		if n == '{' {
			close = '}'
		}
		e, ok := closing(cmd, i+2, n, close)
		if !ok {
			return 0, "", false
		}
		return e, cmd[i+2 : e], true
	case n == '\'':
		for j := i + 2; j < len(cmd); j++ {
			switch cmd[j] {
			case '\\':
				j++
			case n:
				return j, "", true
			}
		}
		return 0, "", false
	case n == '_' || (n >= 'A' && n <= 'Z') || (n >= 'a' && n <= 'z'):
		j := i + 1
		for j+1 < len(cmd) && (cmd[j+1] == '_' || (cmd[j+1] >= 'A' && cmd[j+1] <= 'Z') || (cmd[j+1] >= 'a' && cmd[j+1] <= 'z') || (cmd[j+1] >= '0' && cmd[j+1] <= '9')) {
			j++
		}
		return j, "", true
	case (n >= '0' && n <= '9') || strings.IndexByte("@*#?$!-", n) >= 0:
		return i + 1, "", true
	}
	return -1, "", true
}

// closing finds the byte that closes an open bracket before j, skipping quotes and escapes.
func closing(cmd string, j int, open, close byte) (int, bool) {
	depth := 1
	for ; j < len(cmd); j++ {
		switch c := cmd[j]; {
		case c == '\\':
			j++
		case c == '\'':
			k := strings.IndexByte(cmd[j+1:], '\'')
			if k < 0 {
				return 0, false
			}
			j += k + 1
		case c == '"':
			for j++; j < len(cmd) && cmd[j] != '"'; j++ {
				if cmd[j] == '\\' {
					j++
				}
			}
			if j >= len(cmd) {
				return 0, false
			}
		case c == open:
			depth++
		case c == close:
			depth--
			if depth == 0 {
				return j, true
			}
		}
	}
	return 0, false
}

// Reasons of hardGit.
const (
	pushUnread      = "a git push lea cannot read (heredoc, process substitution, odd quoting) is never allowed: run it plainly"
	pushIndirect    = "a git push run through another program, a shell or with variables before it is never allowed: run git push plainly"
	pushTransport   = "git transports other than push (send-pack, http-push, remote-<helper>, svn, p4) are never allowed"
	pushWild        = "a git push with an unquoted *, ?, [, {, or ~ is never allowed: the shell would choose the refs"
	pushForce       = "force-push (or a push that rewrites or prunes remote branches) is never allowed"
	pushEvery       = "pushing every branch or tag (--all, --branches, --tags, --follow-tags, --mirror, --prune, a wildcard or a bare \":\") is never allowed: name the branch"
	pushMain        = "pushing to the default branch is never allowed"
	pushImplicit    = "a push must name the agent's branch (git push origin <branch>): a bare git push, HEAD or @ follows whatever is checked out"
	pushOther       = "pushing to anything but the agent's own branch is never allowed"
	pushUnqualified = "a push refspec must name the branch in full (git push origin refs/heads/<branch> or <src>:refs/heads/<branch>): git makes an unqualified destination a tag when the source is one"
	pushDelete      = "deleting a branch that is not the agent's own is never allowed"
	pushDeleteAnd   = "deleting a remote branch in a compound command is never allowed"
	gitGlobal       = "git options before the subcommand other than -C, -c user.name|user.email|color.ui|core.quotepath and display options are never allowed: " +
		"a configuration, an alias or another repository could make any subcommand a push"
	gitBuilt    = "git run by xargs, parallel, find (or with no subcommand by another program) is never allowed: they build its words, lea cannot read them"
	gitComputed = "a git program, subcommand or push argument computed by the shell ($…, `…`) is never allowed: write it out"
	gitEnv      = "setting GIT_* (but GIT_EDITOR/GIT_SEQUENCE_EDITOR=true|:, GIT_PAGER=cat, GIT_TERMINAL_PROMPT=0), HOME or XDG_CONFIG_HOME is never allowed: git would read another configuration, repository or program"
)

// pushFlags are the git push options that change neither what is pushed nor where.
var pushFlags = map[string]bool{"--set-upstream": true, "--verbose": true, "--quiet": true, "--dry-run": true, "--porcelain": true,
	"--progress": true, "--no-progress": true, "--atomic": true, "--no-atomic": true, "--verify": true, "--no-verify": true,
	"--ipv4": true, "--ipv6": true, "--thin": true, "--no-thin": true, "--no-follow-tags": true}

// safeConfig are the keys "git -c" may set from inside an agent: none runs a program, includes
// a file or defines an alias.
var safeConfig = map[string]bool{"user.name": true, "user.email": true, "color.ui": true, "core.quotepath": true}

// gitFlags are git's own options that take no value and change neither its configuration nor
// its repository.
var gitFlags = map[string]bool{"--no-pager": true, "-P": true, "-p": true, "--paginate": true, "--no-replace-objects": true,
	"--no-optional-locks": true, "--literal-pathspecs": true, "--glob-pathspecs": true, "--noglob-pathspecs": true,
	"--icase-pathspecs": true, "--no-lazy-fetch": true, "--version": true, "--help": true, "-v": true, "-h": true}

// builders run a program with words they build (from stdin, file names…): git run by one
// gets words lea cannot read.
var builders = map[string]bool{"xargs": true, "parallel": true, "find": true, "fd": true, "fdfind": true, "busybox": true}

// wrappers run the program their arguments name: a computed one is any program.
var wrappers = map[string]bool{"env": true, "sudo": true, "doas": true, "su": true, "runuser": true, "nice": true, "ionice": true,
	"nohup": true, "time": true, "timeout": true, "stdbuf": true, "setsid": true, "command": true, "exec": true, "builtin": true,
	"xargs": true, "parallel": true, "flock": true, "watch": true, "chronic": true, "unbuffer": true, "chroot": true,
	"sh": true, "bash": true, "dash": true, "zsh": true, "ksh": true, "eval": true}

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

// readers only search or print their words: a git push in one is text.
var readers = map[string]bool{"grep": true, "egrep": true, "fgrep": true, "rg": true, "ag": true, "echo": true, "printf": true}

// printsOnly says whether w (a program and its words) runs none of its words: a reader, or gh
// pr/issue or glab mr/issue (titles, bodies, comments). gh alias set can define a shell alias: not a reader.
func printsOnly(w []string) bool {
	b := path.Base(w[0])
	return readers[b] || (b == "gh" && len(w) > 1 && (w[1] == "pr" || w[1] == "issue")) ||
		(b == "glab" && len(w) > 1 && (w[1] == "mr" || w[1] == "issue"))
}

// pushWord says whether x, a whole word, is a push or a git transport that writes remote refs
// (a branch named fix/x-push is not).
func pushWord(x string) bool {
	return x == "push" || x == "send-pack" || x == "http-push" || strings.HasPrefix(x, "remote-")
}

// safeGitEnv are the GIT_* assignments an agent may make: fixed values that run no program of
// its choosing.
var safeGitEnv = map[string]bool{"GIT_EDITOR=true": true, "GIT_EDITOR=:": true, "GIT_SEQUENCE_EDITOR=true": true,
	"GIT_SEQUENCE_EDITOR=:": true, "GIT_PAGER=cat": true, "GIT_TERMINAL_PROMPT=0": true}

// holdsPush says whether a word names git and a push or a ref-writing transport: a script.
// A shell reading the word as a script drops its quotes and escapes: g""it pu""sh is git push.
func holdsPush(x string) bool { return namesGit(x) && pushWordRe.MatchString(unquote.Replace(x)) }

// namesGit says whether a word, read as a script, names git.
func namesGit(x string) bool { return gitWordRe.MatchString(unquote.Replace(x)) }

var gitWordRe = regexp.MustCompile(`\bgit\b`)

// gitComputedRe: in a command lea cannot split, git with a computed subcommand, or a computed
// program.
var gitComputedRe = regexp.MustCompile("\\bgit\\s+(-\\S+\\s+)*[^a-z\\s-]|(^|[;&|(]\\s*)[$`]")

// programOpts are git options, on any subcommand, whose value is a program git runs.
var programOpts = []string{"--upload-pack", "--receive-pack", "--exec", "--extcmd", "--open-files-in-pager", "-O"}

// hardGit returns why a command is a hard deny for what it makes git read or push, or "". It
// parses every simple command, git's own options and every refspec: what it cannot read is
// denied, never guessed. Out of its reach: an alias the repository's config already holds and a
// script file that pushes (writing config is a hard deny of its own); branch protection on the
// remote remains the control that holds.
func hardGit(raw, cmd string, c Caller) string {
	segs, inners, ok := segments(raw)
	if !ok {
		switch {
		case gitEnvRe.MatchString(cmd):
			return gitEnv
		case gitOptRe.MatchString(cmd):
			return gitGlobal
		case pushWordRe.MatchString(cmd):
			return pushUnread
		case gitComputedRe.MatchString(raw):
			return gitComputed
		}
		return ""
	}
	for _, in := range inners {
		if why := hardGit(in, unquote.Replace(in), c); why != "" {
			return why
		}
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
			name, _, _ := strings.Cut(x, "=")
			if (gitVar(x) && !safeGitEnv[x]) || strings.Contains(name, expanded) {
				return gitEnv
			}
		}
	}
	for _, x := range w {
		// Wherever it stands: sudo, env and others pass assignments on to the program they run.
		if isAssignment(x) && gitVar(x) && !safeGitEnv[x] {
			return gitEnv
		}
	}
	p := 0 // the program, after VAR=value prefixes
	for p < len(w) && isAssignment(w[p]) {
		p++
	}
	if p == len(w) {
		return ""
	}
	if strings.Contains(w[p], expanded) {
		return gitComputed
	}
	if isGit(w[p]) {
		if why := gitArgs(w[p:], c, compound); why != "" {
			return why
		}
		if s := gitSub(w[p:]); p > 0 && s > 0 && pushWord(w[p+s]) {
			return pushIndirect
		}
		return ""
	}
	if strings.Contains(w[p], wild) && contains(w[p:], "git") || strings.Contains(w[p], wild) && anyWord(w[p:], pushWord) {
		return pushWild // the shell picks the program: maybe git
	}
	// Under a builder (xargs, find -exec…), any word naming git may become a git call with
	// words lea cannot read, a script's (sh -c 'git X …') included.
	for k := p; k < len(w); k++ {
		if builders[path.Base(w[k])] && anyWord(w[k+1:], namesGit) {
			return gitBuilt
		}
	}
	reader := printsOnly(w[p:])
	scripted := false // a word names git and holds an expansion, or another word is a push
	for k := p + 1; k < len(w); k++ {
		if namesGit(w[k]) && !reader && (strings.ContainsAny(w[k], "$`") || holdsPush(w[k])) {
			scripted = true
		}
	}
	if scripted || (!reader && anyWord(w[p+1:], func(x string) bool { return namesGit(x) && x != "git" }) && anyWord(w[p+1:], pushWord)) {
		return pushIndirect // a script of a shell or an interpreter that pushes
	}
	for k := p; k < len(w); k++ {
		if wrappers[path.Base(w[k])] && anyWord(w[k+1:], func(x string) bool { return strings.Contains(x, expanded) }) {
			return gitComputed // env $G …, sudo $P …: the program run is computed
		}
		if !isGit(w[k]) || k == p || reader {
			continue
		}
		// git run by another program: xargs, parallel, find and the like build its words.
		for _, b := range w[p:k] {
			if builders[path.Base(b)] {
				return gitBuilt
			}
		}
		if gitSub(w[k:]) < 0 {
			return gitBuilt // no subcommand: whoever runs it may add one
		}
		if why := gitArgs(w[k:], c, compound); why != "" {
			return why
		}
		if anyWord(w[k:], pushWord) {
			return pushIndirect
		}
	}
	return ""
}

func anyWord(w []string, f func(string) bool) bool {
	for _, x := range w {
		if f(x) {
			return true
		}
	}
	return false
}

// gitSub is the index of git's subcommand in w (w[0] being git), -1 when there is none.
func gitSub(w []string) int {
	j := 1
	for ; j < len(w) && strings.HasPrefix(w[j], "-"); j++ {
		if (w[j] == "-C" || w[j] == "-c") && j+1 < len(w) {
			j++
		}
	}
	if j >= len(w) {
		return -1
	}
	return j
}

// gitArgs checks one git call, w[0] being git: its own options, then its subcommand.
func gitArgs(w []string, c Caller, compound bool) string {
	for j := 1; j < len(w) && strings.HasPrefix(w[j], "-"); j++ {
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
	j := gitSub(w)
	if j < 0 {
		return ""
	}
	sub := w[j]
	if strings.Contains(sub, expanded) || strings.Contains(sub, wild) {
		return gitComputed
	}
	for k := j + 1; k < len(w); k++ {
		x := w[k]
		if sub == "commit" && (x == "-m" || x == "--message") {
			k++ // a commit's message is text
			continue
		}
		for _, o := range programOpts {
			if x == o || strings.HasPrefix(x, o+"=") || (o == "-O" && strings.HasPrefix(x, o)) {
				return "git options that run a program (--upload-pack, --receive-pack, --exec, --extcmd, -O) are never allowed"
			}
		}
		if sub == "rebase" && (x == "-x" || strings.HasPrefix(x, "-x")) {
			return "git rebase --exec is never allowed"
		}
		if holdsPush(x) {
			return pushIndirect
		}
	}
	switch {
	case sub == "push":
		for _, x := range w[j+1:] {
			if strings.Contains(x, expanded) {
				return gitComputed
			}
		}
		return parsePush(w[j+1:], c, compound)
	case sub == "send-pack" || sub == "http-push" || strings.HasPrefix(sub, "remote-") || sub == "svn" || sub == "p4":
		return pushTransport
	case sub == "filter-branch" || (sub == "submodule" && contains(w[j+1:], "foreach")) || (sub == "bisect" && contains(w[j+1:], "run")):
		return "git subcommands that run a script (submodule foreach, bisect run, filter-branch) are never allowed"
	case sub == "subtree" && anyWord(w[j+1:], pushWord):
		return pushIndirect
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
		case a == "--all" || a == "--branches" || a == "--mirror" || a == "--prune" || a == "--tags" || a == "--follow-tags":
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
		// A destination git has to infer takes its kind from the source: a tag's (v1, HEAD
		// pointing at refs/tags/v1) makes refs/tags/<dst>. Only refs/heads/ on either side
		// says it is a branch; a bare <branch> is checked against the repository (RawPushes).
		if colon && !strings.HasPrefix(dst, "refs/heads/") && !strings.HasPrefix(src, "refs/heads/") {
			return pushUnqualified
		}
		if !colon && strings.HasPrefix(dst, "heads/") {
			return pushUnqualified
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
