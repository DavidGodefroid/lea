package grants

import "strings"

const (
	curlPath = "/usr/bin/curl"
	jqPath   = "/usr/bin/jq"
)

// gitlabScript is the script of a pinned glab mr create|merge on the GitLab project host/path:
// glab only reads the token of the agent's glab config for host (glab config get, from /: no
// request), and curl does the rest on the GitLab API. glab's config (GLAB_CONFIG_DIR, a
// repository's .git/glab-cli/config.yml) is the agent's to write: its api_host, proxy or TLS
// settings would send glab's requests to a server of its own, so glab never talks to the forge.
// curl -q reads no .curlrc, env -i leaves no CA variable, -x is lea's proxy (Caller.Proxy:
// CONNECT, TLS end to end), --proto =https and no redirect keep it on https://host. "" when the
// call cannot be pinned.
func gitlabScript(req Request, c Caller, host, path string) string {
	if len(req.Args) < 2 || c.Branch == "" || !BranchName(c.Branch) || Protected(c.Branch) {
		return ""
	}
	api := shellWords([]string{"https://" + host + "/api/v4/projects/" + strings.ReplaceAll(path, "/", "%2F")})
	pre := "cd / || exit 1\nexport GLAB_CHECK_UPDATE=false GLAB_SEND_TELEMETRY=false\n" +
		"t=$(" + shellWords([]string{glabPath, "config", "get", "token", "--host", host}) + " 2>/dev/null) || t=\n" +
		`case "$t" in ''|*[!A-Za-z0-9_.-]*) ` + refuse("no GitLab token for "+host+" in glab's config: glab auth login --hostname "+host) + ";; esac\n" +
		// HOME is the agent's (jq loads ~/.jq into every program): none from here on.
		"export HOME=" + shellWords([]string{noConfigDir}) + "\n" +
		"api=" + api + "\n" +
		// lea_api URL-LAST-ARGS...: the API's answer on stdout; on an error, it on stderr too.
		"lea_api() { r=$(" + shellWords([]string{curlPath, "-q", "--proto", "=https", "--tlsv1.2", "-x", c.Proxy, "--max-time", "60",
		"-sS", "--fail-with-body"}) + ` -H "PRIVATE-TOKEN: $t" "$@") || { printf '%s\n' "$r" >&2; return 1; }; printf '%s\n' "$r"; }` + "\n"
	switch req.Action {
	case PRCreate:
		return pre + gitlabCreate(req.Args[2:])
	case Merge:
		if len(req.Args) < 3 {
			return ""
		}
		return pre + gitlabMerge(req.Args[2], req.Args[3:], c.Branch)
	}
	return ""
}

// gitlabCreate is glab mr create with the args glabMRCreate validated: one POST, the target the
// named one or the project's default branch.
func gitlabCreate(args []string) string {
	var src, tgt, title, desc string
	draft, del := false, false
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--draft":
			draft = true
		case "--remove-source-branch":
			del = true
		case "-s", "--source-branch":
			src, i = args[i+1], i+1
		case "-b", "--target-branch":
			tgt, i = args[i+1], i+1
		case "-t", "--title":
			title, i = args[i+1], i+1
		case "-d", "--description":
			desc, i = args[i+1], i+1
		}
	}
	if draft {
		title = "Draft: " + title
	}
	s := "tgt=" + shellWords([]string{tgt}) + "\n"
	if tgt == "" {
		s = "p=$(lea_api \"$api\") || exit 1\n" +
			"tgt=$(printf '%s\\n' \"$p\" | " + shellWords([]string{jqPath, "-r", ".default_branch // empty"}) + ") || exit 1\n" +
			`[ -n "$tgt" ] || { ` + refuse("the project has no default branch: name one with --target-branch") + "; }\n"
	}
	post := []string{"-X", "POST", "--data-urlencode", "source_branch=" + src}
	tail := []string{"--data-urlencode", "title=" + title}
	if desc != "" {
		tail = append(tail, "--data-urlencode", "description="+desc)
	}
	if del {
		tail = append(tail, "--data", "remove_source_branch=true")
	}
	return s + "out=$(lea_api " + shellWords(post) + ` --data-urlencode "target_branch=$tgt" ` + shellWords(tail) + ` "$api/merge_requests") || exit 1` + "\n" +
		`printf '%s\n' "$out" | ` + shellWords([]string{jqPath, "-r", `"created " + .web_url`}) + "\n"
}

// gitlabMerge is glab mr merge n|<own branch> with the args glabMRMerge validated: the MR (by
// its number, or the one open MR of that branch of the project itself) must be open and come from
// branch of the project itself, not a fork's branch of the same name; the merge names the head
// that was checked (sha): GitLab refuses it if the branch moved since.
func gitlabMerge(n string, args []string, branch string) string {
	s := "iid=" + shellWords([]string{n}) + "\n"
	if !number(n) {
		s = "l=$(lea_api -G --data-urlencode state=opened --data-urlencode " + shellWords([]string{"source_branch=" + branch}) + ` "$api/merge_requests") || exit 1` + "\n" +
			"iid=$(printf '%s\\n' \"$l\" | " + shellWords([]string{jqPath, "-r", `map(select(.source_project_id == .target_project_id)) | if length == 1 then .[0].iid | tostring else "" end`}) + ") || exit 1\n" +
			`case "$iid" in ''|*[!0-9]*) ` + refuse("no single open MR of the agent's own branch "+branch) + ";; esac\n"
	}
	s += "mr=$(lea_api \"$api/merge_requests/$iid\") || exit 1\n" +
		// Each field compared whole in jq: the head commit, or "" when the MR is not it.
		"sha=$(printf '%s\\n' \"$mr\" | " + shellWords([]string{jqPath, "-r", "--arg", "b", branch,
		`if .source_project_id == .target_project_id and .state == "opened" and .source_branch == $b then .sha else "" end`}) + ") || exit 1\n" +
		`[ -n "$sha" ] || { ` + refuse("MR "+n+" is not an open MR of the agent's own branch "+branch) + "; }\n" +
		`case "$sha" in *[!0-9a-f]*) sha= ;; esac` + "\n" +
		`[ "${#sha}" -eq 40 ] || [ "${#sha}" -eq 64 ] || { ` + refuse("MR "+n+" has no head commit") + "; }\n" +
		// A pipeline still running: GitLab merges once it passes (glab's default, auto-merge).
		"am=\nps=$(printf '%s\\n' \"$mr\" | " + shellWords([]string{jqPath, "-r", `.head_pipeline.status // ""`}) + ") || exit 1\n" +
		`case "$ps" in created|waiting_for_resource|preparing|pending|running|scheduled) am='--data auto_merge=true --data merge_when_pipeline_succeeds=true' ;; esac` + "\n"
	put := []string{"-X", "PUT"}
	opts := []string{}
	for _, a := range args {
		switch a {
		case "--squash", "-s":
			opts = append(opts, "--data", "squash=true")
		case "--remove-source-branch", "-d":
			opts = append(opts, "--data", "should_remove_source_branch=true")
		}
	}
	return s + "out=$(lea_api " + shellWords(put) + ` --data-urlencode "sha=$sha" ` + shellWords(opts) + ` $am "$api/merge_requests/$iid/merge") || exit 1` + "\n" +
		`printf '%s\n' "$out" | ` + shellWords([]string{jqPath, "-r", `"merged " + .web_url`}) + "\n"
}
