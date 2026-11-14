package launcher

import (
	"context"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"testing"
	"time"
)

// The ssh-agent e2e: a real ssh-agent container of the engine LEA_E2E_SSH names (docker, or
// podman, rootless included), run with lea's own arguments, loads a fresh key; a container run as
// the agents' uid with the agent container's SSH mounts lists that key through the socket, and
// finds no key file. The image (LEA_E2E_SSH_IMAGE, lea's base by default) needs ssh-agent, ssh-add
// and setpriv.
//
//	LEA_E2E_SSH=docker go test -count=1 -run TestSSHAgentE2E -v ./internal/launcher/
//	LEA_E2E_SSH=podman go test -count=1 -run TestSSHAgentE2E -v ./internal/launcher/
func TestSSHAgentE2E(t *testing.T) {
	engine := os.Getenv("LEA_E2E_SSH")
	if engine == "" {
		t.Skip("LEA_E2E_SSH=docker|podman runs it against that engine")
	}
	bin, err := exec.LookPath(engine)
	if err != nil {
		t.Fatal(err)
	}
	image := os.Getenv("LEA_E2E_SSH_IMAGE")
	if image == "" {
		image = "quay.io/podman/stable:v5.8.7-immutable"
	}
	run := func(args ...string) (string, error) {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
		defer cancel()
		out, err := exec.CommandContext(ctx, bin, args...).CombinedOutput()
		return strings.TrimSpace(string(out)), err
	}
	must := func(args ...string) string {
		t.Helper()
		out, err := run(args...)
		if err != nil {
			t.Fatalf("%s %s: %v\n%s", engine, strings.Join(args, " "), err, out)
		}
		return out
	}
	w := newWorld(t, repoRoot(t))
	l := w.launcher()
	l.Cfg.Export("LEA_IMAGE", image)
	st := w.data + "/lea/state"
	if err := os.MkdirAll(st+"/ssh", 0o700); err != nil {
		t.Fatal(err)
	}
	if out, err := exec.Command("ssh-keygen", "-q", "-t", "ed25519", "-N", "", "-C", "lea-e2e", "-f", st+"/ssh/id_ed25519").CombinedOutput(); err != nil {
		t.Fatalf("ssh-keygen: %v\n%s", err, out)
	}
	pub, err := os.ReadFile(st + "/ssh/id_ed25519.pub")
	if err != nil {
		t.Fatal(err)
	}
	name, vol := l.sshAgentName(w.proj), l.sshAgentVolume(w.proj)
	t.Cleanup(func() { _, _ = run("rm", "-f", name); _, _ = run("volume", "rm", "-f", vol) })
	must(l.sshAgentRunArgs(w.proj)...)
	for i := 0; ; i++ {
		if _, err := run("exec", name, "test", "-S", sshAgentSocket); err == nil {
			break
		}
		if i > 40 {
			logs, _ := run("logs", name)
			t.Fatalf("no ssh-agent socket\n%s", logs)
		}
		time.Sleep(250 * time.Millisecond)
	}
	must("exec", "-u", "0", name, "sh", "-c", sshAddScript)
	uid := strconv.Itoa(os.Getuid())
	if got := must("exec", name, "stat", "-c", "%u %a", sshAgentSocket); got != uid+" 600" {
		t.Errorf("the socket is %q, not the user's (%s) and 0600", got, uid)
	}
	// As the agent container: the user's uid, its SSH mounts, nothing else of lea's state.
	a := []string{"run", "--rm", "--network", "none", "--user", uidgid(), "-e", "SSH_AUTH_SOCK=/run/lea/ssh/agent.sock"}
	a = append(a, l.sshAgentArgs(w.proj)...)
	out := must(append(a, "--entrypoint", "sh", image, "-c",
		`ssh-add -L; for f in /lea/state/ssh/id_ed25519 `+sshAgentKeyDir+`/id_ed25519; do if [ -e "$f" ]; then echo "key file $f"; fi; done`)...)
	if !strings.Contains(out, strings.Fields(string(pub))[1]) {
		t.Errorf("the agent does not reach lea's key through the socket:\n%s", out)
	}
	if strings.Contains(out, "key file") || strings.Contains(out, "PRIVATE KEY") {
		t.Errorf("the agent finds a key file:\n%s", out)
	}
}
