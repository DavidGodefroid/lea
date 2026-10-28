package grants

import (
	"path"
	"regexp"
	"strings"
)

// maxDirs bounds the directories dirProtected follows: past it, where a word points is unknown.
const maxDirs = 32

// maxDepth bounds how deep dirProtected reads scripts inside words and substitutions.
const maxDepth = 4

// chdirOpts are the options, on any program, whose value moves where its relative words point:
// git -C, --git-dir and --work-tree, env --chdir, tar and make -C or --directory.
var chdirOpts = map[string]bool{"-C": true, "--chdir": true, "--directory": true, "--git-dir": true, "--work-tree": true}

// dirMoveRe: in a command split cannot read, a word that moves where relative words point.
var dirMoveRe = regexp.MustCompile(`(^|[\s;&|(])(cd|pushd|popd)(\s|$)|\s(-C|--chdir|--directory|--git-dir|--work-tree)(\s|=)|CDPATH`)

// tailRe: a relative word that, under some directory, is a protected file: one that starts in
// a protected directory (.git/hooks, .claude/session-env, lea/state…) or ends with a protected
// file's name (.git/config, .claude/settings.json…). It judges a word relative to a directory
// lea cannot tell; a word deeper inside a protected directory is out of its reach.
var tailRe = regexp.MustCompile(`^(hooks|info|shell-snapshots|session-env|state)(/|$)|` +
	`(^|/)(settings[^/]*\.json|config|config\.worktree|\.gitconfig|` +
	`\.(bashrc|bash_profile|bash_login|bash_logout|profile|zshrc|zshenv|zprofile|zlogin|inputrc))$`)

// dirProtected says whether cmd, run in directory cwd (the hook's; "" when unknown), names a
// protected path once each relative word is read from the directory it runs in: cd and pushd
// move the commands after them, chdirOpts their own words. The directories are followed as a
// set that only grows: a cd that fails, or a subshell's or a pipeline's that ends, removes
// nothing, so every directory a word may be relative to is tried. A directory lea cannot read
// (no cwd, an expansion, a glob, cd -, ~user, popd, CDPATH) fails closed: after it, a word that
// may name a protected file under some directory (tailRe, protectedGlobs) is one. A glob is one
// when it may match a protected path (globProtected).
func dirProtected(cmd, cwd string) bool {
	dirs, unknown := []string{""}, true
	if strings.HasPrefix(cwd, "/") && !strings.ContainsFunc(cwd, func(r rune) bool { return r < 0x20 || r == 0x7f }) {
		dirs, unknown = append(dirs, path.Clean(cwd)), false
	}
	s := scan{dotglob: strings.Contains(cmd, "dotglob") || strings.Contains(cmd, "GLOBIGNORE")}
	return s.dirs(cmd, dirs, unknown, 0)
}

// scan holds what holds for the whole command: whether a glob may match a dot file.
type scan struct{ dotglob bool }

func (s scan) dirs(cmd string, dirs []string, unknown bool, depth int) bool {
	if depth > maxDepth {
		return true
	}
	segs, redirs, inners, ok := split(cmd)
	if !ok {
		return dirMoveRe.MatchString(cmd) && anyWord(strings.Fields(unquote.Replace(cmd)), tail)
	}
	unknown = unknown || strings.Contains(cmd, "CDPATH")
	r := 0
	for k, w := range segs {
		for ; r < len(redirs) && redirs[r].seg <= k; r++ {
			if s.names(dirs, unknown, redirs[r].target) {
				return true
			}
		}
		own, ownUnknown := dirs, unknown
		for i := 0; i < len(w); i++ {
			opt, val, eq := strings.Cut(w[i], "=")
			if !chdirOpts[opt] {
				continue
			}
			if !eq {
				if i+1 >= len(w) {
					break
				}
				i++
				val = w[i]
			}
			if own, ok = moveTo(own, val); !ok {
				ownUnknown = true
			}
		}
		for _, x := range w {
			if s.names(own, ownUnknown, x) {
				return true
			}
			// A word that holds a script (sh -c '…', a heredoc's text) is read as one.
			if strings.ContainsAny(x, " \t\n;&|") && s.dirs(x, own, ownUnknown, depth+1) {
				return true
			}
		}
		p := 0
		for p < len(w) && (isAssignment(w[p]) || w[p] == "builtin" || w[p] == "command") {
			p++
		}
		if p == len(w) {
			continue
		}
		switch path.Base(w[p]) {
		case "cd", "pushd":
			var args []string
			for _, a := range w[p+1:] {
				if a == "--" || (len(a) > 1 && a[0] == '-' && a != "-") {
					continue
				}
				args = append(args, a)
			}
			switch {
			case len(args) == 0 && w[p] == "cd":
				args = []string{"~"}
			case len(args) != 1 || args[0] == "-" || strings.HasPrefix(args[0], "+"):
				unknown = true
				continue
			}
			if dirs, ok = moveTo(dirs, args[0]); !ok {
				unknown = true
			}
		case "popd":
			unknown = true
		}
	}
	for ; r < len(redirs); r++ {
		if s.names(dirs, unknown, redirs[r].target) {
			return true
		}
	}
	for _, in := range inners {
		if s.dirs(in, dirs, unknown, depth+1) {
			return true
		}
	}
	return false
}

// moveTo returns dirs with d, as each of them reads it, added; ok is false when lea cannot read
// d (then dirs is returned as it is).
func moveTo(dirs []string, d string) ([]string, bool) {
	if strings.HasPrefix(d, wild+"~") {
		d = d[len(wild):]
		if d != "~" && !strings.HasPrefix(d, "~/") {
			return dirs, false // ~user
		}
	}
	if d == "" || d == "-" || strings.Contains(d, wild) || strings.Contains(d, expanded) {
		return dirs, false
	}
	out := dirs
	for _, p := range dirs {
		q := join(p, d)
		if !contains(out, q) {
			if len(out) >= maxDirs {
				return dirs, false
			}
			out = append(out[:len(out):len(out)], q)
		}
	}
	return out, true
}

// join reads x from directory p ("" being the one the command starts in).
func join(p, x string) string {
	if p == "" || strings.HasPrefix(x, "/") || x == "~" || strings.HasPrefix(x, "~/") {
		return path.Clean(x)
	}
	return path.Join(p, x)
}

// names says whether word x, or the value of its --flag=value form, read from any of dirs, is a
// protected path; or, when one directory is unknown, may be one.
func (s scan) names(dirs []string, unknown bool, x string) bool {
	x = strings.ReplaceAll(x, wild+"~", "~")
	if strings.Contains(x, wild) {
		g := strings.ReplaceAll(strings.ReplaceAll(x, wild, ""), expanded, "*")
		for _, p := range dirs {
			if s.globProtected(join(p, g), false) {
				return true
			}
		}
		return unknown && !strings.HasPrefix(g, "/") && !strings.HasPrefix(g, "~") && s.globProtected(path.Clean(g), true)
	}
	cands := []string{x}
	if _, v, ok := strings.Cut(x, "="); ok {
		cands = append(cands, v)
	}
	for _, c := range cands {
		if c == "" {
			continue
		}
		for _, p := range dirs {
			if protectedPath(join(p, c)) {
				return true
			}
		}
		if unknown && tail(c) {
			return true
		}
	}
	return false
}

// tail says whether relative word x may be a protected file under some directory.
func tail(x string) bool {
	return !strings.HasPrefix(x, "/") && !strings.HasPrefix(x, "~") && tailRe.MatchString(path.Clean(x))
}

// comp is one component of a protected path: what matches it (nil: any name) and the names
// that stand for it, for a glob to be matched against.
type comp struct {
	re    *regexp.Regexp
	names []string
}

// lit is a component named n; pre one that starts with n (protectedPathRe does not end it).
func lit(n string) comp {
	return comp{regexp.MustCompile("^" + regexp.QuoteMeta(n) + "$"), []string{n}}
}
func pre(n string) comp { return comp{regexp.MustCompile("^" + regexp.QuoteMeta(n)), []string{n}} }

// protectedGlobs are the paths of protectedPathRe, component by component.
var protectedGlobs = func() [][]comp {
	settings := comp{regexp.MustCompile(`^settings[^/]*\.json`), []string{"settings.json", "settings.local.json"}}
	gs := [][]comp{
		{lit(".claude"), settings}, {lit(".claude"), pre("hooks")}, {lit(".claude"), pre("shell-snapshots")},
		{lit(".claude"), pre("session-env")},
		{lit(".git"), pre("config")}, {lit(".git"), pre("hooks")}, {lit(".git"), pre("info")},
		{lit(".git"), lit("worktrees"), {}, pre("config")},
		{pre(".gitconfig")}, {lit(".config"), lit("git")}, {lit(".config"), lit("lea")},
		{lit("usr"), lit("local"), lit("share"), lit("lea")}, {lit("lea"), lit("state")},
	}
	for _, rc := range []string{"bashrc", "bash_profile", "bash_login", "bash_logout", "profile", "zshrc", "zshenv", "zprofile", "zlogin", "inputrc"} {
		gs = append(gs, []comp{lit("." + rc)})
	}
	return gs
}()

// globProtected says whether glob g, a path, may match a protected one: some run of its
// components matches a protected path's first ones (then anything may follow). With anywhere,
// g is relative to a directory lea cannot tell: its first components may also match a protected
// path's later ones.
func (s scan) globProtected(g string, anywhere bool) bool {
	var cs []string
	for _, c := range strings.Split(g, "/") {
		if c != "" {
			cs = append(cs, c)
		}
	}
	for _, pp := range protectedGlobs {
		for j := range cs {
			for i := 0; i < len(pp) && (i == 0 || (anywhere && j == 0)); i++ {
				need := pp[i:]
				if len(cs)-j < len(need) {
					continue
				}
				ok := true
				for k, c := range need {
					if !s.compMatch(cs[j+k], c) {
						ok = false
						break
					}
				}
				if ok {
					return true
				}
			}
		}
	}
	return false
}

// compMatch says whether glob component g may match a name c stands for.
func (s scan) compMatch(g string, c comp) bool {
	if c.re == nil {
		return true
	}
	if !strings.ContainsAny(g, "*?[]{}") {
		return c.re.MatchString(g)
	}
	if strings.ContainsAny(g, "{}") { // a brace may spell anything: as wide as a star
		g = map[bool]string{true: ".*", false: "*"}[strings.HasPrefix(g, ".")]
	}
	// Without dotglob, the shell's globs leave names that start with a dot to a literal dot.
	if !s.dotglob && !strings.HasPrefix(g, ".") && strings.HasPrefix(c.names[0], ".") {
		return false
	}
	for _, n := range c.names {
		if m, err := path.Match(g, n); err != nil || m {
			return true
		}
	}
	return c.re.MatchString(concrete(g))
}

// concrete is one name glob component g matches: stars match nothing, ? and a class one
// character.
func concrete(g string) string {
	var b strings.Builder
	for i := 0; i < len(g); i++ {
		switch g[i] {
		case '*':
		case '?':
			b.WriteByte('x')
		case '[':
			j := strings.IndexByte(g[i+1:], ']')
			if j < 0 {
				b.WriteByte('[')
				continue
			}
			if cl := g[i+1 : i+1+j]; cl != "" && cl[0] != '!' && cl[0] != '^' {
				b.WriteByte(cl[0])
			} else {
				b.WriteByte('x')
			}
			i += j + 1
		case '\\':
			if i+1 < len(g) {
				i++
				b.WriteByte(g[i])
			}
		default:
			b.WriteByte(g[i])
		}
	}
	return b.String()
}
