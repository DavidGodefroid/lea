package grants

import (
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
// touches one goes to the tower, never merged into production by a lead alone.
var DefaultSensitive = []string{"*migration*", "*migrate*", "*schema*", "*auth*", "*login*", "*password*", "*payment*", "*billing*",
	"*checkout*", "*secret*", "*credential*", "*.env", "*.env.*", "*.pem", "*.key", "infra/*", "terraform/*", "*.tf", "k8s/*",
	"helm/*", "deploy/*", ".github/workflows/*", ".gitlab-ci.yml", "Dockerfile*", "docker-compose*", "compose*.yaml", "compose*.yml"}

// alwaysSensitive are sensitive whatever .lea/sensitive says: the repository's own lea policy.
var alwaysSensitive = []string{".lea/*"}

// irreversibleRe is what an added line of a PR's diff holds when it drops or renames a table or
// a column, or destroys data: a POSIX ERE, case-insensitive (grep -iE).
const irreversibleRe = `^\+.*(drop[[:space:]]+(table|column|schema|database|collection)|rename[[:space:]]+(table|column|to)|` +
	`truncate[[:space:]]|delete[[:space:]]+from|alter[[:space:]]+table.*[[:space:]]drop[[:space:]]|dropcolumn|droptable|drop_table|` +
	`remove_column|rename_column|rename_table|dropindex\(|\.drop\()`

// Paths of the programs the policy runs inside the container.
const (
	grepPath = "/bin/grep"
	headPath = "/usr/bin/head"
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
// github.com/OWNER/NAME pin) unless, when its base is a production branch (.lea/production on the
// default branch, one per line; main and master without it), the lead's window is open, a
// reviewer approved its head, its CI is green on that head, it holds no irreversible migration
// (then the daemon warns the tower) and it touches no sensitive path (.lea/sensitive on the base
// branch, shell patterns one per line; DefaultSensitive without it). It sets lea_head, the head
// commit checked: the merge names it (--match-head-commit), so a push after the checks fails it.
func mergePolicyScript(n, gh string, p MergePolicy) string {
	repo := strings.TrimPrefix(gh, "github.com/")
	view := func(fields, jq string) string {
		return shellWords([]string{ghPath, "pr", "view", n, "--json", fields, "--jq", jq, "-R", gh})
	}
	raw := []string{"-H", "Accept: application/vnd.github.raw"}
	s := "set -f\nlea_nl='\n'\n" +
		"lea_pr=$(" + view("baseRefName,headRefOid", `.baseRefName + " " + .headRefOid`) + ") || exit 1\n" +
		`lea_base=${lea_pr% *} lea_head=${lea_pr##* }` + "\n" +
		`case "$lea_head" in ''|*[!0-9a-f]*) ` + refuse("PR "+n+" has no head commit") + ";; esac\n" +
		"lea_prod=$(" + shellWords(append(append([]string{ghPath, "api"}, raw...), "repos/"+repo+"/contents/.lea/production")) + " 2>/dev/null) || lea_prod='main" + "\n" + "master'\n" +
		"lea_isprod=0\nIFS=$lea_nl\nfor lea_b in $lea_prod; do [ \"$lea_b\" = \"$lea_base\" ] && lea_isprod=1; done\nunset IFS\n" +
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
	s += "  lea_ci=$(" + view("statusCheckRollup", `[.statusCheckRollup[] | (.conclusion // .state // "")] | if length == 0 then "none" elif all(. == "SUCCESS" or . == "NEUTRAL" or . == "SKIPPED") then "green" else "not green" end`) + ") || exit 1\n" +
		`  [ "$lea_ci" = green ] || { ` + refuseQuoted("CI of PR "+n+" is not green on its head: ", "$lea_ci") + "; }\n" +
		"  lea_files=$(" + shellWords([]string{ghPath, "api", "--paginate", "repos/" + repo + "/pulls/" + n + "/files", "--jq", `.[] | "F " + .filename, (.patch // "" | split("\n")[] | select(startswith("+")))`}) + ") || exit 1\n" +
		"  lea_irr=$(printf '%s\\n' \"$lea_files\" | " + shellWords([]string{grepPath, "-iE", irreversibleRe}) + " | " + shellWords([]string{headPath, "-n", "8"}) + ")\n" +
		"  if [ -n \"$lea_irr\" ]; then\n" +
		"    printf '%s\\n' \"$lea_irr\" | " + shellWords([]string{envPath, "-i", "PATH=/usr/bin:/bin", "LEA_AGENT=" + p.Agent, "LEA_BUS_SOCK=" + busSock,
		leaPath, "hook", "lea", "merge-blocked", n}) + " >/dev/null 2>&1\n" +
		"    " + refuse("PR "+n+" holds an irreversible migration (a dropped or renamed table or column, or destroyed data): it is never merged without the user's explicit approval; lea warned your tower") + "\n" +
		"  fi\n" +
		"  lea_sens=$(" + shellWords(append(append([]string{ghPath, "api", "-X", "GET"}, raw...), "repos/"+repo+"/contents/.lea/sensitive")) + ` -f "ref=$lea_base" 2>/dev/null) || lea_sens=` +
		shellWords([]string{strings.Join(DefaultSensitive, "\n")}) + "\n" +
		"  lea_sens=\"$lea_sens$lea_nl\"" + shellWords([]string{strings.Join(alwaysSensitive, "\n")}) + "\n" +
		"  lea_hit=\n  IFS=$lea_nl\n" +
		"  for lea_f in $lea_files; do case \"$lea_f\" in 'F '*) lea_f=${lea_f#??};; *) continue;; esac; for lea_p in $lea_sens; do case \"$lea_p\" in '#'*) continue;; esac\n" +
		"    case \"$lea_f\" in $lea_p) lea_hit=$lea_f; break 2;; esac; done; done\n" +
		"  unset IFS\n" +
		`  [ -z "$lea_hit" ] || { ` + refuseQuoted("PR "+n+" touches a sensitive path (.lea/sensitive), so it goes to your tower: ", "$lea_hit") + "; }\n" +
		"fi\nset +f\n"
	return s
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
