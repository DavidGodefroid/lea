package launcher

import (
	"os"
	"strconv"
	"time"
)

// lea's SSH key (<state>/ssh/id_ed25519, lea init) never enters an agent container: an agent runs
// as the user's uid, the key file's owner, and would read it. The key directory is mounted in the
// projects dir's ssh-agent container only, <container>-ssh: no network, no capability but the
// ones its root start needs (it reads the key, owns the socket directory, drops to the user's
// uid), the user's uid for the ssh-agent itself. The agent container gets the agent's socket
// directory, the volume <prefix>-ssh-<id>, read-only at /run/lea/ssh: ssh-agent answers a peer of
// its own uid, the agent's, and an agent signs with the key without ever holding it.

// sshAgentSocketDir is where the ssh-agent container serves its socket, sshAgentSocket.
const sshAgentSocketDir = "/run/lea-ssh"

const sshAgentSocket = sshAgentSocketDir + "/agent.sock"

// sshAgentKeyDir is where the ssh-agent container (and no other) mounts <state>/ssh.
const sshAgentKeyDir = "/lea/ssh-key"

// sshAgentName is the projects dir's ssh-agent container.
func (l *Launcher) sshAgentName(proj string) string { return l.Cfg.ContainerName(proj) + "-ssh" }

// sshAgentVolume is the volume of its socket directory.
func (l *Launcher) sshAgentVolume(proj string) string {
	return l.prefix() + "-ssh-" + ID(proj)
}

// sshKey is lea's SSH key on the host.
func (l *Launcher) sshKey() string { return l.Cfg.StateDir() + "/ssh/id_ed25519" }

// sshAgentArgs are the agent container's SSH arguments: the agent's socket directory, read-only,
// and the known hosts file the entrypoint installs, never the key directory.
func (l *Launcher) sshAgentArgs(proj string) []string {
	a := []string{"--mount", "type=volume,src=" + l.sshAgentVolume(proj) + ",dst=/run/lea/ssh,readonly"}
	if kh := l.Cfg.StateDir() + "/ssh/known_hosts"; isRegular(kh) {
		a = append(a, "--mount", "type=bind,src="+kh+",dst=/lea/state/ssh/known_hosts,readonly")
	}
	return a
}

// sshAgentScript is the ssh-agent container's start, as root: the socket directory the user's,
// then ssh-agent in the foreground as the user, on a fresh socket.
const sshAgentScript = `set -eu
chown "$LEA_UID:$LEA_GID" ` + sshAgentSocketDir + `
exec setpriv --reuid "$LEA_UID" --regid "$LEA_GID" --clear-groups sh -c 'chmod 0700 ` + sshAgentSocketDir +
	` && rm -f ` + sshAgentSocket + ` && exec ssh-agent -D -a ` + sshAgentSocket + `'`

// sshAddScript loads the key into the agent: root opens the key file, ssh-add (the user, the
// agent's peer) reads it on its standard input. A key with a passphrase asks for it on the
// terminal.
const sshAddScript = `exec setpriv --reuid "$LEA_UID" --regid "$LEA_GID" --clear-groups env SSH_AUTH_SOCK=` + sshAgentSocket +
	` ssh-add -q - <` + sshAgentKeyDir + `/id_ed25519`

// sshAgentRunArgs are the docker run arguments of the ssh-agent container.
func (l *Launcher) sshAgentRunArgs(proj string) []string {
	return []string{"run", "--rm", "-d", "--init", "--name", l.sshAgentName(proj), "--network", "none",
		"--label", "lea.prefix=" + l.prefix(), "--label", "lea.role=ssh-agent", "--label", "lea.ssh-agent-for=" + proj,
		"--user", "0:0", "--cap-drop", "ALL", "--cap-add", "CHOWN", "--cap-add", "SETUID", "--cap-add", "SETGID",
		"--cap-add", "DAC_READ_SEARCH", "--security-opt", "no-new-privileges", "--read-only",
		"--pids-limit", "32", "--memory", "64m",
		"-e", "LEA_UID=" + strconv.Itoa(os.Getuid()), "-e", "LEA_GID=" + strconv.Itoa(os.Getgid()),
		"--mount", "type=bind,src=" + l.Cfg.StateDir() + "/ssh,dst=" + sshAgentKeyDir + ",readonly",
		"--mount", "type=volume,src=" + l.sshAgentVolume(proj) + ",dst=" + sshAgentSocketDir,
		"--entrypoint", "sh", l.Cfg.Get("LEA_IMAGE"), "-c", sshAgentScript}
}

// sshAgentStart starts the projects dir's ssh-agent container afresh and loads lea's key into it,
// before the agent container starts. Without a key there is none (git over SSH has no identity,
// as before). Under a virtual machine runtime a socket does not cross into the VM: the agents go
// without the key rather than with the key file. A failure is a warning: the agents run, without
// git over SSH.
func (l *Launcher) sshAgentStart(proj string, vm bool) {
	name := l.sshAgentName(proj)
	_ = l.quiet("docker", "rm", "-f", name)
	if !isRegular(l.sshKey()) {
		return
	}
	if vm {
		l.logf("warning: lea's SSH key is not available to the agents under %s: a virtual machine runtime cannot share the ssh-agent's socket, and lea never mounts the key itself; git over SSH has no identity", l.Cfg.Get("LEA_RUNTIME"))
		return
	}
	if err := l.run("docker", l.sshAgentRunArgs(proj)...); err != nil {
		l.logf("warning: lea's ssh-agent container %s did not start (docker's message above): git over SSH has no identity", name)
		return
	}
	for i := 0; l.quiet("docker", "exec", name, "test", "-S", sshAgentSocket) != nil; i++ {
		if i >= 10 || l.isInterrupted() {
			l.logf("warning: lea's ssh-agent in %s did not come up: git over SSH has no identity", name)
			return
		}
		l.Sleep(time.Second / 2)
	}
	l.sshAdd(name)
}

// sshAdd loads lea's key into the ssh-agent container name: on the launcher's terminal when it has
// one (a passphrase), else without input.
func (l *Launcher) sshAdd(name string) {
	var err error
	if IsTerminal(l.Stdin) && IsTerminal(l.Stdout) {
		err = l.foreground("docker", "exec", "-it", "-u", "0", name, "sh", "-c", sshAddScript)
	} else {
		err = l.run("docker", "exec", "-u", "0", name, "sh", "-c", sshAddScript)
	}
	if err != nil {
		l.logf("warning: lea's SSH key is not loaded into its ssh-agent (a passphrase needs a terminal: run lea again from one); git over SSH has no identity")
	}
}

// sshAgentEnsure starts the ssh-agent container of a running agent container again when it is
// gone (stopped by hand, the engine restarted).
func (l *Launcher) sshAgentEnsure(proj string) {
	if !isRegular(l.sshKey()) || l.containerRunning(l.sshAgentName(proj)) {
		return
	}
	// Under a virtual machine runtime there is none (sshAgentStart warned at the start).
	if _, vm, err := l.agentRuntime(); err == nil && !vm {
		l.sshAgentStart(proj, false)
	}
}

// sshAgentStop stops the projects dir's ssh-agent container (--rm removes it) with its agent
// container.
func (l *Launcher) sshAgentStop(proj string) { _ = l.quiet("docker", "stop", l.sshAgentName(proj)) }
