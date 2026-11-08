package launcher

import (
	"os"
	"reflect"
	"strings"
	"testing"
)

// The user's status line script under ~/.claude is mounted read-only into the container (#374);
// nothing else the command names is, and no mount without a command.
func TestClaudeMountsUserStatusLine(t *testing.T) {
	setup := func(command string) (*world, string, string, string) {
		w := newWorld(t, repoRoot(t))
		st, c := w.data+"/lea/state", w.home+"/.claude"
		os.MkdirAll(c+"/scripts", 0o755)
		os.WriteFile(c+"/statusline.sh", []byte("#!/bin/sh\necho mine\n"), 0o755)
		os.WriteFile(c+"/scripts/sl.py", []byte("print('x')\n"), 0o644)
		os.WriteFile(c+"/.credentials.json", []byte("secret"), 0o600)
		os.WriteFile(c+"/history.jsonl", []byte("h"), 0o600)
		os.WriteFile(c+"/settings.json", []byte(`{"statusLine":{"type":"command","command":`+command+`}}`), 0o600)
		return w, st, c, st + "/claude/" + ID(w.proj)
	}
	ro := func(c, rel string) string {
		return "type=bind,src=" + c + "/" + rel + ",dst=" + c + "/" + rel + ",readonly"
	}

	w, st, c, priv := setup(`"~/.claude/statusline.sh"`)
	m, err := w.launcher().claudeMounts(w.home, w.proj, st)
	if err != nil || !contains(m, ro(c, "statusline.sh")) {
		t.Fatalf("err %v, mounts %q", err, m)
	}
	if !isRegular(priv + "/statusline.sh") {
		t.Error("the mount point is made in the private base")
	}
	// lea-ctl sees it too: its launcher makes the agents' mounts.
	if es := w.launcher().ctlClaudeEntries(); !contains(es, "statusline.sh") {
		t.Errorf("lea-ctl entries %q", es)
	}

	w, st, c, priv = setup(`"python3 $HOME/.claude/scripts/sl.py --x ~/.claude/.credentials.json ${HOME}/.claude/history.jsonl ~/.claude/projects/x /etc/passwd"`)
	m, err = w.launcher().claudeMounts(w.home, w.proj, st)
	if err != nil || !contains(m, ro(c, "scripts/sl.py")) || !isRegular(priv+"/scripts/sl.py") {
		t.Fatalf("err %v, mounts %q", err, m)
	}
	all := strings.Join(m, "\n")
	// Only scripts: not the credentials, the history (data, not a script) nor what is outside.
	for _, bad := range []string{".credentials.json", "history.jsonl", "/etc/passwd", "projects/x"} {
		if strings.Contains(all, bad) {
			t.Errorf("%s mounted", bad)
		}
	}

	// lea's own command, or none: nothing more than before.
	for _, cmd := range []string{`"lea statusline"`, `""`} {
		w, st, _, _ = setup(cmd)
		m, _ = w.launcher().claudeMounts(w.home, w.proj, st)
		if strings.Contains(strings.Join(m, "\n"), "statusline.sh") || strings.Contains(strings.Join(m, "\n"), "sl.py") {
			t.Errorf("%s: mounts %q", cmd, m)
		}
	}

	// An agent that rewrites its copy of the settings gets no other host file at the next start:
	// the mounts follow the host's settings only.
	w, st, c, priv = setup(`"~/.claude/statusline.sh"`)
	if _, err := w.launcher().claudeMounts(w.home, w.proj, st); err != nil {
		t.Fatal(err)
	}
	os.MkdirAll(c+"/shell-snapshots", 0o755)
	os.WriteFile(c+"/shell-snapshots/snap.sh", []byte("x"), 0o755)
	os.WriteFile(priv+"/settings.json", []byte(`{"statusLine":{"command":"~/.claude/scripts/sl.py"}}`), 0o600)
	m, _ = w.launcher().claudeMounts(w.home, w.proj, st)
	if all := strings.Join(m, "\n"); strings.Contains(all, "sl.py") || !contains(m, ro(c, "statusline.sh")) {
		t.Errorf("copy widened the mounts: %q", m)
	}
	os.WriteFile(c+"/settings.json", []byte(`{"statusLine":{"command":"~/.claude/shell-snapshots/snap.sh"}}`), 0o600)
	if got, _ := statusLineFiles(c, c+"/settings.json"); got != nil {
		t.Errorf("Claude's state mounted: %q", got)
	}

	// A symlinked script is skipped with a warning.
	w, st, c, _ = setup(`"~/.claude/linked.sh"`)
	os.Symlink(c+"/statusline.sh", c+"/linked.sh")
	m, err = w.launcher().claudeMounts(w.home, w.proj, st)
	if err != nil || strings.Contains(strings.Join(m, "\n"), "linked.sh") || !strings.Contains(w.errb.String(), "skipped "+c+"/linked.sh") {
		t.Errorf("symlink: err %v, mounts %q, log %q", err, m, w.errb.String())
	}
}

func TestStatusLineFiles(t *testing.T) {
	c := t.TempDir() + "/.claude"
	os.MkdirAll(c+"/skills", 0o755)
	os.MkdirAll(c+"/bin", 0o755)
	for _, f := range []string{"a.sh", "b.sh", "c.sh", "d.sh", "e.sh", "CLAUDE.md", "skills/s.sh", "bin/x.sh", "settings.local.json"} {
		os.WriteFile(c+"/"+f, []byte("x"), 0o644)
	}
	set := func(cmd string) string {
		p := c + "/settings.json"
		os.WriteFile(p, []byte(`{"statusLine":{"command":`+cmd+`}}`), 0o600)
		return p
	}
	for _, x := range []struct {
		cmd  string
		want []string
	}{
		{`"bash '` + c + `/a.sh'; ~/.claude/a.sh"`, []string{"a.sh"}},
		{`"~/.claude/a.sh ~/.claude/b.sh ~/.claude/c.sh ~/.claude/d.sh ~/.claude/e.sh"`, []string{"a.sh", "b.sh", "c.sh", "d.sh"}},
		{`"~/.claude/CLAUDE.md ~/.claude/skills/s.sh ~/.claude/settings.json ~/.claude/bin/x.sh"`, []string{"bin/x.sh"}},
		{`"~/.claude/../.claude/a.sh ~/.claude/missing.sh ~/.claude/bin"`, nil},
		{`"echo hi"`, nil},
	} {
		got, _ := statusLineFiles(c, set(x.cmd))
		if !reflect.DeepEqual(got, x.want) {
			t.Errorf("%s: %q, want %q", x.cmd, got, x.want)
		}
	}
}
