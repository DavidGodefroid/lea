package launcher

import (
	"os"
	"strings"
	"testing"
)

// sshKeyIn writes a lea SSH key and known hosts file in w's state.
func sshKeyIn(t *testing.T, w *world) string {
	t.Helper()
	st := w.data + "/lea/state"
	if err := os.MkdirAll(st+"/ssh", 0o700); err != nil {
		t.Fatal(err)
	}
	for _, f := range []string{"id_ed25519", "id_ed25519.pub", "known_hosts"} {
		if err := os.WriteFile(st+"/ssh/"+f, []byte(f+"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return st
}

// The agent container never mounts lea's SSH key or its directory: the ssh-agent's socket volume,
// read-only, and the known hosts file only.
func TestRunArgsNeverMountTheSSHKey(t *testing.T) {
	w := newWorld(t, repoRoot(t))
	st := sshKeyIn(t, w)
	args, err := w.launcher().runArgs(w.proj, "172.18.0.5")
	if err != nil {
		t.Fatal(err)
	}
	for _, a := range args {
		if strings.Contains(a, "id_ed25519") || strings.Contains(a, "src="+st+"/ssh,") || strings.Contains(a, sshAgentKeyDir) {
			t.Errorf("the agent container mounts lea's SSH key: %q", a)
		}
	}
	for _, m := range []string{"type=volume,src=lea-ssh-" + w.id() + ",dst=/run/lea/ssh,readonly",
		"type=bind,src=" + st + "/ssh/known_hosts,dst=/lea/state/ssh/known_hosts,readonly"} {
		if !contains(args, m) {
			t.Errorf("missing %q", m)
		}
	}
	// Without a known hosts file, no bind of it.
	os.Remove(st + "/ssh/known_hosts")
	args, _ = w.launcher().runArgs(w.proj, "172.18.0.5")
	if s := strings.Join(args, "\n"); strings.Contains(s, "known_hosts") {
		t.Errorf("a known_hosts mount without the file:\n%s", s)
	}
}

// With a key, the ssh-agent container starts before the agent container, without a network or
// capabilities beyond its start's, with the key directory read-only, and the key is loaded into it.
func TestContainerStartRunsTheSSHAgentContainerFirst(t *testing.T) {
	w := newWorld(t, repoRoot(t))
	st := sshKeyIn(t, w)
	if err := w.launcher().containerStart(w.proj, "172.18.0.5"); err != nil {
		t.Fatalf("%v\n%s", err, w.errb.String())
	}
	side := w.name() + "-ssh"
	var seq []string
	var run string
	for _, c := range w.calls() {
		switch {
		case strings.HasPrefix(c, "docker [run] [--rm] [-d] [--init] [--name] ["+side+"]"):
			seq, run = append(seq, "ssh-agent"), c
		case c == "docker [exec] ["+side+"] [test] [-S] ["+sshAgentSocket+"]":
			seq = append(seq, "socket")
		case strings.HasPrefix(c, "docker [exec] [-u] [0] ["+side+"] [sh] [-c]") && strings.Contains(c, "ssh-add -q -"):
			seq = append(seq, "ssh-add")
		case strings.HasPrefix(c, "docker [run] [-d] [--init] [--name] ["+w.name()+"]"):
			seq = append(seq, "run")
		}
	}
	if strings.Join(seq, " ") != "ssh-agent socket ssh-add run" {
		t.Fatalf("sequence %v\ncalls:\n%s", seq, strings.Join(w.calls(), "\n"))
	}
	for _, m := range []string{"[--network] [none]", "[--cap-drop] [ALL]", "[--security-opt] [no-new-privileges]", "[--read-only]",
		"[type=bind,src=" + st + "/ssh,dst=" + sshAgentKeyDir + ",readonly]",
		"[type=volume,src=lea-ssh-" + w.id() + ",dst=" + sshAgentSocketDir + "]", "[lea.role=ssh-agent]"} {
		if !strings.Contains(run, m) {
			t.Errorf("the ssh-agent container's run lacks %s:\n%s", m, run)
		}
	}
	// Its capabilities: these four only.
	var caps []string
	f := strings.Fields(run)
	for i := range f {
		if f[i] == "[--cap-add]" && i+1 < len(f) {
			caps = append(caps, strings.Trim(f[i+1], "[]"))
		}
	}
	if strings.Join(caps, " ") != "CHOWN SETUID SETGID DAC_READ_SEARCH" {
		t.Errorf("capabilities %v", caps)
	}
}

// Without a key, no ssh-agent container (an old one is removed).
func TestContainerStartWithoutAKeyRunsNoSSHAgentContainer(t *testing.T) {
	w := newWorld(t, repoRoot(t))
	if err := w.launcher().containerStart(w.proj, "172.18.0.5"); err != nil {
		t.Fatalf("%v\n%s", err, w.errb.String())
	}
	side := w.name() + "-ssh"
	if !w.hasCall("docker [rm] [-f] [" + side + "]") {
		t.Errorf("the old ssh-agent container is not removed:\n%s", strings.Join(w.calls(), "\n"))
	}
	if w.hasCall("[--name] [" + side + "]") {
		t.Errorf("an ssh-agent container without a key:\n%s", strings.Join(w.calls(), "\n"))
	}
}

// Under a virtual machine runtime the key is never handed over: no ssh-agent container, a warning.
func TestSSHAgentUnderAVirtualMachineRuntime(t *testing.T) {
	w := newWorld(t, repoRoot(t))
	sshKeyIn(t, w)
	w.env["LEA_RUNTIME"] = "kata"
	w.launcher().sshAgentStart(w.proj, true)
	if w.hasCall("[--name] [" + w.name() + "-ssh]") {
		t.Errorf("an ssh-agent container under a VM runtime:\n%s", strings.Join(w.calls(), "\n"))
	}
	if !strings.Contains(w.errb.String(), "lea's SSH key is not available to the agents under kata") {
		t.Errorf("stderr %q", w.errb.String())
	}
}

// A failed ssh-agent container is a warning: the agent container starts, without the key.
func TestSSHAgentContainerFailureIsAWarning(t *testing.T) {
	w := newWorld(t, repoRoot(t))
	sshKeyIn(t, w)
	w.env["FAKE_DOCKER_RC_RUN___RM"] = "1"
	if err := w.launcher().containerStart(w.proj, "172.18.0.5"); err != nil {
		t.Fatalf("%v\n%s", err, w.errb.String())
	}
	if !strings.Contains(w.errb.String(), "did not start") || w.hasCall("ssh-add") {
		t.Errorf("stderr %q\ncalls:\n%s", w.errb.String(), strings.Join(w.calls(), "\n"))
	}
}

// The ssh-agent container's scripts: ssh-agent and ssh-add run as the user, the key only ever
// read from the key directory of the ssh-agent container.
func TestSSHAgentScripts(t *testing.T) {
	for _, m := range []string{`chown "$LEA_UID:$LEA_GID" /run/lea-ssh`, `setpriv --reuid "$LEA_UID" --regid "$LEA_GID" --clear-groups`,
		"exec ssh-agent -D -a /run/lea-ssh/agent.sock"} {
		if !strings.Contains(sshAgentScript, m) {
			t.Errorf("sshAgentScript lacks %q", m)
		}
	}
	if !strings.HasPrefix(sshAddScript, `exec setpriv --reuid "$LEA_UID" --regid "$LEA_GID" --clear-groups`) ||
		!strings.HasSuffix(sshAddScript, "ssh-add -q - <"+sshAgentKeyDir+"/id_ed25519") {
		t.Errorf("sshAddScript %q", sshAddScript)
	}
}
