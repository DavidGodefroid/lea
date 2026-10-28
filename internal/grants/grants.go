// Package grants decides, deterministically, whether a tool call an agent is about to make is
// covered by a grant the user recorded on the host (lea grant add), or is one of the hard denies
// no grant can cover. It never sees who granted what beyond the rows it is given: the daemon
// stores grants and only the host API writes them (issue #73).
//
// Fail closed: a command the parser does not fully understand is never allowed. Hard denies are
// checked first, on the raw command as well as on its words, so shell chaining cannot hide them.
package grants

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
)

// The actions a grant can name: a closed list, each mapped to concrete tool calls below.
const (
	Merge           = "merge"
	StopFinished    = "stop-finished"
	CloseIssue      = "close-issue"
	DeleteOwnBranch = "delete-own-branch"
	CommitIdentity  = "commit-identity"
	PRCreate        = "pr-create"
)

// Actions lists every action a grant may name, in display order.
var Actions = []string{Merge, StopFinished, CloseIssue, DeleteOwnBranch, CommitIdentity, PRCreate}

// DeliveryActions are the standing grants lea delivery on records for each repository: push the
// agent's own branch, open its PR, merge it, delete the branch (issue #92).
var DeliveryActions = []string{CommitIdentity, PRCreate, Merge, DeleteOwnBranch}

// Standing is the expiry of a delivery grant: it holds until lea delivery off revokes it.
const Standing int64 = 1 << 62

// ValidAction says whether a is one of Actions.
func ValidAction(a string) bool {
	for _, x := range Actions {
		if x == a {
			return true
		}
	}
	return false
}

// StopTool is the MCP tool stop-finished covers.
const StopTool = "mcp__lea__agents_stop"

// Grant is one user decision. Container is the lea it applies to; Repo the repository ("" any
// repository of that lea); Branch a glob on the caller's branch ("" or "*" any); Agent the key of
// the agent whose subtree it covers ("" every agent of the lea). Expires and Revoked are unix
// seconds, Revoked 0 while active.
type Grant struct {
	ID                              int64
	Container, Action, Repo, Branch string
	Agent                           string
	// GitHub is the repository gh acts on, github.com/OWNER/NAME: every gh call a grant allows
	// is pinned to it with -R (merge and close-issue grants need one).
	GitHub                    string
	Created, Expires, Revoked int64
}

// NeedsGitHub says whether a grant of action names its GitHub repository: every action but
// stop-finished runs gh or git against it (gh -R, git push only to an origin that is it).
func NeedsGitHub(action string) bool { return action != StopFinished }

// IsStanding says whether g is a delivery grant (lea delivery on), with no expiry.
func (g Grant) IsStanding() bool { return g.Expires == Standing }

// Active says whether g still applies at now.
func (g Grant) Active(now int64) bool {
	return g.Revoked == 0 && g.Expires > now
}

// Caller is the agent making the call, as the daemon knows it (never as the agent says).
// Lineage is its key followed by its ancestors' keys, nearest first. Branch is "" when another
// live agent of the lea works on the same repo and branch: then no branch is the caller's own.
// OwnWorktree says the call runs at the root of the caller's own git worktree
// (<projects>/.lea/worktrees/<repo>/<agent>): gh and git then act on the caller's repo.
type Caller struct {
	Key, Container, Repo, Branch string
	Lineage                      []string
	OwnWorktree                  bool
	// Worktree is the caller's worktree root (the hook's cwd, when OwnWorktree): every git call
	// a grant allows runs there (git -C).
	Worktree string
	// Cwd is the directory the tool call runs in (the hook's cwd), wherever it is: relative
	// paths are read from it. "" when the hook gave none.
	Cwd string
	// Env is the environment an allowed call runs with, and nothing else (env -i): the variables
	// of SafeEnv, as the hook's own process (Claude's, not the agent's shell) has them.
	Env map[string]string
}

// SafeEnv are the variables an allowed git or gh call keeps, each checked by safeEnvValue.
// Everything else (GIT_*, GH_REPO, GH_HOST, tokens, LD_*, BASH_ENV…) is dropped.
var SafeEnv = []string{"HOME", "GH_CONFIG_DIR", "SSH_AUTH_SOCK", "LANG"}

var safeEnvValue = regexp.MustCompile(`^[A-Za-z0-9/._@:+-]{1,256}$`)

// InWorktree says whether cwd is exactly the root of <...>/.lea/worktrees/<repo>/<name>: not a
// subdirectory (which could hold a nested repository), and with one worktrees segment only.
func InWorktree(cwd, repo, name string) bool {
	if repo == "" || name == "" || !strings.HasPrefix(cwd, "/") || strings.Contains(cwd, "/..") ||
		strings.Contains(cwd, "/./") || strings.Contains(cwd, "//") {
		return false
	}
	cwd = strings.TrimSuffix(cwd, "/")
	const marker = "/.lea/worktrees/"
	return strings.Count(cwd, marker) == 1 && strings.HasSuffix(cwd, marker+repo+"/"+name)
}

// Request is a tool call recognised as one action. Target names what it acts on, for the audit
// ("pr 42", "issue 7", a branch, a child agent's name): never the tool input itself.
type Request struct {
	Action, Target string
	// Prog ("git" or "gh") and Args are a Bash call's validated argv after the program: an
	// allowed call runs exactly that, rewritten by Command.
	Prog string
	Args []string
}

// Verdicts of Decide.
const (
	Allow = "allow"
	Deny  = "deny"
)

// Decision is Decide's answer. Verdict "" means no opinion: the call takes the normal path
// (Claude's own checks, the deck).
type Decision struct {
	Verdict, Reason string
	Grant           int64
	Request         Request
	// Command replaces an allowed Bash call's command (the hook's updatedInput): the validated
	// argv, pinned (Command).
	Command string
}

// Classify parses a tool call: hard is the reason of a hard deny ("" when none); req is the
// action the call is, when it is exactly one the grants know (ok false otherwise). It never
// allows anything by itself.
func Classify(tool string, input json.RawMessage, c Caller) (hard string, req Request, ok bool) {
	switch tool {
	case "Bash":
		var in struct {
			Command string `json:"command"`
		}
		if json.Unmarshal(input, &in) != nil {
			return "", Request{}, false
		}
		if h := hardBash(in.Command, c); h != "" {
			return h, Request{}, false
		}
		words, wok := Words(in.Command)
		if !wok {
			return "", Request{}, false
		}
		req, ok = bashRequest(words, c)
		return "", req, ok
	case "Edit", "Write", "MultiEdit", "NotebookEdit":
		var in struct {
			FilePath     string `json:"file_path"`
			NotebookPath string `json:"notebook_path"`
		}
		_ = json.Unmarshal(input, &in)
		for _, p := range []string{in.FilePath, in.NotebookPath} {
			if protectedPath(p) || fileProtected(p, c.Cwd) {
				return "editing Claude's, git's, the shell's or lea's own settings from inside an agent is never allowed", Request{}, false
			}
		}
		return "", Request{}, false
	case StopTool:
		var in struct {
			Name string `json:"name"`
		}
		if json.Unmarshal(input, &in) != nil || in.Name == "" || len(in.Name) > 129 {
			return "", Request{}, false
		}
		return "", Request{Action: StopFinished, Target: in.Name}, true
	}
	return "", Request{}, false
}

// Match returns the first active grant of gs that covers req for c, or ok false.
func Match(req Request, c Caller, gs []Grant, now int64) (Grant, bool) {
	for _, g := range gs {
		if g.Action != req.Action || !g.Active(now) || g.Container != c.Container {
			continue
		}
		if g.Repo != "" && g.Repo != c.Repo {
			continue
		}
		if g.Branch != "" && !Glob(g.Branch, c.Branch) {
			continue
		}
		if g.Agent != "" && !contains(c.Lineage, g.Agent) {
			continue
		}
		// --repo binds the worktree to --github: a grant without it pins nothing to the caller's
		// repository.
		if req.Prog != "" && (g.GitHub == "" || g.Repo == "") {
			continue
		}
		return g, true
	}
	return Grant{}, false
}

// Decide is Classify then Match, for every action whose conditions the call itself carries.
// stop-finished also needs the target to be the caller's finished child: the daemon checks that
// before it calls Decide with finished true (false otherwise).
func Decide(tool string, input json.RawMessage, c Caller, gs []Grant, now int64, finished bool) Decision {
	hard, req, ok := Classify(tool, input, c)
	if hard != "" {
		return Decision{Verdict: Deny, Reason: "lea hard deny: " + hard}
	}
	if !ok || (req.Action == StopFinished && !finished) {
		return Decision{}
	}
	g, ok := Match(req, c, gs, now)
	if !ok {
		return Decision{}
	}
	d := Decision{Verdict: Allow, Grant: g.ID, Request: req,
		Reason: fmt.Sprintf("lea grant #%d (%s) recorded by the user", g.ID, g.Action)}
	switch req.Prog {
	case "gh":
		d.Request.Target = g.GitHub + " " + req.Target
		d.Command = Command(req, c, g)
	case "git":
		d.Request.Target = c.Repo + " " + req.Target
		d.Command = Command(req, c, g)
	}
	if req.Prog != "" && d.Command == "" {
		return Decision{}
	}
	return d
}

// Programs of an allowed call: absolute paths, so no PATH, alias or function lookup.
const (
	gitPath = "/usr/bin/git"
	ghPath  = "/usr/bin/gh"
	envPath = "/usr/bin/env"
	sshPath = "/usr/bin/ssh"
)

// gitPins neutralise what in git's configuration would run a program or send the push elsewhere
// during commit or push: hooks, fsmonitor, the SSH command, signing, pager, editor, askpass, and
// every transport but https and ssh.
var gitPins = []string{"core.hooksPath=/dev/null", "core.fsmonitor=false", "core.sshCommand=" + sshPath,
	"commit.gpgSign=false", "core.pager=cat", "core.editor=true", "core.askPass=/bin/false",
	"push.recurseSubmodules=no", "protocol.allow=never", "protocol.https.allow=always", "protocol.ssh.allow=always",
	// An empty helper resets the list (the repository's too); then only lea's: the image's store
	// and gh's.
	"credential.helper=", "credential.helper=store --file=/lea/state/git/credentials",
	"credential.helper=!/usr/bin/gh auth git-credential",
	"http.sslVerify=true", "http.proxy=", "http.extraHeader=", "http.cookieFile=", "http.curloptResolve=",
	"http.sslCAInfo=/etc/ssl/certs/ca-certificates.crt", "http.sslCAPath=/etc/ssl/certs"}

// Command is the command an allowed Bash call runs instead of the agent's. Claude's shell only
// starts env, by a quoted absolute path (a quoted word is never alias-expanded); everything else
// runs in a fresh /bin/sh under env -i, so the agent's variables, aliases and functions do not
// reach it. Not against a hostile agent: bash accepts "function /usr/bin/env { … }" in Claude's
// shell snapshot, and that would replace the call (README, grants: what they protect against).
//
//   - gh: the validated argv, pinned to the grant's repository with -R;
//   - git commit: plumbing in the caller's worktree (write-tree, commit-tree, update-ref): no
//     hook, filter, index refresh or signing runs, whatever the repository's config says;
//   - git push: from a bare repository lea creates for the call (mktemp -d: random name, mode
//     0700), with the worktree's objects borrowed (alternates), to the grant's URL and the full
//     ref: the worktree repository's config (url, pushurl, http, credential, push refspecs) is
//     not read, and the bare repository's is checked just before the push. Not against a hostile
//     agent: it runs as the same user and could rewrite that config between the check and the
//     push.
//
// Every word lea builds is single-quoted. "" when the call cannot be pinned.
func Command(req Request, c Caller, g Grant) string {
	if g.GitHub == "" {
		return ""
	}
	// GIT_CONFIG_GLOBAL=/dev/null: ~/.gitconfig is the agent's to write (url.insteadOf,
	// include.path, helpers); /etc/gitconfig is the image's.
	// GIT_CONFIG_NOSYSTEM: lea's credential store comes in as a pin (gitPins), not from it.
	env := []string{envPath, "-i", "PATH=/usr/bin:/bin", "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_NOSYSTEM=1", "GIT_TERMINAL_PROMPT=0",
		"GIT_EDITOR=true", "GH_PROMPT_DISABLED=1"}
	for _, k := range SafeEnv {
		if v, ok := c.Env[k]; ok {
			if !safeEnvValue.MatchString(v) || (k != "LANG" && !strings.HasPrefix(v, "/")) {
				return ""
			}
			env = append(env, k+"="+v)
		}
	}
	var script string
	switch req.Prog {
	case "gh":
		script = shellWords(append(append([]string{ghPath}, req.Args...), "-R", g.GitHub))
		if req.Action == PRCreate {
			script = prCreateScript(req.Args, c.Worktree, g.GitHub)
		}
		if req.Action == Merge && len(req.Args) > 2 {
			if c.Branch == "" || !BranchName(c.Branch) || Protected(c.Branch) {
				return ""
			}
			script = prHeadCheck(req.Args[2], c.Branch, g.GitHub) + script
		}
	case "git":
		if !c.OwnWorktree || c.Worktree == "" {
			return ""
		}
		switch req.Args[0] {
		case "commit":
			script = commitScript(req.Args[1:], c.Worktree)
		case "push":
			script = pushScript(req.Args, c.Worktree, g.GitHub)
		}
	}
	if script == "" {
		return ""
	}
	return shellWords(append(env, shPath, "-c", script))
}

const (
	shPath     = "/bin/sh"
	mktempPath = "/usr/bin/mktemp"
	rmPath     = "/bin/rm"
	mkdirPath  = "/bin/mkdir"
	cpPath     = "/bin/cp"
)

// prHeadCheck is the script lines that end the call unless the PR n names (a number or a head
// branch) has its head on branch, in the repository gh (not a fork's branch of the same name: gh
// finds a PR by its head's name, whatever its owner). A PR's head branch never changes once
// opened.
func prHeadCheck(n, branch, gh string) string {
	view := shellWords([]string{ghPath, "pr", "view", n, "--json", "headRefName,isCrossRepository",
		"--jq", `(.isCrossRepository|tostring) + " " + .headRefName`, "-R", gh})
	return "head=$(" + view + ") || exit 1\n" +
		`if [ "$head" != ` + shellWords([]string{"false " + branch}) + " ]; then " +
		refuse("PR "+n+" is not the agent's own branch "+branch) + "; fi\n"
}

// prCreateScript is gh pr create with the validated args, pinned to gh. A --body-file (a path
// relative to the worktree, ghPRCreate) is opened once, on fd 3; the file actually opened must
// resolve (realpath of /proc/self/fd/3) to a plain file of the worktree wt, outside .git; gh reads
// that open file on its stdin (--body-file -): swapping the path for a symlink to lea's state or
// a credential after the check changes nothing.
func prCreateScript(args []string, wt, gh string) string {
	file := -1
	for i, a := range args {
		if (a == "--body-file" || a == "-F") && i+1 < len(args) {
			file = i + 1
		}
	}
	if file < 0 {
		return shellWords(append(append([]string{ghPath}, args...), "-R", gh))
	}
	if wt == "" || !strings.HasPrefix(wt, "/") {
		return ""
	}
	q := shellWords([]string{wt})
	argv := append(append(append([]string{ghPath}, args[:file]...), "-"), args[file+1:]...)
	path := shellWords([]string{wt + "/" + args[file]})
	// The type is checked before the open too: opening a FIFO would block until a writer comes.
	return "[ -f " + path + " ] || { " + refuse("the body file is not a plain file") + "; }\n" +
		"exec 3< " + path + " || exit 1\n" +
		"r=$(" + shellWords([]string{"/usr/bin/realpath", "-e", "--", "/proc/self/fd/3"}) + ") || exit 1\n" +
		`case "$r" in ` + q + "/.git|" + q + "/.git/*) " + refuse("the body file is in .git") + ";; " + q + "/*) ;; *) " +
		refuse("the body file is not in the agent's worktree") + ";; esac\n" +
		`[ -f /proc/self/fd/3 ] || { ` + refuse("the body file is not a plain file") + "; }\n" +
		shellWords(append(argv, "-R", gh)) + " <&3"
}

// pins are lea's -c options for git.
func pins() []string {
	var o []string
	for _, p := range gitPins {
		o = append(o, "-c", p)
	}
	return o
}

// gitIn is git in dir with lea's pins.
func gitIn(dir string) []string {
	return append([]string{gitPath, "-C", dir}, pins()...)
}

// refuse is the script line that ends the call with lea's reason.
func refuse(why string) string {
	return "echo " + shellWords([]string{"lea: grant not applied: " + why}) + " >&2; exit 1"
}

// configGuard refuses a repository whose config holds a key that would redirect git or run a
// program: defence in depth, the plumbing and the bare push do not depend on it. It reads the
// repository's file only (--local: lea's own -c pins are not in it, includes are listed, not
// followed).
func configGuard(wt string) string {
	return shellWords([]string{gitPath, "-C", wt, "config", "--local", "--get-regexp", riskyConfig}) +
		" >/dev/null 2>&1; s=$?; if [ \"$s\" -ne 1 ]; then " + refuse("the repository git config redirects or runs programs") + "; fi\n"
}

// commitScript commits the index with the messages of args (-m/--message; -q/--quiet) through
// plumbing.
func commitScript(args []string, wt string) string {
	var msgs []string
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "-m", "--message":
			if i+1 >= len(args) {
				return ""
			}
			msgs = append(msgs, "-m", args[i+1])
			i++
		case "-q", "--quiet":
		default:
			return ""
		}
	}
	if len(msgs) == 0 {
		return ""
	}
	git := shellWords(gitIn(wt))
	// write-tree runs on a copy of the index against an empty work tree: writing the index back
	// re-checks racily clean entries against the work tree, through the clean filters.
	isolated := shellWords([]string{envPath}) + ` GIT_INDEX_FILE="$tmp/index" ` + shellWords([]string{gitPath, "--git-dir"}) +
		` "$gd" --work-tree "$tmp/w" ` + shellWords(pins())
	return configGuard(wt) +
		"gd=$(" + git + " rev-parse --absolute-git-dir) || exit 1\n" +
		"tmp=$(" + shellWords([]string{mktempPath, "-d"}) + ") || exit 1\n" +
		"trap " + shellWords([]string{rmPath + ` -rf "$tmp"`}) + " EXIT\n" +
		shellWords([]string{mkdirPath}) + ` "$tmp/w" && ` + shellWords([]string{cpPath}) + ` "$gd/index" "$tmp/index" || exit 1` + "\n" +
		"tree=$(" + isolated + " write-tree) || exit 1\n" +
		"parent=$(" + git + " rev-parse --verify HEAD) || exit 1\n" +
		"new=$(" + git + ` commit-tree "$tree" -p "$parent" ` + shellWords(msgs) + ") || exit 1\n" +
		git + ` update-ref -m 'commit (lea grant)' HEAD "$new" "$parent" || exit 1` + "\n" +
		`echo "[lea grant] committed $new"`
}

// pushScript pushes the caller's branch (or deletes it) from a bare repository lea creates for
// the call (pushArgs gives the URL and the ref).
func pushScript(args []string, wt, gh string) string {
	pa := pushArgs(args, gh)
	if pa == nil {
		return ""
	}
	git := shellWords(gitIn(wt))
	bare := shellWords([]string{gitPath, "--git-dir"}) + ` "$tmp" ` + shellWords(pins())
	s := configGuard(wt) +
		"common=$(" + git + " rev-parse --path-format=absolute --git-common-dir) || exit 1\n" +
		"tmp=$(" + shellWords([]string{mktempPath, "-d"}) + ") || exit 1\n" +
		"trap " + shellWords([]string{rmPath + ` -rf "$tmp"`}) + " EXIT\n" +
		shellWords([]string{gitPath, "init", "-q", "--bare", "--template="}) + ` "$tmp" || exit 1` + "\n" +
		`echo "$common/objects" > "$tmp/objects/info/alternates" || exit 1` + "\n"
	url, ref := pa[0], pa[1]
	// The bare repository's own config, checked again right before the push.
	check := shellWords([]string{gitPath, "--git-dir"}) + ` "$tmp" ` + shellWords([]string{"config", "--local", "--get-regexp", riskyConfig}) +
		" >/dev/null 2>&1; s=$?; if [ \"$s\" -ne 1 ]; then " + refuse("the push repository config changed") + "; fi\n"
	if pa[2] == "delete" {
		return s + check + bare + " push " + shellWords([]string{url, "--delete", ref})
	}
	return s + "sha=$(" + git + " rev-parse --verify " + shellWords([]string{ref + "^{commit}"}) + ") || exit 1\n" +
		check + bare + " push " + shellWords([]string{url}) + ` "$sha"` + shellWords([]string{":" + ref})
}

// riskyConfig matches the repository config keys a pinned git refuses to run with (names as git
// prints them: section and key lower-case).
const riskyConfig = `^(url\..*\.(insteadof|pushinsteadof)|include\.path|includeif\..*|credential\..+\.helper|` +
	`remote\..*\.(pushurl|push|mirror|receivepack|uploadpack|proxy)|filter\..*|diff\..*\.(textconv|command)|core\.gitproxy|` +
	`core\.attributesfile|core\.hookspath|core\.fsmonitor|core\.sshcommand|extensions\.worktreeconfig|http\..+)$`

// pushArgs reads a validated "push [-u|--set-upstream] [--delete|-d] origin <branch>": it returns
// the grant's repository URL, the full ref and "delete" or "push", or nil when args are not that
// shape. Where it goes never depends on the remote's config. -u is accepted and does nothing: the
// bare repository a push runs from has no upstream to record.
func pushArgs(args []string, gh string) []string {
	upstream, del := false, false
	var pos []string
	for _, a := range args[1:] {
		switch a {
		case "-u", "--set-upstream":
			upstream = true
		case "--delete", "-d":
			del = true
		default:
			pos = append(pos, a)
		}
	}
	if len(pos) != 2 || pos[0] != "origin" || !BranchName(pos[1]) || Protected(pos[1]) || !strings.HasPrefix(gh, "github.com/") || (del && upstream) {
		return nil
	}
	mode := "push"
	if del {
		mode = "delete"
	}
	return []string{"https://" + gh + ".git", "refs/heads/" + pos[1], mode}
}

// shellWords single-quotes every word.
func shellWords(argv []string) string {
	q := make([]string, len(argv))
	for i, w := range argv {
		q[i] = "'" + strings.ReplaceAll(w, "'", `'\''`) + "'"
	}
	return strings.Join(q, " ")
}

func contains(xs []string, x string) bool {
	for _, y := range xs {
		if y == x {
			return true
		}
	}
	return false
}

// Glob matches s against pattern, where * matches any run of characters (slashes included) and
// every other character itself.
func Glob(pattern, s string) bool {
	if pattern == "" || pattern == "*" {
		return true
	}
	parts := strings.Split(pattern, "*")
	if !strings.HasPrefix(s, parts[0]) {
		return false
	}
	s = s[len(parts[0]):]
	last := len(parts) - 1
	if last == 0 {
		return s == ""
	}
	for _, p := range parts[1:last] {
		i := strings.Index(s, p)
		if i < 0 {
			return false
		}
		s = s[i+len(p):]
	}
	return strings.HasSuffix(s, parts[last])
}

var branchName = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._/-]{0,199}$`)

// BranchName says whether b is a branch name a grant may act on: plain characters only (a
// subset of git check-ref-format's rules), no "..", no "//", no ".lock" or "/" at the end.
func BranchName(b string) bool {
	return branchName.MatchString(b) && !strings.Contains(b, "..") && !strings.Contains(b, "//") &&
		!strings.HasSuffix(b, ".lock") && !strings.HasSuffix(b, "/") && !strings.HasSuffix(b, ".") && !strings.Contains(b, "/.")
}

// Protected says whether a branch is one no agent may push to or delete: the default branch.
func Protected(b string) bool {
	b = strings.TrimPrefix(b, "refs/heads/")
	return b == "main" || b == "master" || b == "HEAD" || b == ""
}

var protectedPathRe = regexp.MustCompile(`(^|[\s'"=:/])(` +
	`\.claude/(settings[^/\s'"]*\.json|hooks|shell-snapshots|session-env)` +
	`|\.git/(config|hooks|info|worktrees/[^/\s'"]+/config)` +
	`|\.gitconfig|\.config/git(/|$)` +
	`|\.(bashrc|bash_profile|bash_login|bash_logout|profile|zshrc|zshenv|zprofile|zlogin|inputrc)([\s'"]|$)` +
	`|\.config/lea(/|$)|usr/local/share/lea(/|$)|lea/state(/|$))`)

// protectedPath says whether p is one of the files that make a later command run something else:
// Claude's settings, hooks and shell snapshots, git's config and hooks (repository, worktree,
// user), the shell's startup files, lea's settings for Claude (/usr/local/share/lea), lea's state
// in the container (/lea/state: git identity, gh login, SSH) or lea's config. Relative and quoted
// spellings count.
func protectedPath(p string) bool {
	return p != "" && protectedPathRe.MatchString(p)
}

// fileProtected says whether relative path p, read from cwd (the hook's), is a protected path,
// or may be one when cwd is unknown.
func fileProtected(p, cwd string) bool {
	if p == "" || strings.HasPrefix(p, "/") {
		return false
	}
	if !strings.HasPrefix(cwd, "/") {
		return tail(p)
	}
	return protectedPath(join(cwd, p))
}
