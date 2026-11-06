package grants

import (
	"encoding/json"
	"regexp"
	"strconv"
	"strings"
)

// MergePolicy is what a merge by an agent of a tower's tree must pass when it targets a
// production branch (#110, C2-C3). The daemon fills it from what it knows (the lead's deploy
// window, the reviewer approvals it recorded); the forge's answers (the PR's base and head, its
// CI, its files, the repository's .lea files) are read by the pinned call itself, just before
// the merge, since the daemon never talks to the forge.
type MergePolicy struct {
	// Lead is the name of the lead whose window applies; Window its text ("" when none).
	Lead, Window string
	// Open: the lead's window is open now.
	Open bool
	// Approved are the head commits of this PR a reviewer agent approved (review_approve).
	Approved []string
	// Agent is the caller's name: the pinned call tells the daemon, as that agent, of an
	// irreversible migration it refused (lea hook lea merge-blocked).
	Agent string
}

// DefaultSensitive are the sensitive paths of a repository without .lea/sensitive: a PR that
// touches one goes to the tower, never merged into production by a lead alone. They, like the
// patterns of .lea/sensitive, match case-insensitively: the policy lower-cases both the paths and
// the patterns before matching (so Dockerfile* also catches dockerfile.prod, *auth* src/Auth/).
var DefaultSensitive = []string{"*migration*", "*migrate*", "*schema*", "*auth*", "*login*", "*password*", "*payment*", "*billing*",
	"*checkout*", "*secret*", "*credential*", "*.env", "*.env.*", "*.pem", "*.key", "infra/*", "terraform/*", "*.tf", "k8s/*",
	"helm/*", "deploy/*", ".github/workflows/*", ".gitlab-ci.yml", "Dockerfile*", "docker-compose*", "compose*.yaml", "compose*.yml"}

// alwaysSensitive are sensitive whatever .lea/sensitive says: the repository's own lea policy.
var alwaysSensitive = []string{".lea/*"}

// irreversibleRe is what an added line of a PR's diff holds when it drops or renames a table or
// a column, or destroys data: a POSIX ERE, case-insensitive (grep -iE).
const irreversibleRe = `^\+.*(drop[[:space:]]+(table|column|schema|database|collection)|rename[[:space:]]+(table|column|to)|` +
	`truncate[[:space:]]|delete[[:space:]]+from|alter[[:space:]]+table.*[[:space:]]drop[[:space:]]|dropcolumn|droptable|drop_table|` +
	`remove_column|rename_column|rename_table|dropindex\(|\.drop\(|` +
	// Alembic (op.drop_column, op.drop_table, op.alter_column(new_column_name=...)), Laravel
	// (Schema::dropIfExists, Schema::drop(, renameColumn, renameTable), Django (RemoveField,
	// DeleteModel, RenameField, RenameModel); Rails and Knex are above.
	`drop_column|op\.drop_|new_column_name|dropifexists|schema::drop\(|renamecolumn|renametable|` +
	`removefield|deletemodel|renamefield|renamemodel)`

// Paths of the programs the policy runs inside the container.
const (
	grepPath = "/bin/grep"
	headPath = "/usr/bin/head"
	trPath   = "/usr/bin/tr"
	leaPath  = "/usr/local/bin/lea"
	// busSock is the container's bus socket, as the launcher mounts it (LEA_BUS_SOCK).
	busSock = "/run/lea/bus/bus.sock"
)

// refuseQuoted is refuse with shell text that may expand variables (expr is double-quoted shell,
// lea's own words only).
func refuseQuoted(why, expr string) string {
	return "echo " + shellWords([]string{"lea: grant not applied: " + why}) + `"` + expr + `" >&2; exit 1`
}

// mergePolicyScript is the script lines that end a gh merge of PR n in the repository gh (a
// github.com/OWNER/NAME pin) unless, when its base is a production branch, the lead's window is
// open, a reviewer approved its head, its CI is green on that head, it holds no irreversible
// migration (then the daemon warns the tower), GitHub showed lea the diff of every file it
// changes, and it touches no sensitive path. It sets lea_head, the head commit checked: the merge
// names it (--match-head-commit), so a push after the checks fails it.
//
// Production is the union of: the repository's default branch, main and master, the branches
// .lea/production names on the default branch (the trusted list: a PR into the base branch
// cannot rewrite it) and those it names on the base branch (one per line, # comments, blanks and
// CRs cut). When the default branch's list names the base but the base's own file is missing or
// lists other branches, the merge is refused: the two must agree.
//
// The sensitive paths are .lea/sensitive on the base branch (shell patterns one per line, blanks
// and CRs cut, # comments; DefaultSensitive without it) and always .lea/*: paths and patterns
// are both lower-cased before they are matched, so *auth* catches src/Auth/Login.go.
//
// Every read of the forge fails closed: a .lea file is "missing" only on an HTTP 404; any other
// error (rate limit, 5xx, network) refuses the merge.
func mergePolicyScript(n, gh string, p MergePolicy) string {
	repo := strings.TrimPrefix(gh, "github.com/")
	view := func(fields, jq string) string {
		return shellWords([]string{ghPath, "pr", "view", n, "--json", fields, "--jq", jq, "-R", gh})
	}
	lower := shellWords([]string{trPath, "[:upper:]", "[:lower:]"})
	s := "set -f\nlea_nl='\n'\nlea_cr=$(printf '\\r')\n" +
		"lea_pr=$(" + view("baseRefName,headRefOid,changedFiles", `.baseRefName + " " + .headRefOid + " " + (.changedFiles | tostring)`) + ") || exit 1\n" +
		`lea_base=${lea_pr%% *} lea_rest=${lea_pr#* }` + "\n" +
		`lea_head=${lea_rest%% *} lea_nfiles=${lea_rest#* }` + "\n" +
		`case "$lea_base" in ''|*[[:space:]]*) ` + refuse("PR "+n+" has no base branch") + ";; esac\n" +
		`case "$lea_head" in ''|*[!0-9a-f]*) ` + refuse("PR "+n+" has no head commit") + ";; esac\n" +
		`case "$lea_nfiles" in ''|*[!0-9]*) ` + refuse("PR "+n+" has no count of changed files") + ";; esac\n" +
		forgeReads(repo) +
		"lea_get .lea/production \"$lea_def\"; lea_rcd=$?\n" +
		"if [ \"$lea_rcd\" = 2 ]; then " + refuseQuoted("cannot read .lea/production (not a 404) on the default branch ", "$lea_def") + "; fi\n" +
		"lea_list \"$lea_got\"; lea_pdef=$lea_norm\n" +
		"if [ \"$lea_base\" = \"$lea_def\" ]; then lea_rcb=$lea_rcd lea_pbase=$lea_pdef\nelse\n" +
		"  lea_get .lea/production \"$lea_base\"; lea_rcb=$?\n" +
		"  if [ \"$lea_rcb\" = 2 ]; then " + refuseQuoted("cannot read .lea/production (not a 404) on the base branch ", "$lea_base") + "; fi\n" +
		"  lea_list \"$lea_got\"; lea_pbase=$lea_norm\nfi\n" +
		"lea_isprod=0\n" +
		"case \"$lea_base\" in main|master) lea_isprod=1;; esac\n" +
		"[ \"$lea_base\" = \"$lea_def\" ] && lea_isprod=1\n" +
		"lea_names \"$lea_pbase\" \"$lea_base\" && lea_isprod=1\n" +
		"if lea_names \"$lea_pdef\" \"$lea_base\"; then\n  lea_isprod=1\n" +
		"  if [ \"$lea_rcb\" != 0 ] || [ \"$lea_pbase\" != \"$lea_pdef\" ]; then " +
		refuseQuoted("PR "+n+": .lea/production on the default branch names the base branch, but the base branch's own .lea/production is missing or differs; make them agree (or ask your tower): ", "$lea_base") + "; fi\n" +
		"fi\n" +
		"if [ \"$lea_isprod\" = 1 ]; then\n"
	if !p.Open {
		why := "PR " + n + " targets the production branch "
		tail := " outside the deploy window of " + p.Lead
		if p.Window == "" {
			tail = ": " + p.Lead + " has no deploy window (the tower sets one with lead_window)"
		} else {
			tail += " (" + p.Window + ")"
		}
		s += "  echo " + shellWords([]string{"lea: grant not applied: " + why}) + `"$lea_base"` + shellWords([]string{tail + ": merge it inside the window, or ask your tower"}) + " >&2; exit 1\n"
	}
	ok := []string{}
	for _, sha := range p.Approved {
		if hexSHA(sha) {
			ok = append(ok, sha)
		}
	}
	if len(ok) == 0 {
		s += "  " + refuse("no reviewer agent approved PR "+n+" (review_approve with its head commit): a production merge needs one") + "\n"
	} else {
		s += `  case "$lea_head" in ` + strings.Join(ok, "|") + ") ;; *) " +
			refuseQuoted("no reviewer agent approved PR "+n+" at its head ", "$lea_head") + ";; esac\n"
	}
	// The files: "F name" for each, "P name" for the name a renamed file had, "N name" for a file
	// (not deleted) GitHub shows no patch of (large, binary, a pure rename), and the added lines.
	filesJQ := `.[] | "F " + .filename, (if .previous_filename then "P " + .previous_filename else empty end), ` +
		`(if .patch == null and .status != "removed" then "N " + .filename else empty end), ` +
		`(.patch // "" | split("\n")[] | select(startswith("+")))`
	// Per page of the files: its length, then how many names hold a control character.
	countJQ := `"\(length) \([.[] | select([.filename, (.previous_filename // "")] | any(test("[\\x00-\\x1f\\x7f]")))] | length)"`
	s += "  lea_ci=$(" + view("statusCheckRollup", `[.statusCheckRollup[] | (.conclusion // .state // "")] | if length == 0 then "none" elif all(. == "SUCCESS" or . == "NEUTRAL" or . == "SKIPPED") then "green" else "not green" end`) + ") || exit 1\n" +
		`  [ "$lea_ci" = green ] || { ` + refuseQuoted("CI of PR "+n+" is not green on its head: ", "$lea_ci") + "; }\n" +
		// The count of files GitHub lists, summed over the pages with jq, and of those whose name
		// (or old name) holds a newline or a control character: such a name would split into
		// lines of its own below, so it is refused, and the count never comes from those lines.
		"  lea_cnt=$(" + shellWords([]string{ghPath, "api", "--paginate", "repos/" + repo + "/pulls/" + n + "/files", "--jq", countJQ}) + ") || exit 1\n" +
		"  lea_nf=0 lea_bad=0\n  IFS=$lea_nl\n  for lea_b in $lea_cnt; do\n" +
		// Each line is two numbers of at most 9 digits, one space between.
		"    case \"$lea_b\" in *' '*) ;; *) lea_b=-;; esac\n" +
		"    case \"$lea_b\" in *[!0-9\\ ]*|' '*|*' '|*' '*' '*|??????????' '*|*' '??????????*) " +
		refuse("cannot count the files of PR "+n+" (an odd answer of GitHub); it goes to your tower") + ";; esac\n" +
		"    lea_n=${lea_b%% *} lea_x=${lea_b#* }\n" +
		"    lea_nf=$((lea_nf + lea_n)) lea_bad=$((lea_bad + lea_x))\n  done\n  unset IFS\n" +
		"  [ \"$lea_bad\" = 0 ] || { " +
		refuse("a file name of PR "+n+" holds a newline or a control character, which lea cannot check for a sensitive path; it goes to your tower") + "; }\n" +
		"  [ \"$lea_nf\" -ge \"$lea_nfiles\" ] 2>/dev/null || { " +
		refuseQuoted("GitHub listed only part of the files of PR "+n+", so lea cannot check it for an irreversible migration or a sensitive path; it goes to your tower: files listed/changed ", "$lea_nf/$lea_nfiles") + "; }\n" +
		"  lea_files=$(" + shellWords([]string{ghPath, "api", "--paginate", "repos/" + repo + "/pulls/" + n + "/files", "--jq", filesJQ}) + ") || exit 1\n" +
		"  lea_irr=$(printf '%s\\n' \"$lea_files\" | " + shellWords([]string{grepPath, "-iE", irreversibleRe}) + " | " + shellWords([]string{headPath, "-n", "8"}) + ")\n" +
		"  if [ -n \"$lea_irr\" ]; then\n" +
		"    printf '%s\\n' \"$lea_irr\" | " + shellWords([]string{envPath, "-i", "PATH=/usr/bin:/bin", "LEA_AGENT=" + p.Agent, "LEA_BUS_SOCK=" + busSock,
		leaPath, "hook", "lea", "merge-blocked", n}) + " >/dev/null 2>&1\n" +
		"    " + refuse("PR "+n+" holds an irreversible migration (a dropped or renamed table or column, or destroyed data): it is never merged without the user's explicit approval; lea warned your tower") + "\n" +
		"  fi\n" +
		// Fail closed on what lea could not read: a truncated file list, a file without a patch.
		"  lea_np=$(printf '%s\\n' \"$lea_files\" | " + shellWords([]string{grepPath, "^N "}) + " | " + shellWords([]string{headPath, "-n", "1"}) + ")\n" +
		"  [ -z \"$lea_np\" ] || { " +
		refuseQuoted("GitHub shows no diff of a file of PR "+n+" (large, binary or a pure rename), so lea cannot check it for an irreversible migration; it goes to your tower: ", "${lea_np#??}") + "; }\n" +
		"  lea_get .lea/sensitive \"$lea_base\"\n" +
		"  case $? in 0) lea_sens=$lea_got;; 1) lea_sens=" + shellWords([]string{strings.Join(DefaultSensitive, "\n")}) + ";; *) " +
		refuseQuoted("cannot read .lea/sensitive (not a 404) on the base branch ", "$lea_base") + ";; esac\n" +
		"  lea_list \"$lea_sens$lea_nl\"" + shellWords([]string{strings.Join(alwaysSensitive, "\n")}) + "\n" +
		"  lea_sens=$(printf '%s' \"$lea_norm\" | " + lower + ")\n" +
		"  lea_paths=$(printf '%s\\n' \"$lea_files\" | " + shellWords([]string{grepPath, "-E", "^[FP] "}) + " | " + lower + ")\n" +
		"  lea_hit=\n  IFS=$lea_nl\n" +
		"  for lea_f in $lea_paths; do lea_f=${lea_f#??}; for lea_p in $lea_sens; do\n" +
		"    case \"$lea_f\" in $lea_p) lea_hit=$lea_f; break 2;; esac; done; done\n" +
		"  unset IFS\n" +
		`  [ -z "$lea_hit" ] || { ` + refuseQuoted("PR "+n+" touches a sensitive path (.lea/sensitive), so it goes to your tower: ", "$lea_hit") + "; }\n" +
		"fi\nset +f\n"
	return s
}

// pushPolicyScript is the script lines that end a pinned push to branch (the agent's own) of the
// GitHub repository gh (github.com/OWNER/NAME) in a tower's tree (#110) when branch is a
// production branch: the repository's default branch, main or master, or a branch .lea/production
// names on the default branch (the trusted list) or on branch itself. A push there would skip the
// whole production-merge policy (window, CI, review, sensitive paths) and could rewrite the
// trusted list. Every read fails closed: a .lea/production is missing only on an HTTP 404 (a
// branch the remote does not have yet answers 404 too); any other error refuses the push.
func pushPolicyScript(branch, gh string) string {
	repo := strings.TrimPrefix(gh, "github.com/")
	why := "a push to the production branch " + branch + " never runs in a tower's tree: it would skip the production-merge policy " +
		"(push to a branch of your own and merge through the tree's merge grant), so the push is refused"
	q := shellWords([]string{branch})
	return "set -f\nlea_nl='\n'\nlea_cr=$(printf '\\r')\n" +
		forgeReads(repo) +
		"lea_dst=" + q + "\n" +
		"case \"$lea_dst\" in main|master) " + refuse(why) + ";; esac\n" +
		"[ \"$lea_dst\" = \"$lea_def\" ] && { " + refuse(why+" (the repository's default branch)") + "; }\n" +
		"lea_get .lea/production \"$lea_def\"\n" +
		"[ $? = 2 ] && { " + refuseQuoted("cannot read .lea/production (not a 404) on the default branch ", "$lea_def") + "; }\n" +
		"lea_list \"$lea_got\"\n" +
		"lea_names \"$lea_norm\" \"$lea_dst\" && { " + refuse(why+" (.lea/production on the default branch names it)") + "; }\n" +
		"lea_get .lea/production \"$lea_dst\"\n" +
		"[ $? = 2 ] && { " + refuseQuoted("cannot read .lea/production (not a 404) on the branch ", "$lea_dst") + "; }\n" +
		"lea_list \"$lea_got\"\n" +
		"lea_names \"$lea_norm\" \"$lea_dst\" && { " + refuse(why+" (its own .lea/production names it)") + "; }\n" +
		"set +f\n"
}

// forgeReads is the script lines, after "set -f" and lea_nl, lea_cr, that read the forge for the
// production-branch checks of the GitHub repository repo (OWNER/NAME): lea_t (removed on EXIT),
// the functions lea_get, lea_list and lea_names, and lea_def, the default branch (any error
// refuses).
func forgeReads(repo string) string {
	content := shellWords([]string{ghPath, "api", "-X", "GET", "-H", "Accept: application/vnd.github.raw", "repos/" + repo + "/contents/"})
	trim := "while :; do case \"$lea_b\" in [[:space:]]*) lea_b=${lea_b#?};; *[[:space:]]) lea_b=${lea_b%?};; *) break;; esac; done"
	return "" +
		// The stderr of each forge read, to tell a 404 from any other error.
		"lea_t=$(" + shellWords([]string{mktempPath}) + ") || exit 1\n" +
		"trap " + shellWords([]string{rmPath + ` -f "$lea_t"`}) + " EXIT\n" +
		// lea_get PATH REF: lea_got is the file; returns 0, 1 on a 404 (no file), 2 on any other error.
		"lea_get() {\n" +
		"  lea_got=$(" + content + `"$1" -f "ref=$2" 2>"$lea_t") && return 0` + "\n" +
		"  lea_got=\n" +
		"  " + shellWords([]string{grepPath, "-qF", "(HTTP 404)"}) + ` "$lea_t" && return 1` + "\n" +
		"  return 2\n}\n" +
		// lea_list TEXT: lea_norm is its lines, CR and surrounding blanks cut, blank and # lines dropped.
		"lea_list() {\n  lea_norm=\n  IFS=$lea_nl\n  for lea_b in $1; do\n" +
		"    lea_b=${lea_b%\"$lea_cr\"}\n    " + trim + "\n" +
		"    case \"$lea_b\" in ''|'#'*) continue;; esac\n" +
		"    lea_norm=\"$lea_norm$lea_b$lea_nl\"\n  done\n  unset IFS\n}\n" +
		// lea_names LIST BRANCH: whether the normalised LIST names BRANCH.
		"lea_names() {\n  IFS=$lea_nl\n  for lea_b in $1; do [ \"$lea_b\" = \"$2\" ] && { unset IFS; return 0; }; done\n  unset IFS\n  return 1\n}\n" +
		"lea_def=$(" + shellWords([]string{ghPath, "api", "repos/" + repo, "--jq", ".default_branch"}) + ") || { " +
		refuse("cannot read the default branch of "+repo+": lea's production-branch checks need it") + "; }\n" +
		`case "$lea_def" in ''|*[[:space:]]*) ` + refuse("cannot read the default branch of "+repo+": lea's production-branch checks need it") + ";; esac\n"
}

// hexSHA says whether s is a full git commit id (SHA-1 or SHA-256), lowercase hex.
func hexSHA(s string) bool {
	if len(s) != 40 && len(s) != 64 {
		return false
	}
	for i := 0; i < len(s); i++ {
		if !(s[i] >= '0' && s[i] <= '9' || s[i] >= 'a' && s[i] <= 'f') {
			return false
		}
	}
	return true
}

// HexSHA is hexSHA for the daemon (review_approve).
func HexSHA(s string) bool { return hexSHA(s) }

// MergeNumber is the PR or MR number a merge request names ("" when it names a branch).
func MergeNumber(req Request) string {
	if req.Action != Merge || len(req.Args) < 3 || !number(req.Args[2]) {
		return ""
	}
	if _, err := strconv.Atoi(req.Args[2]); err != nil {
		return ""
	}
	return req.Args[2]
}

var (
	// mergeCLIRe is a gh or glab merge in one simple command (gh pr merge, gh pr -R x merge, glab
	// mr merge, and a gh alias set to one).
	mergeCLIRe = regexp.MustCompile(`(?i)\b(gh|glab)\b.*\b(pr|mr)\b.*\bmerge\b`)
	// mergeAPIRe is a merge through a forge's API, by any program: the REST merge of a PR or MR,
	// the merge of two branches (POST /repos/O/R/merges), GraphQL's mergePullRequest, mergeBranch
	// and auto-merge, GitLab's merge when the pipeline succeeds.
	// Bitbucket's and Gitea's merges too (pullrequests/N/merge, pulls/N/merge), whatever lies
	// between the PR and merge in the path.
	mergeAPIRe = regexp.MustCompile(`(?i)(pulls|pullrequests|merge_requests)/\S*merge\b|/merges\b|mergepullrequest|automerge|mergebranch|merge_when_pipeline_succeeds`)
	// graphQLCallRe is a GraphQL call by a forge CLI (gh api graphql, glab api graphql); graphQLURLRe
	// a GraphQL endpoint's URL, by any program (curl, wget, a script).
	graphQLCallRe = regexp.MustCompile(`(?i)\b(gh|glab)\b.*\bapi\b.*\bgraphql\b`)
	graphQLURLRe  = regexp.MustCompile(`(?i)//\S*/graphql\b|\bapi\.github\.com/graphql\b`)
	// requestFileRe is a request lea cannot read from the command: a body or field from a file or
	// stdin (@file, @-, --input, -T, --upload-file, wget's --post-file and --body-file).
	requestFileRe = regexp.MustCompile(`@|--input\b|(?:^|\s)-[a-zA-Z]*T|--upload-file\b|--post-file\b|--body-file\b`)
	// optionsFileRe is an API call whose options (URL, method, body) come from a file: curl's
	// -K/--config, wget's -i/--input-file and -e/--execute.
	optionsFileRe = regexp.MustCompile(`(?i)\bcurl\b.*((?:^|\s)-[a-zA-Z]*K|--config\b)|\bwget\b.*((?:^|\s)-[a-zA-Z]*[ie]|--input-file\b|--execute\b)`)
	segmentRe     = regexp.MustCompile(`[;&|\n()]+`)
	// apiCallRe is a call of a forge's HTTP API from the shell: gh api, glab api, curl, wget, xh.
	apiCallRe = regexp.MustCompile(`(?i)\b(gh|glab)\b.*\bapi\b|\b(curl|wget|xh)\b`)
	// apiMethodRe is the method an API call names: gh's -X/--method, curl's -X (also in a group of
	// short flags, -sXPUT) and --request, wget's --method.
	apiMethodRe = regexp.MustCompile(`(?:^|\s)-[a-zA-Z]*X\s*(\S+)|--(?:method|request)(?:=|\s+)(\S+)`)
	// apiBodyRe is a body an API call sends, which makes it a write without a method: gh's -f/-F,
	// --field, --raw-field and --input (gh api then POSTs), curl's -d/-F/-T and --data*, --json,
	// --form, --upload-file, wget's --post-* and --body-*.
	apiBodyRe = regexp.MustCompile(`(?:^|\s)(?:-[fF]|-[a-zA-Z]*[dFT])|--(?:raw-)?field\b|--input\b|--data|--json\b|--form\b|` +
		`--upload-file\b|--post-(?:data|file)\b|--body-(?:data|file)\b`)
	// branchWriteRe is a forge write that moves a branch or rewrites a file on one without a push
	// or a merge: the REST contents API (PUT, DELETE a file), git refs (create, update, delete),
	// a fork's merge-upstream, and the GraphQL mutations that do the same.
	branchWriteRe = regexp.MustCompile(`repos/[^/\s]+/[^/\s]+/(contents(/|\b)|git/refs\b|merge-upstream\b)|` +
		`\b(createref|updateref|updaterefs|deleteref|createcommitonbranch)\b`)
	// branchMCPRe is an MCP tool named for such a write (create_or_update_file, push_files,
	// delete_file, create_branch, update_ref…).
	branchMCPRe = regexp.MustCompile(`(?i)(create|update|delete|push|put|write).*(file|ref|branch|commit)|(file|ref|branch|commit).*(create|update|delete|push|put|write)`)
)

// forgeText is cmd as the forge forwarder routes it: quotes and backslashes removed and percent
// escapes decoded (until none is left, so a double escape is caught too). The forwarder decodes
// each path segment (url.PathUnescape) and lower-cases it, so pull%73/7/merge is the REST merge
// of PR 7; the regular expressions matched against this text are case-insensitive.
func forgeText(cmd string) string {
	s := unquote.Replace(cmd)
	for i := 0; i < 8; i++ {
		d := percentDecode(s)
		if d == s {
			break
		}
		s = d
	}
	return s
}

// percentDecode decodes every valid %XX escape of s once, leaving the rest as it is.
func percentDecode(s string) string {
	if !strings.Contains(s, "%") {
		return s
	}
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		if s[i] == '%' && i+2 < len(s) && isHex(s[i+1]) && isHex(s[i+2]) {
			v, _ := strconv.ParseUint(s[i+1:i+3], 16, 8)
			b.WriteByte(byte(v))
			i += 2
			continue
		}
		b.WriteByte(s[i])
	}
	return b.String()
}

func isHex(c byte) bool { return c >= '0' && c <= '9' || c >= 'a' && c <= 'f' || c >= 'A' && c <= 'F' }

// apiWrite says whether text (forgeText of a command) sends a write: a method other than GET or
// HEAD (an expanded method, $M, counts as a write), or, without a method, a body.
func apiWrite(text string) bool {
	ms := apiMethodRe.FindAllStringSubmatch(text, -1)
	for _, m := range ms {
		v := strings.ToUpper(m[1] + m[2])
		if v != "GET" && v != "HEAD" {
			return true
		}
	}
	return len(ms) == 0 && apiBodyRe.MatchString(text)
}

// unquotedExpansion says whether raw holds a $ or a backquote outside quotes: an expansion the
// shell splits into words, so that it can add a method, a field or a path lea does not see.
func unquotedExpansion(raw string) bool {
	single, double := false, false
	for i := 0; i < len(raw); i++ {
		switch c := raw[i]; {
		case single:
			single = c != '\''
		case c == '\\':
			i++
		case c == '\'' && !double:
			single = true
		case c == '"':
			double = !double
		case (c == '$' || c == '`') && !double:
			return true
		}
	}
	return false
}

// ghAliasImportRe is gh alias import: aliases (a merge among them) from a file lea cannot read.
var ghAliasImportRe = regexp.MustCompile(`(?i)\bgh\b.*\balias\b.*\bimport\b`)

// shellExpansion says whether raw holds a $ or a backquote outside single quotes: an expansion
// whose value lea does not see (unquotedExpansion also lets double-quoted ones through).
func shellExpansion(raw string) bool {
	single, double := false, false
	for i := 0; i < len(raw); i++ {
		switch c := raw[i]; {
		case single:
			single = c != '\''
		case c == '\\':
			i++
		case c == '\'' && !double:
			single = true
		case c == '"':
			double = !double
		case c == '$' || c == '`':
			return true
		}
	}
	return false
}

// nonClientGraphQL says whether a segment of cmd names a GraphQL endpoint's URL without being a
// forge CLI or an HTTP client's call (python -c, node -e…: they build a request lea cannot read).
func nonClientGraphQL(cmd string) bool {
	for _, seg := range segmentRe.Split(cmd, -1) {
		if graphQLURLRe.MatchString(seg) && !apiCallRe.MatchString(seg) {
			return true
		}
	}
	return false
}

// MergeAttempt says whether a tool call looks like a merge of a PR or MR, in any form: gh pr
// merge, glab mr merge, a REST or GraphQL merge (gh api, curl…, percent-escaped or not), a
// GraphQL call lea cannot read literally (an expansion, a query from a file or stdin, an escape,
// a program other than a forge CLI or an HTTP client), an API call whose options come from a file
// (curl -K, wget -i), an API write whose path or method holds an expansion (lea cannot tell it is
// not a merge), gh alias import, or an MCP tool named for a merge (lea's own tools aside). Loose on
// purpose: in a tower's tree a merge runs only as a grant pins it (with the production-merge
// policy), and the daemon denies every other merge this matches. Defence in depth: a shell can
// build any word (hardBash denies the obfuscated spellings it sees), and an alias the agent set
// in its gh config, or a script file it runs, is not seen.
func MergeAttempt(tool string, input json.RawMessage) bool {
	if strings.HasPrefix(tool, "mcp__") {
		return !strings.HasPrefix(tool, "mcp__lea__") && strings.Contains(strings.ToLower(tool), "merge")
	}
	raw, ok := bashCommand(tool, input)
	if !ok {
		return false
	}
	cmd := forgeText(raw)
	if mergeAPIRe.MatchString(cmd) {
		return true
	}
	segs := segmentRe.Split(cmd, -1)
	for _, seg := range segs {
		if mergeCLIRe.MatchString(seg) || ghAliasImportRe.MatchString(seg) {
			return true
		}
		if apiCallRe.MatchString(seg) && optionsFileRe.MatchString(seg) {
			return true // curl -K, wget -i: lea cannot read where it goes nor what it sends
		}
	}
	// A GraphQL call is a merge unless lea reads its whole request in the command: no expansion
	// (even double-quoted: "$Q" may hold any mutation), no query from a file or stdin, no escape
	// (a JSON \u006d spells the m of a mutation), and a forge CLI or a plain HTTP client sends it
	// (any other program builds a request lea cannot read). Its literal text was matched above.
	if graphQLCallRe.MatchString(cmd) || graphQLURLRe.MatchString(cmd) {
		if shellExpansion(raw) || strings.Contains(raw, `\`) || requestFileRe.MatchString(cmd) ||
			!apiCallRe.MatchString(cmd) || nonClientGraphQL(cmd) {
			return true
		}
	}
	// An API call with an expansion: its path may be a merge's (pulls/7/${M}). A read whose
	// expansions are all double-quoted (one word each, no method or field hidden in them) is not.
	if shellExpansion(raw) && apiCallRe.MatchString(cmd) && (apiWrite(cmd) || unquotedExpansion(raw)) {
		return true
	}
	return false
}

// BranchWrite is why a tool call is a forge write that moves a branch or rewrites a file on one
// without a push or a merge, "" when it is not: a REST contents write (PUT or DELETE
// repos/O/R/contents/…), a git refs create, update or delete (repos/O/R/git/refs…), a
// merge-upstream, a GraphQL ref or commit mutation, or an MCP tool named for one. In a tower's
// tree the daemon denies it (#110): it would rewrite the trusted .lea/production on the default
// branch, or move a production branch to a commit no window, CI, review or sensitive check saw.
// A gh api, curl or wget call that only reads (GET, HEAD) passes; any other program naming such
// a path is taken for a write (lea cannot read what it sends).
func BranchWrite(tool string, input json.RawMessage) string {
	const why = "a forge write that rewrites a file on a branch or moves a branch (the contents API, git refs, merge-upstream) never runs " +
		"in a tower's tree: it would skip the production-merge policy; push to your own branch and merge through the tree's merge grant"
	if strings.HasPrefix(tool, "mcp__") {
		if !strings.HasPrefix(tool, "mcp__lea__") && branchMCPRe.MatchString(tool) {
			return why
		}
		return ""
	}
	raw, ok := bashCommand(tool, input)
	if !ok {
		return ""
	}
	for _, seg := range segmentRe.Split(forgeText(raw), -1) {
		if !branchWriteRe.MatchString(strings.ToLower(seg)) {
			continue
		}
		if !apiCallRe.MatchString(seg) || apiWrite(seg) || unquotedExpansion(raw) {
			return why
		}
	}
	return ""
}

// bashCommand is the command of a Bash tool call.
func bashCommand(tool string, input json.RawMessage) (string, bool) {
	if tool != "Bash" {
		return "", false
	}
	var in struct {
		Command string `json:"command"`
	}
	if json.Unmarshal(input, &in) != nil {
		return "", false
	}
	return in.Command, true
}
