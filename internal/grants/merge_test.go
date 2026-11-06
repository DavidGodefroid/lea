package grants

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
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

// fakeGH answers the policy's gh calls from files in its directory: base.txt (the PR view),
// default.txt (the repository's default branch), the .lea files (production.<ref> or
// sensitive.<ref> on that ref, else production or sensitive on any ref; absent: a 404, as gh
// reports it), files.txt (the API's files, jq-applied by the fake as the policy expects: "F path"
// per file, "P path" per previous name, "N path" per file without a patch, and the added lines),
// count.txt (the per-page counts: "files names-with-a-control-character").
// err.<what> makes that call fail with a non-404 error (err.default, err.production.<ref>,
// err.sensitive.<ref>).
const fakeGH = `#!/bin/sh
d=$(dirname "$0")
echo "$*" >> "$d/gh.log"
lea_file() {
  ref=${2##*ref=}
  if [ -f "$d/err.$1.$ref" ]; then echo "gh: Server Error (HTTP 502)" >&2; exit 1; fi
  if [ -f "$d/$1.$ref" ]; then cat "$d/$1.$ref"; elif [ -f "$d/$1" ]; then cat "$d/$1"
  else echo '{"message":"Not Found"}'; echo "gh: Not Found (HTTP 404)" >&2; exit 1; fi
}
case "$*" in
  "pr view 7 --json baseRefName,headRefOid,changedFiles "*) cat "$d/base.txt" ;;
  "pr view 7 --json statusCheckRollup "*) cat "$d/ci.txt" ;;
  "api repos/someone/shop --jq .default_branch") [ -f "$d/err.default" ] && { echo "gh: API rate limit exceeded (HTTP 403)" >&2; exit 1; }; cat "$d/default.txt" ;;
  "api -X GET -H Accept: application/vnd.github.raw repos/someone/shop/contents/.lea/production -f ref="*) lea_file production "$*" ;;
  "api -X GET -H Accept: application/vnd.github.raw repos/someone/shop/contents/.lea/sensitive -f ref="*) lea_file sensitive "$*" ;;
  "api --paginate repos/someone/shop/pulls/7/files --jq \"\\(length) "*) cat "$d/count.txt" ;;
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
	// def is the repository's default branch ("" main).
	def string
	// productionAt and sensitiveAt are .lea files on given refs (production.<ref>).
	productionAt, sensitiveAt map[string]string
	// changed is the PR's count of changed files (0: the F lines of files).
	changed int
	// count is the per-page count lines ("" one page: the F lines of files, no odd name).
	count string
	// errs are the calls that fail with a non-404 error (fakeGH's err.<what>).
	errs   []string
	policy MergePolicy
}

const headSHA = "0123456789abcdef0123456789abcdef01234567"

func runPolicy(t *testing.T, c policyCase) (out string, merged bool, dir string) {
	t.Helper()
	for _, p := range []string{shPath, envPath, grepPath, headPath, trPath, mktempPath, rmPath} {
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
	listed := 0
	for _, l := range strings.Split(c.files, "\n") {
		if strings.HasPrefix(l, "F ") {
			listed++
		}
	}
	changed := c.changed
	if changed == 0 {
		changed = listed
	}
	count := c.count
	if count == "" {
		count = strconv.Itoa(listed) + " 0\n"
	}
	def := c.def
	if def == "" {
		def = "main"
	}
	write("gh", fakeGH, 0o755)
	write("lea", fakeLea, 0o755)
	write("base.txt", c.base+" "+headSHA+" "+strconv.Itoa(changed)+"\n", 0o644)
	write("default.txt", def+"\n", 0o644)
	write("ci.txt", c.ci+"\n", 0o644)
	write("files.txt", c.files, 0o644)
	write("count.txt", count, 0o644)
	if c.production != "" {
		write("production", c.production, 0o644)
	}
	for ref, body := range c.productionAt {
		write("production."+ref, body, 0o644)
	}
	if c.sensitive != "" {
		write("sensitive", c.sensitive, 0o644)
	}
	for ref, body := range c.sensitiveAt {
		write("sensitive."+ref, body, 0o644)
	}
	for _, e := range c.errs {
		write("err."+e, "", 0o644)
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
		// main, master and the default branch are production whatever .lea/production says.
		{"main stays production when the file names another branch", policyCase{base: "main", ci: "not green", files: plain, production: "release\n", policy: closed}, false, "outside the deploy window"},
		{"master is production", policyCase{base: "master", def: "develop", ci: green, files: plain, policy: closed}, false, "outside the deploy window"},
		{"the default branch is production", policyCase{base: "develop", def: "develop", ci: green, files: plain, production: "release\n", policy: closed}, false, "outside the deploy window"},
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
		// .lea/production that names no branch, or names it with a CR, is no list: main stays
		// production; the base branch's file applies, not another branch's.
		{"an empty production file", policyCase{base: "main", ci: "not green", files: plain, productionAt: map[string]string{"main": "\n"}, policy: closed}, false, "outside the deploy window"},
		{"a production file of comments only", policyCase{base: "main", ci: "not green", files: plain, productionAt: map[string]string{"main": "# none\n\n  \n"}, policy: closed}, false, "outside the deploy window"},
		{"a CRLF production file", policyCase{base: "main", ci: "not green", files: plain, productionAt: map[string]string{"main": "main\r\n"}, policy: closed}, false, "outside the deploy window"},
		{"a CRLF production file naming another branch", policyCase{base: "release", ci: green, files: plain, productionAt: map[string]string{"release": "# prod\r\n release \r\n"}, policy: closed}, false, "outside the deploy window"},
		{"the default branch's production file does not apply", policyCase{base: "main", ci: "not green", files: plain,
			productionAt: map[string]string{"develop": "\n", "main": "main\n"}, policy: closed}, false, "outside the deploy window"},
		// The default branch's list is the trusted one: a base branch cannot drop its own protection.
		{"the default branch's list names the base, the base's file is missing", policyCase{base: "release", def: "develop", ci: green, files: plain,
			productionAt: map[string]string{"develop": "release\n"}, policy: open}, false, "missing or differs"},
		{"the default branch's list names the base, the base's file differs", policyCase{base: "release", def: "develop", ci: green, files: plain,
			productionAt: map[string]string{"develop": "release\n", "release": "staging\n"}, policy: open}, false, "missing or differs"},
		{"the base's file removed its own name", policyCase{base: "release", def: "develop", ci: green, files: plain,
			productionAt: map[string]string{"develop": "release\nstaging\n", "release": "staging\n"}, policy: open}, false, "missing or differs"},
		{"both lists agree", policyCase{base: "release", def: "develop", ci: green, files: plain,
			productionAt: map[string]string{"develop": "# prod\nrelease\n", "release": "release\r\n"}, policy: closed}, false, "outside the deploy window"},
		{"both lists agree, inside the window", policyCase{base: "release", def: "develop", ci: green, files: plain,
			productionAt: map[string]string{"develop": "release\n", "release": "release\n"}, policy: open}, true, ""},
		{"the base's own list still counts", policyCase{base: "release", def: "develop", ci: green, files: plain,
			productionAt: map[string]string{"release": "release\n"}, policy: closed}, false, "outside the deploy window"},
		{"a branch no list names is not production", policyCase{base: "staging", def: "develop", ci: "not green", files: plain,
			productionAt: map[string]string{"develop": "release\n", "release": "release\n"}, policy: closed}, true, ""},
		// Any forge error but a 404 refuses the merge, production branch or not.
		{"the default branch cannot be read", policyCase{base: "staging", ci: green, files: plain, errs: []string{"default"}, policy: open}, false, "cannot read the default branch"},
		{"the default branch's production file: an API error", policyCase{base: "staging", def: "develop", ci: green, files: plain,
			productionAt: map[string]string{"staging": "\n"}, errs: []string{"production.develop"}, policy: open}, false, "cannot read .lea/production (not a 404) on the default branch develop"},
		{"the base's production file: an API error", policyCase{base: "release", def: "develop", ci: green, files: plain,
			productionAt: map[string]string{"develop": "release\n"}, errs: []string{"production.release"}, policy: open}, false, "cannot read .lea/production (not a 404) on the base branch release"},
		{"the sensitive file: an API error", policyCase{base: "main", ci: green, files: plain, errs: []string{"sensitive.main"}, policy: open}, false, "cannot read .lea/sensitive (not a 404) on the base branch main"},
		{"the sensitive file: a 404 is the defaults", policyCase{base: "main", ci: green, files: files("F src/Payment.go"), policy: open}, false, "src/payment.go"},
		// What lea could not read fails closed.
		{"a file without a patch", policyCase{base: "main", ci: green, files: files("F db/big.sql", "N db/big.sql", "F src/x.go", "+x"), sensitive: "nothing/*\n", policy: open},
			false, "shows no diff of a file of PR 7 (large, binary or a pure rename), so lea cannot check it for an irreversible migration; it goes to your tower: db/big.sql"},
		{"a deleted file without a patch", policyCase{base: "main", ci: green, files: files("F db/old.bin"), sensitive: "nothing/*\n", policy: open}, true, ""},
		{"a truncated file list", policyCase{base: "main", ci: green, files: plain, changed: 3001, sensitive: "nothing/*\n", policy: open}, false, "files listed/changed 1/3001"},
		// The count comes from jq over the pages, never from the lines a file name could forge.
		{"pages summed", policyCase{base: "main", ci: green, files: files("F a.go", "F b.go", "F c.go"), count: "2 0\n1 0\n", sensitive: "nothing/*\n", policy: open}, true, ""},
		{"pages short of the count", policyCase{base: "main", ci: green, files: files("F a.go", "F b.go", "F c.go"), count: "2 0\n", sensitive: "nothing/*\n", policy: open},
			false, "files listed/changed 2/3"},
		{"a name with a newline forges F lines", policyCase{base: "main", ci: green, files: files("F x", "F y", "F z"), changed: 3, count: "1 1\n", sensitive: "nothing/*\n", policy: open},
			false, "holds a newline or a control character"},
		{"an odd count line", policyCase{base: "main", ci: green, files: plain, count: "1 0 7\n", policy: open}, false, "cannot count the files"},
		{"a count line without a space", policyCase{base: "main", ci: green, files: plain, count: "10\n", policy: open}, false, "cannot count the files"},
		{"a count line with a sign", policyCase{base: "main", ci: green, files: plain, count: "-1 0\n", policy: open}, false, "cannot count the files"},
		{"no file count", policyCase{base: "main", ci: green, files: plain, changed: -1, policy: open}, false, "has no count of changed files"},
		// Case-insensitive, trimmed sensitive patterns; the old name of a renamed file counts.
		{"a mixed-case default path", policyCase{base: "main", ci: green, files: files("F src/Auth/Login.go", "+x"), policy: open}, false, "src/auth/login.go"},
		{"a mixed-case Dockerfile", policyCase{base: "main", ci: green, files: files("F DOCKERFILE.prod"), policy: open}, false, "dockerfile.prod"},
		{"a mixed-case pattern", policyCase{base: "main", ci: green, files: files("F BILLING/x.go"), sensitive: "Billing/*\n", policy: open}, false, "billing/x.go"},
		{"a pattern with blanks", policyCase{base: "main", ci: green, files: files("F billing/x.go"), sensitive: "  billing/*\t \n", policy: open}, false, "billing/x.go"},
		{"lea's files, mixed case", policyCase{base: "main", ci: green, files: files("F .LEA/production"), sensitive: "nothing/*\n", policy: open}, false, ".lea/production"},
		{"a file renamed away from a sensitive path", policyCase{base: "main", ci: green, files: files("F src/x.go", "P src/auth/old.go"), policy: open}, false, "src/auth/old.go"},
		{"a CRLF sensitive list", policyCase{base: "main", ci: green, files: files("F billing/x.go"), sensitive: "# ours\r\nbilling/*\r\n", policy: open}, false, "billing/x.go"},
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

// An irreversible migration of a framework is refused and told to the tower whatever the
// repository's sensitive list says (#110, C3): one case per family.
func TestMergePolicyFrameworkMigrations(t *testing.T) {
	open := MergePolicy{Lead: "lead-shop", Window: "w", Open: true, Approved: []string{headSHA}, Agent: "dev-1"}
	for _, tc := range []struct{ family, file, line string }{
		{"SQL", "db/1.sql", "+DROP TABLE orders;"},
		{"Rails", "db/2.rb", "+    remove_column :orders, :note"},
		{"Knex", "db/3.js", "+  table.dropColumn('note')"},
		{"Alembic drop_column", "alembic/versions/4.py", "+    op.drop_column('orders', 'note')"},
		{"Alembic drop_constraint", "alembic/versions/5.py", "+    op.drop_constraint('fk', 'orders')"},
		{"Alembic rename", "alembic/versions/6.py", "+    op.alter_column('orders', 'note', new_column_name='memo')"},
		{"Django RemoveField", "shop/m/0007.py", "+        migrations.RemoveField(model_name='order', name='note'),"},
		{"Django DeleteModel", "shop/m/0008.py", "+        migrations.DeleteModel(name='Order'),"},
		{"Django RenameField", "shop/m/0009.py", "+        migrations.RenameField(model_name='order', old_name='note', new_name='memo'),"},
		{"Django RenameModel", "shop/m/0010.py", "+        migrations.RenameModel(old_name='Order', new_name='Purchase'),"},
		{"Laravel dropIfExists", "database/m/11.php", "+        Schema::dropIfExists('orders');"},
		{"Laravel drop", "database/m/12.php", "+        Schema::drop('orders');"},
		{"Laravel renameColumn", "database/m/13.php", "+            $table->renameColumn('note', 'memo');"},
		{"Laravel renameTable", "database/m/14.php", "+        Schema::renameTable('orders', 'purchases');"},
	} {
		t.Run(tc.family, func(t *testing.T) {
			out, merged, dir := runPolicy(t, policyCase{base: "main", ci: "green", files: files("F "+tc.file, tc.line), sensitive: "nothing/*\n", policy: open})
			if merged || !strings.Contains(out, "irreversible migration") {
				t.Fatalf("merged=%v out:\n%s", merged, out)
			}
			log, _ := os.ReadFile(filepath.Join(dir, "lea.log"))
			if !strings.Contains(string(log), "args hook lea merge-blocked 7") || !strings.Contains(string(log), strings.TrimPrefix(tc.line, "+")) {
				t.Fatalf("lea was not told: %q", log)
			}
		})
	}
	// Reversible framework lines are no migration of that kind.
	for _, line := range []string{"+    op.add_column('orders', sa.Column('memo'))", "+        migrations.AddField(model_name='order', name='memo'),", "+        Schema::create('orders', function ($t) {"} {
		if _, merged, _ := runPolicy(t, policyCase{base: "main", ci: "green", files: files("F src/x.py", line), sensitive: "nothing/*\n", policy: open}); !merged {
			t.Fatalf("%q refused", line)
		}
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

// MergeAttempt sees a merge in every form a tower's tree must not run without a grant, and no
// merge in commands that only read or name merges.
func TestMergeAttempt(t *testing.T) {
	bash := func(cmd string) json.RawMessage {
		b, _ := json.Marshal(map[string]string{"command": cmd})
		return b
	}
	for _, cmd := range []string{
		"gh pr merge 7", "gh pr merge 7 --squash --auto", "gh pr -R someone/shop merge 7", "cd x && gh pr merge fix/7",
		`gh pr "merge" 7`, "glab mr merge 3", "gh alias set pm 'pr merge'",
		"gh api -X PUT repos/someone/shop/pulls/7/merge", `gh api --method PUT "/repos/someone/shop/pulls/$N/merge"`,
		"curl -X PUT https://api.github.com/repos/someone/shop/pulls/7/merge", "gh api repos/someone/shop/merges -f base=main -f head=dev",
		`gh api graphql -f query='mutation { mergePullRequest(input: {pullRequestId: "x"}) { clientMutationId } }'`,
		`gh api graphql -f query='mutation { enablePullRequestAutoMerge(input: {pullRequestId: "x"}) { clientMutationId } }'`,
		"gh api graphql -F query=@q.graphql", "gh api graphql --input q.json",
		"glab api -X PUT projects/1/merge_requests/3/merge",
		// Percent escapes, as the forge forwarder decodes them (url.PathUnescape, lower-cased).
		"gh api -XPUT repos/o/r/pull%73/7/merge", "gh api -XPUT repos/o/r/pulls/7/%6d%65rge", "gh api -XPUT repos/o/r/PULLS/7/MERGE",
		"gh api -XPUT repos/o/r/pulls/7/%256derge", "curl -sXPUT https://api.github.com/repos/o/r/pull%53/7/merge",
		// An expansion in the path or the method of a write, or unquoted (it may add either).
		"gh api -XPUT repos/o/r/pulls/7/${M}", "gh api -X PUT repos/o/r/pulls/7/$(echo merge)", "gh api -X $M repos/o/r/pulls/7/x",
		"gh api repos/o/r/pulls/7/`echo merge` -f merge_method=squash", "gh api repos/o/r/pulls/7/$M", "curl $URL -d x",
		// A GraphQL call lea cannot read literally: a variable (even double-quoted), a backquote,
		// a query from a file or stdin, an escape, a curl or wget POST of a body it cannot read,
		// another program.
		`gh api graphql -f query="$Q"`, `gh api graphql -f query="it's $Q"`, `gh api graphql -f query="mutation { $(cat m) }"`, "gh api graphql -f query=`cat q`",
		`gh api graphql -F query=@q.graphql`, `gh api graphql -f query=@-`, `gh api graphql --input -`, `cat q.json | gh api graphql --input -`,
		`glab api graphql -f query="$Q"`,
		`curl -X POST https://api.github.com/graphql -d @q.json`, `curl https://api.github.com/graphql --data-binary @-`,
		`curl -s https://api.github.com/graphql -d "{\"query\": \"$Q\"}"`,
		`curl https://api.github.com/graphql -d '{"query":"\u006dutation { m\u0065rgePullRequest(input:{}) { clientMutationId } }"}'`,
		`curl -T q.json https://api.github.com/graphql`, `wget --post-file=q.json https://api.github.com/graphql`,
		`wget -O- --body-file=q.json --method=POST https://gitlab.example.com/api/graphql`,
		`python3 -c 'import requests; requests.post("https://api.github.com/graphql", json=q)'`,
		`curl -X POST https://api.github.com/graph%71l -d @q`,
		// Options from a file: curl -K, wget -i or -e.
		"curl -K req.cfg", "curl -sK req.cfg", "curl --config req.cfg", "wget -i urls.txt", "wget -e 'method=PUT' https://x",
		// Other merge endpoints.
		"curl -X POST https://api.bitbucket.org/2.0/repositories/o/r/pullrequests/7/merge",
		"gh api -X PUT repos/o/r/pulls/7//merge", "gh alias import aliases.yml",
	} {
		if !MergeAttempt("Bash", bash(cmd)) {
			t.Errorf("%q: not seen as a merge", cmd)
		}
	}
	for _, cmd := range []string{
		"gh pr view 7 --json mergeable,mergedAt", "gh pr list --search is:merged", "git merge origin/main", "git log --merges",
		"gh pr checkout 7 && git merge main", "gh api repos/someone/shop/pulls/7", "gh api graphql -f query='{ viewer { login } }'",
		// A read whose expansions are double-quoted, a write with no expansion and no merge path.
		`gh api "repos/someone/shop/pulls/$N"`, `gh api -X GET "repos/o/r/pulls/$N/files"`, "gh api -X PATCH repos/o/r/pulls/7 -f title=x",
		"echo $HOME && git status", "gh api repos/o/r/pulls/7%2Ffiles",
		// A literal GraphQL query or mutation that is no merge, its variables single-quoted.
		`gh api graphql -f query='query($n: Int!) { repository(owner: "o", name: "r") { pullRequest(number: $n) { mergeable } } }' -F n=7`,
		`gh api graphql -f query='mutation { addComment(input: {subjectId: "x", body: "y"}) { clientMutationId } }'`,
		`curl -s -X POST https://api.github.com/graphql -d '{"query":"{ viewer { login } }"}'`,
		"grep -rn graphql src/", "curl -i https://api.github.com/repos/o/r",
	} {
		if MergeAttempt("Bash", bash(cmd)) {
			t.Errorf("%q: seen as a merge", cmd)
		}
	}
	if !MergeAttempt("mcp__github__merge_pull_request", json.RawMessage(`{}`)) || MergeAttempt("mcp__lea__merge_blocked", json.RawMessage(`{}`)) ||
		MergeAttempt("Read", json.RawMessage(`{"file_path":"merge"}`)) {
		t.Fatal("MCP and other tools")
	}
}

func TestBranchWrite(t *testing.T) {
	bash := func(cmd string) json.RawMessage {
		b, _ := json.Marshal(map[string]string{"command": cmd})
		return b
	}
	for _, cmd := range []string{
		"gh api -X PUT repos/o/r/contents/.lea/production -f branch=main -f message=x -f content=eA== -f sha=abc",
		"gh api --method DELETE repos/o/r/contents/.lea/sensitive -f branch=main -f sha=abc",
		"gh api -X PATCH repos/o/r/git/refs/heads/main -f sha=X -F force=true",
		"gh api repos/o/r/git/refs -f ref=refs/heads/main -f sha=X", "gh api -X DELETE repos/o/r/git/refs/heads/release",
		"gh api -XPUT repos/o/r/cont%65nts/.lea/production -f content=x", "gh api -X PATCH repos/O/R/GIT/REFS/heads/main -f sha=X",
		"curl -X PUT -d @body.json https://api.github.com/repos/o/r/contents/.lea/production",
		"curl -sSXPATCH https://api.github.com/repos/o/r/git/refs/heads/main --data '{\"sha\":\"x\"}'",
		"gh api repos/o/r/merge-upstream -f branch=main",
		"python3 -c 'import requests; requests.put(\"https://api.github.com/repos/o/r/contents/x\")'",
		"gh api repos/o/r/contents/$F -X $M", "gh api repos/o/r/git/refs/heads/$B",
		`gh api graphql -f query='mutation { updateRef(input: {refId: "x", oid: "y", force: true}) { clientMutationId } }'`,
		`gh api graphql -f query='mutation { createCommitOnBranch(input: {}) { clientMutationId } }'`,
	} {
		if BranchWrite("Bash", bash(cmd)) == "" {
			t.Errorf("%q: not seen as a branch write", cmd)
		}
	}
	for _, cmd := range []string{
		"gh api repos/o/r/contents/README.md --jq .content", "gh api -X GET repos/o/r/contents/.lea/production -f ref=main",
		"gh api -H 'Accept: application/vnd.github.raw' repos/o/r/contents/.lea/production", "gh api repos/o/r/git/refs/heads/main",
		"curl -sSL https://api.github.com/repos/o/r/git/refs/heads/main", `gh api "repos/o/r/contents/$F"`,
		"git update-ref refs/heads/x HEAD", "git push origin fix/7", "gh api -X PATCH repos/o/r/pulls/7 -f title=x",
	} {
		if why := BranchWrite("Bash", bash(cmd)); why != "" {
			t.Errorf("%q: seen as a branch write", cmd)
		}
	}
	for tool, want := range map[string]bool{"mcp__github__create_or_update_file": true, "mcp__github__push_files": true,
		"mcp__github__delete_file": true, "mcp__github__create_branch": true, "mcp__github__get_file_contents": false,
		"mcp__github__list_branches": false, "mcp__lea__update_branch": false, "Read": false} {
		if got := BranchWrite(tool, json.RawMessage(`{}`)) != ""; got != want {
			t.Errorf("%s: %v", tool, got)
		}
	}
}

// runPushPolicy runs pushPolicyScript for a push to branch against fakeGH (default.txt, the
// production files, err.<what>), as the pinned push runs it: it says whether the push would go on.
func runPushPolicy(t *testing.T, branch string, c policyCase) (out string, pushed bool, log string) {
	t.Helper()
	for _, p := range []string{shPath, grepPath, mktempPath, rmPath} {
		if _, err := os.Stat(p); err != nil {
			t.Skipf("%s: %v", p, err)
		}
	}
	dir := t.TempDir()
	write := func(name, s string) {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(s), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	def := c.def
	if def == "" {
		def = "main"
	}
	write("gh", fakeGH)
	write("default.txt", def+"\n")
	for ref, body := range c.productionAt {
		write("production."+ref, body)
	}
	for _, e := range c.errs {
		write("err."+e, "")
	}
	script := "(\n" + pushPolicyScript(branch, "github.com/someone/shop") + ") || exit 1\necho PUSHED\n"
	script = strings.ReplaceAll(script, "'"+ghPath+"'", "'"+filepath.Join(dir, "gh")+"'")
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	b, _ := exec.CommandContext(ctx, shPath, "-c", script).CombinedOutput()
	l, _ := os.ReadFile(filepath.Join(dir, "gh.log"))
	return string(b), strings.Contains(string(b), "PUSHED"), string(l)
}

// In a tower's tree a pinned push never goes to a production branch (#110, re-review B1): the
// default branch (main or not), main, master, or a branch .lea/production names on the default
// branch or on the branch itself; any forge error but a 404 refuses it.
func TestPushPolicyScript(t *testing.T) {
	for _, tc := range []struct {
		name, branch string
		c            policyCase
		pushed       bool
		want         string
	}{
		{"a branch of its own", "fix/7", policyCase{}, true, ""},
		{"a branch of its own, lists naming others", "fix-7", policyCase{def: "develop",
			productionAt: map[string]string{"develop": "release\n", "fix-7": "# none\nstaging\n"}}, true, ""},
		{"main", "main", policyCase{def: "develop"}, false, "production branch main"},
		{"master", "master", policyCase{}, false, "production branch master"},
		{"a default branch other than main", "develop", policyCase{def: "develop"}, false, "the repository's default branch"},
		{"listed on the default branch", "release", policyCase{productionAt: map[string]string{"main": "# prod\nrelease\n"}}, false,
			".lea/production on the default branch names it"},
		{"listed with a CR and blanks", "release", policyCase{productionAt: map[string]string{"main": " release \r\n"}}, false,
			".lea/production on the default branch names it"},
		{"listed on its own branch only", "release", policyCase{productionAt: map[string]string{"release": "release\n"}}, false,
			"its own .lea/production names it"},
		{"the default branch cannot be read", "fix/7", policyCase{errs: []string{"default"}}, false, "cannot read the default branch"},
		{"the default branch's list: an API error", "fix/7", policyCase{errs: []string{"production.main"}}, false,
			"cannot read .lea/production (not a 404) on the default branch main"},
		{"the branch's own list: an API error", "fix-7", policyCase{errs: []string{"production.fix-7"}}, false,
			"cannot read .lea/production (not a 404) on the branch fix-7"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			out, pushed, log := runPushPolicy(t, tc.branch, tc.c)
			if pushed != tc.pushed || tc.want != "" && !strings.Contains(out, tc.want) {
				t.Fatalf("pushed=%v out:\n%s\ngh:\n%s", pushed, out, log)
			}
		})
	}
}

// Command pins a push of a caller of a tower's tree behind the production-branch check, in a
// subshell before the push; a GitLab pin is not pinned there (the check reads GitHub).
func TestCommandAppliesThePushPolicy(t *testing.T) {
	for _, b := range []string{"release", "develop", "feat/73-x"} {
		c := dev
		c.Branch, c.Tree = b, true
		g := all(CommitIdentity)
		g.GitHub, g.Branch = "github.com/someone/shop", "*"
		cmd := Command(Request{Prog: "git", Args: []string{"push", "origin", b}}, c, g)
		script := lastWord(t, cmd)
		if !strings.HasPrefix(script, "(\n") || !strings.Contains(script, "lea_dst='"+b+"'") || !strings.Contains(script, ") || exit 1\n") ||
			strings.Index(script, "lea_dst=") > strings.Index(script, " push ") {
			t.Fatalf("%s: policy missing or after the push: %s", b, script)
		}
		d := Decide("Bash", bash("git push origin "+b), c, []Grant{g}, now, false)
		if d.Verdict != Allow || !strings.Contains(d.Command, "lea_dst") {
			t.Fatalf("%s: %+v", b, d)
		}
		c.Tree = false
		if cmd := Command(Request{Prog: "git", Args: []string{"push", "origin", b}}, c, g); cmd == "" || strings.Contains(cmd, "lea_dst") {
			t.Fatalf("%s outside a tower's tree: %s", b, cmd)
		}
		c.Tree = true
		g.GitHub = "gitlab.com/someone/shop"
		if cmd := Command(Request{Prog: "git", Args: []string{"push", "origin", b}}, c, g); cmd != "" {
			t.Fatalf("%s: GitLab push pinned in a tower's tree: %s", b, cmd)
		}
	}
}
