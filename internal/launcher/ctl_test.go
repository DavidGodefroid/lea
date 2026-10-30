package launcher

import (
	"fmt"
	"os"
	"slices"
	"strings"
	"testing"
)

// lea-ctl (#165): the control container alone gets the Docker socket; every host path lea uses is
// mounted at the same absolute path, so the binds it gives agent containers resolve on the host.
func TestCtlRunArgs(t *testing.T) {
	w := newWorld(t, repoRoot(t))
	run := w.dir + "/run"
	for _, d := range []string{w.home + "/.claude", run + "/lockhub", w.dir + "/config/lea"} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	w.env["XDG_RUNTIME_DIR"] = run
	l := w.launcher()
	args, err := l.ctlRunArgs([]string{w.proj}, "/var/run/docker.sock", 998)
	if err != nil {
		t.Fatal(err)
	}
	all := strings.Join(args, "\n")
	for _, want := range []string{
		"run", "-d", "--init", "--name", "lea-ctl", "type=bind,src=/etc/passwd,dst=/etc/passwd,readonly", "--restart", "unless-stopped", "--label", "lea.role=ctl",
		fmt.Sprintf("%d:%d", os.Getuid(), os.Getgid()), "--group-add", "998",
		"type=bind,src=/var/run/docker.sock,dst=/var/run/docker.sock",
		"type=bind,src=" + w.root + ",dst=" + w.root + ",readonly",
		"type=bind,src=" + w.data + "/lea,dst=" + w.data + "/lea",
		"type=bind,src=" + w.proj + ",dst=" + w.proj,
		"type=bind,src=" + w.dir + "/config/lea,dst=" + w.dir + "/config/lea,readonly",
		"type=bind,src=" + w.home + "/.claude,dst=" + w.home + "/.claude,readonly",
		"type=bind,src=" + run + "/lockhub,dst=" + run + "/lockhub",
		"HOME=" + w.home, "XDG_DATA_HOME=" + w.data, "XDG_RUNTIME_DIR=" + run, "LEA_CTL_INSIDE=1",
		"lea.projects=" + w.proj, "--pid=host", "--cgroupns=host", "LEA_CLIPBOARD=0",
		fmt.Sprintf("%s:rw,nosuid,nodev,uid=%d,gid=%d,mode=0700", w.home, os.Getuid(), os.Getgid()),
	} {
		if !slices.Contains(args, want) && !strings.Contains(all, "\n"+want+"\n") {
			t.Errorf("missing %q in\n%s", want, all)
		}
	}
	if args[len(args)-1] != l.Cfg.Get("LEA_CTL_IMAGE") || l.Cfg.Get("LEA_CTL_IMAGE") == "" {
		t.Errorf("image: %q (LEA_CTL_IMAGE %q)", args[len(args)-1], l.Cfg.Get("LEA_CTL_IMAGE"))
	}
	for _, bad := range []string{"--privileged", "--network=host", "--userns", "--cap-add"} {
		if strings.Contains(all, bad) {
			t.Errorf("ctl run args contain %q", bad)
		}
	}
	// A path that does not exist on the host is not mounted (docker would create it as root).
	if strings.Contains(all, w.home+"/.codex") {
		t.Errorf("a missing ~/.codex is mounted")
	}
	// A projects directory with a comma would split the mount: refused.
	if _, err := l.ctlRunArgs([]string{w.dir + "/a,b"}, "/var/run/docker.sock", 998); err == nil {
		t.Error("a projects dir with a comma was accepted")
	}
}

// LEA_CTL is 0 (the host mode, the default for one release) or 1.
func TestCtlConfig(t *testing.T) {
	w := newWorld(t, repoRoot(t))
	for file, want := range map[string]bool{"": false, "LEA_CTL=0\n": false, "LEA_CTL=1\n": true} {
		writeUserConf(t, w, file)
		if c, err := LoadConfig(w.getenv, w.root, w.proj); err != nil || c.Ctl() != want {
			t.Errorf("%q: %v, want %v", file, err, want)
		}
	}
	writeUserConf(t, w, "LEA_CTL=yes\n")
	if _, err := LoadConfig(w.getenv, w.root, w.proj); err == nil || !strings.Contains(err.Error(), "LEA_CTL must be 1") {
		t.Fatalf("LEA_CTL=yes: %v", err)
	}
	if !slices.Contains(ExportedKeys, "LEA_CTL") || !slices.Contains(ExportedKeys, "LEA_CTL_IMAGE") {
		t.Fatal("LEA_CTL keys not exported")
	}
}

func ctlWorld(t *testing.T) (*world, string) {
	t.Helper()
	w := newWorld(t, repoRoot(t))
	writeUserConf(t, w, "LEA_CTL=1\n")
	sock := w.dir + "/docker.sock"
	if err := os.WriteFile(sock, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	w.env["DOCKER_HOST"] = "unix://" + sock
	return w, sock
}

// With LEA_CTL=1 the host's lea starts lea-ctl when it does not run (or runs for other projects,
// or an older image) and runs the same command in it, at the same working directory; init,
// config, update, rollback, doctor and help stay on the host.
func TestCtlRunsLeaInTheControlContainer(t *testing.T) {
	w, sock := ctlWorld(t)
	l := w.launcher()
	if err := l.dispatch([]string{"new"}); err != nil {
		t.Fatal(err)
	}
	log := readLog(t, w)
	if !strings.Contains(log, "docker [rm] [-f] [lea-ctl]") || !strings.Contains(log, "docker [run] [-d] [--init] [--name] [lea-ctl]") ||
		!strings.Contains(log, "[type=bind,src="+sock+",dst="+sock+"]") {
		t.Fatalf("lea-ctl not started:\n%s", log)
	}
	if len(w.execs) != 1 {
		t.Fatalf("execs %q", w.execs)
	}
	got := strings.Join(w.execs[0], " ")
	want := fmt.Sprintf("exec -i -u %d:%d -w %s", os.Getuid(), os.Getgid(), w.proj)
	if !strings.HasSuffix(w.execs[0][0], "docker") || !strings.Contains(got, want) || !strings.HasSuffix(got, "lea-ctl "+w.core+" new") {
		t.Fatalf("exec %q", got)
	}
	// Running for these projects with the current image: used as it is.
	w2, _ := ctlWorld(t)
	w2.env["FAKE_DOCKER_OUT_INSPECT__F_AT_LEA_CTL"] = "true|" + w2.proj + "|sha256:abc"
	w2.env["FAKE_DOCKER_OUT_IMAGE_INSPECT_AT_LEA_CTL_DEV"] = "sha256:abc"
	if err := w2.launcher().dispatch(nil); err != nil {
		t.Fatal(err)
	}
	if log := readLog(t, w2); strings.Contains(log, "docker [run]") {
		t.Fatalf("a matching lea-ctl was replaced:\n%s", log)
	}
	if len(w2.execs) != 1 || !strings.HasSuffix(strings.Join(w2.execs[0], " "), "lea-ctl "+w2.core) {
		t.Fatalf("exec %q", w2.execs)
	}
	// Host-only commands never reach it.
	w3, _ := ctlWorld(t)
	if err := w3.launcher().dispatch([]string{"help"}); err != nil {
		t.Fatal(err)
	}
	if log := readLog(t, w3); strings.Contains(log, "lea-ctl") || len(w3.execs) != 0 {
		t.Fatalf("help went to lea-ctl:\n%s %q", log, w3.execs)
	}
	// Inside lea-ctl, lea runs as on the host.
	w4, _ := ctlWorld(t)
	w4.env["LEA_CTL_INSIDE"] = "1"
	w4.launcher().dispatch([]string{"stop"})
	if log := readLog(t, w4); strings.Contains(log, "[lea-ctl]") {
		t.Fatalf("lea-ctl started from inside itself:\n%s", log)
	}
}

func readLog(t *testing.T, w *world) string {
	t.Helper()
	return strings.Join(w.calls(), "\n")
}

// lea config ctl on|off sets LEA_CTL in the user configuration, on the host; lea ctl down removes
// lea-ctl, lea ctl up starts it, lea ctl attach opens its tmux, from the host.
func TestCtlSwitchAndCommands(t *testing.T) {
	w := newWorld(t, repoRoot(t))
	for arg, want := range map[string]string{"on": "LEA_CTL='1'", "off": "LEA_CTL='0'"} {
		if err := w.launcher().dispatch([]string{"config", "ctl", arg}); err != nil {
			t.Fatal(err)
		}
		if b, _ := os.ReadFile(w.dir + "/config/lea/lea.conf"); !strings.Contains(string(b), want) {
			t.Fatalf("config ctl %s: %q", arg, b)
		}
	}
	if err := w.launcher().dispatch([]string{"config", "ctl", "maybe"}); err == nil {
		t.Fatal("config ctl maybe accepted")
	}
	w2, _ := ctlWorld(t)
	if err := w2.launcher().dispatch([]string{"ctl", "down"}); err != nil {
		t.Fatal(err)
	}
	if log := readLog(t, w2); !strings.Contains(log, "docker [rm] [-f] [lea-ctl]") || strings.Contains(log, "docker [run]") {
		t.Fatalf("ctl down:\n%s", log)
	}
	if err := w2.launcher().dispatch([]string{"ctl", "attach"}); err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(w2.execs[len(w2.execs)-1], " "); !strings.Contains(got, "lea-ctl tmux -L lea attach") {
		t.Fatalf("ctl attach exec %q", got)
	}
}
