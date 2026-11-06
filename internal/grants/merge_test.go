package grants

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestParseWindowAndOpen(t *testing.T) {
	w, err := ParseWindow("mon-sun 21:30-23:59 Europe/Brussels")
	if err != nil {
		t.Fatal(err)
	}
	bxl, _ := time.LoadLocation("Europe/Brussels")
	for _, tc := range []struct {
		at   time.Time
		open bool
	}{
		{time.Date(2026, 11, 6, 21, 29, 0, 0, bxl), false},
		{time.Date(2026, 11, 6, 21, 30, 0, 0, bxl), true},
		{time.Date(2026, 11, 6, 23, 59, 30, 0, bxl), true},
		{time.Date(2026, 11, 7, 0, 0, 0, 0, bxl), false},
		{time.Date(2026, 11, 6, 20, 45, 0, 0, time.UTC), true}, // 21:45 in Brussels
	} {
		if got := w.Open(tc.at); got != tc.open {
			t.Fatalf("%s: open=%v", tc.at, got)
		}
	}
	// Past midnight, weekdays only: Friday 23:00 to Saturday 02:00 is Friday's.
	w, err = ParseWindow("mon-fri 22:00-02:00 UTC")
	if err != nil {
		t.Fatal(err)
	}
	fri := time.Date(2026, 11, 6, 23, 0, 0, 0, time.UTC)
	if !w.Open(fri) || !w.Open(fri.Add(2*time.Hour)) || w.Open(fri.Add(3*time.Hour)) || w.Open(fri.Add(24*time.Hour)) {
		t.Fatal("past-midnight window")
	}
	if w, err := ParseWindow("sat,sun,wed 10:00-11:00 UTC"); err != nil || !w.Days[time.Wednesday] || w.Days[time.Monday] || !w.Days[time.Saturday] {
		t.Fatalf("list: %+v %v", w, err)
	}
	if w, err := ParseWindow("fri-mon 10:00-11:00 UTC"); err != nil || !w.Days[time.Sunday] || w.Days[time.Wednesday] {
		t.Fatalf("wrapping range: %+v %v", w, err)
	}
	for _, bad := range []string{"", "mon-sun 21:30-23:59", "mon-sun 25:00-26:00 UTC", "xyz 10:00-11:00 UTC", "mon 10:00-10:00 UTC",
		"mon 10:00-11:00 Mars/Olympus", "mon 10:00-11:00 Local", "mon  10:00-11:00 UTC", "mon 10:00-11:00 UTC; rm -rf /", strings.Repeat("m", 200)} {
		if _, err := ParseWindow(bad); err == nil {
			t.Fatalf("%q accepted", bad)
		}
	}
}

// fakeGH answers the policy's gh calls from files in its directory: pr.json (the PR view), the
// production and sensitive files (absent: a 404), files.txt (the API's files, jq-applied by the
// fake as the policy expects: one "F path" line per file and the added lines).
const fakeGH = `#!/bin/sh
d=$(dirname "$0")
echo "$*" >> "$d/gh.log"
case "$*" in
  "pr view 7 --json baseRefName,headRefOid "*) cat "$d/base.txt" ;;
  "pr view 7 --json statusCheckRollup "*) cat "$d/ci.txt" ;;
  "api -H Accept: application/vnd.github.raw repos/someone/shop/contents/.lea/production") [ -f "$d/production" ] && cat "$d/production" || exit 1 ;;
  "api -X GET -H Accept: application/vnd.github.raw repos/someone/shop/contents/.lea/sensitive -f ref="*) [ -f "$d/sensitive" ] && cat "$d/sensitive" || exit 1 ;;
  "api --paginate repos/someone/shop/pulls/7/files "*) cat "$d/files.txt" ;;
  *) echo "unexpected gh $*" >&2; exit 3 ;;
esac
`

const fakeLea = `#!/bin/sh
d=$(dirname "$0")
{ echo "args $*"; echo "agent $LEA_AGENT"; cat; } > "$d/lea.log"
`

type policyCase struct {
	base, ci, files, production, sensitive string
	policy                                 MergePolicy
}

const headSHA = "0123456789abcdef0123456789abcdef01234567"

func runPolicy(t *testing.T, c policyCase) (out string, merged bool, dir string) {
	t.Helper()
	for _, p := range []string{shPath, envPath, grepPath, headPath} {
		if _, err := os.Stat(p); err != nil {
			t.Skipf("%s: %v", p, err)
		}
	}
	dir = t.TempDir()
	write := func(name, s string, mode os.FileMode) {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(s), mode); err != nil {
			t.Fatal(err)
		}
	}
	write("gh", fakeGH, 0o755)
	write("lea", fakeLea, 0o755)
	write("base.txt", c.base+" "+headSHA+"\n", 0o644)
	write("ci.txt", c.ci+"\n", 0o644)
	write("files.txt", c.files, 0o644)
	if c.production != "" {
		write("production", c.production, 0o644)
	}
	if c.sensitive != "" {
		write("sensitive", c.sensitive, 0o644)
	}
	script := mergePolicyScript("7", "github.com/someone/shop", c.policy) + `echo "MERGED $lea_head"` + "\n"
	script = strings.ReplaceAll(script, "'"+ghPath+"'", "'"+filepath.Join(dir, "gh")+"'")
	script = strings.ReplaceAll(script, "'"+leaPath+"'", "'"+filepath.Join(dir, "lea")+"'")
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	b, _ := exec.CommandContext(ctx, shPath, "-c", script).CombinedOutput()
	out = string(b)
	return out, strings.Contains(out, "MERGED "+headSHA), dir
}

func files(paths ...string) string {
	var b strings.Builder
	for _, p := range paths {
		b.WriteString(p + "\n")
	}
	return b.String()
}

func TestMergePolicyScript(t *testing.T) {
	open := MergePolicy{Lead: "lead-shop", Window: "mon-sun 21:30-23:59 Europe/Brussels", Open: true, Approved: []string{headSHA}, Agent: "dev-1"}
	closed := open
	closed.Open = false
	green := "green"
	plain := files("F src/cart.go", "+func total() int { return 1 }")
	for _, tc := range []struct {
		name   string
		c      policyCase
		merged bool
		want   string
	}{
		{"not production: no check", policyCase{base: "develop", ci: "not green", files: plain, policy: closed}, true, ""},
		{"outside the window", policyCase{base: "main", ci: green, files: plain, policy: closed}, false, "outside the deploy window of lead-shop"},
		{"no window", policyCase{base: "main", ci: green, files: plain, policy: MergePolicy{Lead: "lead-shop"}}, false, "has no deploy window"},
		{"low risk inside the window", policyCase{base: "main", ci: green, files: plain, policy: open}, true, ""},
		{"a production file names the branch", policyCase{base: "release", ci: green, files: plain, production: "release\n", policy: closed}, false, "outside the deploy window"},
		{"main is not production when the file says so", policyCase{base: "main", ci: "not green", files: plain, production: "release\n", policy: closed}, true, ""},
		{"no approval", policyCase{base: "main", ci: green, files: plain, policy: MergePolicy{Lead: "l", Window: "w", Open: true}}, false, "no reviewer agent approved"},
		{"approval of another head", policyCase{base: "main", ci: green, files: plain,
			policy: MergePolicy{Lead: "l", Window: "w", Open: true, Approved: []string{strings.Repeat("a", 40)}}}, false, "at its head " + headSHA},
		{"CI red", policyCase{base: "main", ci: "not green", files: plain, policy: open}, false, "is not green"},
		{"default sensitive path", policyCase{base: "main", ci: green, files: files("F src/auth/login.go", "+x"), policy: open}, false, "sensitive path (.lea/sensitive), so it goes to your tower: src/auth/login.go"},
		{"the repository's sensitive list", policyCase{base: "main", ci: green, files: files("F billing/x.go"), sensitive: "# ours\nbilling/*\n", policy: open}, false, "billing/x.go"},
		{"the repository's list replaces the defaults", policyCase{base: "main", ci: green, files: files("F src/auth/x.go"), sensitive: "billing/*\n", policy: open}, true, ""},
		{"lea's own files always", policyCase{base: "main", ci: green, files: files("F .lea/sensitive"), sensitive: "billing/*\n", policy: open}, false, ".lea/sensitive"},
		{"a glob in a file name is not expanded", policyCase{base: "main", ci: green, files: files("F *"), sensitive: "billing/*\n", policy: open}, true, ""},
		{"irreversible migration", policyCase{base: "main", ci: green, files: files("F db/0042.sql", "+ALTER TABLE orders DROP COLUMN note;"), sensitive: "nothing/*\n", policy: open},
			false, "irreversible migration"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			out, merged, dir := runPolicy(t, tc.c)
			if merged != tc.merged || tc.want != "" && !strings.Contains(out, tc.want) {
				t.Fatalf("merged=%v out:\n%s", merged, out)
			}
			log, _ := os.ReadFile(filepath.Join(dir, "lea.log"))
			if tc.name == "irreversible migration" {
				if !strings.Contains(string(log), "args hook lea merge-blocked 7") || !strings.Contains(string(log), "agent dev-1") ||
					!strings.Contains(string(log), "DROP COLUMN note") {
					t.Fatalf("lea was not told: %q", log)
				}
			} else if len(log) != 0 {
				t.Fatalf("lea told of a merge: %q", log)
			}
		})
	}
}

// Command wraps the merge in the policy for a caller of a tower's tree: the merge names the head
// commit it checked, and a branch-named PR is not pinned.
func TestCommandAppliesTheMergePolicy(t *testing.T) {
	c := dev
	c.Worktree = "/p/.lea/worktrees/shop/dev-2"
	c.Merge = &MergePolicy{Lead: "l", Open: true, Approved: []string{headSHA}, Agent: "dev-2"}
	g := all(Merge)
	g.GitHub = "github.com/someone/shop"
	cmd := Command(Request{Action: Merge, Prog: "gh", Args: []string{"pr", "merge", "7", "--squash"}}, c, g)
	if !strings.Contains(cmd, "lea_isprod") || !strings.Contains(cmd, `--match-head-commit "$lea_head"`) || !strings.Contains(cmd, headSHA) {
		t.Fatalf("policy missing: %s", cmd)
	}
	if strings.Index(cmd, "headRefName,isCrossRepository") > strings.Index(cmd, "lea_isprod") {
		t.Fatal("the policy runs before the head check")
	}
	if cmd := Command(Request{Action: Merge, Prog: "gh", Args: []string{"pr", "merge", c.Branch}}, c, g); cmd != "" {
		t.Fatalf("a branch-named merge was pinned under the policy: %s", cmd)
	}
	c.Merge = nil
	if cmd := Command(Request{Action: Merge, Prog: "gh", Args: []string{"pr", "merge", "7"}}, c, g); cmd == "" || strings.Contains(cmd, "lea_isprod") {
		t.Fatalf("outside a tower's tree: %s", cmd)
	}
	if MergeNumber(Request{Action: Merge, Args: []string{"pr", "merge", "7"}}) != "7" || MergeNumber(Request{Action: Merge, Args: []string{"pr", "merge", "x"}}) != "" {
		t.Fatal("MergeNumber")
	}
}
